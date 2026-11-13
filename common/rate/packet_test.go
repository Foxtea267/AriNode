package rate_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/common/rate"
	"github.com/juju/ratelimit"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type rateTestPacket struct {
	data []byte
	addr M.Socksaddr
}

// Include optional fast paths on the transport to detect their accidental
// promotion through the rate wrapper. Packet writers own their input buffers.
type rateTestPacketConn struct {
	mu                                 sync.Mutex
	reads, writes                      []rateTestPacket
	readErr, writeErr, closeErr        error
	readErrorAddr                      M.Socksaddr
	closed                             bool
	closes, waits, successes, failures int
}

func (c *rateTestPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return M.Socksaddr{}, net.ErrClosed
	}
	if c.readErr != nil {
		return c.readErrorAddr, c.readErr
	}
	if len(c.reads) == 0 {
		return M.Socksaddr{}, io.EOF
	}
	packet := c.reads[0]
	c.reads = c.reads[1:]
	_, err := buffer.Write(packet.data)
	return packet.addr, err
}

func (c *rateTestPacketConn) WritePacket(buffer *buf.Buffer, addr M.Socksaddr) error {
	defer buffer.Release()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	if c.writeErr != nil {
		return c.writeErr
	}
	c.writes = append(c.writes, rateTestPacket{append([]byte(nil), buffer.Bytes()...), addr})
	return nil
}

func (c *rateTestPacketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.closes++
	return c.closeErr
}

func (*rateTestPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (*rateTestPacketConn) SetDeadline(time.Time) error      { return nil }
func (*rateTestPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (*rateTestPacketConn) SetWriteDeadline(time.Time) error { return nil }
func (*rateTestPacketConn) FrontHeadroom() int               { return 12 }
func (*rateTestPacketConn) RearHeadroom() int                { return 4 }
func (*rateTestPacketConn) InitializeReadWaiter(N.ReadWaitOptions) bool {
	return false
}

func (c *rateTestPacketConn) WaitReadPacket() (*buf.Buffer, M.Socksaddr, error) {
	c.mu.Lock()
	c.waits++
	c.mu.Unlock()
	buffer := buf.NewPacket()
	addr, err := c.ReadPacket(buffer)
	return buffer, addr, err
}

func (c *rateTestPacketConn) HandshakeSuccess() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.successes++
	return nil
}

func (c *rateTestPacketConn) HandshakeFailure(error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures++
	return nil
}

func rateTestPackets() []rateTestPacket {
	return []rateTestPacket{
		{bytes.Repeat([]byte{'4'}, 32), M.ParseSocksaddr("192.0.2.1:443")},
		{bytes.Repeat([]byte{'6'}, 32), M.ParseSocksaddr("[2001:db8::1]:5353")},
		{bytes.Repeat([]byte{'d'}, 32), M.ParseSocksaddr("example.com:53")},
	}
}

func assertRateTestPackets(t *testing.T, got, want []rateTestPacket) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("packet count=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].addr != want[i].addr || !bytes.Equal(got[i].data, want[i].data) {
			t.Fatalf("packet %d content/address changed: %v, want %v", i, got[i], want[i])
		}
	}
}

func TestPacketReadWriteAndCopyRemainLimited(t *testing.T) {
	for _, direction := range []string{"read", "write"} {
		for _, copyPath := range []bool{false, true} {
			name := direction + "/direct"
			if copyPath {
				name = direction + "/sing_CopyPacket_with_cached_first_packet"
			}
			t.Run(name, func(t *testing.T) {
				packets := rateTestPackets()
				const total = 96
				base := &rateTestPacketConn{}
				var transport N.PacketConn = base
				if copyPath {
					// Match router wrapping: prefetched packet -> rate -> counter.
					cached := bufio.NewCachedPacketConn(base, buf.As(packets[0].data), packets[0].addr)
					transport = cached
					base.reads = append(base.reads, packets[1:]...)
				} else {
					base.reads = append(base.reads, packets...)
				}
				bucket := bucketForPayload(total)
				limited := rate.NewPacketConnRateLimiter(transport, bucket)
				storage := &counter.TrafficStorage{}
				conn := counter.NewPacketConnCounter(limited, storage)
				t.Cleanup(func() { conn.Close() })
				var got []rateTestPacket
				bucket.TakeAvailable(bucket.Capacity())
				started := time.Now()
				if copyPath {
					var src N.PacketReader = conn
					var dst N.PacketWriter
					sink := &rateTestPacketConn{}
					dst = sink
					if direction == "write" {
						// The copy also handles a cached source; the destination's
						// rate wrapper must charge every resulting packet.
						source := &rateTestPacketConn{reads: append([]rateTestPacket(nil), packets[1:]...)}
						src = bufio.NewCachedPacketConn(source, buf.As(packets[0].data), packets[0].addr)
						dst = conn
					}
					n, err := bufio.CopyPacket(dst, src)
					if n != total || !errors.Is(err, io.EOF) {
						t.Fatalf("CopyPacket n=%d err=%v, want %d and EOF", n, err, total)
					}
					got = sink.writes
				} else {
					for _, packet := range packets {
						if direction == "read" {
							buffer := buf.NewPacket()
							addr, err := conn.ReadPacket(buffer)
							if err != nil {
								buffer.Release()
								t.Fatal(err)
							}
							got = append(got, rateTestPacket{append([]byte(nil), buffer.Bytes()...), addr})
							buffer.Release()
						} else {
							buffer := buf.NewSize(len(packet.data))
							buffer.Write(packet.data)
							if err := conn.WritePacket(buffer, packet.addr); err != nil {
								t.Fatal(err)
							}
							if buffer.RawCap() != 0 {
								t.Fatal("write did not hand buffer ownership to transport")
							}
						}
					}
				}
				assertThrottled(t, time.Since(started), total)
				wantUp, wantDown := int64(total), int64(0)
				if direction == "write" {
					got = base.writes
					wantUp, wantDown = 0, total
				}
				assertRateTestPackets(t, got, packets)
				if up, down := storage.UpCounter.Load(), storage.DownCounter.Load(); up != wantUp || down != wantDown {
					t.Fatalf("accounting %d/%d want %d/%d", up, down, wantUp, wantDown)
				}
				if base.waits != 0 {
					t.Fatal("optional packet waiter bypassed rate-limited ReadPacket")
				}
			})
		}
	}
}

func TestPacketControlInterfacesRemainVisibleWithoutDataPathUnwrapping(t *testing.T) {
	base := &rateTestPacketConn{}
	limited := rate.NewPacketConnRateLimiter(base, ratelimit.NewBucketWithRate(256, 128))
	wrapped := counter.NewPacketConnCounter(limited, &counter.TrafficStorage{})
	t.Cleanup(func() { wrapped.Close() })
	if limited.Upstream() != base {
		t.Fatal("Upstream did not return original transport")
	}
	if got, ok := common.Cast[*rateTestPacketConn](wrapped); !ok || got != base {
		t.Fatal("control lookup could not reach transport through counter and rate")
	}
	if err := N.ReportPacketConnHandshakeSuccess(wrapped, nil); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("target unavailable")
	if err := N.ReportHandshakeFailure(wrapped, cause); !errors.Is(err, cause) {
		t.Fatalf("handshake failure lost target error: %v", err)
	}
	if base.successes != 1 || base.failures != 1 {
		t.Fatalf("control replies %d/%d want 1/1", base.successes, base.failures)
	}
	if limited.ReaderReplaceable() || limited.WriterReplaceable() || N.UnwrapPacketReader(limited) != limited || N.UnwrapPacketWriter(limited) != limited {
		t.Fatal("rate-limited packet data path is replaceable")
	}
	reader, _ := N.UnwrapCountPacketReader(wrapped, nil)
	writer, _ := N.UnwrapCountPacketWriter(wrapped, nil)
	if reader != limited || writer != limited {
		t.Fatal("counter optimization unwrapped past rate limiter")
	}
	if _, ok := bufio.CreatePacketReadWaiter(limited); ok {
		t.Fatal("packet waiter bypasses rate wrapper")
	}
	if _, ok := any(limited).(N.CachedPacketReader); ok {
		t.Fatal("cached packet interface bypasses rate wrapper")
	}
	if front, rear := N.CalculateFrontHeadroom(wrapped), N.CalculateRearHeadroom(wrapped); front != 12 || rear != 4 {
		t.Fatalf("lost transport headroom: %d/%d", front, rear)
	}
}

func TestPacketPreservesTransportErrorsAndOwnership(t *testing.T) {
	readErr, writeErr := errors.New("read sentinel"), errors.New("write sentinel")
	addr := M.ParseSocksaddr("[2001:db8::2]:53")
	base := &rateTestPacketConn{readErr: readErr, readErrorAddr: addr, writeErr: writeErr}
	limited := rate.NewPacketConnRateLimiter(base, ratelimit.NewBucketWithRate(256, 1024))
	storage := &counter.TrafficStorage{}
	wrapped := counter.NewPacketConnCounter(limited, storage)
	t.Cleanup(func() { wrapped.Close() })
	buffer := buf.NewPacket()
	actual, err := wrapped.ReadPacket(buffer)
	buffer.Release()
	if err != readErr || actual != addr {
		t.Fatalf("changed transport read result: %v %v", actual, err)
	}
	buffer = buf.NewSize(32)
	buffer.Write(bytes.Repeat([]byte{'x'}, 32))
	if err := wrapped.WritePacket(buffer, addr); err != writeErr {
		t.Fatalf("changed transport write error: %v", err)
	}
	if buffer.RawCap() != 0 {
		t.Fatal("failed transport write did not consume owned buffer")
	}
	if storage.UpCounter.Load() != 0 || storage.DownCounter.Load() != 0 {
		t.Fatal("failed packets were counted as successfully forwarded")
	}
}

func TestPacketCloseCancelsSharedDebtWaits(t *testing.T) {
	packet := rateTestPacket{bytes.Repeat([]byte{'c'}, 64), M.ParseSocksaddr("192.0.2.9:443")}
	closeErr := errors.New("close sentinel")
	base := &rateTestPacketConn{reads: []rateTestPacket{packet}, closeErr: closeErr}
	bucket := ratelimit.NewBucketWithRate(1, 1)
	bucket.TakeAvailable(bucket.Capacity())
	limited := rate.NewPacketConnRateLimiter(base, bucket)
	t.Cleanup(func() { limited.Close() })
	readBuffer, writeBuffer := buf.NewPacket(), buf.NewSize(len(packet.data))
	defer readBuffer.Release()
	writeBuffer.Write(packet.data)
	results := make(chan error, 2)
	go func() { _, err := limited.ReadPacket(readBuffer); results <- err }()
	go func() { results <- limited.WritePacket(writeBuffer, packet.addr) }()
	// Observe both reservations before closing. Synchronize on the actual shared
	// bucket debt, rather than assuming a sleep means both workers are waiting.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for bucket.Available() > -120 {
		select {
		case err := <-results:
			t.Fatalf("packet operation escaped 1 B/s debt wait: %v", err)
		case <-deadline.C:
			t.Fatal("read and write did not reserve from the same bucket")
		case <-poll.C:
		}
	}
	started := time.Now()
	closes := make(chan error, 4)
	for range 4 {
		go func() { closes <- limited.Close() }()
	}
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for range 4 {
		select {
		case err := <-closes:
			if err != closeErr {
				t.Fatalf("Close lost original error: %v", err)
			}
		case <-timeout.C:
			t.Fatal("Close blocked on slow shared bucket")
		}
	}
	for range 2 {
		select {
		case err := <-results:
			if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("cancelled packet operation error=%v", err)
			}
		case <-timeout.C:
			t.Fatal("Close did not promptly cancel packet waits")
		}
	}
	if time.Since(started) >= time.Second || base.closes != 1 || len(base.writes) != 0 {
		t.Fatalf("incorrect cancellation lifecycle: closes=%d writes=%d", base.closes, len(base.writes))
	}
	if writeBuffer.RawCap() != 0 || !bytes.Equal(readBuffer.Bytes(), packet.data) {
		t.Fatal("cancelled write ownership or caller-owned read buffer changed")
	}
	if bucket.Available() > -100 {
		t.Fatal("cancellation refunded shared debt and could over-admit other sessions")
	}
}

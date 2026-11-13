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
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

// A finite, immediate transport isolates bucket delay from socket timing. Its
// fast-copy methods deliberately do not apply rate limiting.
type memoryConn struct {
	reader *bytes.Reader
	writes bytes.Buffer
	chunk  int
}

func newMemoryConn(payload []byte) *memoryConn {
	return &memoryConn{reader: bytes.NewReader(payload), chunk: 32}
}

func (c *memoryConn) Read(p []byte) (int, error) {
	if len(p) > c.chunk {
		p = p[:c.chunk]
	}
	return c.reader.Read(p)
}
func (c *memoryConn) Write(p []byte) (int, error)         { return c.writes.Write(p) }
func (c *memoryConn) WriteTo(w io.Writer) (int64, error)  { return c.reader.WriteTo(w) }
func (c *memoryConn) ReadFrom(r io.Reader) (int64, error) { return c.writes.ReadFrom(r) }
func (*memoryConn) Close() error                          { return nil }
func (*memoryConn) LocalAddr() net.Addr                   { return &net.TCPAddr{} }
func (*memoryConn) RemoteAddr() net.Addr                  { return &net.TCPAddr{} }
func (*memoryConn) SetDeadline(time.Time) error           { return nil }
func (*memoryConn) SetReadDeadline(time.Time) error       { return nil }
func (*memoryConn) SetWriteDeadline(time.Time) error      { return nil }

type handshakeConn struct {
	*memoryConn
	successes int
	failures  int
	failure   error
}

func (c *handshakeConn) HandshakeSuccess() error {
	c.successes++
	_, err := c.Write([]byte{0})
	return err
}
func (c *handshakeConn) HandshakeFailure(err error) error {
	c.failures++
	c.failure = err
	_, writeErr := c.Write([]byte{1})
	return writeErr
}

func TestConnHandshakeAcrossCounterExtendedAndRate(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			base := &handshakeConn{memoryConn: newMemoryConn(nil)}
			limited := rate.NewConnRateLimiter(base, ratelimit.NewBucketWithRate(512, 128))
			upstream, ok := any(limited).(interface{ Upstream() any })
			if !ok || upstream.Upstream() != base {
				t.Fatal("rate wrapper did not expose its original connection")
			}
			storage := &counter.TrafficStorage{}
			// This is the HookServer wrapping order: ConnCounter's extended
			// wrapper sits between the counter and rate layers.
			wrapped := counter.NewConnCounter(limited, storage)
			if _, ok := wrapped.(*counter.ConnCounter).ExtendedConn.(*bufio.ExtendedConnWrapper); !ok {
				t.Fatal("test must exercise the extended connection wrapper")
			}
			if outcome == "success" {
				if err := N.ReportConnHandshakeSuccess(wrapped, wrapped); err != nil {
					t.Fatal(err)
				}
				if base.successes != 1 || base.failures != 0 {
					t.Fatalf("handshake reply counts: successes=%d failures=%d", base.successes, base.failures)
				}
				if got := base.writes.Bytes(); !bytes.Equal(got, []byte{0}) {
					t.Fatalf("success SOCKS reply was not sent exactly once: %v", got)
				}
			} else {
				cause := errors.New("upstream dial failed")
				if err := N.ReportHandshakeFailure(wrapped, cause); !errors.Is(err, cause) {
					t.Fatalf("failure report lost original dial error: %v", err)
				}
				if base.failures != 1 || base.successes != 0 || base.failure != cause {
					t.Fatalf("failure interface was lost or replied more than once: %#v", base)
				}
				if got := base.writes.Bytes(); !bytes.Equal(got, []byte{1}) {
					t.Fatalf("failure SOCKS reply was not sent exactly once: %v", got)
				}
			}
		})
	}
}

func bucketForPayload(payload int) *ratelimit.Bucket {
	bucket := ratelimit.NewBucketWithRate(256, int64(payload))
	// Exhaust capacity before sending so the transport cannot consume the
	// initial burst without waiting.
	bucket.Wait(int64(payload))
	return bucket
}

func TestConnReadWriteAndCopyPathsRemainLimited(t *testing.T) {
	payload := bytes.Repeat([]byte("limited payload/"), 8) // 128 bytes.
	for _, tc := range []struct {
		name      string
		direction string
		copy      func(io.Writer, io.Reader) (int64, error)
	}{
		{name: "Read", direction: "read", copy: func(dst io.Writer, src io.Reader) (int64, error) {
			data, err := io.ReadAll(src)
			if err != nil {
				return int64(len(data)), err
			}
			n, err := dst.Write(data)
			return int64(n), err
		}},
		{name: "Write", direction: "write", copy: func(dst io.Writer, src io.Reader) (int64, error) {
			data, err := io.ReadAll(src)
			if err != nil {
				return int64(len(data)), err
			}
			n, err := dst.Write(data)
			return int64(n), err
		}},
		{name: "io.Copy/read", direction: "read", copy: io.Copy},
		{name: "io.Copy/write", direction: "write", copy: io.Copy},
		{name: "sing_bufio.Copy/read", direction: "read", copy: bufio.Copy},
		{name: "sing_bufio.Copy/write", direction: "write", copy: bufio.Copy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			underlying := newMemoryConn(payload)
			bucket := bucketForPayload(len(payload))
			limited := rate.NewConnRateLimiter(underlying, bucket)
			var conn net.Conn = limited
			if tc.name != "Read" && tc.name != "Write" {
				// Exercise the real counter/extended wrappers as well as sing's
				// count unwrapping and optional fast-copy paths.
				conn = counter.NewConnCounter(limited, &counter.TrafficStorage{})
			}
			var received bytes.Buffer
			var src io.Reader = conn
			var dst io.Writer = &received
			if tc.direction == "write" {
				src, dst = bytes.NewReader(payload), conn
			}
			// Setup above may take long enough for tokens to refill. Drain them
			// immediately before timing so every path measures the same wait.
			bucket.TakeAvailable(bucket.Capacity())
			start := time.Now()
			n, err := tc.copy(dst, src)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			if n != int64(len(payload)) {
				t.Fatalf("copied %d bytes, want %d", n, len(payload))
			}
			got := received.Bytes()
			if tc.direction == "write" {
				got = underlying.writes.Bytes()
			}
			if !bytes.Equal(got, payload) {
				t.Fatal("copied content changed")
			}
			assertThrottled(t, elapsed, len(payload))
		})
	}
	// sing's optional copy paths can bypass ordinary Read/Write unless both
	// replacement contracts explicitly say that the rate wrapper is not replaceable.
	limited := rate.NewConnRateLimiter(newMemoryConn(payload), bucketForPayload(len(payload)))
	reader, ok := any(limited).(N.ReaderWithUpstream)
	if !ok || reader.ReaderReplaceable() {
		t.Fatal("rate-limited reader was marked replaceable")
	}
	writer, ok := any(limited).(N.WriterWithUpstream)
	if !ok || writer.WriterReplaceable() {
		t.Fatal("rate-limited writer was marked replaceable")
	}
	if N.UnwrapReader(limited) != limited || N.UnwrapWriter(limited) != limited {
		t.Fatal("sing unwrapped the rate-limited data path")
	}
}

func assertThrottled(t *testing.T, elapsed time.Duration, bytes int) {
	t.Helper()
	minimum := time.Duration(bytes) * time.Second / 256 * 3 / 4
	if elapsed < minimum {
		t.Fatalf("%d bytes bypassed 256 B/s bucket: elapsed %v, expected at least %v", bytes, elapsed, minimum)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("bucket wait took unexpectedly long: %v", elapsed)
	}
}

func TestConnSharesBucketAcrossReadsWritesAndConnections(t *testing.T) {
	const ratePerSecond = 256
	const totalBytes = 1024
	bucket := bucketForPayload(totalBytes)
	payload := bytes.Repeat([]byte{0x5a}, totalBytes/8)
	connections := []*memoryConn{newMemoryConn(bytes.Repeat(payload, 2)), newMemoryConn(bytes.Repeat(payload, 2))}
	limited := []*rate.Conn{rate.NewConnRateLimiter(connections[0], bucket), rate.NewConnRateLimiter(connections[1], bucket)}
	var wait sync.WaitGroup
	start := time.Now()
	for _, conn := range limited {
		wait.Add(1)
		go func(conn *rate.Conn) {
			defer wait.Done()
			for i := 0; i < 2; i++ {
				read := make([]byte, len(payload))
				if _, err := io.ReadFull(conn, read); err != nil || !bytes.Equal(read, payload) {
					t.Errorf("read: content matches=%v err=%v", bytes.Equal(read, payload), err)
				}
				if n, err := conn.Write(payload); err != nil || n != len(payload) {
					t.Errorf("write: n=%d err=%v", n, err)
				}
			}
		}(conn)
	}
	wait.Wait()
	assertThrottled(t, time.Since(start), totalBytes)
	for i, conn := range connections {
		if !bytes.Equal(conn.writes.Bytes(), bytes.Repeat(payload, 2)) {
			t.Fatalf("connection %d data mismatch", i)
		}
	}
	if bucket.Rate() != ratePerSecond {
		t.Fatalf("test bucket rate changed: %v", bucket.Rate())
	}
}

package sing

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/juju/ratelimit"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type hookRatePacket struct {
	data        []byte
	destination M.Socksaddr
}

// A finite packet fixture makes the hook test independent of UDP scheduling and
// loss. Successful writes own their buffers, just like sing's UDP connection.
type hookRatePacketConn struct {
	mu     sync.Mutex
	reads  []hookRatePacket
	writes []hookRatePacket
	closed bool
}

func (c *hookRatePacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return M.Socksaddr{}, net.ErrClosed
	}
	if len(c.reads) == 0 {
		return M.Socksaddr{}, io.EOF
	}
	packet := c.reads[0]
	c.reads = c.reads[1:]
	n, err := buffer.Write(packet.data)
	if err == nil && n != len(packet.data) {
		err = io.ErrShortBuffer
	}
	return packet.destination, err
}

func (c *hookRatePacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.writes = append(c.writes, hookRatePacket{
		data: append([]byte(nil), buffer.Bytes()...), destination: destination,
	})
	buffer.Release()
	return nil
}

func (c *hookRatePacketConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (*hookRatePacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (*hookRatePacketConn) SetDeadline(time.Time) error      { return nil }
func (*hookRatePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (*hookRatePacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestHookTCPAndPacketSessionsShareUserRateAndAccounting(t *testing.T) {
	const tag, user, other = "shared-tcp-packet-rate", "limited", "separate"
	const size, rounds = 64, 2
	limiter.Init()
	l := limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{
		{Id: 1, Uuid: user, SpeedLimit: 300, DeviceLimit: 1},
		{Id: 2, Uuid: other, SpeedLimit: 300, DeviceLimit: 1},
	}, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	userTag := format.UserTag(tag, user)
	original, rejected := l.CheckLimit(userTag, "192.0.2.1", true, true)
	if rejected || original == nil || original.Rate() != 300_000_000/8 {
		t.Fatalf("speed_limit=300 changed units or admission: bucket=%v reject=%v", original, rejected)
	}
	// Keep the original per-user map entry, substituting only a small bucket so
	// aggregate limiting is observable without transferring hundreds of MB.
	bucket := ratelimit.NewBucketWithRate(256, size*4)
	l.SpeedLimiter.Store(userTag, bucket)
	hook := &HookServer{}
	metadata := adapter.InboundContext{
		Inbound: tag, User: user, Source: M.ParseSocksaddr("192.0.2.1:1234"),
		Destination: M.ParseSocksaddr("example.com:443"),
	}
	raw, peer := net.Pipe()
	raw.SetDeadline(time.Now().Add(15 * time.Second))
	peer.SetDeadline(time.Now().Add(15 * time.Second))
	stream := hook.RoutedConnection(context.Background(), raw, metadata, nil, nil)
	t.Cleanup(func() { stream.Close(); peer.Close() })
	extended, ok := stream.(N.ExtendedConn)
	if !ok {
		t.Fatalf("missing extended TCP counter: %T", stream)
	}
	payload := bytes.Repeat([]byte{'p'}, size)
	destination := M.ParseSocksaddr("[2001:db8::53]:53")
	fixtures := make([]*hookRatePacketConn, 2)
	packets := make([]N.PacketConn, len(fixtures))
	var create sync.WaitGroup
	for i := range fixtures {
		fixtures[i] = &hookRatePacketConn{}
		for n := 0; n < rounds; n++ {
			fixtures[i].reads = append(fixtures[i].reads, hookRatePacket{payload, destination})
		}
		create.Add(1)
		go func(i int) {
			defer create.Done()
			packets[i] = hook.RoutedPacketConnection(context.Background(), fixtures[i], metadata, nil, nil)
		}(i)
	}
	create.Wait()
	for _, conn := range packets {
		t.Cleanup(func() { conn.Close() })
		if _, ok := conn.(*counter.PacketConnCounter); !ok {
			t.Fatalf("packet accounting wrapper missing: %T", conn)
		}
	}
	if stored, ok := l.SpeedLimiter.Load(userTag); !ok || stored != bucket {
		t.Fatal("opening TCP and concurrent UDP sessions replaced the shared bucket")
	}
	otherMeta := metadata
	otherMeta.User = other
	otherRaw := &hookRatePacketConn{reads: []hookRatePacket{{payload, destination}}}
	otherConn := hook.RoutedPacketConnection(context.Background(), otherRaw, otherMeta, nil, nil)
	t.Cleanup(func() { otherConn.Close() })
	otherBucket, rejected := l.CheckLimit(format.UserTag(tag, other), "192.0.2.1", false, false)
	if rejected || otherBucket == nil || otherBucket == bucket {
		t.Fatal("different users share a speed bucket")
	}
	if _, rejected := l.CheckLimit(userTag, "192.0.2.2", true, true); !rejected {
		t.Fatal("a UDP session changed the existing one-device admission limit")
	}
	server := &Sing{
		hookServer:                hook,
		users:                     &UserMap{uidMap: map[string]map[string]int{tag: {user: 1, other: 2}}},
		nodeReportMinTrafficBytes: map[string]int64{},
	}
	readPacket := func(conn N.PacketConn) error {
		buffer := buf.NewPacket()
		defer buffer.Release()
		actual, err := conn.ReadPacket(buffer)
		if err == nil && (actual != destination || !bytes.Equal(buffer.Bytes(), payload)) {
			err = fmt.Errorf("packet payload or destination changed: %v", actual)
		}
		return err
	}
	writePacket := func(conn N.PacketConn) error {
		buffer := buf.As(append([]byte(nil), payload...))
		if err := conn.WritePacket(buffer, destination); err != nil {
			buffer.Release()
			return err
		}
		return nil
	}
	runPhase := func(name string, tasks ...func() error) {
		t.Helper()
		bucket.TakeAvailable(bucket.Capacity())
		started := time.Now()
		results := make(chan error, len(tasks))
		for _, task := range tasks {
			go func(task func() error) { results <- task() }(task)
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for range tasks {
			select {
			case err := <-results:
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			case <-deadline.C:
				t.Fatalf("%s exceeded the bounded transfer deadline", name)
			}
		}
		// Three 64-byte transfers need 750 ms from one drained 256 B/s
		// bucket. Per-session buckets or either UDP bypass finish too early.
		if elapsed := time.Since(started); elapsed < 600*time.Millisecond {
			t.Fatalf("%s bypassed aggregate TCP+UDP limiting: %v", name, elapsed)
		}
	}
	for round := 0; round < rounds; round++ {
		runPhase("upload", func() error {
			peerResult := make(chan error, 1)
			go func() { _, err := peer.Write(payload); peerResult <- err }()
			buffer := buf.NewSize(size)
			defer buffer.Release()
			// The extended copy path is additive; do not hide the separate
			// legacy ConnCounter.Read/Write Store-vs-Add issue in this test.
			if err := extended.ReadBuffer(buffer); err != nil {
				return err
			}
			if !bytes.Equal(buffer.Bytes(), payload) {
				return io.ErrUnexpectedEOF
			}
			return <-peerResult
		}, func() error { return readPacket(packets[0]) }, func() error { return readPacket(packets[1]) })
		runPhase("download", func() error {
			peerResult := make(chan error, 1)
			go func() {
				received := make([]byte, size)
				_, err := io.ReadFull(peer, received)
				if err == nil && !bytes.Equal(received, payload) {
					err = io.ErrUnexpectedEOF
				}
				peerResult <- err
			}()
			if err := extended.WriteBuffer(buf.As(append([]byte(nil), payload...))); err != nil {
				return err
			}
			return <-peerResult
		}, func() error { return writePacket(packets[0]) }, func() error { return writePacket(packets[1]) })
		up, down := server.GetUserTraffic(tag, user, false)
		want := int64((round + 1) * (len(packets) + 1) * size)
		if up != want || down != want {
			t.Fatalf("round %d lost cumulative TCP+UDP accounting: up=%d down=%d want=%d", round, up, down, want)
		}
	}
	if err := readPacket(otherConn); err != nil {
		t.Fatal(err)
	}
	if err := writePacket(otherConn); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range append(fixtures, otherRaw) {
		fixture.mu.Lock()
		for _, packet := range fixture.writes {
			if packet.destination != destination || !bytes.Equal(packet.data, payload) {
				t.Error("packet write changed payload or IPv6 destination")
			}
		}
		fixture.mu.Unlock()
	}
	traffic, err := server.GetUserTrafficSlice(tag, true)
	if err != nil || len(traffic) != 2 {
		t.Fatalf("billing snapshot lost user identities: %+v %v", traffic, err)
	}
	for _, row := range traffic {
		want := int64(rounds * (len(packets) + 1) * size)
		if row.UID == 2 {
			want = size
		} else if row.UID != 1 {
			t.Fatalf("unknown billed UID: %+v", row)
		}
		if row.Upload != want || row.Download != want {
			t.Fatalf("incorrect bidirectional billing snapshot: %+v want=%d", row, want)
		}
	}
	if again, err := server.GetUserTrafficSlice(tag, true); err != nil || len(again) != 0 {
		t.Fatalf("billing reset duplicated already reported traffic: %+v %v", again, err)
	}
	for _, uuid := range []string{user, other} {
		if up, down := server.GetUserTraffic(tag, uuid, false); up != 0 || down != 0 {
			t.Fatalf("billing reset left counters for %s: %d/%d", uuid, up, down)
		}
	}
	assertDevices := func(want int) {
		t.Helper()
		online, err := l.GetOnlineDevice()
		if err != nil || len(*online) != want {
			t.Fatalf("incorrect shared-session device count: %+v err=%v want=%d", online, err, want)
		}
	}
	assertDevices(2) // TCP and both UDP sessions are one UID+IP device.
	assertDevices(2) // Repeated reports must retain live sessions.
	packets[0].Close()
	packets[0].Close()
	assertDevices(2)
	stream.Close()
	assertDevices(2)
	packets[1].Close()
	assertDevices(1)
	otherConn.Close()
	assertDevices(0)
}

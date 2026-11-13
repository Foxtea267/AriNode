package sing

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/common/rate"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/juju/ratelimit"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
)

func TestHookConnectionsKeepSharedUserRateBucket(t *testing.T) {
	const tag, user, other = "shared-rate-hook", "limited-user", "other-user"
	const size = 64
	limiter.Init()
	l := limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{
		{Id: 1, Uuid: user, SpeedLimit: 300}, {Id: 2, Uuid: other, SpeedLimit: 300},
	}, nil)
	defer limiter.DeleteLimiter(tag)
	userTag := format.UserTag(tag, user)
	original, rejected := l.CheckLimit(userTag, "127.0.0.1", true, true)
	if rejected || original == nil || original.Rate() != 300_000_000/8 {
		t.Fatalf("unexpected original speed_limit=300 bucket: %v rejected=%v", original, rejected)
	}
	// A smaller test bucket makes any wrapper bypass observable without a large
	// transfer. Hook must reuse it rather than creating a fresh per-connection burst.
	bucket := ratelimit.NewBucketWithRate(256, size*4)
	bucket.Wait(size * 4)
	l.SpeedLimiter.Store(userTag, bucket)
	hook := &HookServer{}
	connections := make([]net.Conn, 2)
	peers := make([]net.Conn, len(connections))
	var create sync.WaitGroup
	for i := range connections {
		create.Add(1)
		go func(i int) {
			defer create.Done()
			raw, peer := net.Pipe()
			raw.SetDeadline(time.Now().Add(5 * time.Second))
			peer.SetDeadline(time.Now().Add(5 * time.Second))
			connections[i] = hook.RoutedConnection(context.Background(), raw, adapter.InboundContext{
				Inbound: tag, User: user, Source: M.ParseSocksaddr("127.0.0.1:12345"),
			}, nil, nil)
			peers[i] = peer
		}(i)
	}
	create.Wait()
	for i, conn := range connections {
		t.Cleanup(func() { conn.Close(); peers[i].Close() })
		if _, ok := conn.(*counter.ConnCounter); !ok {
			t.Fatalf("missing counter wrapper: %T", conn)
		}
		if _, ok := common.Cast[*rate.Conn](conn); !ok {
			t.Fatal("missing shared-user rate wrapper")
		}
	}
	stored, ok := l.SpeedLimiter.Load(userTag)
	if !ok || stored != bucket {
		t.Fatal("opening concurrent connections replaced the shared bucket")
	}
	separate, rejected := l.CheckLimit(format.UserTag(tag, other), "127.0.0.1", true, true)
	if rejected || separate == nil || separate == bucket {
		t.Fatal("another user shared the limited user's bucket")
	}
	payload := bytes.Repeat([]byte{'s'}, size)
	errors := make(chan error, len(connections)*2)
	var transfers sync.WaitGroup
	bucket.TakeAvailable(bucket.Capacity()) // discard tokens refilled during setup
	started := time.Now()
	for i, conn := range connections {
		transfers.Add(2)
		go func(peer net.Conn) {
			defer transfers.Done()
			_, err := peer.Write(payload)
			errors <- err
		}(peers[i])
		go func(conn net.Conn) {
			defer transfers.Done()
			var received bytes.Buffer
			n, err := bufio.Copy(&received, io.LimitReader(conn, size))
			if err == nil && (n != size || !bytes.Equal(received.Bytes(), payload)) {
				err = io.ErrUnexpectedEOF
			}
			errors <- err
		}(conn)
	}
	transfers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(started); elapsed < 375*time.Millisecond {
		t.Fatalf("concurrent streams bypassed the shared 256 B/s bucket: %s", elapsed)
	}
	// Both sessions count as one device, and closing one must retain the other.
	l.GetOnlineDevice() // consume the one-shot admission snapshot
	for i, conn := range connections {
		online, err := l.GetOnlineDevice()
		if err != nil || len(*online) != 1 || (*online)[0].UID != 1 {
			t.Fatalf("before closing connection %d: devices=%v err=%v", i, online, err)
		}
		conn.Close()
	}
	online, err := l.GetOnlineDevice()
	if err != nil || len(*online) != 0 {
		t.Fatalf("shared-user device not released: devices=%v err=%v", online, err)
	}
}

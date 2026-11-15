package dispatcher

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/juju/ratelimit"
	"github.com/xtls/xray-core/common/buf"
	XNet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
)

func TestConcurrentXrayLinksRemainRevocableAfterUploadHalfClose(t *testing.T) {
	const tag = "xray-concurrent-revoke"
	user := &protocol.MemoryUser{Email: tag + "|user"}
	limiter.Init()
	limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 42, Uuid: "user"}}, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	d := &DefaultDispatcher{}
	if err := d.RegisterUser(user); err != nil {
		t.Fatal(err)
	}
	ctx := func(user *protocol.MemoryUser) context.Context {
		return session.ContextWithInbound(context.Background(), &session.Inbound{
			Tag: tag, User: user, Source: XNet.TCPDestination(XNet.ParseAddress("127.0.0.1"), 12345),
		})
	}
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			in, out, _, err := d.getLink(ctx(user), XNet.Network_TCP)
			if err != nil {
				t.Error(err)
				return
			}
			payload := buf.New()
			payload.Extend(7)
			if err := out.Writer.WriteMultiBuffer(buf.MultiBuffer{payload}); err != nil {
				t.Error(err)
			}
			_ = in.Writer.(interface{ Close() error }).Close()
		})
	}
	wg.Wait()
	value, ok := d.LinkManagers.Load(user.Email)
	if !ok {
		t.Fatal("lost user link manager")
	}
	manager := value.(*LinkManager)
	manager.mu.Lock()
	active := len(manager.links)
	manager.mu.Unlock()
	if active != 24 {
		t.Fatalf("upload half-close removed active downloads: %d/24", active)
	}
	counts, _ := d.Counter.Load(tag)
	if up, down := counts.(*counter.TrafficCounter).GetTraffic(user.Email, false); up != 0 || down != 24*7 {
		t.Fatalf("concurrent first links split their counters: %d/%d", up, down)
	}
	d.RevokeUser(user.Email)
	renewed := &protocol.MemoryUser{Email: user.Email}
	if err := d.RegisterUser(renewed); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.getLink(ctx(user), XNet.Network_TCP); err == nil {
		t.Fatal("old authentication was accepted after same credential re-add")
	}
	if _, _, _, err := d.getLink(ctx(renewed), XNet.Network_TCP); err != nil {
		t.Fatalf("new authentication rejected: %v", err)
	}
	d.RevokeUser(user.Email)
}

type oneBufferReader struct {
	deviceTestIO
	timeout time.Duration
}

func (r *oneBufferReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	r.timeout = timeout
	b := buf.New()
	b.Extend(32)
	return buf.MultiBuffer{b}, nil
}

func TestDispatchLinkReaderHonorsTimeoutAndUplinkBucket(t *testing.T) {
	raw := &oneBufferReader{}
	bucket := ratelimit.NewBucketWithRate(128, 32)
	bucket.TakeAvailable(32)
	var counted int64
	reader := &CounterReader{Reader: raw, Bucket: bucket, Add: func(n int64) { counted += n }}
	start := time.Now()
	buffers, err := reader.ReadMultiBufferTimeout(17 * time.Millisecond)
	buf.ReleaseMulti(buffers)
	if err != nil || raw.timeout != 17*time.Millisecond || counted != 32 || time.Since(start) < 180*time.Millisecond {
		t.Fatalf("uplink read: error=%v timeout=%v bytes=%d delay=%v", err, raw.timeout, counted, time.Since(start))
	}
}

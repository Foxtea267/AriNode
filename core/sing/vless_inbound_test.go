package sing

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	vmess "github.com/wyx2685/sing-vmess"
	"github.com/wyx2685/sing-vmess/vless"
)

const stableVLESSTestUser = "11111111-1111-4111-8111-111111111111"
const stableVLESSTestOther = "22222222-2222-4222-8222-222222222222"

type stableVLESSTestRouter struct {
	adapter.Router
	routes atomic.Int32
}

func (r *stableVLESSTestRouter) RouteConnectionEx(_ context.Context, conn net.Conn, _ adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.routes.Add(1)
	_ = conn.Close()
	if onClose != nil {
		onClose(nil)
		onClose(nil)
	}
}

func (r *stableVLESSTestRouter) RoutePacketConnectionEx(_ context.Context, conn N.PacketConn, _ adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.routes.Add(1)
	_ = conn.Close()
	if onClose != nil {
		onClose(nil)
		onClose(nil)
	}
}

func newUnitStableVLESS(t *testing.T) (*streamAuthInbound, *stableVLESSTestRouter) {
	t.Helper()
	router := &stableVLESSTestRouter{}
	inbound, err := newStableVLESSInbound(context.Background(), router, log.NewNOPFactory().NewLogger("unit"), "unit-vless", option.VLESSInboundOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h := inbound.(*streamAuthInbound)
	t.Cleanup(func() { h.Close() })
	return h, router
}

func stableVLESSUnitOptions(names ...string) []option.VLESSUser {
	users := make([]option.VLESSUser, len(names))
	for i, name := range names {
		users[i] = option.VLESSUser{Name: name, UUID: name}
	}
	return users
}

func TestVLESSStableAuthenticationGenerationsAndCallbacks(t *testing.T) {
	h, _ := newUnitStableVLESS(t)
	if err := h.AddUsers(stableVLESSUnitOptions(stableVLESSTestUser, stableVLESSTestOther)); err != nil {
		t.Fatal(err)
	}
	old, healthy := h.users[stableVLESSTestUser], h.users[stableVLESSTestOther]
	if err := h.AddUsers(stableVLESSUnitOptions(stableVLESSTestOther)); err != nil {
		t.Fatal(err)
	}
	if h.users[stableVLESSTestOther] != healthy {
		t.Fatal("duplicate snapshot changed a healthy authentication generation")
	}
	raw, peer := net.Pipe()
	defer peer.Close()
	parent := &streamAuthParent{conn: raw}
	h.parents[parent] = struct{}{}
	ctx := context.WithValue(auth.ContextWithUser(context.Background(), old), streamAuthParentKey{}, parent)
	var callbacks atomic.Int32
	_, release, ok := h.track(ctx, raw, func(error) { callbacks.Add(1) })
	if !ok {
		t.Fatal("current generation rejected")
	}
	if err := h.DelUsers([]string{stableVLESSTestUser}); err != nil {
		t.Fatal(err)
	}
	if !parent.closed || h.users[stableVLESSTestOther] != healthy {
		t.Fatal("revocation failed to invalidate only its authenticated parent")
	}
	release(nil)
	release(io.EOF)
	if callbacks.Load() != 1 || len(h.sessions) != 0 {
		t.Fatal("close callback was repeated or left session registration")
	}
	if err := h.AddUsers(stableVLESSUnitOptions(stableVLESSTestUser)); err != nil {
		t.Fatal(err)
	}
	if h.users[stableVLESSTestUser] == old {
		t.Fatal("readd reused the removed authentication generation")
	}
	if _, _, allowed := h.track(ctx, raw, nil); allowed {
		t.Fatal("old authenticated transport regained access after credential readd")
	}
	if err := h.DelUsers([]string{stableVLESSTestUser, stableVLESSTestOther}); err != nil {
		t.Fatal(err)
	}
	if len(h.users) != 0 {
		t.Fatal("empty snapshot retained credentials")
	}
}

func TestVLESSStableCapturedServiceRejectsDelayedRevokedAuthentication(t *testing.T) {
	h, router := newUnitStableVLESS(t)
	if err := h.AddUsers(stableVLESSUnitOptions(stableVLESSTestUser, stableVLESSTestOther)); err != nil {
		t.Fatal(err)
	}
	oldService := h.service
	if err := h.DelUsers([]string{stableVLESSTestUser}); err != nil {
		t.Fatal(err)
	}
	if err := h.AddUsers(stableVLESSUnitOptions(stableVLESSTestUser)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		credential string
		wantRoutes int32
	}{{stableVLESSTestUser, 0}, {stableVLESSTestOther, 1}} {
		raw, peer := net.Pipe()
		parent := &streamAuthParent{conn: raw}
		ctx := context.WithValue(context.Background(), streamAuthParentKey{}, parent)
		metadata := adapter.InboundContext{Source: M.ParseSocksaddr("127.0.0.1:12345")}
		var request bytes.Buffer
		key := uuid.Must(uuid.FromString(test.credential))
		if err := vless.WriteRequest(&request, vless.Request{UUID: key, Command: vmess.CommandTCP, Destination: M.ParseSocksaddr("127.0.0.1:80")}, nil); err != nil {
			t.Fatal(err)
		}
		writeErr := make(chan error, 1)
		go func() { _, err := peer.Write(request.Bytes()); writeErr <- err }()
		raw.SetDeadline(time.Now().Add(time.Second))
		if err := oldService.NewConnection(adapter.WithContext(ctx, &metadata), raw, metadata.Source, nil); err != nil {
			t.Fatal(err)
		}
		if err := <-writeErr; err != nil {
			t.Fatal(err)
		}
		_ = raw.Close()
		_ = peer.Close()
		if router.routes.Load() != test.wantRoutes {
			t.Fatalf("captured pre-update service routed wrong authentication: got=%d want=%d", router.routes.Load(), test.wantRoutes)
		}
	}
}

func TestVLESSStableConcurrentSnapshotsAndAuthentication(t *testing.T) {
	h, _ := newUnitStableVLESS(t)
	if err := h.AddUsers(stableVLESSUnitOptions(stableVLESSTestOther)); err != nil {
		t.Fatal(err)
	}
	healthy := h.users[stableVLESSTestOther]
	var group sync.WaitGroup
	errors := make(chan error, 2)
	group.Add(2)
	go func() {
		defer group.Done()
		for i := 0; i < 100; i++ {
			if err := h.AddUsers(stableVLESSUnitOptions(stableVLESSTestUser)); err != nil {
				errors <- err
				return
			}
			if err := h.DelUsers([]string{stableVLESSTestUser}); err != nil {
				errors <- err
				return
			}
		}
	}()
	go func() {
		defer group.Done()
		for i := 0; i < 100; i++ {
			raw, peer := net.Pipe()
			parent := &streamAuthParent{conn: raw}
			ctx := context.WithValue(auth.ContextWithUser(context.Background(), healthy), streamAuthParentKey{}, parent)
			_, release, allowed := h.track(ctx, raw, nil)
			if !allowed {
				errors <- io.ErrUnexpectedEOF
				_ = raw.Close()
				_ = peer.Close()
				return
			}
			release(nil)
			release(nil)
			_ = raw.Close()
			_ = peer.Close()
		}
	}()
	group.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	if h.users[stableVLESSTestOther] != healthy || len(h.sessions) != 0 {
		t.Fatal("unrelated snapshots changed healthy generation or retained sessions")
	}
}

func TestVLESSStableConnectionCloseCallbackRunsOnce(t *testing.T) {
	h, router := newUnitStableVLESS(t)
	if err := h.AddUsers(stableVLESSUnitOptions(stableVLESSTestUser)); err != nil {
		t.Fatal(err)
	}
	raw, peer := net.Pipe()
	defer peer.Close()
	defer raw.Close()
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.NewConnectionEx(context.Background(), raw, adapter.InboundContext{Source: M.ParseSocksaddr("127.0.0.1:12345")}, func(error) { calls.Add(1) })
	}()
	peer.SetDeadline(time.Now().Add(time.Second))
	key := uuid.Must(uuid.FromString(stableVLESSTestUser))
	if err := vless.WriteRequest(peer, vless.Request{UUID: key, Command: vmess.CommandTCP, Destination: M.ParseSocksaddr("127.0.0.1:80")}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("authenticated route did not finish")
	}
	if calls.Load() != 1 || router.routes.Load() != 1 || len(h.parents) != 0 || len(h.sessions) != 0 {
		t.Fatalf("connection cleanup callback=%d routes=%d parents=%d sessions=%d", calls.Load(), router.routes.Load(), len(h.parents), len(h.sessions))
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("repeated inbound close replayed a connection callback")
	}
}

type stableVLESSDelayedHookRouter struct {
	adapter.Router
	entered chan struct{}
	resume  chan struct{}
	hook    *HookServer
	lease   chan bool
}

func (r *stableVLESSDelayedHookRouter) wait(ctx context.Context, metadata adapter.InboundContext) {
	close(r.entered)
	<-r.resume
	lease, present := ctx.Value(routedUserCounterKey{}).(routedUserCounterLease)
	if !present {
		r.lease <- false
		return
	}
	storage, allowed := lease(r.hook, metadata.Inbound, metadata.User)
	r.lease <- !allowed && storage == nil
}

func (r *stableVLESSDelayedHookRouter) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.wait(ctx, metadata)
	tracked := r.hook.RoutedConnection(ctx, conn, metadata, nil, nil)
	_ = tracked.Close()
	if onClose != nil {
		onClose(nil)
	}
}

func (r *stableVLESSDelayedHookRouter) RoutePacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.wait(ctx, metadata)
	tracked := r.hook.RoutedPacketConnection(ctx, conn, metadata, nil, nil)
	_ = tracked.Close()
	if onClose != nil {
		onClose(nil)
	}
}

func TestVLESSStableDelayedRouteCounterAdmissionRejectsRevokedGeneration(t *testing.T) {
	for _, packet := range []bool{false, true} {
		t.Run(map[bool]string{false: "TCP", true: "UDP"}[packet], func(t *testing.T) {
			h, _ := newUnitStableVLESS(t)
			users := stableVLESSUnitOptions(stableVLESSTestUser)
			if err := h.AddUsers(users); err != nil {
				t.Fatal(err)
			}
			hook := &HookServer{}
			router := &stableVLESSDelayedHookRouter{entered: make(chan struct{}), resume: make(chan struct{}), hook: hook, lease: make(chan bool, 1)}
			gate := &streamAuthRouteGate{ConnectionRouterEx: router, inbound: h}
			limiter.Init()
			limiter.AddLimiter(h.Tag(), &conf.LimitConfig{}, []panel.UserInfo{{Id: 41, Uuid: stableVLESSTestUser}}, nil)
			defer limiter.DeleteLimiter(h.Tag())
			raw, peer := net.Pipe()
			defer raw.Close()
			defer peer.Close()
			parent := &streamAuthParent{conn: raw}
			h.parents[parent] = struct{}{}
			ctx := context.WithValue(auth.ContextWithUser(context.Background(), h.users[stableVLESSTestUser]), streamAuthParentKey{}, parent)
			metadata := adapter.InboundContext{Inbound: h.Tag(), InboundType: h.Type(), User: stableVLESSTestUser, Source: M.ParseSocksaddr("127.0.0.1:12345"), Destination: M.ParseSocksaddr("127.0.0.1:53")}
			done := make(chan struct{})
			if packet {
				udp, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer udp.Close()
				go func() {
					defer close(done)
					gate.RoutePacketConnectionEx(ctx, bufio.NewPacketConn(udp), metadata, nil)
				}()
			} else {
				go func() {
					defer close(done)
					gate.RouteConnectionEx(ctx, raw, metadata, nil)
				}()
			}
			select {
			case <-router.entered:
			case <-time.After(time.Second):
				t.Fatal("controlled route never reached the pre-Hook delay")
			}
			if err := h.DelUsers([]string{stableVLESSTestUser}); err != nil {
				t.Fatal(err)
			}
			if err := h.AddUsers(users); err != nil {
				t.Fatal(err)
			}
			// The new user is authorized in the limiter, but this delayed route
			// still carries the old authentication generation from before delete.
			close(router.resume)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("revoked route did not finish")
			}
			if !<-router.lease {
				t.Fatal("counter admission accepted an old generation after delete/readd or lease was missing")
			}
			if stored, ok := hook.counter.Load(h.Tag()); ok && stored.(*counter.TrafficCounter).Len() != 0 {
				t.Fatal("delayed revoked route recreated a live user counter")
			}
		})
	}
}

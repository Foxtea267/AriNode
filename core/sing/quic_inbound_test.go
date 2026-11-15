//go:build with_quic

package sing

import (
	"context"
	stdTLS "crypto/tls"
	"errors"
	"io"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/cert"
	"github.com/sagernet/quic-go"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/byteformats"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const (
	unitQUICUser  = "11111111-1111-4111-8111-111111111111"
	unitQUICOther = "22222222-2222-4222-8222-222222222222"
)

func newUnitQUICInbound(t *testing.T, protocol string) *safeQUICInbound {
	t.Helper()
	certificate, key, err := cert.SelfSigned("arinode.local")
	if err != nil {
		t.Fatal(err)
	}
	addr := netip.MustParseAddr("127.0.0.1")
	listen := option.ListenOptions{Listen: (*badoption.Addr)(&addr)}
	tlsOptions := &option.InboundTLSOptions{Enabled: true, ServerName: "arinode.local", Certificate: certificate, Key: key}
	factory := log.NewNOPFactory()
	var inbound adapter.Inbound
	if protocol == "hysteria2" {
		inbound, err = newSafeHY2Inbound(context.Background(), nil, factory.NewLogger("unit"), "unit", option.Hysteria2InboundOptions{
			ListenOptions: listen, InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: tlsOptions},
		})
	} else if protocol == "hysteria" {
		tlsOptions.ALPN = []string{"hysteria"}
		inbound, err = newSafeHY1Inbound(context.Background(), nil, factory.NewLogger("unit"), "unit", option.HysteriaInboundOptions{
			ListenOptions: listen, UpMbps: 100, DownMbps: 100, InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: tlsOptions},
		})
	} else {
		tlsOptions.ALPN = []string{"h3"}
		inbound, err = newSafeTUICInbound(context.Background(), nil, factory.NewLogger("unit"), "unit", option.TUICInboundOptions{
			ListenOptions: listen, InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: tlsOptions}, CongestionControl: "bbr",
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	h := inbound.(*safeQUICInbound)
	t.Cleanup(func() { h.Close() })
	return h
}

type unitQUICCloser struct {
	closes atomic.Int32
}

func (c *unitQUICCloser) Close() error { c.closes.Add(1); return nil }

func TestQUICAuthenticationGenerationAndNoopSnapshots(t *testing.T) {
	for _, protocol := range []string{"hysteria", "hysteria2", "tuic"} {
		t.Run(protocol, func(t *testing.T) {
			h := newUnitQUICInbound(t, protocol)
			users := []panel.UserInfo{{Id: 41, Uuid: unitQUICUser}, {Id: 73, Uuid: unitQUICOther}}
			if err := h.AddUsers(users); err != nil {
				t.Fatal(err)
			}
			original, healthy, snapshot := h.users[unitQUICUser], h.users[unitQUICOther], h.current
			if err := h.AddUsers(users); err != nil {
				t.Fatal(err)
			}
			if err := h.DelUsers(nil); err != nil {
				t.Fatal(err)
			}
			if err := h.DelUsers([]string{"unknown"}); err != nil {
				t.Fatal(err)
			}
			if h.current != snapshot || h.users[unitQUICUser] != original || h.users[unitQUICOther] != healthy {
				t.Fatal("duplicate or no-op update changed authentication generations or SDK snapshot")
			}
			oldContext := auth.ContextWithUser(context.Background(), original)
			conn := &unitQUICCloser{}
			name, release, ok := h.track(oldContext, conn)
			if !ok || name != unitQUICUser {
				t.Fatal("current authentication generation could not route")
			}
			if err := h.DelUsers([]string{unitQUICUser}); err != nil {
				t.Fatal(err)
			}
			if conn.closes.Load() != 1 || h.users[unitQUICOther] != healthy {
				t.Fatal("revocation failed to close only its user sessions")
			}
			release()
			release()
			if len(h.sessions) != 0 {
				t.Fatal("router close callback did not unregister session exactly once")
			}
			if err := h.AddUsers(users[:1]); err != nil {
				t.Fatal(err)
			}
			if h.users[unitQUICUser] == original {
				t.Fatal("readded UUID reused its revoked authentication generation")
			}
			if _, _, ok := h.track(oldContext, &unitQUICCloser{}); ok {
				t.Fatal("old authenticated parent regained authorization after UUID readd")
			}
			if _, _, ok := h.track(context.Background(), &unitQUICCloser{}); ok {
				t.Fatal("missing authentication context was accepted")
			}
			_, release, ok = h.track(auth.ContextWithUser(context.Background(), h.users[unitQUICUser]), &unitQUICCloser{})
			if !ok {
				t.Fatal("fresh authentication generation was rejected")
			}
			release()
		})
	}
}

type unitQUICPacketConn struct{ unitQUICCloser }

func (*unitQUICPacketConn) ReadPacket(*buf.Buffer) (M.Socksaddr, error) { return M.Socksaddr{}, io.EOF }
func (*unitQUICPacketConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.Release()
	return nil
}
func (*unitQUICPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (*unitQUICPacketConn) SetDeadline(time.Time) error      { return nil }
func (*unitQUICPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (*unitQUICPacketConn) SetWriteDeadline(time.Time) error { return nil }

type unitQUICRouter struct {
	adapter.ConnectionRouterEx
	mu        sync.Mutex
	callbacks []N.CloseHandlerFunc
	users     []string
}

func (r *unitQUICRouter) RouteConnectionEx(_ context.Context, _ net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.mu.Lock()
	r.callbacks = append(r.callbacks, onClose)
	r.users = append(r.users, metadata.User)
	r.mu.Unlock()
}
func (r *unitQUICRouter) RoutePacketConnectionEx(_ context.Context, _ N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.mu.Lock()
	r.callbacks = append(r.callbacks, onClose)
	r.users = append(r.users, metadata.User)
	r.mu.Unlock()
}

func TestQUICTracksTCPAndPacketCloseCallbacks(t *testing.T) {
	for _, protocol := range []string{"hysteria", "hysteria2", "tuic"} {
		t.Run(protocol, func(t *testing.T) { testQUICTracksTCPAndPacketCloseCallbacks(t, protocol) })
	}
}

func testQUICTracksTCPAndPacketCloseCallbacks(t *testing.T, protocol string) {
	h := newUnitQUICInbound(t, protocol)
	if err := h.AddUsers([]panel.UserInfo{{Id: 41, Uuid: unitQUICUser}}); err != nil {
		t.Fatal(err)
	}
	router := &unitQUICRouter{}
	h.router = router
	ctx := auth.ContextWithUser(context.Background(), h.users[unitQUICUser])
	stream, peer := net.Pipe()
	t.Cleanup(func() { stream.Close(); peer.Close() })
	packet := &unitQUICPacketConn{}
	var notifications atomic.Int32
	onClose := func(error) { notifications.Add(1) }
	h.NewConnectionEx(ctx, stream, M.ParseSocksaddr("127.0.0.1:1"), M.ParseSocksaddr("example.com:443"), onClose)
	h.NewPacketConnectionEx(ctx, packet, M.ParseSocksaddr("127.0.0.1:1"), M.ParseSocksaddr("example.com:53"), onClose)
	if len(h.sessions) != 2 || len(router.callbacks) != 2 {
		t.Fatal("routed TCP/packet sessions were not both registered")
	}
	for _, user := range router.users {
		if user != unitQUICUser {
			t.Fatal("immutable authentication identity not copied to routed metadata")
		}
	}
	if err := h.DelUsers([]string{unitQUICUser}); err != nil {
		t.Fatal(err)
	}
	if packet.closes.Load() != 1 {
		t.Fatal("packet-only closer was not revoked")
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("TCP stream was not closed: %v", err)
	}
	for _, callback := range router.callbacks {
		callback(nil)
	}
	if len(h.sessions) != 0 || notifications.Load() != 2 {
		t.Fatalf("router callbacks did not release both sessions: sessions=%d callbacks=%d", len(h.sessions), notifications.Load())
	}
}

func TestQUICConcurrentAuthenticationAndUserUpdates(t *testing.T) {
	for _, protocol := range []string{"hysteria", "hysteria2", "tuic"} {
		t.Run(protocol, func(t *testing.T) { testQUICConcurrentAuthenticationAndUserUpdates(t, protocol) })
	}
}

func testQUICConcurrentAuthenticationAndUserUpdates(t *testing.T, protocol string) {
	h := newUnitQUICInbound(t, protocol)
	users := []panel.UserInfo{{Id: 41, Uuid: unitQUICUser}, {Id: 73, Uuid: unitQUICOther}}
	if err := h.AddUsers(users); err != nil {
		t.Fatal(err)
	}
	healthy := h.users[unitQUICOther]
	var group sync.WaitGroup
	results := make(chan error, 5)
	for worker := 0; worker < 4; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 60; iteration++ {
				h.mu.Lock()
				user := h.users[unitQUICUser]
				h.mu.Unlock()
				if user == nil {
					continue
				}
				_, release, ok := h.track(auth.ContextWithUser(context.Background(), user), &unitQUICCloser{})
				if ok {
					release()
				}
			}
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		for iteration := 0; iteration < 30; iteration++ {
			if err := h.DelUsers([]string{unitQUICUser}); err != nil {
				results <- err
				return
			}
			if err := h.AddUsers(users[:1]); err != nil {
				results <- err
				return
			}
		}
	}()
	group.Wait()
	close(results)
	for err := range results {
		t.Fatal(err)
	}
	if h.users[unitQUICOther] != healthy || len(h.sessions) != 0 {
		t.Fatal("concurrent updates changed healthy generation or leaked tracked sessions")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.AddUsers(users); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed inbound accepted update: %v", err)
	}
	if _, _, ok := h.track(auth.ContextWithUser(context.Background(), healthy), &unitQUICCloser{}); ok {
		t.Fatal("closed inbound accepted a session")
	}
}

func TestQUICSnapshotLifetimeAndNodeClose(t *testing.T) {
	for _, protocol := range []string{"hysteria", "hysteria2", "tuic"} {
		t.Run(protocol, func(t *testing.T) {
			h := newUnitQUICInbound(t, protocol)
			if err := h.AddUsers([]panel.UserInfo{{Id: 41, Uuid: unitQUICUser}}); err != nil {
				t.Fatal(err)
			}
			if err := h.Start(adapter.StartStateStart); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			alpn := "h3"
			if protocol == "hysteria" {
				alpn = "hysteria"
			}
			conn, err := quic.DialAddr(ctx, h.acceptor.Addr().String(), &stdTLS.Config{InsecureSkipVerify: true, NextProtos: []string{alpn}}, &quic.Config{EnableDatagrams: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.CloseWithError(0, "test finished") })
			waitQUICUnit(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.parents) == 1 })
			h.mu.Lock()
			old := h.current
			if old.refs != 1 {
				h.mu.Unlock()
				t.Fatal("accepted parent did not retain its SDK snapshot")
			}
			h.mu.Unlock()
			if err := h.AddUsers([]panel.UserInfo{{Id: 73, Uuid: unitQUICOther}}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-old.queue.done:
				t.Fatal("retired SDK queue closed while a parent still owns it")
			default:
			}
			conn.CloseWithError(0, "release old snapshot")
			waitQUICUnit(t, func() bool {
				select {
				case <-old.queue.done:
					return true
				default:
					return false
				}
			})
			waitQUICUnit(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.parents) == 0 })
			second, err := quic.DialAddr(ctx, h.acceptor.Addr().String(), &stdTLS.Config{InsecureSkipVerify: true, NextProtos: []string{alpn}}, &quic.Config{EnableDatagrams: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { second.CloseWithError(0, "test finished") })
			waitQUICUnit(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.parents) == 1 })
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-second.Context().Done():
			case <-time.After(time.Second):
				t.Fatal("node Close did not close its parent QUIC connection")
			}
			waitQUICUnit(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.parents) == 0 })
		})
	}
}

func TestHY1RegistrationUsesStableUUIDsInsteadOfPositions(t *testing.T) {
	h := newUnitQUICInbound(t, "hysteria")
	users := []panel.UserInfo{{Id: 41, Uuid: unitQUICUser}, {Id: 73, Uuid: unitQUICOther}}
	if err := h.AddUsers(users); err != nil {
		t.Fatal(err)
	}
	first, healthy := h.users[unitQUICUser], h.users[unitQUICOther]
	if err := h.DelUsers([]string{unitQUICUser}); err != nil {
		t.Fatal(err)
	}
	if h.users[unitQUICOther] != healthy || len(h.users) != 1 {
		t.Fatal("deleting one Hysteria user changed the unrelated user's stable identity")
	}
	if _, _, ok := h.track(auth.ContextWithUser(context.Background(), first), &unitQUICCloser{}); ok {
		t.Fatal("revoked Hysteria authentication generation was accepted")
	}
	_, release, ok := h.track(auth.ContextWithUser(context.Background(), healthy), &unitQUICCloser{})
	if !ok {
		t.Fatal("remaining Hysteria user was rejected")
	}
	release()
	if err := h.AddUsers(users[:1]); err != nil {
		t.Fatal(err)
	}
	if h.users[unitQUICUser] == first {
		t.Fatal("re-added Hysteria user reused the revoked generation")
	}
}

func TestHY1BuildSnapshotPreservesLegacySettings(t *testing.T) {
	certificate, key, err := cert.SelfSigned("arinode.local")
	if err != nil {
		t.Fatal(err)
	}
	addr := netip.MustParseAddr("127.0.0.1")
	options := option.HysteriaInboundOptions{
		ListenOptions: option.ListenOptions{Listen: (*badoption.Addr)(&addr)},
		UpMbps:        37, DownMbps: 81, Obfs: "legacy-obfs", ReceiveWindowConn: 1 << 20,
		ReceiveWindowClient: 2 << 20, MaxConnClient: 99, DisableMTUDiscovery: true,
		InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: &option.InboundTLSOptions{
			Enabled: true, ServerName: "arinode.local", Certificate: certificate, Key: key,
		}},
	}
	in, err := newSafeHY1Inbound(context.Background(), nil, log.NewNOPFactory().NewLogger("unit"), "legacy-hy1", options)
	if err != nil {
		t.Fatal(err)
	}
	h := in.(*safeQUICInbound)
	t.Cleanup(func() { h.Close() })
	if h.Type() != "hysteria" || h.hy1 == nil || h.hy1.UpMbps != 37 || h.hy1.DownMbps != 81 || h.hy1.Obfs != "legacy-obfs" ||
		h.hy1.ReceiveWindowConn != 1<<20 || h.hy1.ReceiveWindowClient != 2<<20 || h.hy1.MaxConnClient != 99 || !h.hy1.DisableMTUDiscovery {
		t.Fatal("Hysteria inbound did not retain legacy server settings")
	}
	if _, ok := h.current.service.(*hysteria.Service[*quicUser]); !ok {
		t.Fatalf("Hysteria was built with %T instead of its native service", h.current.service)
	}
	if !slices.Equal(h.hy1.TLS.Certificate, certificate) || !slices.Equal(h.hy1.TLS.Key, key) {
		t.Fatal("Hysteria TLS certificate material changed")
	}
	compiled := h.hy1ServiceOptions(h.hy1, h.tlsConfig)
	if compiled.SendBPS != 37*hysteria.MbpsToBps || compiled.ReceiveBPS != 81*hysteria.MbpsToBps || compiled.XPlusPassword != "legacy-obfs" ||
		compiled.ConnReceiveWindow != 1<<20 || compiled.StreamReceiveWindow != 2<<20 || compiled.MaxIncomingStreams != 99 || !compiled.DisableMTUDiscovery || compiled.Handler != h {
		t.Fatal("Hysteria native service options dropped a legacy setting")
	}
	var up, down byteformats.NetworkBytesCompat
	if err := up.UnmarshalJSON([]byte(`"12 Mbps"`)); err != nil {
		t.Fatal(err)
	}
	if err := down.UnmarshalJSON([]byte(`"24 Mbps"`)); err != nil {
		t.Fatal(err)
	}
	explicit := *h.hy1
	explicit.Up, explicit.Down = &up, &down
	explicit.UDPTimeout = option.UDPTimeoutCompat(17 * time.Second)
	compiled = h.hy1ServiceOptions(&explicit, h.tlsConfig)
	if compiled.SendBPS != up.Value() || compiled.ReceiveBPS != down.Value() || compiled.UDPTimeout != 17*time.Second {
		t.Fatal("Hysteria explicit bandwidth/UDP timeout was not preserved")
	}
}

func waitQUICUnit(t *testing.T, condition func() bool) {
	t.Helper()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !condition() {
		select {
		case <-tick.C:
		case <-timeout.C:
			t.Fatal("QUIC state did not reach the expected value before timeout")
		}
	}
}

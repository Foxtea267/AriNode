package sing

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/mux"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2ray"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/wyx2685/sing-vmess/packetaddr"
	"github.com/wyx2685/sing-vmess/vless"
)

// Authentication records remain stable across unrelated user updates. A removed
// and re-added credential gets a different record, so its old mux transport
// cannot become authorized again. SDK services are immutable after publication.
type streamAuthUser struct{ name, uuid, flow string }

type streamAuthParent struct {
	conn          net.Conn
	user          *streamAuthUser
	closed        bool
	finish        func(error)
	header        *authHeaderConn
	checkedHeader bool
	ready         bool
}

type streamAuthSession struct {
	user *streamAuthUser
	conn io.Closer
}

type streamAuthParentKey struct{}

type streamAuthInbound struct {
	inbound.Adapter
	ctx              context.Context
	router           adapter.ConnectionRouterEx
	logger           log.ContextLogger
	listener         *listener.Listener
	tlsConfig        tls.ServerConfig
	transport        adapter.V2RayServerTransport
	mu               sync.Mutex
	users            map[string]*streamAuthUser
	service          streamAuthService
	buildService     func(map[string]*streamAuthUser) (streamAuthService, error)
	fallbackAddr     M.Socksaddr
	fallbackALPN     map[string]M.Socksaddr
	fallbackRouter   adapter.ConnectionRouterEx
	headerReplay     func([]byte) bool
	headerReplaySize int
	parents          map[*streamAuthParent]struct{}
	sessions         map[*streamAuthSession]struct{}
	closed           bool
}

type streamAuthService interface {
	NewConnection(context.Context, net.Conn, M.Socksaddr, N.CloseHandlerFunc) error
}

var _ adapter.TCPInjectableInbound = (*streamAuthInbound)(nil)

func newStableVLESSInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.VLESSInboundOptions) (adapter.Inbound, error) {
	h := &streamAuthInbound{Adapter: inbound.NewAdapter(C.TypeVLESS, tag), ctx: ctx, logger: logger,
		users:   make(map[string]*streamAuthUser),
		parents: make(map[*streamAuthParent]struct{}), sessions: make(map[*streamAuthSession]struct{})}
	h.buildService = h.newService
	// Recheck below mux/UoT, too: a child callback queued before revocation
	// must not be authorized by a later re-add of the same credential.
	h.router = uot.NewRouter(&streamAuthRouteGate{ConnectionRouterEx: router, inbound: h}, logger)
	var err error
	h.router, err = mux.NewRouterWithOptions(h.router, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}
	if err := h.AddUsers(options.Users); err != nil {
		return nil, err
	}
	if options.TLS != nil {
		h.tlsConfig, err = tls.NewServerWithOptions(tls.ServerOptions{Context: ctx, Logger: logger,
			Options: *options.TLS, KTLSCompatible: common.PtrValueOrDefault(options.Transport).Type == "" &&
				!common.PtrValueOrDefault(options.Multiplex).Enabled && common.All(options.Users, func(user option.VLESSUser) bool { return user.Flow == "" })})
		if err != nil {
			return nil, err
		}
	}
	if options.Transport != nil {
		h.transport, err = v2ray.NewServerTransport(ctx, logger, *options.Transport, h.tlsConfig, (*streamAuthTransportHandler)(h))
		if err != nil {
			_ = common.Close(h.tlsConfig)
			return nil, fmt.Errorf("create VLESS server transport: %w", err)
		}
	}
	h.listener = listener.New(listener.Options{Context: ctx, Logger: logger, Network: []string{N.NetworkTCP}, Listen: options.ListenOptions, ConnectionHandler: h})
	return h, nil
}

func (h *streamAuthInbound) newService(users map[string]*streamAuthUser) (streamAuthService, error) {
	if err := validateStreamUUIDs(users); err != nil {
		return nil, err
	}
	service := vless.NewService[*streamAuthUser](h.logger, adapter.NewUpstreamContextHandlerEx(h.newConnectionEx, h.newPacketConnectionEx))
	list := make([]*streamAuthUser, 0, len(users))
	ids, flows := make([]string, 0, len(users)), make([]string, 0, len(users))
	for _, user := range users {
		list, ids, flows = append(list, user), append(ids, user.uuid), append(flows, user.flow)
	}
	service.UpdateUsers(list, ids, flows)
	return service, nil
}

// Match the SDK's normalization: equivalent UUID strings must not select
// whichever user's entry happens to be visited first in a map.
func validateStreamUUIDs(users map[string]*streamAuthUser) error {
	seen := make(map[uuid.UUID]struct{}, len(users))
	for _, user := range users {
		key, err := uuid.FromString(user.uuid)
		if err != nil {
			key = uuid.NewV5(uuid.Nil, user.uuid)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate effective user UUID")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (h *streamAuthInbound) AddUsers(users []option.VLESSUser) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return net.ErrClosed
	}
	next := make(map[string]*streamAuthUser, len(h.users)+len(users))
	for name, user := range h.users {
		next[name] = user
	}
	for _, user := range users {
		name := user.Name
		if name == "" {
			name = user.UUID
		}
		if name == "" || user.UUID == "" {
			return fmt.Errorf("empty %s user credential", h.Type())
		}
		if old := next[name]; old != nil {
			if old.uuid != user.UUID || old.flow != user.Flow {
				return fmt.Errorf("%s user must be removed before changing credentials", h.Type())
			}
			continue
		}
		next[name] = &streamAuthUser{name: name, uuid: user.UUID, flow: user.Flow}
	}
	service, err := h.buildService(next)
	if err != nil {
		return err
	}
	h.users, h.service = next, service
	return nil
}

func (h *streamAuthInbound) DelUsers(names []string) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return net.ErrClosed
	}
	next := make(map[string]*streamAuthUser, len(h.users))
	for name, user := range h.users {
		next[name] = user
	}
	for _, name := range names {
		delete(next, name)
	}
	service, err := h.buildService(next)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	h.users, h.service = next, service
	var revoked []io.Closer
	for parent := range h.parents {
		if parent.user != nil && next[parent.user.name] != parent.user {
			parent.closed = true
			revoked = append(revoked, parent.conn)
		}
	}
	for session := range h.sessions {
		if next[session.user.name] != session.user {
			revoked = append(revoked, session.conn)
		}
	}
	h.mu.Unlock()
	for _, conn := range revoked {
		_ = conn.Close()
	}
	return nil
}

func (h *streamAuthInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		if err := h.tlsConfig.Start(); err != nil {
			return err
		}
	}
	if h.transport == nil {
		return h.listener.Start()
	}
	if common.Contains(h.transport.Network(), N.NetworkTCP) {
		conn, err := h.listener.ListenTCP()
		if err != nil {
			return err
		}
		go func() {
			if err := h.transport.Serve(conn); err != nil && !E.IsClosed(err) {
				h.logger.Error("VLESS transport stopped")
			}
		}()
	}
	if common.Contains(h.transport.Network(), N.NetworkUDP) {
		conn, err := h.listener.ListenUDP()
		if err != nil {
			return err
		}
		go func() {
			if err := h.transport.ServePacket(conn); err != nil && !E.IsClosed(err) {
				h.logger.Error("VLESS packet transport stopped")
			}
		}()
	}
	return nil
}

func (h *streamAuthInbound) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	var connections []io.Closer
	for parent := range h.parents {
		parent.closed = true
		connections = append(connections, parent.conn)
	}
	for session := range h.sessions {
		connections = append(connections, session.conn)
	}
	h.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	return common.Close(h.listener, h.tlsConfig, h.transport)
}

func (h *streamAuthInbound) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	parent := &streamAuthParent{conn: conn}
	var once sync.Once
	parent.finish = func(err error) {
		once.Do(func() {
			h.mu.Lock()
			parent.closed = true
			delete(h.parents, parent)
			h.mu.Unlock()
			_ = parent.conn.Close()
			if onClose != nil {
				onClose(err)
			}
		})
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		parent.finish(net.ErrClosed)
		return
	}
	h.parents[parent] = struct{}{}
	service := h.service
	h.mu.Unlock()
	if h.tlsConfig != nil && h.transport == nil {
		secured, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			N.CloseOnHandshakeFailure(conn, parent.finish, err)
			return
		}
		conn = secured
	}
	ctx = context.WithValue(ctx, streamAuthParentKey{}, parent)
	if h.headerReplay != nil {
		parent.header = &authHeaderConn{Conn: conn}
		conn = parent.header
	}
	_ = conn.SetReadDeadline(time.Now().Add(C.TCPTimeout))
	if err := service.NewConnection(adapter.WithContext(ctx, &metadata), conn, metadata.Source, parent.finish); err != nil {
		N.CloseOnHandshakeFailure(conn, parent.finish, err)
		// SDK errors can embed the rejected UUID; do not log the raw error.
		h.logger.DebugContext(ctx, h.Type(), " connection rejected or closed")
	}
}

func (h *streamAuthInbound) track(ctx context.Context, conn io.Closer, onClose N.CloseHandlerFunc) (string, N.CloseHandlerFunc, bool) {
	user, found := auth.UserFromContext[*streamAuthUser](ctx)
	parent, _ := ctx.Value(streamAuthParentKey{}).(*streamAuthParent)
	h.mu.Lock()
	if !found || user == nil || h.closed || h.users[user.name] != user || parent == nil || parent.closed {
		h.mu.Unlock()
		return "", onClose, false
	}
	if h.headerReplay != nil && !parent.checkedHeader {
		if parent.header == nil || !parent.header.check(h.headerReplaySize, h.headerReplay) {
			parent.closed = true
			h.mu.Unlock()
			_ = parent.conn.Close()
			return "", onClose, false
		}
		parent.checkedHeader = true
	}
	parent.user = user
	if parent.conn != nil && !parent.ready {
		_ = parent.conn.SetReadDeadline(time.Time{})
		parent.ready = true
	}
	session := &streamAuthSession{user: user, conn: conn}
	h.sessions[session] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return user.name, func(err error) {
		once.Do(func() {
			h.mu.Lock()
			delete(h.sessions, session)
			h.mu.Unlock()
			if onClose != nil {
				onClose(err)
			}
		})
	}, true
}

func (h *streamAuthInbound) newConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	user, release, authorized := h.track(ctx, conn, onClose)
	if !authorized {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrPermission)
		return
	}
	metadata.Inbound, metadata.InboundType, metadata.User = h.Tag(), h.Type(), user
	h.router.RouteConnectionEx(ctx, conn, metadata, release)
}

func (h *streamAuthInbound) newPacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	user, release, authorized := h.track(ctx, conn, onClose)
	if !authorized {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrPermission)
		return
	}
	metadata.Inbound, metadata.InboundType, metadata.User = h.Tag(), h.Type(), user
	if metadata.Destination.Fqdn == packetaddr.SeqPacketMagicAddress {
		metadata.Destination = M.Socksaddr{}
		conn = packetaddr.NewConn(bufio.NewNetPacketConn(conn), metadata.Destination)
	}
	h.router.RoutePacketConnectionEx(ctx, conn, metadata, release)
}

type streamAuthTransportHandler streamAuthInbound

func (h *streamAuthTransportHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	metadata := adapter.InboundContext{Source: source, Destination: destination,
		InboundDetour: h.listener.ListenOptions().Detour, InboundOptions: h.listener.ListenOptions().InboundOptions}
	(*streamAuthInbound)(h).NewConnectionEx(ctx, conn, metadata, onClose)
}

type streamAuthRouteGate struct {
	adapter.ConnectionRouterEx
	inbound *streamAuthInbound
}

func (r *streamAuthRouteGate) counterContext(ctx context.Context) context.Context {
	user, found := auth.UserFromContext[*streamAuthUser](ctx)
	parent, _ := ctx.Value(streamAuthParentKey{}).(*streamAuthParent)
	lease := routedUserCounterLease(func(hook *HookServer, tag, name string) (*counter.TrafficStorage, bool) {
		r.inbound.mu.Lock()
		defer r.inbound.mu.Unlock()
		if !found || user == nil || r.inbound.closed || r.inbound.users[user.name] != user || parent == nil || parent.closed || tag != r.inbound.Tag() || name != user.name {
			return nil, false
		}
		// Serialize acquiring the counter with credential retirement. A route
		// delayed by sniffing/DNS cannot recreate a counter after deletion;
		// callbacks that already acquired it remain attached to RetireTo.
		return hook.userCounter(tag, name), true
	})
	return context.WithValue(ctx, routedUserCounterKey{}, lease)
}

func (r *streamAuthRouteGate) authorized(ctx context.Context) bool {
	user, found := auth.UserFromContext[*streamAuthUser](ctx)
	parent, _ := ctx.Value(streamAuthParentKey{}).(*streamAuthParent)
	r.inbound.mu.Lock()
	defer r.inbound.mu.Unlock()
	return found && user != nil && !r.inbound.closed && r.inbound.users[user.name] == user && parent != nil && !parent.closed
}

func (r *streamAuthRouteGate) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	user, release, allowed := r.inbound.track(ctx, conn, onClose)
	if !allowed {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrPermission)
		return
	}
	metadata.User = user
	r.ConnectionRouterEx.RouteConnectionEx(r.counterContext(ctx), conn, metadata, release)
}

func (r *streamAuthRouteGate) RoutePacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	user, release, allowed := r.inbound.track(ctx, conn, onClose)
	if !allowed {
		N.CloseOnHandshakeFailure(conn, onClose, os.ErrPermission)
		return
	}
	metadata.User = user
	r.ConnectionRouterEx.RoutePacketConnectionEx(r.counterContext(ctx), conn, metadata, release)
}

func (r *streamAuthRouteGate) RouteConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	if !r.authorized(ctx) {
		_ = conn.Close()
		return os.ErrPermission
	}
	return r.ConnectionRouterEx.RouteConnection(r.counterContext(ctx), conn, metadata)
}

func (r *streamAuthRouteGate) RoutePacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	if !r.authorized(ctx) {
		_ = conn.Close()
		return os.ErrPermission
	}
	return r.ConnectionRouterEx.RoutePacketConnection(r.counterContext(ctx), conn, metadata)
}

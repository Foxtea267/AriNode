//go:build with_quic

package sing

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/quic-go"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	qtls "github.com/sagernet/sing-quic"
	"github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing-quic/hysteria2"
	"github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
)

func registerQUICInbounds(registry *inbound.Registry) {
	inbound.Register[option.Hysteria2InboundOptions](registry, "hysteria2", newSafeHY2Inbound)
	inbound.Register[option.HysteriaInboundOptions](registry, "hysteria", newSafeHY1Inbound)
	inbound.Register[option.TUICInboundOptions](registry, "tuic", newSafeTUICInbound)
}

// Each authentication keeps an immutable generation pointer, never a reusable
// panel UID. Removing/re-adding the same UUID cannot authorize an old session.
type quicUser struct {
	name, password string
	uuid           [16]byte
}

type quicService interface {
	Start(net.PacketConn) error
	Close() error
}

type quicSnapshot struct {
	service quicService
	queue   *quicAcceptQueue
	refs    int
	retired bool
}

type quicSession struct {
	user *quicUser
	conn io.Closer
}

// The pinned SDK exposes no synchronized UpdateUsers or parent-session revoke
// API. Keep SDK authentication maps immutable, switch only the accept target,
// and guard every routed TCP/UDP session with its authentication generation.
// Existing healthy QUIC connections retain their original SDK instance.
type safeQUICInbound struct {
	inbound.Adapter
	ctx        context.Context
	router     adapter.ConnectionRouterEx
	logger     log.ContextLogger
	listener   *listener.Listener
	tlsConfig  tls.ServerConfig
	hy2        *option.Hysteria2InboundOptions
	hy1        *option.HysteriaInboundOptions
	tuic       *option.TUICInboundOptions
	masquerade http.Handler
	mu         sync.Mutex
	users      map[string]*quicUser
	sessions   map[*quicSession]struct{}
	parents    map[*quic.Conn]*quicSnapshot
	current    *quicSnapshot
	acceptor   qtls.Listener
	started    bool
	closed     bool
}

func newSafeHY2Inbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.Hysteria2InboundOptions) (adapter.Inbound, error) {
	h, err := newSafeQUICInbound(ctx, router, logger, tag, "hysteria2", options.ListenOptions, options.TLS)
	if err != nil {
		return nil, err
	}
	h.hy2 = &options
	h.masquerade, err = quicMasquerade(options.Masquerade)
	if err != nil {
		common.Close(h.tlsConfig)
		return nil, err
	}
	if options.Obfs != nil && (options.Obfs.Type != "salamander" || options.Obfs.Password == "") {
		common.Close(h.tlsConfig)
		return nil, fmt.Errorf("invalid Hysteria2 obfuscation")
	}
	for _, user := range options.Users {
		name := user.Name
		if name == "" {
			name = user.Password
		}
		h.users[name] = &quicUser{name: name, password: user.Password}
	}
	h.current, err = h.buildSnapshot(h.users)
	if err != nil {
		common.Close(h.tlsConfig)
		return nil, err
	}
	return h, nil
}

func newSafeHY1Inbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.HysteriaInboundOptions) (adapter.Inbound, error) {
	h, err := newSafeQUICInbound(ctx, router, logger, tag, "hysteria", options.ListenOptions, options.TLS)
	if err != nil {
		return nil, err
	}
	h.hy1 = &options
	for _, configured := range options.Users {
		name := configured.Name
		if name == "" {
			if configured.AuthString != "" {
				name = configured.AuthString
			} else {
				name = string(configured.Auth)
			}
		}
		password := configured.AuthString
		if password == "" {
			password = string(configured.Auth)
		}
		if name == "" || password == "" {
			common.Close(h.tlsConfig)
			return nil, fmt.Errorf("invalid Hysteria user")
		}
		h.users[name] = &quicUser{name: name, password: password}
	}
	h.current, err = h.buildSnapshot(h.users)
	if err != nil {
		common.Close(h.tlsConfig)
		return nil, err
	}
	return h, nil
}

func newSafeTUICInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TUICInboundOptions) (adapter.Inbound, error) {
	h, err := newSafeQUICInbound(ctx, router, logger, tag, "tuic", options.ListenOptions, options.TLS)
	if err != nil {
		return nil, err
	}
	h.tuic = &options
	h.router = uot.NewRouter(router, logger)
	for _, user := range options.Users {
		id, parseErr := uuid.FromString(user.UUID)
		if parseErr != nil {
			common.Close(h.tlsConfig)
			return nil, fmt.Errorf("invalid TUIC user UUID")
		}
		name := user.Name
		if name == "" {
			name = user.UUID
		}
		h.users[name] = &quicUser{name: name, password: user.Password, uuid: id}
	}
	h.current, err = h.buildSnapshot(h.users)
	if err != nil {
		common.Close(h.tlsConfig)
		return nil, err
	}
	return h, nil
}

func newSafeQUICInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag, protocol string, listen option.ListenOptions, tlsOptions *option.InboundTLSOptions) (*safeQUICInbound, error) {
	if tlsOptions == nil || !tlsOptions.Enabled {
		return nil, C.ErrTLSRequired
	}
	tlsConfig, err := tls.NewServer(ctx, logger, *tlsOptions)
	if err != nil {
		return nil, err
	}
	listen.UDPFragmentDefault = true
	h := &safeQUICInbound{Adapter: inbound.NewAdapter(protocol, tag), ctx: ctx, router: router, logger: logger, tlsConfig: tlsConfig,
		users: make(map[string]*quicUser), sessions: make(map[*quicSession]struct{}), parents: make(map[*quic.Conn]*quicSnapshot)}
	h.listener = listener.New(listener.Options{Context: ctx, Logger: logger, Listen: listen})
	return h, nil
}

func quicMasquerade(options *option.Hysteria2Masquerade) (http.Handler, error) {
	if options == nil || options.Type == "" {
		return nil, nil
	}
	switch options.Type {
	case C.Hysterai2MasqueradeTypeFile:
		return http.FileServer(http.Dir(options.FileOptions.Directory)), nil
	case C.Hysterai2MasqueradeTypeProxy:
		target, err := url.Parse(options.ProxyOptions.URL)
		if err != nil {
			return nil, fmt.Errorf("invalid Hysteria2 masquerade URL")
		}
		return &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			if !options.ProxyOptions.RewriteHost {
				r.Out.Host = r.In.Host
			}
		}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) }}, nil
	case C.Hysterai2MasqueradeTypeString:
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for key, values := range options.StringOptions.Headers {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			if options.StringOptions.StatusCode != 0 {
				w.WriteHeader(options.StringOptions.StatusCode)
			}
			_, _ = io.WriteString(w, options.StringOptions.Content)
		}), nil
	default:
		return nil, fmt.Errorf("unknown Hysteria2 masquerade type")
	}
}

func (h *safeQUICInbound) buildSnapshot(users map[string]*quicUser) (*quicSnapshot, error) {
	snapshot := &quicSnapshot{queue: &quicAcceptQueue{connections: make(chan *quic.Conn, 1), done: make(chan struct{})}}
	config := &quicTLSConfig{ServerConfig: h.tlsConfig, inbound: h, queue: snapshot.queue}
	names := make([]string, 0, len(users))
	for name := range users {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]*quicUser, 0, len(users))
	passwords := make([]string, 0, len(users))
	ids := make([][16]byte, 0, len(users))
	for _, name := range names {
		user := users[name]
		list = append(list, user)
		passwords = append(passwords, user.password)
		ids = append(ids, user.uuid)
	}
	if h.hy1 != nil {
		o := h.hy1
		service, err := hysteria.NewService[*quicUser](h.hy1ServiceOptions(o, config))
		if err != nil {
			return nil, err
		}
		service.UpdateUsers(list, passwords) // unpublished service: no concurrent authentication
		snapshot.service = service
	} else if h.hy2 != nil {
		o := h.hy2
		obfs := ""
		if o.Obfs != nil {
			obfs = o.Obfs.Password
		}
		timeout := time.Duration(o.UDPTimeout)
		if timeout == 0 {
			timeout = C.UDPTimeout
		}
		s, err := hysteria2.NewService[*quicUser](hysteria2.ServiceOptions{Context: h.ctx, Logger: h.logger, TLSConfig: config,
			BrutalDebug: o.BrutalDebug, SendBPS: uint64(o.UpMbps * hysteria.MbpsToBps), ReceiveBPS: uint64(o.DownMbps * hysteria.MbpsToBps),
			IgnoreClientBandwidth: o.IgnoreClientBandwidth, SalamanderPassword: obfs, UDPTimeout: timeout, Handler: h, MasqueradeHandler: h.masquerade})
		if err != nil {
			return nil, err
		}
		s.UpdateUsers(list, passwords) // unpublished service: no concurrent authentication
		snapshot.service = s
	} else {
		o := h.tuic
		timeout := time.Duration(o.UDPTimeout)
		if timeout == 0 {
			timeout = C.UDPTimeout
		}
		s, err := tuic.NewService[*quicUser](tuic.ServiceOptions{Context: h.ctx, Logger: h.logger, TLSConfig: config, CongestionControl: o.CongestionControl,
			AuthTimeout: time.Duration(o.AuthTimeout), ZeroRTTHandshake: o.ZeroRTTHandshake, Heartbeat: time.Duration(o.Heartbeat), UDPTimeout: timeout, Handler: h})
		if err != nil {
			return nil, err
		}
		s.UpdateUsers(list, ids, passwords)
		snapshot.service = s
	}
	return snapshot, nil
}

func (h *safeQUICInbound) hy1ServiceOptions(o *option.HysteriaInboundOptions, config aTLS.ServerConfig) hysteria.ServiceOptions {
	var sendBPS, receiveBPS uint64
	if o.Up.Value() > 0 {
		sendBPS = o.Up.Value()
	} else {
		sendBPS = uint64(o.UpMbps) * hysteria.MbpsToBps
	}
	if o.Down.Value() > 0 {
		receiveBPS = o.Down.Value()
	} else {
		receiveBPS = uint64(o.DownMbps) * hysteria.MbpsToBps
	}
	timeout := time.Duration(o.UDPTimeout)
	if timeout == 0 {
		timeout = C.UDPTimeout
	}
	return hysteria.ServiceOptions{
		Context: h.ctx, Logger: h.logger, SendBPS: sendBPS, ReceiveBPS: receiveBPS,
		XPlusPassword: o.Obfs, TLSConfig: config, Handler: h,
		UDPTimeout: timeout, ConnReceiveWindow: o.ReceiveWindowConn,
		StreamReceiveWindow: o.ReceiveWindowClient, MaxIncomingStreams: int64(o.MaxConnClient),
		DisableMTUDiscovery: o.DisableMTUDiscovery,
	}
}

func (h *safeQUICInbound) AddUsers(users []panel.UserInfo) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return net.ErrClosed
	}
	next := make(map[string]*quicUser, len(h.users)+len(users))
	for name, user := range h.users {
		next[name] = user
	}
	changed := false
	for _, user := range users {
		if user.Uuid == "" {
			return fmt.Errorf("empty QUIC user UUID")
		}
		if next[user.Uuid] != nil {
			continue
		}
		token := &quicUser{name: user.Uuid, password: user.Uuid}
		if h.tuic != nil {
			id, err := uuid.FromString(user.Uuid)
			if err != nil {
				return fmt.Errorf("invalid TUIC user UUID")
			}
			token.uuid = id
		}
		next[user.Uuid] = token
		changed = true
	}
	if !changed {
		return nil
	}
	return h.publishLocked(next)
}

func (h *safeQUICInbound) publishLocked(next map[string]*quicUser) error {
	snapshot, err := h.buildSnapshot(next)
	if err != nil {
		return err
	}
	if h.started {
		// The listener is already established; this only starts an SDK accept
		// loop on its private queue, without rebinding or closing the UDP socket.
		if err := snapshot.service.Start(nil); err != nil {
			snapshot.service.Close()
			return err
		}
	}
	old := h.current
	h.users, h.current = next, snapshot
	old.retired = true
	if old.refs == 0 {
		_ = old.service.Close()
	}
	return nil
}

func (h *safeQUICInbound) DelUsers(names []string) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return net.ErrClosed
	}
	next := make(map[string]*quicUser, len(h.users))
	for name, user := range h.users {
		next[name] = user
	}
	changed := false
	for _, name := range names {
		if next[name] != nil {
			delete(next, name)
			changed = true
		}
	}
	if !changed {
		h.mu.Unlock()
		return nil
	}
	if err := h.publishLocked(next); err != nil {
		h.mu.Unlock()
		return err
	}
	var revoked []io.Closer
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

func (h *safeQUICInbound) track(ctx context.Context, conn io.Closer) (string, func(), bool) {
	user, ok := auth.UserFromContext[*quicUser](ctx)
	h.mu.Lock()
	if !ok || user == nil || h.closed || h.users[user.name] != user {
		h.mu.Unlock()
		return "", nil, false
	}
	session := &quicSession{user: user, conn: conn}
	h.sessions[session] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return user.name, func() { once.Do(func() { h.mu.Lock(); delete(h.sessions, session); h.mu.Unlock() }) }, true
}

func (h *safeQUICInbound) metadata(user string, source, destination M.Socksaddr) adapter.InboundContext {
	return adapter.InboundContext{Inbound: h.Tag(), InboundType: h.Type(), User: user, Source: source, Destination: destination,
		OriginDestination: h.listener.UDPAddr(), InboundDetour: h.listener.ListenOptions().Detour, InboundOptions: h.listener.ListenOptions().InboundOptions}
}

func (h *safeQUICInbound) NewConnectionEx(ctx context.Context, conn net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	user, release, ok := h.track(ctx, conn)
	if !ok {
		N.CloseOnHandshakeFailure(conn, onClose, fmt.Errorf("QUIC authentication revoked"))
		return
	}
	h.router.RouteConnectionEx(ctx, conn, h.metadata(user, source, destination), func(err error) {
		release()
		if onClose != nil {
			onClose(err)
		}
	})
}

func (h *safeQUICInbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	user, release, ok := h.track(ctx, conn)
	if !ok {
		N.CloseOnHandshakeFailure(conn, onClose, fmt.Errorf("QUIC authentication revoked"))
		return
	}
	h.router.RoutePacketConnectionEx(ctx, conn, h.metadata(user, source, destination), func(err error) {
		release()
		if onClose != nil {
			onClose(err)
		}
	})
}

func (h *safeQUICInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return net.ErrClosed
	}
	if h.started {
		return nil
	}
	if err := h.tlsConfig.Start(); err != nil {
		return err
	}
	conn, err := h.listener.ListenUDP()
	if err != nil {
		return err
	}
	if err := h.current.service.Start(conn); err != nil {
		conn.Close()
		return err
	}
	h.started = true
	go h.acceptLoop()
	return nil
}

func (h *safeQUICInbound) acceptLoop() {
	for {
		conn, err := h.acceptor.Accept(h.ctx)
		if err != nil {
			return
		}
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			_ = conn.CloseWithError(0, "")
			return
		}
		snapshot := h.current
		snapshot.refs++
		h.parents[conn] = snapshot
		context.AfterFunc(conn.Context(), func() {
			h.mu.Lock()
			delete(h.parents, conn)
			snapshot.refs--
			if snapshot.retired && snapshot.refs == 0 {
				_ = snapshot.service.Close()
			}
			h.mu.Unlock()
		})
		// The SDK accept loop drains promptly. Bound the handoff queue rather
		// than accumulating unbounded unauthenticated QUIC connections.
		select {
		case snapshot.queue.connections <- conn:
		default:
			_ = conn.CloseWithError(0, "accept queue full")
		}
		h.mu.Unlock()
	}
}

func (h *safeQUICInbound) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	parents := make([]*quic.Conn, 0, len(h.parents))
	for conn := range h.parents {
		parents = append(parents, conn)
	}
	connections := make([]io.Closer, 0, len(h.sessions))
	for session := range h.sessions {
		connections = append(connections, session.conn)
	}
	current, acceptor := h.current, h.acceptor
	h.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	for _, conn := range parents {
		_ = conn.CloseWithError(0, "inbound closed")
	}
	return common.Close(current.service, acceptor, h.listener, h.tlsConfig)
}

// SDK services own only their private accept queue. The inbound alone owns the
// real listener and existing parent connections across authentication updates.
type quicAcceptQueue struct {
	connections chan *quic.Conn
	done        chan struct{}
	once        sync.Once
	addr        net.Addr
}

func (q *quicAcceptQueue) Accept(ctx context.Context) (*quic.Conn, error) {
	select {
	case <-q.done:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case conn := <-q.connections:
		return conn, nil
	}
}
func (q *quicAcceptQueue) Addr() net.Addr { return q.addr }
func (q *quicAcceptQueue) Close() error   { q.once.Do(func() { close(q.done) }); return nil }

type quicTLSConfig struct {
	aTLS.ServerConfig
	inbound *safeQUICInbound
	queue   *quicAcceptQueue
}

// The SDK calls this for each new service snapshot. Configure only the real
// listener in listen(), so an authentication update never mutates live TLS.
func (c *quicTLSConfig) ConfigureHTTP3() {}
func (c *quicTLSConfig) Listen(conn net.PacketConn, config *quic.Config) (qtls.Listener, error) {
	return c.listen(conn, config, false)
}
func (c *quicTLSConfig) ListenEarly(conn net.PacketConn, config *quic.Config) (qtls.EarlyListener, error) {
	return c.listen(conn, config, true)
}
func (c *quicTLSConfig) listen(conn net.PacketConn, config *quic.Config, early bool) (*quicAcceptQueue, error) {
	h := c.inbound // called under h.mu, before the snapshot is published
	if h.acceptor == nil {
		var err error
		if h.hy2 != nil {
			if err = qtls.ConfigureHTTP3(c.ServerConfig); err != nil {
				return nil, err
			}
		}
		if early {
			h.acceptor, err = qtls.ListenEarly(conn, c.ServerConfig, config)
		} else {
			h.acceptor, err = qtls.Listen(conn, c.ServerConfig, config)
		}
		if err != nil {
			return nil, err
		}
	}
	c.queue.addr = h.acceptor.Addr()
	return c.queue, nil
}

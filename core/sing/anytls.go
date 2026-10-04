package sing

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/padding"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// AnyTLS authenticates a pooled TLS connection once, then opens multiple streams.
// Track that parent connection until the entire session ends, not until one stream
// closes. Every stream also checks its immutable authentication snapshot against
// the current user record, so in-flight handshakes cannot bypass revocation.
type anyTLSInbound struct {
	inbound.Adapter
	router      adapter.ConnectionRouterEx
	logger      log.ContextLogger
	listener    *listener.Listener
	tlsConfig   tls.ServerConfig
	padding     []byte
	mu          sync.Mutex
	users       map[string]*option.AnyTLSUser
	service     *anytls.Service
	connections map[*anyTLSSession]struct{}
	closed      bool
}

type anyTLSSession struct {
	conn  net.Conn
	users map[string]*option.AnyTLSUser
	user  string
}

type anyTLSSessionKey struct{}

func newAnyTLSInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.AnyTLSInboundOptions) (adapter.Inbound, error) {
	h := &anyTLSInbound{Adapter: inbound.NewAdapter("anytls", tag), router: uot.NewRouter(router, logger), logger: logger, padding: padding.DefaultPaddingScheme, users: make(map[string]*option.AnyTLSUser), connections: make(map[*anyTLSSession]struct{})}
	if len(options.PaddingScheme) > 0 {
		h.padding = []byte(strings.Join(options.PaddingScheme, "\n"))
	}
	if options.TLS != nil && options.TLS.Enabled {
		var err error
		h.tlsConfig, err = tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
	}
	for _, user := range options.Users {
		if user.Name == "" || user.Password == "" {
			common.Close(h.tlsConfig)
			return nil, fmt.Errorf("empty AnyTLS user or password")
		}
		copy := user
		h.users[user.Name] = &copy
	}
	service, err := h.newService(h.users)
	if err != nil {
		common.Close(h.tlsConfig)
		return nil, err
	}
	h.service = service
	h.listener = listener.New(listener.Options{Context: ctx, Logger: logger, Network: []string{N.NetworkTCP}, Listen: options.ListenOptions, ConnectionHandler: h})
	return h, nil
}

func (h *anyTLSInbound) newService(users map[string]*option.AnyTLSUser) (*anytls.Service, error) {
	list := make([]anytls.User, 0, len(users))
	for _, user := range users {
		list = append(list, anytls.User{Name: user.Name, Password: user.Password})
	}
	// The pinned upstream UpdateUsers writes a map concurrently with authentication.
	// Publish a new immutable service instead; established healthy sessions retain it.
	return anytls.NewService(anytls.ServiceConfig{Users: list, PaddingScheme: h.padding, Handler: (*anyTLSHandler)(h), Logger: h.logger})
}

func (h *anyTLSInbound) AddUsers(users []panel.UserInfo) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return net.ErrClosed
	}
	next := make(map[string]*option.AnyTLSUser, len(h.users)+len(users))
	for name, user := range h.users {
		next[name] = user
	}
	for _, user := range users {
		if user.Uuid == "" {
			return fmt.Errorf("empty AnyTLS user UUID")
		}
		if old := next[user.Uuid]; old != nil && old.Password == user.Uuid {
			continue
		}
		next[user.Uuid] = &option.AnyTLSUser{Name: user.Uuid, Password: user.Uuid}
	}
	service, err := h.newService(next)
	if err != nil {
		return err
	}
	h.users, h.service = next, service
	return nil
}

func (h *anyTLSInbound) DelUsers(names []string) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return net.ErrClosed
	}
	next := make(map[string]*option.AnyTLSUser, len(h.users))
	for name, user := range h.users {
		next[name] = user
	}
	for _, name := range names {
		delete(next, name)
	}
	service, err := h.newService(next)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	h.users, h.service = next, service
	var revoked []net.Conn
	for session := range h.connections {
		if session.user != "" && (next[session.user] == nil || next[session.user] != session.users[session.user]) {
			revoked = append(revoked, session.conn)
		}
	}
	h.mu.Unlock()
	for _, conn := range revoked {
		conn.Close()
	}
	return nil
}

func (h *anyTLSInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		if err := h.tlsConfig.Start(); err != nil {
			return err
		}
	}
	return h.listener.Start()
}

func (h *anyTLSInbound) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	connections := make([]net.Conn, 0, len(h.connections))
	for session := range h.connections {
		connections = append(connections, session.conn)
	}
	h.mu.Unlock()
	for _, conn := range connections {
		conn.Close()
	}
	return common.Close(h.listener, h.tlsConfig)
}

func (h *anyTLSInbound) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	defer func() { conn.Close() }()
	if h.tlsConfig != nil {
		handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		tlsConn, err := tls.ServerHandshake(handshakeCtx, conn, h.tlsConfig)
		cancel()
		if err != nil {
			N.CloseOnHandshakeFailure(conn, onClose, err)
			return
		}
		conn = tlsConn
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		N.CloseOnHandshakeFailure(conn, onClose, net.ErrClosed)
		return
	}
	session := &anyTLSSession{conn: conn, users: h.users}
	service := h.service
	h.connections[session] = struct{}{}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.connections, session); h.mu.Unlock() }()
	ctx = context.WithValue(ctx, anyTLSSessionKey{}, session)
	// The SDK passes its onClose to every child stream. Parent cleanup belongs here.
	err := service.NewConnection(ctx, conn, metadata.Source, nil)
	if onClose != nil {
		onClose(err)
	}
}

type anyTLSHandler anyTLSInbound

func (h *anyTLSHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	session, _ := ctx.Value(anyTLSSessionKey{}).(*anyTLSSession)
	user, _ := auth.UserFromContext[string](ctx)
	h.mu.Lock()
	authorized := session != nil && user != "" && !h.closed && h.users[user] != nil && h.users[user] == session.users[user]
	if authorized {
		session.user = user
		session.conn.SetReadDeadline(time.Time{})
	}
	h.mu.Unlock()
	if !authorized {
		N.CloseOnHandshakeFailure(conn, onClose, fmt.Errorf("AnyTLS user revoked"))
		if session != nil {
			session.conn.Close()
		}
		return
	}
	metadata := adapter.InboundContext{Inbound: h.Tag(), InboundType: h.Type(), User: user, Source: source, Destination: destination.Unwrap()}
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.InboundOptions = h.listener.ListenOptions().InboundOptions
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

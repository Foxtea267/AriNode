package sing

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/trojan"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing-box/transport/v2rayhttp"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	STLS "github.com/sagernet/sing/common/tls"
	sHTTP "github.com/sagernet/sing/protocol/http"
)

// The pinned HTTPUpgrade server discards Hijack's bufio.Reader. An early VLESS
// header can already be in that reader and disappear, causing intermittent EOF.
// This frontend keeps it; authentication, mux, users, routes and stats remain in
// the native protocol service and this node's existing router.
type httpUpgradeInbound struct {
	adapter.Inbound
	ctx           context.Context
	logger        log.ContextLogger
	listenOptions option.ListenOptions
	options       option.V2RayHTTPUpgradeOptions
	listener      *listener.Listener
	tlsConfig     tls.ServerConfig
	server        *http.Server
	mu            sync.Mutex
	closed        bool
	connections   map[net.Conn]struct{}
}

func newHTTPUpgradeInbound(ctx context.Context, logger log.ContextLogger, inner adapter.Inbound, listen option.ListenOptions, tlsOptions *option.InboundTLSOptions, options option.V2RayHTTPUpgradeOptions) (adapter.Inbound, error) {
	h := &httpUpgradeInbound{Inbound: inner, ctx: ctx, logger: logger, listenOptions: listen, options: options, connections: map[net.Conn]struct{}{}}
	if !strings.HasPrefix(h.options.Path, "/") {
		h.options.Path = "/" + h.options.Path
	}
	if tlsOptions != nil && tlsOptions.Enabled {
		var err error
		h.tlsConfig, err = tls.NewServer(ctx, logger, *tlsOptions)
		if err != nil {
			_ = inner.Close()
			return nil, err
		}
		h.tlsConfig.SetNextProtos([]string{"http/1.1"})
	}
	h.listener = listener.New(listener.Options{Context: ctx, Logger: logger, Network: []string{"tcp"}, Listen: listen})
	h.server = &http.Server{Handler: h, ReadHeaderTimeout: C.TCPTimeout, MaxHeaderBytes: http.DefaultMaxHeaderBytes,
		BaseContext: func(net.Listener) context.Context { return ctx }, ConnContext: func(ctx context.Context, _ net.Conn) context.Context { return log.ContextWithNewID(ctx) }}
	return h, nil
}
func (h *httpUpgradeInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		if err := h.tlsConfig.Start(); err != nil {
			return err
		}
	}
	l, err := h.listener.ListenTCP()
	if err != nil {
		return err
	}
	if h.tlsConfig != nil {
		l = STLS.NewListener(l, h.tlsConfig)
	}
	go func() {
		if err := h.server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			h.logger.Error("HTTPUpgrade listener stopped")
		}
	}()
	return nil
}
func (h *httpUpgradeInbound) Close() error {
	h.mu.Lock()
	h.closed = true
	connections := make([]net.Conn, 0, len(h.connections))
	for c := range h.connections {
		connections = append(connections, c)
	}
	h.mu.Unlock()
	for _, c := range connections {
		_ = c.Close()
	}
	var tlsErr error
	if h.tlsConfig != nil {
		tlsErr = h.tlsConfig.Close()
	}
	ignoreClosed := func(err error) error {
		if E.IsClosed(err) {
			return nil
		}
		return err
	}
	return errors.Join(ignoreClosed(h.server.Close()), ignoreClosed(h.listener.Close()), ignoreClosed(h.Inbound.Close()), ignoreClosed(tlsErr))
}
func (h *httpUpgradeInbound) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != h.options.Path || (h.options.Host != "" && r.Host != h.options.Host) || !strings.EqualFold(r.Header.Get("Connection"), "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Key") != "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for key, values := range h.options.Headers.Build() {
		w.Header()[key] = values
	}
	w.Header().Set("Connection", "upgrade")
	w.Header().Set("Upgrade", "websocket")
	w.WriteHeader(http.StatusSwitchingProtocols)
	if f, ok := w.(interface{ FlushError() error }); ok {
		if err := f.FlushError(); err != nil {
			return
		}
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if n := buffered.Reader.Buffered(); n > 0 {
		cached := buf.NewSize(n)
		if _, err := cached.ReadFullFrom(buffered.Reader, n); err != nil {
			cached.Release()
			_ = conn.Close()
			return
		}
		conn = bufio.NewCachedConn(conn, cached)
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return
	}
	h.connections[conn] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	onClose := func(error) {
		once.Do(func() { h.mu.Lock(); delete(h.connections, conn); h.mu.Unlock(); _ = conn.Close() })
	}
	metadata := adapter.InboundContext{Source: sHTTP.SourceAddress(r), InboundDetour: h.listenOptions.Detour, InboundOptions: h.listenOptions.InboundOptions}
	h.Inbound.(adapter.ConnectionHandlerEx).NewConnectionEx(v2rayhttp.DupContext(r.Context()), conn, metadata, onClose)
}
func newVLESSInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, o option.VLESSInboundOptions) (adapter.Inbound, error) {
	if o.Transport == nil || o.Transport.Type != "httpupgrade" {
		return vless.NewInbound(ctx, router, logger, tag, o)
	}
	transport, tlsOptions := o.Transport, o.TLS
	// Keep native transport/TLS metadata (including ALPN fallback); its HTTP
	// listener is never started. The frontend owns the single listening socket.
	inner, err := vless.NewInbound(ctx, router, logger, tag, o)
	if err != nil {
		return nil, err
	}
	return newHTTPUpgradeInbound(ctx, logger, inner, o.ListenOptions, tlsOptions, transport.HTTPUpgradeOptions)
}
func newTrojanInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, o option.TrojanInboundOptions) (adapter.Inbound, error) {
	if o.Transport == nil || o.Transport.Type != "httpupgrade" {
		return trojan.NewInbound(ctx, router, logger, tag, o)
	}
	transport, tlsOptions := o.Transport, o.TLS
	inner, err := trojan.NewInbound(ctx, router, logger, tag, o)
	if err != nil {
		return nil, err
	}
	return newHTTPUpgradeInbound(ctx, logger, inner, o.ListenOptions, tlsOptions, transport.HTTPUpgradeOptions)
}

package sing

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/mux"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/trojan"
	"github.com/sagernet/sing-box/transport/v2ray"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type stableTrojanInbound struct{ *streamAuthInbound }

func newStableTrojanInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TrojanInboundOptions) (adapter.Inbound, error) {
	h := &streamAuthInbound{
		Adapter: inbound.NewAdapter("trojan", tag), ctx: ctx, logger: logger,
		fallbackRouter: router,
		users:          make(map[string]*streamAuthUser),
		parents:        make(map[*streamAuthParent]struct{}), sessions: make(map[*streamAuthSession]struct{}),
	}
	var err error
	h.router, err = mux.NewRouterWithOptions(&streamAuthRouteGate{ConnectionRouterEx: router, inbound: h}, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}
	if options.Fallback != nil && options.Fallback.Server != "" {
		h.fallbackAddr = options.Fallback.Build()
		if !h.fallbackAddr.IsValid() {
			return nil, fmt.Errorf("invalid Trojan fallback address")
		}
	}
	if len(options.FallbackForALPN) > 0 {
		if options.TLS == nil || !options.TLS.Enabled {
			return nil, fmt.Errorf("Trojan fallback per ALPN requires TLS")
		}
		h.fallbackALPN = make(map[string]M.Socksaddr)
		for alpn, target := range options.FallbackForALPN {
			addr := target.Build()
			if !addr.IsValid() {
				return nil, fmt.Errorf("invalid Trojan fallback address for ALPN")
			}
			h.fallbackALPN[alpn] = addr
		}
	}
	var fallback N.TCPConnectionHandlerEx
	if h.fallbackAddr.IsValid() || len(h.fallbackALPN) != 0 {
		fallback = adapter.NewUpstreamContextHandlerEx(h.trojanFallback, nil)
	}
	h.buildService = func(users map[string]*streamAuthUser) (streamAuthService, error) {
		service := trojan.NewService[*streamAuthUser](adapter.NewUpstreamContextHandlerEx(h.newConnectionEx, h.newPacketConnectionEx), fallback, logger)
		records, passwords := make([]*streamAuthUser, 0, len(users)), make([]string, 0, len(users))
		for _, user := range users {
			records, passwords = append(records, user), append(passwords, user.uuid)
		}
		if err := service.UpdateUsers(records, passwords); err != nil {
			return nil, fmt.Errorf("duplicate Trojan credentials")
		}
		if fallback == nil {
			return completeTrojanHeader{service}, nil
		}
		return service, nil
	}
	wrapper := &stableTrojanInbound{h}
	if err := wrapper.AddUsers(options.Users); err != nil {
		return nil, err
	}
	if options.TLS != nil {
		h.tlsConfig, err = tls.NewServerWithOptions(tls.ServerOptions{
			Context: ctx, Logger: logger, Options: *options.TLS,
			KTLSCompatible: common.PtrValueOrDefault(options.Transport).Type == "" && !common.PtrValueOrDefault(options.Multiplex).Enabled,
		})
		if err != nil {
			return nil, err
		}
	}
	if options.Transport != nil {
		h.transport, err = v2ray.NewServerTransport(ctx, logger, *options.Transport, h.tlsConfig, (*streamAuthTransportHandler)(h))
		if err != nil {
			_ = common.Close(h.tlsConfig)
			return nil, err
		}
	}
	h.listener = listener.New(listener.Options{Context: ctx, Logger: logger, Network: []string{N.NetworkTCP}, Listen: options.ListenOptions, ConnectionHandler: h})
	return wrapper, nil
}

// The pinned decoder treats a short first Read as invalid authentication.
// TCP/TLS/HTTPUpgrade are byte streams and can split the 56-byte key.
type completeTrojanHeader struct{ streamAuthService }

func (s completeTrojanHeader) NewConnection(ctx context.Context, conn net.Conn, source M.Socksaddr, close N.CloseHandlerFunc) error {
	return s.streamAuthService.NewConnection(ctx, &trojanHeaderConn{Conn: conn}, source, close)
}

type trojanHeaderConn struct {
	net.Conn
	started bool
}

func (c *trojanHeaderConn) Read(p []byte) (int, error) {
	if !c.started {
		c.started = true
		return io.ReadFull(c.Conn, p)
	}
	return c.Conn.Read(p)
}

func (c *trojanHeaderConn) Upstream() any           { return c.Conn }
func (c *trojanHeaderConn) ReaderReplaceable() bool { return c.started }
func (*trojanHeaderConn) WriterReplaceable() bool   { return true }

func (h *stableTrojanInbound) AddUsers(users []option.TrojanUser) error {
	records := make([]option.VLESSUser, len(users))
	for i, user := range users {
		records[i] = option.VLESSUser{Name: user.Name, UUID: user.Password}
	}
	return h.streamAuthInbound.AddUsers(records)
}

func (h *streamAuthInbound) trojanFallback(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	target := h.fallbackAddr
	if tlsConn, ok := common.Cast[tls.Conn](conn); ok && len(h.fallbackALPN) != 0 {
		if alpn := tlsConn.ConnectionState().NegotiatedProtocol; alpn != "" {
			target = h.fallbackALPN[alpn]
		}
	}
	if !target.IsValid() {
		_ = N.CloseOnHandshakeFailure(conn, onClose, os.ErrPermission)
		return
	}
	metadata.Inbound, metadata.InboundType, metadata.Destination = h.Tag(), h.Type(), target
	// Fallback is not an authenticated Trojan proxy session.
	h.fallbackRouter.RouteConnectionEx(ctx, conn, metadata, onClose)
}

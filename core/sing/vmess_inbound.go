package sing

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/mux"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/uot"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	boxVMess "github.com/sagernet/sing-box/protocol/vmess"
	"github.com/sagernet/sing-box/transport/v2ray"
	"github.com/sagernet/sing-vmess"
	"github.com/sagernet/sing/common"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/common/replay"
)

type stableVMessInbound struct{ *streamAuthInbound }

// Capture only the replay identifier consumed by the protocol decoder.
type authHeaderConn struct {
	net.Conn
	mu    sync.Mutex
	first [32]byte
	n     int
}

func (c *authHeaderConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n < len(c.first) {
		c.n += copy(c.first[c.n:], p[:n])
	}
	return n, err
}

func (c *authHeaderConn) check(size int, check func([]byte) bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n >= size && check(c.first[:size])
}

func (c *authHeaderConn) Upstream() any         { return c.Conn }
func (*authHeaderConn) ReaderReplaceable() bool { return false }
func (*authHeaderConn) WriterReplaceable() bool { return true }

func newStableVMessInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.VMessInboundOptions) (adapter.Inbound, error) {
	// Static OriginalPath configurations may still use the legacy AlterID
	// protocol. Panel VMess users always have AlterID zero.
	for _, user := range options.Users {
		if user.AlterId != 0 {
			return boxVMess.NewInbound(ctx, router, logger, tag, options)
		}
	}
	h := &streamAuthInbound{
		Adapter: inbound.NewAdapter("vmess", tag), ctx: ctx, logger: logger,
		users:   make(map[string]*streamAuthUser),
		parents: make(map[*streamAuthParent]struct{}), sessions: make(map[*streamAuthSession]struct{}),
		headerReplay:     replay.NewSimple(120 * time.Second).Check,
		headerReplaySize: 16,
	}
	var err error
	h.router, err = mux.NewRouterWithOptions(uot.NewRouter(&streamAuthRouteGate{ConnectionRouterEx: router, inbound: h}, logger), logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}
	var serviceOptions []vmess.ServiceOption
	if timeFunc := ntp.TimeFuncFromContext(ctx); timeFunc != nil {
		serviceOptions = append(serviceOptions, vmess.ServiceWithTimeFunc(timeFunc))
	}
	if options.Transport != nil && options.Transport.Type != "" {
		serviceOptions = append(serviceOptions, vmess.ServiceWithDisableHeaderProtection())
	}
	h.buildService = func(users map[string]*streamAuthUser) (streamAuthService, error) {
		if err := validateStreamUUIDs(users); err != nil {
			return nil, err
		}
		service := vmess.NewService[*streamAuthUser](adapter.NewUpstreamContextHandlerEx(h.newConnectionEx, h.newPacketConnectionEx), serviceOptions...)
		records, ids := make([]*streamAuthUser, 0, len(users)), make([]string, 0, len(users))
		for _, user := range users {
			records, ids = append(records, user), append(ids, user.uuid)
		}
		if err := service.UpdateUsers(records, ids, make([]int, len(records))); err != nil {
			return nil, fmt.Errorf("invalid VMess user configuration")
		}
		return service, nil
	}
	wrapper := &stableVMessInbound{h}
	if err := wrapper.AddUsers(options.Users); err != nil {
		return nil, err
	}
	if options.TLS != nil {
		h.tlsConfig, err = tls.NewServer(ctx, logger, *options.TLS)
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

func (h *stableVMessInbound) AddUsers(users []option.VMessUser) error {
	records := make([]option.VLESSUser, len(users))
	for i, user := range users {
		if user.AlterId != 0 {
			return fmt.Errorf("dynamic VMess users require AlterID zero")
		}
		records[i] = option.VLESSUser{Name: user.Name, UUID: user.UUID}
	}
	return h.streamAuthInbound.AddUsers(records)
}

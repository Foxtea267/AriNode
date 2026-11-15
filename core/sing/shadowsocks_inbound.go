package sing

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/mux"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	boxSS "github.com/sagernet/sing-box/protocol/shadowsocks"
	"github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type stableShadowsocksInbound struct {
	*streamAuthInbound
	minPacketSize int
}

// The TCP API is synchronous for a plain route. The Ex handler must retain
// ownership until the route finishes, including when mux returns asynchronously.
type shadowsocksAuthService struct {
	shadowsocks.MultiService[*streamAuthUser]
}

func (s shadowsocksAuthService) NewConnection(ctx context.Context, conn net.Conn, source M.Socksaddr, onClose N.CloseHandlerFunc) error {
	err := s.MultiService.NewConnection(ctx, conn, M.Metadata{Source: source})
	if err == nil {
		onClose(nil)
	}
	return err
}

func newStableShadowsocksInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksInboundOptions) (adapter.Inbound, error) {
	ss2022 := options.Method == "2022-blake3-aes-128-gcm" || options.Method == "2022-blake3-aes-256-gcm"
	if options.Managed || len(options.Destinations) != 0 || len(options.Users) == 0 || (!ss2022 && !common.Contains(shadowaead.List, options.Method)) {
		return boxSS.NewInbound(ctx, router, logger, tag, options)
	}
	h := &streamAuthInbound{
		Adapter: inbound.NewAdapter("shadowsocks", tag), ctx: ctx, logger: logger,
		users:   make(map[string]*streamAuthUser),
		parents: make(map[*streamAuthParent]struct{}), sessions: make(map[*streamAuthSession]struct{}),
	}
	wrapper := &stableShadowsocksInbound{streamAuthInbound: h}
	var err error
	h.router, err = mux.NewRouterWithOptions(uot.NewRouter(&streamAuthRouteGate{ConnectionRouterEx: router, inbound: h}, logger), logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(options.UDPTimeout)
	if timeout == 0 {
		timeout = C.UDPTimeout
	}
	var base *shadowaead_2022.MultiService[*streamAuthUser]
	if ss2022 {
		base, err = shadowaead_2022.NewMultiServiceWithPassword[*streamAuthUser](options.Method, options.Password, int64(timeout.Seconds()), wrapper, nil)
		if err != nil {
			return nil, fmt.Errorf("invalid Shadowsocks 2022 server credentials")
		}
		wrapper.minPacketSize = 2 * 16 // separate header and encrypted identity header
	}
	h.buildService = func(users map[string]*streamAuthUser) (streamAuthService, error) {
		var service shadowsocks.MultiService[*streamAuthUser]
		if base != nil {
			// The pinned SDK replaces the three credential maps on update.
			// Share the embedded Service so replay windows/NAT survive refresh.
			snapshot := *base
			service = &snapshot
		} else {
			var err error
			service, err = shadowaead.NewMultiService[*streamAuthUser](options.Method, int64(timeout.Seconds()), wrapper)
			if err != nil {
				return nil, err
			}
		}
		records, passwords := make([]*streamAuthUser, 0, len(users)), make([]string, 0, len(users))
		seen := make(map[string]bool, len(users))
		for _, user := range users {
			if seen[user.uuid] {
				return nil, fmt.Errorf("duplicate effective Shadowsocks user key")
			}
			seen[user.uuid] = true
			records, passwords = append(records, user), append(passwords, user.uuid)
		}
		if err := service.UpdateUsersWithPasswords(records, passwords); err != nil {
			return nil, fmt.Errorf("invalid Shadowsocks credentials")
		}
		return shadowsocksAuthService{service}, nil
	}
	if err := wrapper.AddUsers(options.Users); err != nil {
		return nil, err
	}
	h.listener = listener.New(listener.Options{
		Context: ctx, Logger: logger, Listen: options.ListenOptions, Network: options.Network.Build(),
		ConnectionHandler: h, PacketHandler: wrapper, ThreadUnsafePacketWriter: true,
	})
	return wrapper, nil
}

func (h *stableShadowsocksInbound) AddUsers(users []option.ShadowsocksUser) error {
	records := make([]option.VLESSUser, len(users))
	for i, user := range users {
		records[i] = option.VLESSUser{Name: user.Name, UUID: user.Password}
	}
	return h.streamAuthInbound.AddUsers(records)
}

func (h *stableShadowsocksInbound) NewConnection(ctx context.Context, conn net.Conn, source M.Metadata) error {
	metadata := adapter.InboundContext{Source: source.Source, Destination: source.Destination,
		Protocol: source.Protocol, InboundDetour: h.listener.ListenOptions().Detour, InboundOptions: h.listener.ListenOptions().InboundOptions}
	done := make(chan error, 1)
	h.newConnectionEx(ctx, conn, metadata, func(err error) { done <- err })
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = conn.Close()
		return ctx.Err()
	}
}

func (h *stableShadowsocksInbound) NewPacketConnection(ctx context.Context, conn N.PacketConn, source M.Metadata) error {
	// A native UDP association has no authenticated TCP parent.
	ctx = context.WithValue(ctx, streamAuthParentKey{}, &streamAuthParent{})
	metadata := adapter.InboundContext{Source: source.Source, Destination: source.Destination,
		Protocol: source.Protocol, InboundDetour: h.listener.ListenOptions().Detour, InboundOptions: h.listener.ListenOptions().InboundOptions}
	done := make(chan error, 1)
	h.newPacketConnectionEx(log.ContextWithNewID(ctx), conn, metadata, func(err error) { done <- err })
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = conn.Close()
		return ctx.Err()
	}
}

func (h *stableShadowsocksInbound) NewError(_ context.Context, err error) {
	// SDK error text can contain authentication material.
	if closer, ok := err.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

type shadowsocksPacketSocket struct {
	N.PacketWriter
	local net.Addr
}

func (s shadowsocksPacketSocket) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, os.ErrInvalid
}
func (s shadowsocksPacketSocket) Close() error                     { return nil }
func (s shadowsocksPacketSocket) LocalAddr() net.Addr              { return s.local }
func (s shadowsocksPacketSocket) SetDeadline(time.Time) error      { return os.ErrInvalid }
func (s shadowsocksPacketSocket) SetReadDeadline(time.Time) error  { return os.ErrInvalid }
func (s shadowsocksPacketSocket) SetWriteDeadline(time.Time) error { return os.ErrInvalid }

func (h *stableShadowsocksInbound) NewPacketEx(packet *buf.Buffer, source M.Socksaddr) {
	if packet.Len() < h.minPacketSize {
		packet.Release()
		return
	}
	h.mu.Lock()
	service, closed := h.service.(shadowsocksAuthService), h.closed
	h.mu.Unlock()
	if closed {
		packet.Release()
		return
	}
	if err := service.NewPacket(h.ctx, shadowsocksPacketSocket{h.listener.PacketWriter(), h.listener.UDPAddr()}, packet, M.Metadata{Source: source}); err != nil {
		packet.Release()
	}
}

var _ shadowsocks.Handler = (*stableShadowsocksInbound)(nil)

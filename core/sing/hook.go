package sing

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/common/rate"

	"github.com/Foxtea267/AriNode/limiter"

	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	N "github.com/sagernet/sing/common/network"
)

var _ adapter.ConnectionTracker = (*HookServer)(nil)

type HookServer struct {
	counter sync.Map // map[string]*counter.TrafficCounter
}

// Stable protocol adapters bind authorization and counter acquisition under
// their own generation lock. A delayed routed callback cannot create a fresh
// unreportable counter after its credential was revoked and re-added.
type routedUserCounterKey struct{}
type routedUserCounterLease func(*HookServer, string, string) (*counter.TrafficStorage, bool)

func (h *HookServer) userCounter(tag, user string) *counter.TrafficStorage {
	stored, _ := h.counter.LoadOrStore(tag, counter.NewTrafficCounter())
	return stored.(*counter.TrafficCounter).GetCounter(user)
}

func (h *HookServer) routedCounter(ctx context.Context, tag, user string) (*counter.TrafficStorage, bool) {
	if lease, ok := ctx.Value(routedUserCounterKey{}).(routedUserCounterLease); ok {
		return lease(h, tag, user)
	}
	return h.userCounter(tag, user), true
}

func (h *HookServer) ModeList() []string {
	return nil
}

func (h *HookServer) RoutedConnection(ctx context.Context, conn net.Conn, m adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) net.Conn {
	l, err := limiter.GetLimiter(m.Inbound)
	if err != nil {
		log.Warn("get limiter for ", m.Inbound, " error: ", err)
		return conn
	}
	taguuid := format.UserTag(m.Inbound, m.User)
	ip := m.Source.Addr.String()
	if b, r := l.CheckLimit(taguuid, ip, true, true); r {
		conn.Close()
		log.Error("[", m.Inbound, "] user limited by IP or connection policy")
		return conn
	} else if b != nil {
		conn = rate.NewConnRateLimiter(conn, b)
	}
	if l != nil {
		destStr := m.Destination.AddrString()
		protocol := m.Protocol
		if l.CheckDomainRule(destStr) {
			log.Error(fmt.Sprintf(
				"Access to domain %s rejected by rule",
				destStr))
			conn.Close()
			return conn
		}
		if len(protocol) != 0 {
			if l.CheckProtocolRule(protocol) {
				log.Error(fmt.Sprintf(
					"Access to protocol %s rejected by rule",
					protocol))
				conn.Close()
				return conn
			}
		}
	}
	storage, allowed := h.routedCounter(ctx, m.Inbound, m.User)
	if !allowed {
		_ = conn.Close()
		return conn
	}
	conn = counter.NewConnCounter(conn, storage, l.TrackDeviceIP(taguuid, ip))
	return conn
}

func (h *HookServer) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, m adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) N.PacketConn {
	l, err := limiter.GetLimiter(m.Inbound)
	if err != nil {
		log.Warn("get limiter for ", m.Inbound, " error: ", err)
		return conn
	}
	ip := m.Source.Addr.String()
	taguuid := format.UserTag(m.Inbound, m.User)
	if b, r := l.CheckLimit(taguuid, ip, false, false); r {
		conn.Close()
		log.Error("[", m.Inbound, "] user limited by IP or connection policy")
		return conn
	} else if b != nil {
		conn = rate.NewPacketConnRateLimiter(conn, b)
	}
	if l != nil {
		destStr := m.Destination.AddrString()
		protocol := m.Destination.Network()
		if l.CheckDomainRule(destStr) {
			log.Error(fmt.Sprintf(
				"Access to domain %s rejected by rule",
				destStr))
			conn.Close()
			return conn
		}
		if len(protocol) != 0 {
			if l.CheckProtocolRule(protocol) {
				log.Error(fmt.Sprintf(
					"Access to protocol %s rejected by rule",
					protocol))
				conn.Close()
				return conn
			}
		}
	}
	storage, allowed := h.routedCounter(ctx, m.Inbound, m.User)
	if !allowed {
		_ = conn.Close()
		return conn
	}
	conn = counter.NewPacketConnCounter(conn, storage, l.TrackDeviceIP(taguuid, ip))
	return conn
}

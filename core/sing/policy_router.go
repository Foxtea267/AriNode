package sing

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/Foxtea267/AriNode/common/nodepolicy"
	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
	log "github.com/sirupsen/logrus"
)

type singPolicyState struct {
	sniff         bool
	sniffOverride bool
	router        adapter.Router
	mu            sync.Mutex
	refs          int
	retired       bool
	close         func()
}

func (s *singPolicyState) release() {
	s.mu.Lock()
	s.refs--
	close := s.retired && s.refs == 0
	s.mu.Unlock()
	if close {
		s.close()
	}
}
func (s *singPolicyState) retire() {
	s.mu.Lock()
	s.retired = true
	close := s.refs == 0
	s.mu.Unlock()
	if close {
		s.close()
	}
}

type nodeRouter struct {
	adapter.Router
	mu             sync.RWMutex
	state          *singPolicyState
	closed         bool
	nextConnection uint64
	connections    map[uint64]io.Closer
}

func (r *nodeRouter) swap(s *singPolicyState) {
	r.mu.Lock()
	old := r.state
	r.state = s
	r.mu.Unlock()
	if old != nil {
		old.retire()
	}
}
func (r *nodeRouter) acquire(conn io.Closer) (adapter.Router, func(), *singPolicyState, bool) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, nil, false
	}
	r.nextConnection++
	id := r.nextConnection
	if r.connections == nil {
		r.connections = map[uint64]io.Closer{}
	}
	r.connections[id] = conn
	s := r.state
	if s != nil {
		s.mu.Lock()
		s.refs++
		s.mu.Unlock()
	}
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		delete(r.connections, id)
		r.mu.Unlock()
		if s != nil {
			s.release()
		}
	}
	if s == nil {
		return r.Router, release, nil, true
	}
	return s.router, release, s, true
}

// A deleted inbound can leave an already authenticated multiplex transport
// alive in the upstream protocol service. Reject its future substreams and close
// this node's existing streams; it must never fall back to the shared router.
func (r *nodeRouter) shutdown() {
	r.mu.Lock()
	r.closed = true
	old := r.state
	r.state = nil
	connections := make([]io.Closer, 0, len(r.connections))
	for _, conn := range r.connections {
		connections = append(connections, conn)
	}
	r.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	if old != nil {
		old.retire()
	}
}
func (r *nodeRouter) RouteConnectionEx(ctx context.Context, conn net.Conn, m adapter.InboundContext, onClose N.CloseHandlerFunc) {
	router, release, state, ok := r.acquire(conn)
	if !ok {
		N.CloseOnHandshakeFailure(conn, onClose, errors.New("node router is closed"))
		return
	}
	if state != nil {
		m.InboundOptions.SniffEnabled = state.sniff
		m.InboundOptions.SniffOverrideDestination = state.sniffOverride
	}
	once := sync.Once{}
	router.RouteConnectionEx(ctx, conn, m, func(err error) {
		once.Do(func() {
			release()
			if onClose != nil {
				onClose(err)
			}
		})
	})
}
func (r *nodeRouter) RoutePacketConnectionEx(ctx context.Context, conn N.PacketConn, m adapter.InboundContext, onClose N.CloseHandlerFunc) {
	router, release, state, ok := r.acquire(conn)
	if !ok {
		N.CloseOnHandshakeFailure(conn, onClose, errors.New("node router is closed"))
		return
	}
	if state != nil {
		m.InboundOptions.SniffEnabled = state.sniff
		m.InboundOptions.SniffOverrideDestination = state.sniffOverride
	}
	once := sync.Once{}
	router.RoutePacketConnectionEx(ctx, conn, m, func(err error) {
		once.Do(func() {
			release()
			if onClose != nil {
				onClose(err)
			}
		})
	})
}
func (r *nodeRouter) RouteConnection(ctx context.Context, conn net.Conn, m adapter.InboundContext) error {
	done := make(chan struct{})
	r.RouteConnectionEx(ctx, conn, m, func(error) { close(done) })
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		_ = conn.Close()
		return ctx.Err()
	}
}
func (r *nodeRouter) RoutePacketConnection(ctx context.Context, conn N.PacketConn, m adapter.InboundContext) error {
	done := make(chan struct{})
	r.RoutePacketConnectionEx(ctx, conn, m, func(error) { close(done) })
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		_ = conn.Close()
		return ctx.Err()
	}
}

type policyTracker struct {
	*HookServer
	Node   string
	Labels map[adapter.Rule]nodepolicy.RouteLog
}

func (p *policyTracker) record(rule adapter.Rule) {
	if m, ok := p.Labels[rule]; ok {
		log.WithFields(log.Fields{"node": p.Node, "route": m.Name, "outbound": m.Outbound}).Debug("Panel route selected")
	}
}
func (p *policyTracker) RoutedConnection(ctx context.Context, c net.Conn, m adapter.InboundContext, r adapter.Rule, o adapter.Outbound) net.Conn {
	p.record(r)
	return p.HookServer.RoutedConnection(ctx, c, m, r, o)
}
func (p *policyTracker) RoutedPacketConnection(ctx context.Context, c N.PacketConn, m adapter.InboundContext, r adapter.Rule, o adapter.Outbound) N.PacketConn {
	p.record(r)
	return p.HookServer.RoutedPacketConnection(ctx, c, m, r, o)
}

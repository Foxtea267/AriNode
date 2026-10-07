package dispatcher

import (
	"github.com/Foxtea267/AriNode/common/nodepolicy"
	"github.com/xtls/xray-core/features/routing"
	"sync"
)

// A retired generation stays alive until dispatches using it finish. Publishing
// a new generation never waits for an unrelated node or a long-lived stream.
type NodePolicy struct {
	Router  routing.Router
	Logs    map[string]nodepolicy.RouteLog
	mu      sync.Mutex
	refs    int
	retired bool
	close   func()
}

func NewNodePolicy(router routing.Router, logs map[string]nodepolicy.RouteLog, close func()) *NodePolicy {
	return &NodePolicy{Router: router, Logs: logs, close: close}
}
func (p *NodePolicy) release() {
	p.mu.Lock()
	p.refs--
	close := p.retired && p.refs == 0
	p.mu.Unlock()
	if close {
		p.close()
	}
}
func (p *NodePolicy) retire() {
	p.mu.Lock()
	p.retired = true
	close := p.refs == 0
	p.mu.Unlock()
	if close {
		p.close()
	}
}
func (d *DefaultDispatcher) SetNodePolicy(tag string, p *NodePolicy) {
	d.nodePolicyMu.Lock()
	old := d.nodePolicies[tag]
	if p == nil {
		delete(d.nodePolicies, tag)
	} else {
		if d.nodePolicies == nil {
			d.nodePolicies = make(map[string]*NodePolicy)
		}
		d.nodePolicies[tag] = p
	}
	d.nodePolicyMu.Unlock()
	if old != nil {
		old.retire()
	}
}
func (d *DefaultDispatcher) acquireNodePolicy(tag string) *NodePolicy {
	d.nodePolicyMu.RLock()
	p := d.nodePolicies[tag]
	if p != nil {
		p.mu.Lock()
		p.refs++
		p.mu.Unlock()
	}
	d.nodePolicyMu.RUnlock()
	return p
}

package panel

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/Foxtea267/AriNode/common/nodepolicy"
)

// Public panel names share one model with the runtime normalization layer.
type OutboundConfig = nodepolicy.OutboundConfig
type CustomRouteRule = nodepolicy.CustomRouteRule
type RouteMatch = nodepolicy.RouteMatch
type RouteAction = nodepolicy.RouteAction
type MultiplexConfig = nodepolicy.MultiplexConfig
type BrutalConfig = nodepolicy.BrutalConfig

func (n *NodeInfo) RoutingEnabled() bool {
	if n.Common == nil {
		return false
	}
	c := n.Common
	if len(c.CustomOutbounds)+len(c.CustomRoutes)+len(c.CustomRouteRules) > 0 {
		return true
	}
	for _, r := range c.Routes {
		switch strings.ToLower(r.Action) {
		case "proxy", "route", "direct":
			return true
		}
	}
	return false
}
func (n *NodeInfo) PolicyConfig() (nodepolicy.Policy, error) {
	p := n.Policy
	if n.Common != nil {
		c := n.Common
		p.Outbounds = c.CustomOutbounds
		p.Structured = c.CustomRouteRules
		p.Raw = c.CustomRoutes
		p.Ordinary = nil
		if n.RoutingEnabled() {
			for _, r := range c.Routes {
				matches, err := routeMatches(r.Match)
				if err != nil {
					return p, fmt.Errorf("route %d: %w", r.Id, err)
				}
				rules, err := nodepolicy.LegacyRules(r.Id, strings.ToLower(r.Action), r.ActionValue, matches)
				if err != nil {
					return p, err
				}
				p.Ordinary = append(p.Ordinary, rules...)
			}
		}
	}
	return p, p.Validate()
}

// Advanced rules must be evaluated by the ordered kernel router, not preempted
// by the historical audit/block hook. Unextended panels keep that hook intact.
func (n *NodeInfo) LimiterRules() *Rules {
	if n.RoutingEnabled() {
		return &Rules{}
	}
	return &n.Rules
}

func (n *NodeInfo) inboundView() NodeInfo {
	v := *n
	v.Policy = nodepolicy.Policy{}
	v.Rules = Rules{}
	v.PullInterval = 0
	v.PushInterval = 0
	clean := func(c CommonNode) CommonNode {
		c.CustomOutbounds = nil
		c.CustomRoutes = nil
		c.CustomRouteRules = nil
		c.Routes = nil
		c.BaseConfig = nil
		return c
	}
	if n.Common != nil {
		c := clean(*n.Common)
		v.Common = &c
	}
	if n.VAllss != nil {
		c := *n.VAllss
		c.CommonNode = clean(c.CommonNode)
		v.VAllss = &c
	}
	if n.Shadowsocks != nil {
		c := *n.Shadowsocks
		c.CommonNode = clean(c.CommonNode)
		v.Shadowsocks = &c
	}
	if n.Trojan != nil {
		c := *n.Trojan
		c.CommonNode = clean(c.CommonNode)
		v.Trojan = &c
	}
	if n.Tuic != nil {
		c := *n.Tuic
		c.CommonNode = clean(c.CommonNode)
		v.Tuic = &c
	}
	if n.AnyTls != nil {
		c := *n.AnyTls
		c.CommonNode = clean(c.CommonNode)
		v.AnyTls = &c
	}
	if n.Mieru != nil {
		c := *n.Mieru
		c.CommonNode = clean(c.CommonNode)
		v.Mieru = &c
	}
	if n.Hysteria != nil {
		c := *n.Hysteria
		c.CommonNode = clean(c.CommonNode)
		v.Hysteria = &c
	}
	if n.Hysteria2 != nil {
		c := *n.Hysteria2
		c.CommonNode = clean(c.CommonNode)
		v.Hysteria2 = &c
	}
	return v
}
func InboundConfigEqual(a, b *NodeInfo) bool {
	if a == nil || b == nil {
		return false
	}
	return reflect.DeepEqual(a.inboundView(), b.inboundView())
}

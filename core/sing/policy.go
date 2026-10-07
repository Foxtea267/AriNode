package sing

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/nodepolicy"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route"
	sjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/service"
	log "github.com/sirupsen/logrus"
)

type compiledPolicy struct {
	Outbounds []policyOutbound
	Rules     []option.Rule
	Logs      []nodepolicy.RouteLog
}

func (b *Sing) compilePolicy(tag, generation string, info *panel.NodeInfo, options *conf.Options) (*compiledPolicy, error) {
	p, err := info.PolicyConfig()
	if err != nil {
		return nil, err
	}
	tags := nodepolicy.Tags(tag, generation, p.Outbounds)
	result := &compiledPolicy{}
	direct := &option.DirectOutboundOptions{}
	if options.SendIP != "" {
		addr, err := netip.ParseAddr(options.SendIP)
		if err != nil {
			return nil, fmt.Errorf("invalid SendIP")
		}
		if addr.Is4() {
			direct.Inet4BindAddress = (*badoption.Addr)(&addr)
		} else {
			direct.Inet6BindAddress = (*badoption.Addr)(&addr)
		}
	}
	result.Outbounds = append(result.Outbounds, policyOutbound{Outbound: option.Outbound{Type: "direct", Tag: tags["direct"], Options: direct}}, policyOutbound{Outbound: option.Outbound{Type: "block", Tag: tags["block"], Options: &option.StubOptions{}}})
	for _, outbound := range p.OrderedOutbounds() {
		out, err := singOutbound(b.ctx, outbound, tags)
		if err != nil {
			return nil, err
		}
		result.Outbounds = append(result.Outbounds, out)
	}
	// Local OriginalPath rules are explicit local overrides and retain their tags.
	result.Rules = append(result.Rules, b.localRules...)
	for range b.localRules {
		result.Logs = append(result.Logs, nodepolicy.RouteLog{Name: "local", Outbound: "local"})
	}
	appendRule := func(value map[string]any, name, panelTag string) error {
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("route %q: invalid configuration", name)
		}
		r, err := sjson.UnmarshalExtendedContext[option.Rule](b.ctx, data)
		if err != nil {
			return fmt.Errorf("route %q: invalid native sing-box rule (payload omitted)", name)
		}
		result.Rules = append(result.Rules, r)
		result.Logs = append(result.Logs, nodepolicy.RouteLog{Name: name, Outbound: panelTag})
		return nil
	}
	for _, stage := range p.Stages() {
		for i, r := range stage.Rules {
			if r.Disabled {
				continue
			}
			name := r.Name
			if name == "" {
				name = stage.Name + ":" + strconv.Itoa(i)
			}
			if len(r.Match.IPCIDRs) > 0 {
				// The pinned native route engine does not resolve a domain merely
				// because an IP condition exists. Resolve only after the other
				// conditions match, preserving higher-priority domain routes.
				guard := r
				guard.Match.IPCIDRs = nil
				value := singRule(guard, tags)
				delete(value, "outbound")
				value["action"] = "resolve"
				if err := appendRule(value, name+":resolve", ""); err != nil {
					return nil, err
				}
			}
			if err := appendRule(singRule(r, tags), name, nodepolicy.Target(r.Action)); err != nil {
				return nil, err
			}
		}
		for i, raw := range stage.Raw {
			action, _ := raw["action"].(string)
			if (action == "" || action == "route") && raw["outbound"] == nil {
				return nil, fmt.Errorf("custom_routes[%d]: sing-box route requires outbound", i)
			}
			value, err := nodepolicy.RewriteRaw(raw, tags)
			if err != nil {
				return nil, err
			}
			name := "custom:" + strconv.Itoa(i)
			panelTag, _ := raw["outbound"].(string)
			if err := appendRule(value, name, panelTag); err != nil {
				return nil, err
			}
		}
	}
	fallback := tags["direct"]
	if b.localFinal != "" {
		fallback = b.localFinal
	}
	if err := appendRule(map[string]any{"outbound": fallback}, "default", "direct"); err != nil {
		return nil, err
	}
	return result, nil
}
func singRule(r nodepolicy.CustomRouteRule, tags map[string]string) map[string]any {
	m := r.Match
	value := map[string]any{"outbound": tags[nodepolicy.Target(r.Action)]}
	domains := []string{}
	suffixes := append([]string(nil), m.DomainSuffixes...)
	for _, v := range m.Domains {
		if strings.HasPrefix(v, "*.") {
			suffixes = append(suffixes, strings.TrimPrefix(v, "*."))
		} else {
			domains = append(domains, v)
		}
	}
	for i, v := range suffixes {
		suffixes[i] = strings.TrimPrefix(v, "*.")
	}
	if len(domains) > 0 {
		value["domain"] = domains
	}
	if len(suffixes) > 0 {
		value["domain_suffix"] = suffixes
	}
	if len(m.DomainKeywords) > 0 {
		value["domain_keyword"] = m.DomainKeywords
	}
	if len(m.DomainRegex) > 0 {
		value["domain_regex"] = m.DomainRegex
	}
	if len(m.IPCIDRs) > 0 {
		value["ip_cidr"] = m.IPCIDRs
	}
	if len(m.SourceCIDRs) > 0 {
		value["source_ip_cidr"] = m.SourceCIDRs
	}
	if len(m.Networks) > 0 {
		value["network"] = m.Networks
	}
	if len(m.Protocols) > 0 {
		value["protocol"] = m.Protocols
	}
	for key, values := range map[string][]string{"port": m.Ports, "source_port": m.SourcePorts} {
		var ports []uint16
		var ranges []string
		for _, v := range values {
			a, z, _ := nodepolicy.PortRange(v)
			if a == z {
				ports = append(ports, a)
			} else {
				ranges = append(ranges, strconv.Itoa(int(a))+":"+strconv.Itoa(int(z)))
			}
		}
		if len(ports) > 0 {
			value[key] = ports
		}
		if len(ranges) > 0 {
			value[key+"_range"] = ranges
		}
	}
	// sing-box groups destination domains and IP CIDRs with OR by default.
	// Structured policy requires both when both categories are supplied.
	if len(m.IPCIDRs) > 0 && len(domains)+len(suffixes)+len(m.DomainKeywords)+len(m.DomainRegex) > 0 {
		domain := map[string]any{}
		for _, key := range []string{"domain", "domain_suffix", "domain_keyword", "domain_regex"} {
			if v, ok := value[key]; ok {
				domain[key] = v
				delete(value, key)
			}
		}
		target := value["outbound"]
		delete(value, "outbound")
		return map[string]any{"type": "logical", "mode": "and", "rules": []any{domain, value}, "outbound": target}
	}
	return value
}

func (b *Sing) UpdateNodePolicy(tag string, info *panel.NodeInfo, options *conf.Options) error {
	b.policyMu.Lock()
	defer b.policyMu.Unlock()
	wrapper := b.nodeRouters[tag]
	if wrapper == nil {
		return fmt.Errorf("node %s: router is unavailable", tag)
	}
	return b.updatePolicyLocked(tag, info, options, wrapper)
}
func (b *Sing) updatePolicyLocked(tag string, info *panel.NodeInfo, options *conf.Options, wrapper *nodeRouter) error {
	if b.closing {
		return fmt.Errorf("sing-box is closing")
	}
	if !info.RoutingEnabled() {
		wrapper.swap(nil)
		return nil
	}
	b.policyGeneration++
	compiled, err := b.compilePolicy(tag, strconv.FormatUint(b.policyGeneration, 10), info, options)
	if err != nil {
		return fmt.Errorf("node %s: %w", tag, err)
	}
	native := route.NewRouter(b.ctx, b.logFactory, option.RouteOptions{Rules: compiled.Rules}, option.DNSOptions{})
	if err := native.Initialize(compiled.Rules, nil); err != nil {
		_ = native.Close()
		return fmt.Errorf("node %s: initialize native routes failed (payload omitted)", tag)
	}
	var added []policyOutbound
	cleanup := func() {
		_ = native.Close()
		b.lifecycleMu.Lock()
		defer b.lifecycleMu.Unlock()
		// Box.Close owns all registered outbounds during shutdown. Its pinned
		// manager does not permit concurrent Remove while its slice is cleared.
		if b.closing {
			return
		}
		for i := len(added) - 1; i >= 0; i-- {
			o := added[i]
			var err error
			if o.Endpoint {
				err = service.FromContext[adapter.EndpointManager](b.ctx).Remove(o.Tag)
			} else {
				err = b.box.Outbound().Remove(o.Tag)
			}
			if err != nil {
				log.WithField("node", tag).Warn("Failed to retire node outbound")
			}
		}
	}
	for _, o := range compiled.Outbounds {
		if _, exists := b.box.Outbound().Outbound(o.Tag); exists {
			cleanup()
			return fmt.Errorf("node %s: generated outbound namespace already exists", tag)
		}
		logger := b.logFactory.NewLogger("outbound/" + o.Type)
		var err error
		if o.Endpoint {
			err = service.FromContext[adapter.EndpointManager](b.ctx).Create(b.ctx, b.router, logger, o.Tag, o.Type, o.Options)
		} else {
			err = b.box.Outbound().Create(b.ctx, b.router, logger, o.Tag, o.Type, o.Options)
		}
		if err != nil {
			cleanup()
			return fmt.Errorf("node %s: register %s outbound failed (credentials omitted)", tag, o.Type)
		}
		added = append(added, o)
	}
	for _, stage := range adapter.ListStartStages {
		if err := native.Start(stage); err != nil {
			cleanup()
			return fmt.Errorf("node %s: start native routes failed (payload omitted)", tag)
		}
	}
	labels := map[adapter.Rule]nodepolicy.RouteLog{}
	for i, r := range native.Rules() {
		labels[r] = compiled.Logs[i]
	}
	native.AppendTracker(&policyTracker{HookServer: b.hookServer, Node: tag, Labels: labels})
	state := &singPolicyState{router: native, close: cleanup}
	if options.SingOptions != nil {
		state.sniff = options.SingOptions.SniffEnabled
		state.sniffOverride = options.SingOptions.SniffOverrideDestination
	}
	wrapper.swap(state)
	log.WithFields(log.Fields{"node": tag, "outbounds": len(compiled.Outbounds) - 2, "routes": len(compiled.Rules) - 1}).Info("sing-box panel policy applied")
	return nil
}

// Context is provided by the existing box, so DNS, outbound managers, stats,
// connection tracking and OriginalPath rule-sets remain shared and functional.

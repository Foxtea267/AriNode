package xray

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/nodepolicy"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core/xray/app/dispatcher"
	log "github.com/sirupsen/logrus"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	native "github.com/xtls/xray-core/infra/conf"
)

type compiledPolicy struct {
	outbounds []*core.OutboundHandlerConfig
	route     *native.RouterConfig
	logs      map[string]nodepolicy.RouteLog
	tags      []string
}

func compilePolicy(tag, generation string, info *panel.NodeInfo, options *conf.Options) (*compiledPolicy, error) {
	p, err := info.PolicyConfig()
	if err != nil {
		return nil, err
	}
	if err := info.Common.Multiplex.Validate(); err != nil {
		return nil, err
	}
	tags := nodepolicy.Tags(tag, generation, p.Outbounds)
	result := &compiledPolicy{route: &native.RouterConfig{}, logs: map[string]nodepolicy.RouteLog{}}
	direct, err := buildOutbound(options, tags["direct"])
	if err != nil {
		return nil, err
	}
	result.outbounds = append(result.outbounds, direct)
	result.tags = append(result.tags, tags["direct"])
	block := native.OutboundDetourConfig{Protocol: "blackhole", Tag: tags["block"]}
	b, err := block.Build()
	if err != nil {
		return nil, err
	}
	result.outbounds = append(result.outbounds, b)
	result.tags = append(result.tags, tags["block"])
	for _, o := range p.OrderedOutbounds() {
		switch o.Protocol {
		case "vmess", "vless", "trojan", "shadowsocks", "socks", "http", "wireguard":
		default:
			return nil, fmt.Errorf("outbound %q: unsupported Xray protocol %q", o.Tag, o.Protocol)
		}
		settings, err := nodepolicy.CloneMap(o.Settings)
		if err != nil {
			return nil, err
		}
		outbound := native.OutboundDetourConfig{Tag: tags[o.Tag], Protocol: o.Protocol}
		// Optional transport settings belong to the outbound, not protocol settings.
		if value, ok := settings["streamSettings"]; ok {
			data, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("outbound %q: invalid transport", o.Tag)
			}
			outbound.StreamSetting = &native.StreamConfig{}
			if err = json.Unmarshal(data, outbound.StreamSetting); err != nil {
				return nil, fmt.Errorf("outbound %q: invalid transport", o.Tag)
			}
			delete(settings, "streamSettings")
		}
		data, err := json.Marshal(settings)
		if err != nil {
			return nil, fmt.Errorf("outbound %q: invalid settings", o.Tag)
		}
		raw := json.RawMessage(data)
		outbound.Settings = &raw
		if o.ProxyTag != "" {
			outbound.ProxySettings = &native.ProxyConfig{Tag: tags[o.ProxyTag]}
		}
		compiled, err := outbound.Build()
		if err != nil {
			return nil, fmt.Errorf("outbound %q: invalid %s settings (credentials omitted)", o.Tag, o.Protocol)
		}
		result.outbounds = append(result.outbounds, compiled)
		result.tags = append(result.tags, tags[o.Tag])
	}
	appendRule := func(value map[string]any, name, panelTag string) error {
		value["inboundTag"] = []string{tag}
		ruleID := "panel-rule:" + strconv.Itoa(len(result.route.RuleList))
		value["ruleTag"] = ruleID
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("route %q: invalid configuration", name)
		}
		result.route.RuleList = append(result.route.RuleList, data)
		result.logs[ruleID] = nodepolicy.RouteLog{Name: name, Outbound: panelTag}
		return nil
	}
	for _, stage := range p.Stages() {
		for i, r := range stage.Rules {
			if r.Disabled {
				continue
			}
			if len(r.Match.IPCIDRs) > 0 {
				strategy := "IPOnDemand"
				result.route.DomainStrategy = &strategy
			}
			name := r.Name
			if name == "" {
				name = stage.Name + ":" + strconv.Itoa(i)
			}
			value := xrayRule(r, tags)
			if err := appendRule(value, name, nodepolicy.Target(r.Action)); err != nil {
				return nil, err
			}
		}
		for i, raw := range stage.Raw {
			if raw["ip"] != nil {
				strategy := "IPOnDemand"
				result.route.DomainStrategy = &strategy
			}
			for key := range raw {
				if !xrayRouteFields[key] {
					return nil, fmt.Errorf("custom_routes[%d]: unsupported Xray field %q", i, key)
				}
			}
			value, err := nodepolicy.RewriteRaw(raw, tags)
			if err != nil {
				return nil, err
			}
			if _, ok := value["outboundTag"]; !ok {
				return nil, fmt.Errorf("custom_routes[%d]: Xray outboundTag is required", i)
			}
			if typ, ok := value["type"]; ok && typ != "field" {
				return nil, fmt.Errorf("custom_routes[%d]: Xray type must be field", i)
			}
			value["type"] = "field"
			panelTag, _ := raw["outboundTag"].(string)
			name, _ := raw["ruleTag"].(string)
			if name == "" {
				name = "custom:" + strconv.Itoa(i)
			}
			if err := appendRule(value, name, panelTag); err != nil {
				return nil, err
			}
		}
	}
	if err := appendRule(map[string]any{"type": "field", "outboundTag": tags["direct"]}, "default", "direct"); err != nil {
		return nil, err
	}
	if _, err := result.route.Build(); err != nil {
		return nil, fmt.Errorf("invalid native Xray route configuration (payload omitted)")
	}
	return result, nil
}

var xrayRouteFields = map[string]bool{"type": true, "ruleTag": true, "outboundTag": true, "domain": true, "domains": true, "ip": true, "port": true, "network": true, "sourceIP": true, "source": true, "sourcePort": true, "user": true, "vlessRoute": true, "inboundTag": true, "protocol": true, "attrs": true, "localIP": true, "localPort": true}

func xrayRule(r nodepolicy.CustomRouteRule, tags map[string]string) map[string]any {
	m := r.Match
	rule := map[string]any{"type": "field", "outboundTag": tags[nodepolicy.Target(r.Action)]}
	var domains []string
	for _, v := range m.Domains {
		if strings.HasPrefix(v, "*.") {
			domains = append(domains, "domain:"+strings.TrimPrefix(v, "*."))
		} else {
			domains = append(domains, "full:"+v)
		}
	}
	for _, v := range m.DomainSuffixes {
		domains = append(domains, "domain:"+strings.TrimPrefix(v, "*."))
	}
	for _, v := range m.DomainKeywords {
		domains = append(domains, "keyword:"+v)
	}
	for _, v := range m.DomainRegex {
		domains = append(domains, "regexp:"+v)
	}
	if len(domains) > 0 {
		rule["domain"] = domains
	}
	if len(m.IPCIDRs) > 0 {
		rule["ip"] = m.IPCIDRs
	}
	if len(m.SourceCIDRs) > 0 {
		rule["sourceIP"] = m.SourceCIDRs
	}
	if len(m.Ports) > 0 {
		rule["port"] = strings.Join(m.Ports, ",")
	}
	if len(m.SourcePorts) > 0 {
		rule["sourcePort"] = strings.Join(m.SourcePorts, ",")
	}
	if len(m.Networks) > 0 {
		rule["network"] = strings.Join(m.Networks, ",")
	}
	if len(m.Protocols) > 0 {
		rule["protocol"] = m.Protocols
	}
	return rule
}

func (c *Xray) UpdateNodePolicy(tag string, info *panel.NodeInfo, options *conf.Options) error {
	c.access.Lock()
	defer c.access.Unlock()
	if c.ihm == nil {
		return fmt.Errorf("Xray is not running")
	}
	return c.updatePolicyLocked(tag, info, options)
}
func (c *Xray) updatePolicyLocked(tag string, info *panel.NodeInfo, options *conf.Options) error {
	if c.closing {
		return fmt.Errorf("Xray is closing")
	}
	if !info.RoutingEnabled() {
		c.dispatcher.SetNodePolicy(tag, nil)
		return nil
	}
	c.policyGeneration++
	compiled, err := compilePolicy(tag, strconv.FormatUint(c.policyGeneration, 10), info, options)
	if err != nil {
		return fmt.Errorf("node %s: %w", tag, err)
	}
	var added []string
	cleanup := func() {
		c.lifecycleMu.Lock()
		defer c.lifecycleMu.Unlock()
		if c.closing {
			return
		}
		for i := len(added) - 1; i >= 0; i-- {
			if err := c.removeOutbound(added[i]); err != nil {
				log.WithField("node", tag).Warn("Failed to retire node outbound")
			}
		}
	}
	for i, o := range compiled.outbounds {
		if err := c.addOutbound(o); err != nil {
			cleanup()
			return fmt.Errorf("node %s: register outbound %d failed (credentials omitted)", tag, i)
		}
		added = append(added, compiled.tags[i])
	}
	cfg, err := compiled.route.Build()
	if err != nil {
		cleanup()
		return fmt.Errorf("node %s: invalid routes", tag)
	}
	object, err := core.CreateObject(c.Server, cfg)
	if err != nil {
		cleanup()
		return fmt.Errorf("node %s: initialize router failed", tag)
	}
	router, ok := object.(routing.Router)
	if !ok {
		cleanup()
		return fmt.Errorf("node %s: invalid router feature", tag)
	}
	state := dispatcher.NewNodePolicy(router, compiled.logs, func() { _ = router.Close(); cleanup() })
	c.dispatcher.SetNodePolicy(tag, state)
	log.WithFields(log.Fields{"node": tag, "outbounds": len(compiled.outbounds) - 2, "routes": len(compiled.route.RuleList) - 1}).Info("Xray panel policy applied")
	return nil
}

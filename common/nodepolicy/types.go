// Package nodepolicy validates panel policy without depending on a proxy kernel.
package nodepolicy

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type OutboundConfig struct {
	Tag      string         `json:"tag"`
	Protocol string         `json:"protocol"`
	Settings map[string]any `json:"settings,omitempty"`
	ProxyTag string         `json:"proxy_tag,omitempty"`
}
type CustomRouteRule struct {
	Name     string      `json:"name,omitempty"`
	Disabled bool        `json:"disabled,omitempty"`
	Match    RouteMatch  `json:"match,omitempty"`
	Action   RouteAction `json:"action"`
}
type RouteMatch struct {
	Domains        []string `json:"domains,omitempty"`
	DomainSuffixes []string `json:"domain_suffixes,omitempty"`
	IPCIDRs        []string `json:"ip_cidrs,omitempty"`
	Ports          []string `json:"ports,omitempty"`
	Networks       []string `json:"networks,omitempty"`
	SourceCIDRs    []string `json:"source_cidrs,omitempty"`
	SourcePorts    []string `json:"source_ports,omitempty"`
	// Normalized legacy matches; shared by both compilers.
	DomainKeywords []string `json:"-"`
	DomainRegex    []string `json:"-"`
	Protocols      []string `json:"-"`
}
type RouteAction struct {
	Type   string `json:"type"`
	Target string `json:"target,omitempty"`
}
type MultiplexConfig struct {
	Enabled        bool          `json:"enabled"`
	Protocol       string        `json:"protocol,omitempty"`
	MaxConnections int           `json:"max_connections,omitempty"`
	MinStreams     int           `json:"min_streams,omitempty"`
	MaxStreams     int           `json:"max_streams,omitempty"`
	Padding        bool          `json:"padding"`
	Brutal         *BrutalConfig `json:"brutal,omitempty"`
}
type BrutalConfig struct {
	Enabled  bool `json:"enabled"`
	UpMbps   int  `json:"up_mbps"`
	DownMbps int  `json:"down_mbps"`
}

// Use one runtime mux model, accepting historical local Enable/UpMbps names.
func (m *MultiplexConfig) UnmarshalJSON(data []byte) error {
	type plain MultiplexConfig
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	aliases := map[string]string{"Enable": "enabled", "Protocol": "protocol", "MaxConnections": "max_connections", "MinStreams": "min_streams", "MaxStreams": "max_streams", "Padding": "padding", "Brutal": "brutal"}
	for old, key := range aliases {
		if _, ok := fields[key]; !ok {
			if value, ok := fields[old]; ok {
				fields[key] = value
			}
		}
		delete(fields, old)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, (*plain)(m))
}
func (b *BrutalConfig) UnmarshalJSON(data []byte) error {
	type plain BrutalConfig
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for old, key := range map[string]string{"Enable": "enabled", "UpMbps": "up_mbps", "DownMbps": "down_mbps"} {
		if _, ok := fields[key]; !ok {
			if v, ok := fields[old]; ok {
				fields[key] = v
			}
		}
		delete(fields, old)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, (*plain)(b))
}
func (m *MultiplexConfig) Validate() error {
	if m == nil {
		return nil
	}
	switch m.Protocol {
	case "", "smux", "yamux", "h2mux":
	default:
		return fmt.Errorf("unsupported multiplex protocol %q", m.Protocol)
	}
	if m.MaxConnections < 0 || m.MinStreams < 0 || m.MaxStreams < 0 {
		return fmt.Errorf("multiplex stream and connection counts must be nonnegative")
	}
	if m.Brutal != nil {
		b := m.Brutal
		if b.UpMbps < 0 || b.DownMbps < 0 || (b.Enabled && (b.UpMbps == 0 || b.DownMbps == 0)) {
			return fmt.Errorf("enabled multiplex brutal requires positive up_mbps and down_mbps")
		}
	}
	return nil
}

type Policy struct {
	Outbounds  []OutboundConfig
	Structured []CustomRouteRule
	Raw        []map[string]any
	Ordinary   []CustomRouteRule
}

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (p Policy) Validate() error {
	if len(p.Outbounds) > 256 || len(p.Structured)+len(p.Raw)+len(p.Ordinary) > 4096 {
		return fmt.Errorf("node policy exceeds 256 outbounds or 4096 rules")
	}
	tags := map[string]bool{"direct": true, "block": true}
	for _, o := range p.Outbounds {
		if !tagPattern.MatchString(o.Tag) {
			return fmt.Errorf("outbound tag must be 1-128 letters, digits, dots, underscores or hyphens")
		}
		if tags[o.Tag] || strings.EqualFold(o.Tag, "direct") || strings.EqualFold(o.Tag, "block") {
			return fmt.Errorf("duplicate or reserved outbound tag %q", o.Tag)
		}
		if o.Protocol == "" {
			return fmt.Errorf("outbound %q: protocol is required", o.Tag)
		}
		for _, key := range []string{"tag", "type", "protocol", "detour", "proxy_tag", "proxySettings"} {
			if _, ok := o.Settings[key]; ok {
				return fmt.Errorf("outbound %q: settings.%s cannot override the normalized outbound", o.Tag, key)
			}
		}
		tags[o.Tag] = true
	}
	edges := make(map[string]string)
	for _, o := range p.Outbounds {
		if o.ProxyTag != "" {
			if !tags[o.ProxyTag] {
				return fmt.Errorf("outbound %q: proxy_tag references unknown outbound tag %q", o.Tag, o.ProxyTag)
			}
			if o.ProxyTag == "block" {
				return fmt.Errorf("outbound %q: cannot chain through block", o.Tag)
			}
			edges[o.Tag] = o.ProxyTag
		}
	}
	visiting := map[string]int{}
	var visit func(string) error
	visit = func(tag string) error {
		if visiting[tag] == 1 {
			return fmt.Errorf("proxy_tag cycle involving outbound %q", tag)
		}
		if visiting[tag] == 2 {
			return nil
		}
		visiting[tag] = 1
		if next := edges[tag]; next != "" {
			if err := visit(next); err != nil {
				return err
			}
		}
		visiting[tag] = 2
		return nil
	}
	for tag := range edges {
		if err := visit(tag); err != nil {
			return err
		}
	}
	for _, rules := range [][]CustomRouteRule{p.Structured, p.Ordinary} {
		for i, r := range rules {
			if r.Disabled {
				continue
			}
			if err := ValidateRule(r, tags); err != nil {
				return fmt.Errorf("route %q (index %d): %w", r.Name, i, err)
			}
		}
	}
	for i, r := range p.Raw {
		if err := ValidateRaw(r, tags, 0); err != nil {
			return fmt.Errorf("custom_routes[%d]: %w", i, err)
		}
	}
	return nil
}
func ValidateRule(r CustomRouteRule, tags map[string]bool) error {
	switch r.Action.Type {
	case "direct", "block":
		if r.Action.Target != "" {
			return fmt.Errorf("%s action cannot have a target", r.Action.Type)
		}
	case "route":
		if !tags[r.Action.Target] {
			return fmt.Errorf("route references unknown outbound tag %q", r.Action.Target)
		}
	default:
		return fmt.Errorf("unsupported action %q", r.Action.Type)
	}
	for _, cidrs := range [][]string{r.Match.IPCIDRs, r.Match.SourceCIDRs} {
		for _, v := range cidrs {
			if _, err := netip.ParsePrefix(v); err != nil {
				if _, err = netip.ParseAddr(v); err != nil {
					return fmt.Errorf("invalid CIDR %q", v)
				}
			}
		}
	}
	for _, ports := range [][]string{r.Match.Ports, r.Match.SourcePorts} {
		for _, v := range ports {
			if _, _, err := PortRange(v); err != nil {
				return err
			}
		}
	}
	for _, n := range r.Match.Networks {
		if n != "tcp" && n != "udp" {
			return fmt.Errorf("invalid network %q", n)
		}
	}
	for _, v := range r.Match.DomainRegex {
		if _, err := regexp.Compile(v); err != nil {
			return fmt.Errorf("invalid domain regexp")
		}
	}
	for _, values := range [][]string{r.Match.Domains, r.Match.DomainSuffixes, r.Match.DomainKeywords, r.Match.Protocols} {
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("empty match value")
			}
		}
	}
	return nil
}
func PortRange(value string) (uint16, uint16, error) {
	parts := strings.Split(value, "-")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("invalid port range %q", value)
	}
	a, err := strconv.Atoi(parts[0])
	if err != nil || a < 1 || a > 65535 {
		return 0, 0, fmt.Errorf("invalid port %q", value)
	}
	b := a
	if len(parts) == 2 {
		b, err = strconv.Atoi(parts[1])
		if err != nil || b < a || b > 65535 {
			return 0, 0, fmt.Errorf("invalid port range %q", value)
		}
	}
	return uint16(a), uint16(b), nil
}

func Target(action RouteAction) string {
	if action.Type == "route" {
		return action.Target
	}
	return action.Type
}

// Internal tags include an unambiguous node namespace and a replacement generation.
func Tags(node, generation string, outbounds []OutboundConfig) map[string]string {
	prefix := fmt.Sprintf("an:%d:%s:%s:", len(node), node, generation)
	tags := map[string]string{"direct": prefix + "direct", "block": prefix + "block"}
	for _, o := range outbounds {
		tags[o.Tag] = prefix + o.Tag
	}
	return tags
}
func Resolve(tags map[string]string, tag string) (string, error) {
	if v, ok := tags[tag]; ok {
		return v, nil
	}
	return "", fmt.Errorf("route references unknown outbound tag %q", tag)
}

// Ordinary panel matches are alternatives; categories become separate rules.
// Structured rules keep AND between match categories and OR within a category.
func LegacyRules(id int, action, target string, values []string) ([]CustomRouteRule, error) {
	if action == "dns" {
		return nil, nil
	}
	a := RouteAction{Type: action}
	if action == "proxy" || action == "route" {
		a.Type = "route"
		a.Target = target
	}
	if action != "block" && action != "direct" && action != "proxy" && action != "route" {
		return nil, fmt.Errorf("route %d: unsupported action %q", id, action)
	}
	matches := make([]RouteMatch, 4)
	for _, value := range values {
		v := strings.TrimSpace(value)
		if v == "" {
			continue
		}
		switch {
		case strings.HasPrefix(v, "protocol:"):
			matches[3].Protocols = append(matches[3].Protocols, strings.TrimPrefix(v, "protocol:"))
		case strings.HasPrefix(v, "regexp:"):
			matches[0].DomainRegex = append(matches[0].DomainRegex, strings.TrimPrefix(v, "regexp:"))
		case strings.HasPrefix(v, "keyword:"):
			matches[0].DomainKeywords = append(matches[0].DomainKeywords, strings.TrimPrefix(v, "keyword:"))
		case strings.HasPrefix(v, "full:"):
			matches[0].Domains = append(matches[0].Domains, strings.TrimPrefix(v, "full:"))
		case strings.HasPrefix(v, "domain:"):
			matches[0].DomainSuffixes = append(matches[0].DomainSuffixes, strings.TrimPrefix(v, "domain:"))
		case strings.HasPrefix(v, "geoip:") || strings.HasPrefix(v, "geosite:"):
			return nil, fmt.Errorf("route %d: geo datasets require a kernel-native custom_routes rule", id)
		default:
			if _, err := netip.ParseAddr(v); err == nil {
				matches[1].IPCIDRs = append(matches[1].IPCIDRs, v)
			} else if strings.Contains(v, "/") {
				matches[1].IPCIDRs = append(matches[1].IPCIDRs, v)
			} else {
				matches[0].DomainSuffixes = append(matches[0].DomainSuffixes, strings.TrimPrefix(v, "*."))
			}
		}
	}
	var rules []CustomRouteRule
	for _, m := range matches {
		if len(m.Domains)+len(m.DomainSuffixes)+len(m.IPCIDRs)+len(m.Protocols)+len(m.DomainKeywords)+len(m.DomainRegex) > 0 {
			rules = append(rules, CustomRouteRule{Name: fmt.Sprintf("panel:%d", id), Match: m, Action: a})
		}
	}
	return rules, nil
}

// Dependencies before dependants, for running outbound managers.
func (p Policy) OrderedOutbounds() []OutboundConfig {
	byTag := map[string]OutboundConfig{}
	for _, o := range p.Outbounds {
		byTag[o.Tag] = o
	}
	seen := map[string]bool{}
	var result []OutboundConfig
	var add func(string)
	add = func(tag string) {
		if seen[tag] {
			return
		}
		seen[tag] = true
		o, ok := byTag[tag]
		if !ok {
			return
		}
		if o.ProxyTag != "" {
			add(o.ProxyTag)
		}
		result = append(result, o)
	}
	for _, o := range p.Outbounds {
		add(o.Tag)
	}
	return result
}

// Explicit stage order used by both compilers; default/fallback is appended last.
type Stage struct {
	Name  string
	Rules []CustomRouteRule
	Raw   []map[string]any
}

func (p Policy) Stages() []Stage {
	return []Stage{{Name: "structured", Rules: p.Structured}, {Name: "raw", Raw: p.Raw}, {Name: "defaults"}, {Name: "panel", Rules: p.Ordinary}}
}

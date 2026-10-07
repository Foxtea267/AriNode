package nodepolicy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidationAndChains(t *testing.T) {
	valid := Policy{Outbounds: []OutboundConfig{{Tag: "a", Protocol: "socks"}, {Tag: "b", Protocol: "socks", ProxyTag: "a"}}, Structured: []CustomRouteRule{{Name: "ai", Match: RouteMatch{DomainSuffixes: []string{"claude.com"}, Ports: []string{"443", "8000-9000"}, Networks: []string{"tcp"}, SourceCIDRs: []string{"192.0.2.0/24"}, SourcePorts: []string{"1024-65535"}}, Action: RouteAction{Type: "route", Target: "b"}}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if ordered := valid.OrderedOutbounds(); ordered[0].Tag != "a" || ordered[1].Tag != "b" {
		t.Fatal("invalid dependency order")
	}
	cases := []struct {
		name, want string
		modify     func(*Policy)
	}{
		{"missing", "unknown outbound", func(p *Policy) { p.Structured[0].Action.Target = "missing" }},
		{"cycle", "cycle", func(p *Policy) { p.Outbounds[0].ProxyTag = "b" }},
		{"missing-chain", "unknown outbound", func(p *Policy) { p.Outbounds[0].ProxyTag = "missing" }},
		{"duplicate", "duplicate", func(p *Policy) { p.Outbounds[1].Tag = "a" }},
		{"reserved", "reserved", func(p *Policy) { p.Outbounds[0].Tag = "direct" }},
		{"empty-tag", "tag must", func(p *Policy) { p.Outbounds[0].Tag = "" }},
		{"empty-protocol", "protocol is required", func(p *Policy) { p.Outbounds[0].Protocol = "" }},
		{"port", "invalid port", func(p *Policy) { p.Structured[0].Match.Ports = []string{"0"} }},
		{"reverse-port", "invalid port", func(p *Policy) { p.Structured[0].Match.SourcePorts = []string{"9000-8000"} }},
		{"cidr", "invalid CIDR", func(p *Policy) { p.Structured[0].Match.IPCIDRs = []string{"not/cidr"} }},
		{"network", "invalid network", func(p *Policy) { p.Structured[0].Match.Networks = []string{"sctp"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			p.Outbounds = append([]OutboundConfig(nil), valid.Outbounds...)
			p.Structured = append([]CustomRouteRule(nil), valid.Structured...)
			tc.modify(&p)
			if err := p.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v want %q", err, tc.want)
			}
		})
	}
}
func TestWildcardAndNamespace(t *testing.T) {
	rules, err := LegacyRules(17, "proxy", "sg01", []string{"claude.com", "*.claude.com", "anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || len(rules[0].Match.DomainSuffixes) != 3 || len(rules[0].Match.DomainRegex) != 0 {
		t.Fatal("wildcards must be suffixes")
	}
	a := Tags("node-A", "1", []OutboundConfig{{Tag: "sg01"}})
	b := Tags("node-B", "1", []OutboundConfig{{Tag: "sg01"}})
	if a["sg01"] == b["sg01"] || a["direct"] == a["sg01"] {
		t.Fatal("namespace collision")
	}
	if _, err := LegacyRules(1, "forward", "sg01", []string{"claude.com"}); err == nil {
		t.Fatal("invented panel alias accepted")
	}
}
func TestMultiplexLocalJSONCompatibility(t *testing.T) {
	var m MultiplexConfig
	if err := json.Unmarshal([]byte(`{"Enable":true,"Protocol":"smux","MaxConnections":4,"Brutal":{"Enable":true,"UpMbps":10,"DownMbps":20}}`), &m); err != nil {
		t.Fatal(err)
	}
	if !m.Enabled || m.MaxConnections != 4 || m.Brutal == nil || !m.Brutal.Enabled || m.Brutal.UpMbps != 10 {
		t.Fatal("lost legacy local mux settings")
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"enabled":false,"Enable":true,"padding":false}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Enabled {
		t.Fatal("canonical field must take precedence")
	}
}
func TestRawReferencesAndMalformedRules(t *testing.T) {
	for _, raw := range []string{`{"outbound":"missing"}`, `{"outboundTag":"missing"}`, `{"rules":[4]}`, `{"rules":{}}`, `{"balancerTag":"global"}`} {
		var r map[string]any
		json.Unmarshal([]byte(raw), &r)
		if err := ValidateRaw(r, map[string]bool{"direct": true}, 0); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	var raw map[string]any
	json.Unmarshal([]byte(`{"type":"logical","mode":"and","outbound":"sg01","rules":[{"domain_suffix":["claude.com"]}]}`), &raw)
	rewritten, err := RewriteRaw(raw, map[string]string{"sg01": "node-a::sg01"})
	if err != nil {
		t.Fatal(err)
	}
	if rewritten["outbound"] != "node-a::sg01" || raw["outbound"] != "sg01" {
		t.Fatal("raw rewriting mutated panel data")
	}
}

package xray

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
)

func TestNativeXrayPanelPolicyCompiler(t *testing.T) {
	options := &conf.Options{XrayOptions: conf.NewXrayOptions()}
	info := &panel.NodeInfo{Common: &panel.CommonNode{CustomOutbounds: []panel.OutboundConfig{{Tag: "proxy-b", Protocol: "socks", ProxyTag: "proxy-a", Settings: map[string]any{"servers": []any{map[string]any{"address": "1.2.3.4", "port": 1080}}}}, {Tag: "proxy-a", Protocol: "socks", Settings: map[string]any{"servers": []any{map[string]any{"address": "2.3.4.5", "port": 1080}}}}}, CustomRouteRules: []panel.CustomRouteRule{{Name: "ai", Match: panel.RouteMatch{Domains: []string{"*.claude.com"}, DomainSuffixes: []string{"claude.ai"}, Ports: []string{"443", "8000-9000"}, Networks: []string{"tcp", "udp"}, SourceCIDRs: []string{"192.0.2.0/24"}, SourcePorts: []string{"1024-65535"}}, Action: panel.RouteAction{Type: "route", Target: "proxy-b"}}}, CustomRoutes: []map[string]any{{"type": "field", "domain": []string{"domain:openai.com"}, "outboundTag": "proxy-b"}}, Routes: []panel.Route{{Id: 9, Match: []string{"*.anthropic.com"}, Action: "proxy", ActionValue: "proxy-b"}}, Multiplex: &panel.MultiplexConfig{Enabled: true, Protocol: "smux"}}}
	compiled, err := compilePolicy("HK01", "1", info, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.outbounds) != 4 || !strings.HasSuffix(compiled.outbounds[2].Tag, ":proxy-a") || !strings.HasSuffix(compiled.outbounds[3].Tag, ":proxy-b") {
		t.Fatal("native outbounds were not ordered and namespaced")
	}
	var rule map[string]any
	if err := json.Unmarshal(compiled.route.RuleList[0], &rule); err != nil {
		t.Fatal(err)
	}
	domains := rule["domain"].([]any)
	if domains[0] != "domain:claude.com" || domains[1] != "domain:claude.ai" || rule["outboundTag"] != compiled.outbounds[3].Tag || rule["sourcePort"] != "1024-65535" || rule["port"] != "443,8000-9000" {
		t.Fatalf("invalid native rule: %v", rule)
	}
	native, err := compiled.route.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(native.Rule) != 4 {
		t.Fatal("route priority stages lost")
	}
	// Xray server mux is negotiated on the wire; no fictitious inbound mux field.
	info.Common.CustomOutbounds[0].Protocol = "naive"
	if _, err := compilePolicy("HK01", "1", info, options); err == nil {
		t.Fatal("unsupported protocol was ignored")
	}
}
func TestXrayInvalidNativeRouteRejected(t *testing.T) {
	options := &conf.Options{XrayOptions: conf.NewXrayOptions()}
	for _, raw := range []map[string]any{{"outboundTag": "missing"}, {"outbound": "direct"}, {"domain": []string{"a.com"}}, {"outboundTag": "direct", "unknown": true}} {
		info := &panel.NodeInfo{Common: &panel.CommonNode{CustomRoutes: []map[string]any{raw}}}
		if _, err := compilePolicy("node", "1", info, options); err == nil {
			t.Fatalf("accepted bad native route: %v", raw)
		}
	}
}

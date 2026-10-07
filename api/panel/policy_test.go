package panel

import (
	"encoding/json"
	"fmt"
	"github.com/Foxtea267/AriNode/conf"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPanelPolicyDecodeAndConfigChanges(t *testing.T) {
	payload := map[string]any{"server_port": 12345, "network": "tcp", "tls": 0, "custom_outbounds": []any{map[string]any{"tag": "sg01", "protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": "1.2.3.4", "port": 1080}}}}}, "custom_routes": []any{map[string]any{"domain_suffix": []string{"example.com"}, "outbound": "sg01"}}, "custom_route_rules": []any{map[string]any{"name": "ai-sg", "match": map[string]any{"domain_suffixes": []string{"claude.com"}}, "action": map[string]any{"type": "route", "target": "sg01"}}}, "routes": []any{map[string]any{"id": 12, "match": []string{"*.claude.ai"}, "action": "proxy", "action_value": "sg01"}}, "multiplex": map[string]any{"enabled": true, "protocol": "smux", "max_connections": 4, "min_streams": 4, "max_streams": 0, "padding": true, "brutal": map[string]any{"enabled": false, "up_mbps": 0, "down_mbps": 0}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(payload) }))
	defer server.Close()
	client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "test", NodeID: 1, NodeType: "vless", MachineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Policy.Outbounds) != 1 || len(info.Policy.Raw) != 1 || len(info.Policy.Structured) != 1 || len(info.Policy.Ordinary) != 1 || !info.Common.Multiplex.Enabled || info.Common.Multiplex.MaxConnections != 4 {
		t.Fatal("new panel fields were lost")
	}
	if unchanged, err := client.GetNodeInfo(); err != nil || unchanged != nil {
		t.Fatal("unchanged config returned as changed")
	}
	changes := []func(){
		func() { payload["custom_outbounds"].([]any)[0].(map[string]any)["proxy_tag"] = "direct" },
		func() { payload["custom_routes"].([]any)[0].(map[string]any)["outbound"] = "direct" },
		func() { payload["custom_route_rules"].([]any)[0].(map[string]any)["disabled"] = true },
		func() { payload["multiplex"].(map[string]any)["enabled"] = false },
		func() { payload["routes"].([]any)[0].(map[string]any)["action_value"] = "direct" },
		func() { payload["routes"].([]any)[0].(map[string]any)["action"] = "block" },
	}
	for i, change := range changes {
		change()
		next, err := client.GetNodeInfo()
		if err != nil || next == nil {
			t.Fatalf("change %d ignored: %v", i, err)
		}
	}
	payload["routes"].([]any)[0].(map[string]any)["action"] = "proxy"
	payload["routes"].([]any)[0].(map[string]any)["action_value"] = "missing"
	for i := 0; i < 2; i++ {
		if _, err := client.GetNodeInfo(); err == nil {
			t.Fatal("invalid config was accepted or cached")
		}
	}
}
func TestLegacyPanelAndRoutingOnlyComparison(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"server_port":12345,"network":"tcp","tls":0,"routes":[{"id":1,"match":["*.blocked.example"],"action":"block"},{"id":2,"match":["main"],"action":"dns"}]}`)
	}))
	defer server.Close()
	client, _ := New(&conf.ApiConfig{APIHost: server.URL, Key: "test", NodeID: 1, NodeType: "vless", MachineID: 1})
	info, err := client.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.RoutingEnabled() || info.Common.Multiplex != nil || len(info.Rules.Match) != 1 || info.LimiterRules() != &info.Rules {
		t.Fatal("legacy behavior changed")
	}
	next := *info
	common := *info.Common
	next.Common = &common
	next.Common.CustomOutbounds = []OutboundConfig{{Tag: "sg01", Protocol: "socks"}}
	if !InboundConfigEqual(info, &next) {
		t.Fatal("outbound edit restarted inbound")
	}
	next.Common.Multiplex = &MultiplexConfig{Enabled: true}
	if InboundConfigEqual(info, &next) {
		t.Fatal("mux edit must reload inbound")
	}
}

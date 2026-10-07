package node

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
)

type policyCore struct {
	fakeCore
	updates int
	fail    bool
	last    *panel.NodeInfo
}

func (c *policyCore) UpdateNodePolicy(tag string, info *panel.NodeInfo, options *conf.Options) error {
	c.updates++
	if c.fail {
		return fmt.Errorf("temporarily rejected native settings")
	}
	c.last = info
	return nil
}
func TestPanelPollHotUpdatesAndRetriesNewerPolicy(t *testing.T) {
	payload := map[string]any{"server_port": 12345, "network": "tcp", "tls": 0, "custom_outbounds": []any{map[string]any{"tag": "sg01", "protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": "1.1.1.1", "port": 1080}}}}}, "routes": []any{map[string]any{"id": 1, "match": []string{"claude.com"}, "action": "proxy", "action_value": "sg01"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/config":
			json.NewEncoder(w).Encode(payload)
		case "/api/v1/server/UniProxy/user":
			fmt.Fprint(w, `{"users":[]}`)
		case "/api/v1/server/UniProxy/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		}
	}))
	defer server.Close()
	cfg := testConfig(server.URL, "vless", 1, 0)
	api, err := panel.New(&cfg.ApiConfig)
	if err != nil {
		t.Fatal(err)
	}
	info, err := api.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	kernel := &policyCore{}
	c := NewController(kernel, api, &cfg.Options)
	c.info = info
	c.tag = "HK01"
	c.coreAdded = true
	limiter.Init()
	c.limiter = limiter.AddLimiter(c.tag, &conf.LimitConfig{}, nil, nil)
	defer limiter.DeleteLimiter(c.tag)
	routes := payload["routes"].([]any)
	routes[0].(map[string]any)["action_value"] = "direct"
	c.nodeInfoMonitor()
	if kernel.updates != 1 || len(kernel.added)+len(kernel.removed) != 0 || c.pendingNode != nil || kernel.last.Common.Routes[0].ActionValue != "direct" {
		t.Fatal("panel edit was not applied without inbound restart")
	}
	kernel.fail = true
	routes[0].(map[string]any)["action_value"] = "sg01"
	c.nodeInfoMonitor()
	if c.pendingNode == nil || !c.configError.Load() {
		t.Fatal("failed policy not retained for retry")
	}
	c.nodeInfoMonitor()
	if kernel.updates != 3 {
		t.Fatal("unchanged hash skipped native policy retry")
	}
	// A newer correction must replace the failed pending config, even after 304.
	kernel.fail = false
	routes[0].(map[string]any)["action"] = "direct"
	c.nodeInfoMonitor()
	if c.pendingNode != nil || c.configError.Load() || c.info.Common.Routes[0].Action != "direct" {
		t.Fatal("newer panel config was starved by failed pending config")
	}
	// Multiplex changes the inbound and therefore uses the existing per-node reload.
	payload["multiplex"] = map[string]any{"enabled": true}
	c.nodeInfoMonitor()
	if len(kernel.added) != 1 || len(kernel.removed) != 1 {
		t.Fatal("multiplex change did not reload only this inbound")
	}
}

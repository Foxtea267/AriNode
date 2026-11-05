package node

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/dsom"
	"github.com/Foxtea267/AriNode/limiter"
)

func TestDSOMStateSurvivesNativePanelResyncAnd304(t *testing.T) {
	if ValidateBindings(nil) == nil {
		t.Fatal("empty binding list bypassed hot-reload preflight")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/config":
			if r.Header.Get("If-None-Match") == "unchanged" {
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", "unchanged")
			fmt.Fprint(w, `{"server_port":12345,"routes":[{"id":1,"match":["claude.com"],"action":"proxy","action_value":"bd"}]}`)
		case "/api/v1/server/UniProxy/user":
			fmt.Fprint(w, `{"users":[]}`)
		case "/api/v1/server/UniProxy/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := dsom.New(t.TempDir())
	defer manager.Close()
	dynamic := []dsom.Config{{Tag: "bd", Mode: "gateway", Enabled: false}}
	if err := manager.Reconcile(dynamic); err != nil {
		t.Fatal(err)
	}
	initial := manager.Outbounds()
	limiter.Init()
	kernel := &policyCore{}
	nodes := New()
	nodes.UseDynamicOutbounds(manager)
	cfg := testConfig(server.URL, "vless", 1, 0)
	cfg.Options.RawOptions = []byte(`{"Core":"fake","ListenIP":"0.0.0.0"}`)
	if err := nodes.Start([]conf.NodeConfig{cfg}, kernel); err != nil {
		t.Fatal(err)
	}
	defer nodes.Close()
	controller := nodes.entries[0].controller
	reformatted := cfg
	reformatted.Options.RawOptions = []byte("{\n  \"ListenIP\": \"0.0.0.0\", \"Core\": \"fake\"\n}")
	if err := nodes.Reconcile([]conf.NodeConfig{reformatted}); err != nil {
		t.Fatal(err)
	}
	if nodes.entries[0].controller != controller {
		t.Fatal("JSON formatting during a DSOM edit restarted the inbound")
	}
	changed := cfg
	changed.Options.RawOptions = []byte(`{"Core":"fake","ListenIP":"0.0.0.0","future_kernel_option":true}`)
	if bindingConfigEqual(cfg, changed) {
		t.Fatal("unparsed kernel options were ignored")
	}
	for i := 0; i < 5; i++ {
		controller.nodeInfoMonitor()
		if err := nodes.Reconcile([]conf.NodeConfig{cfg}); err != nil {
			t.Fatal(err)
		}
	}
	if len(kernel.added) != 1 || len(kernel.removed) != 0 || kernel.updates != 0 || nodes.entries[0].controller != controller {
		t.Fatal("panel sync restarted a dynamic node")
	}
	if manager.Outbounds()[0].Settings["servers"].([]any)[0].(map[string]any)["port"] != initial[0].Settings["servers"].([]any)[0].(map[string]any)["port"] {
		t.Fatal("relay port changed")
	}
	dynamic = append(dynamic, dsom.Config{Tag: "bd2", Mode: "gateway", Enabled: false})
	if err := manager.Reconcile(dynamic); err != nil {
		t.Fatal(err)
	}
	controller.nodeInfoMonitor()
	if kernel.updates != 1 || len(kernel.added) != 1 || len(controller.info.Policy.Outbounds) != 2 {
		t.Fatal("304 prevented a newly configured dynamic outbound from being hot installed")
	}
	if err := nodes.Reconcile([]conf.NodeConfig{cfg}); err != nil {
		t.Fatal(err)
	}
}

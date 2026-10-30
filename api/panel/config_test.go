package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/conf"
)

func TestPluginConfigBypassesStaleETagAndDetectsChanges(t *testing.T) {
	var port atomic.Int32
	port.Store(12000)
	var native atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("machine_id") != "39" || r.URL.Query().Get("token") != "test-token" {
			t.Error("plugin request lost machine authentication")
		}
		if r.URL.Path != "/api/v1/arinode/node/config" {
			native.Add(1)
			w.WriteHeader(304)
			return
		}
		if r.Header.Get("If-None-Match") != "" {
			t.Error("plugin request reused an unreliable ETag")
		}
		w.Header().Set("X-AriNode-Config", "1")
		w.Header().Set("ETag", "unchanging-broken-etag")
		fmt.Fprintf(w, `{"server_port":%d,"tls":0}`, port.Load())
	}))
	defer server.Close()
	client, _ := New(&conf.ApiConfig{APIHost: server.URL, NodeID: 244, MachineID: 39, NodeType: "vless", Key: "test-token"})
	first, err := client.GetNodeInfo()
	if err != nil || first == nil || !first.PanelManaged || len(first.RawConfig) == 0 {
		t.Fatalf("plugin configuration not decoded: %v", err)
	}
	if unchanged, err := client.GetNodeInfo(); err != nil || unchanged != nil {
		t.Fatal("unchanged plugin configuration caused an update")
	}
	port.Store(13000)
	next, err := client.GetNodeInfo()
	if err != nil || next == nil || next.Common.ServerPort != 13000 || next.ConfigHash == first.ConfigHash {
		t.Fatalf("updated configuration not delivered: %v", err)
	}
	if native.Load() != 0 {
		t.Fatal("plugin response fell back to stale native config")
	}
}

func TestPluginConfigFallbackAndErrors(t *testing.T) {
	for _, status := range []int{404, 405, 200, 401, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/arinode/node/config" {
					calls++
					w.WriteHeader(status)
					fmt.Fprint(w, "not a config capability")
					return
				}
				fmt.Fprint(w, `{"server_port":12000}`)
			}))
			defer server.Close()
			client, _ := New(&conf.ApiConfig{APIHost: server.URL, NodeID: 1, MachineID: 39, NodeType: "vless"})
			info, err := client.GetNodeInfo()
			if status == 401 || status == 500 {
				if err == nil || info != nil {
					t.Fatal("plugin failure silently used stale native config")
				}
				return
			}
			if err != nil || info == nil || info.PanelManaged {
				t.Fatalf("legacy panel compatibility failed: %v", err)
			}
			client.GetNodeInfo()
			if calls != 1 {
				t.Fatal("missing plugin was probed on every poll")
			}
			client.pluginRetryAt = time.Time{}
			client.GetNodeInfo()
			if calls != 2 {
				t.Fatal("installing a plugin later cannot be discovered")
			}
		})
	}
}

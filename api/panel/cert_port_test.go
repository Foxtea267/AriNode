package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Foxtea267/AriNode/conf"
)

func TestPanelCertificatePortTypesAcrossTLSProtocols(t *testing.T) {
	for _, machine := range []bool{false, true} {
		for _, protocol := range []string{"vless", "vmess", "trojan", "anytls", "hysteria", "hysteria2", "tuic"} {
			t.Run(fmt.Sprintf("machine=%v/%s", machine, protocol), func(t *testing.T) {
				var port atomic.Value
				port.Store(`"3456"`)
				var invalidCached atomic.Bool
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					path := "/api/v1/server/UniProxy/config"
					if machine {
						path = "/api/v1/arinode/node/config"
					}
					if r.URL.Path != path {
						t.Errorf("unexpected configuration endpoint: %s", r.URL.Path)
						http.NotFound(w, r)
						return
					}
					w.Header().Set("ETag", port.Load().(string))
					if machine {
						w.Header().Set("X-AriNode-Config", "1")
					} else if r.Header.Get("If-None-Match") == `"invalid-port"` {
						invalidCached.Store(true)
					}
					fmt.Fprintf(w, `{"server_port":12345,"tls":1,"network":"tcp","cert_config":{"cert_mode":"dns","domain":"node.example.com","dns_provider":"cloudflare","dns_env":{"TEST_TOKEN":"test-secret"},"http_port":%s}}`, port.Load())
				}))
				defer server.Close()
				cfg := &conf.ApiConfig{APIHost: server.URL, NodeID: 244, NodeType: protocol, Key: "test"}
				if machine {
					cfg.MachineID = 39
				}
				client, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				first, err := client.GetNodeInfo()
				if err != nil || first == nil {
					t.Fatalf("quoted panel certificate port failed: %v", err)
				}
				if c := first.Common.CertConfig; c == nil || c.HTTPPort != 3456 || c.CertMode != "dns" ||
					c.DNSProvider != "cloudflare" || c.DNSEnv["TEST_TOKEN"] != "test-secret" {
					t.Fatal("panel certificate fields were lost")
				}
				if first.Security != Tls || first.PanelManaged != machine {
					t.Fatal("panel certificate decode changed node security or binding mode")
				}
				port.Store("3456")
				if next, err := client.GetNodeInfo(); err != nil || next != nil {
					t.Fatalf("equivalent numeric port caused a reload: %v", err)
				}
				port.Store(`"invalid-port"`)
				if bad, err := client.GetNodeInfo(); err == nil || bad != nil {
					t.Fatal("invalid certificate config was accepted")
				}
				port.Store(`"3456"`)
				if next, err := client.GetNodeInfo(); err != nil || next != nil {
					t.Fatalf("invalid response overwrote the last valid hash: %v", err)
				}
				if invalidCached.Load() {
					t.Fatal("invalid response ETag was committed")
				}
				port.Store(`"4567"`)
				updated, err := client.GetNodeInfo()
				if err != nil || updated == nil || updated.Common.CertConfig.HTTPPort != 4567 ||
					updated.ConfigHash == first.ConfigHash || InboundConfigEqual(first, updated) {
					t.Fatalf("real certificate port update was ignored: %v", err)
				}
				if next, err := client.GetNodeInfo(); err != nil || next != nil {
					t.Fatalf("same port update repeated: %v", err)
				}
			})
		}
	}
}

package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Foxtea267/AriNode/common/nodepolicy"
	"github.com/Foxtea267/AriNode/conf"
)

func TestDynamicOutboundsSurvivePanel304AndResync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == "same" {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", "same")
		fmt.Fprint(w, `{"server_port":12345,"routes":[{"id":1,"match":["claude.com"],"action":"proxy","action_value":"bd"}]}`)
	}))
	defer server.Close()
	client, _ := New(&conf.ApiConfig{APIHost: server.URL, NodeID: 1, NodeType: "vless"})
	port := 1080
	client.LocalOutbounds = func() []nodepolicy.OutboundConfig {
		return []nodepolicy.OutboundConfig{{Tag: "bd", Protocol: "socks", Settings: map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": port}}}}}
	}
	first, err := client.GetNodeInfo()
	if err != nil || first == nil || first.Policy.Outbounds[0].Tag != "bd" {
		t.Fatalf("local dynamic tag was not available to panel routes: %v", err)
	}
	for i := 0; i < 5; i++ {
		next, err := client.GetNodeInfo()
		if err != nil || next != nil {
			t.Fatal("panel 304 rebuilt an unchanged dynamic outbound")
		}
	}
	port = 2080
	next, err := client.GetNodeInfo()
	if err != nil || next == nil || next.ConfigHash == first.ConfigHash {
		t.Fatal("panel 304 hid local dynamic policy changes")
	}
	if !InboundConfigEqual(first, next) {
		t.Fatal("dynamic policy edit unnecessarily changes inbound")
	}
	client.LocalOutbounds = func() []nodepolicy.OutboundConfig { return nil }
	if _, err := client.GetNodeInfo(); err == nil {
		t.Fatal("missing dynamic tag silently fell back to direct")
	}
}

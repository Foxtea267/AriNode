package panel

import (
	"github.com/Foxtea267/AriNode/conf"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeAliveCanEnrichKnownIPsWithoutChangingAuth(t *testing.T) {
	for _, plugin := range []bool{true, false} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("token") != "shared" || r.URL.Query().Get("node_id") != "7" {
				t.Error("device endpoint lost native server authorization")
			}
			if r.URL.Path == "/api/v1/server/UniProxy/alivelist" {
				w.Write([]byte(`{"alive":{"1":3}}`))
				return
			}
			if r.URL.Path != "/api/v1/arinode/devices/alivelist" {
				t.Error("unexpected endpoint")
			}
			if !plugin {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(`{"alive":{"1":3},"ips":{"1":["192.0.2.1","192.0.2.2","192.0.2.3"]}}`))
		}))
		c, err := New(&conf.ApiConfig{APIHost: s.URL, Key: "shared", NodeID: 7, NodeType: "vless"})
		if err != nil {
			t.Fatal(err)
		}
		counts, err := c.GetUserAlive()
		if err != nil || counts[1] != 3 {
			t.Fatal("native count limits lost")
		}
		if plugin && len(c.AliveIPs[1]) != 3 {
			t.Fatal("existing client identities lost")
		}
		if !plugin && c.AliveIPs != nil {
			t.Fatal("missing plugin disabled legacy count limits")
		}
		s.Close()
	}
}

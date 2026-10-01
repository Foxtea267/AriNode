package panel

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Foxtea267/AriNode/conf"
)

func TestNodeInfoNativeBlockRules(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"server_port":25350,"network":"tcp","tls":0,"routes":[{"id":1,"action":"block","match":["*.blocked.example","plain.example","192.0.2.0/24","regexp:^regex","protocol:bittorrent"," "]}]}`))
	}))
	defer s.Close()
	c, err := New(&conf.ApiConfig{APIHost: s.URL, Key: "test", NodeID: 1, NodeType: "vless", MachineID: 1})
	if err != nil {
		t.Fatal(err)
	}
	node, err := c.GetNodeInfo()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(node.Rules.Match, []string{"*.blocked.example", "plain.example", "192.0.2.0/24"}) || !reflect.DeepEqual(node.Rules.Regexp, []string{"^regex"}) || !reflect.DeepEqual(node.Rules.Protocol, []string{"bittorrent"}) {
		t.Fatalf("unexpected parsed rules: %+v", node.Rules)
	}
}

func TestRouteMatchesRejectsMalformedValues(t *testing.T) {
	for _, value := range []any{42, []any{"valid", 42}} {
		if _, err := routeMatches(value); err == nil {
			t.Fatal("expected an error instead of panic")
		}
	}
}

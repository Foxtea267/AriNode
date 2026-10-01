package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/conf"
)

func TestMachineMieruConfiguration(t *testing.T) {
	for _, transport := range []string{"TCP", "UDP", "", "invalid"} {
		t.Run(transport, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/server/config" || r.URL.Query().Get("machine_id") != "22" || r.URL.Query().Get("node_id") != "234" {
					t.Errorf("incorrect native machine request")
				}
				fmt.Fprintf(w, `{"server_port":25332,"transport":%q,"traffic_pattern":"CgYIiwEQwAM=","base_config":{"push_interval":60,"pull_interval":60}}`, transport)
			}))
			defer s.Close()
			c, err := New(&conf.ApiConfig{APIHost: s.URL, Key: "test", MachineID: 22, NodeID: 234, NodeType: "mieru"})
			if err != nil {
				t.Fatal(err)
			}
			n, err := c.GetNodeInfo()
			if transport == "invalid" {
				if err == nil {
					t.Fatal("accepted invalid transport")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "tcp"
			if transport == "UDP" {
				want = "udp"
			}
			if n.Mieru.Transport != want || n.Mieru.TrafficPattern != "CgYIiwEQwAM=" || n.Common.ServerPort != 25332 || n.PullInterval != time.Minute || n.Security != None {
				t.Fatalf("incorrect mieru config: %+v", n)
			}
		})
	}
}

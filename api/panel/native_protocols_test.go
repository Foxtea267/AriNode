package panel

import (
	"encoding/json"
	"fmt"
	"github.com/Foxtea267/AriNode/conf"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeRealityScalarFormats(t *testing.T) {
	for _, data := range []string{`{"server_port":443,"xver":0}`, `{"server_port":"443","xver":"0"}`} {
		var settings TlsSettings
		if err := json.Unmarshal([]byte(data), &settings); err != nil {
			t.Fatal(err)
		}
		if settings.ServerPort != "443" || settings.Xver != 0 {
			t.Fatal("Reality scalar value changed")
		}
	}
}

func TestNativeHysteriaVersionAndTLSSettings(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"version":%d,"server_port":25333,"up_mbps":0,"down_mbps":0,"tls_settings":{"server_name":null,"allow_insecure":true},"obfs":null,"obfs-password":null}`, version)
			}))
			defer s.Close()
			c, err := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "hysteria", NodeID: 236, MachineID: 22})
			if err != nil {
				t.Fatal(err)
			}
			n, err := c.GetNodeInfo()
			if version == 3 {
				if err == nil {
					t.Fatal("unsupported version accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "hysteria"
			if version == 2 {
				want = "hysteria2"
			}
			if n.Type != want || !n.TLSSettings.AllowInsecure || n.Security != Tls {
				t.Fatal("native version/TLS settings were lost")
			}
			if c.NodeType != "hysteria" {
				t.Fatal("changed panel API protocol identifier")
			}
		})
	}
}

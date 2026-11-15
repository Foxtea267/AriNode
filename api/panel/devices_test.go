package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Foxtea267/AriNode/conf"
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

func TestNativeAliveFallsBackWhenOptionalDeviceEndpointIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/server/alivelist":
			w.Write([]byte(`{"alive":{"1":2}}`))
		case "/api/v1/arinode/devices/alivelist":
			http.Error(w, "plugin temporarily unavailable", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "shared", MachineID: 39, NodeID: 7, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	counts, err := client.GetUserAlive()
	if err != nil || counts[1] != 2 {
		t.Fatalf("optional device endpoint broke native alive limits: counts=%v err=%v", counts, err)
	}
	if client.AliveIPs != nil {
		t.Fatalf("unavailable optional endpoint unexpectedly replaced alive IPs: %v", client.AliveIPs)
	}
}

func TestNativeAliveFallsBackWhenOptionalDeviceTransportFails(t *testing.T) {
	var deviceAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/alivelist":
			fmt.Fprint(w, `{"alive":{"1":2}}`)
		case "/api/v1/arinode/devices/alivelist":
			deviceAttempts.Add(1)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack optional device endpoint: %v", err)
				return
			}
			_ = connection.Close()
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "shared", NodeID: 7, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	counts, err := client.GetUserAlive()
	if err != nil || counts[1] != 2 {
		t.Fatalf("optional transport failure broke native alive limits: counts=%v err=%v", counts, err)
	}
	if client.AliveIPs != nil {
		t.Fatalf("failed optional request unexpectedly replaced native IP metadata: %v", client.AliveIPs)
	}
	if deviceAttempts.Load() == 0 {
		t.Fatal("optional device endpoint was not requested")
	}
}

func TestNativeAliveFailurePreservesPublishedSnapshot(t *testing.T) {
	for _, failure := range []string{"HTTP", "malformed JSON"} {
		t.Run(failure, func(t *testing.T) {
			var fail atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fail.Load() {
					if failure == "HTTP" {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					fmt.Fprint(w, `{"alive":{"BODY_SECRET":"MALFORMED_VALUE_SECRET"}}`)
					return
				}
				fmt.Fprint(w, `{"alive":{"1":2},"ips":{"1":["192.0.2.1"]}}`)
			}))
			defer server.Close()
			client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "shared", NodeID: 7, NodeType: "vless"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.GetUserAlive(); err != nil {
				t.Fatal(err)
			}
			previous := client.AliveMap
			fail.Store(true)
			counts, err := client.GetUserAlive()
			if err == nil || counts != nil {
				t.Fatalf("failed snapshot unexpectedly accepted: counts=%v err=%v", counts, err)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("malformed response contents leaked into error: %v", err)
			}
			if client.AliveMap != previous || client.AliveMap.Alive[1] != 2 || len(client.AliveIPs[1]) != 1 {
				t.Fatal("failed request changed the last valid alive snapshot")
			}
		})
	}
}

func TestNativeAliveMissingEndpointColdStartCompatibility(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "LEGACY_ENDPOINT_BODY_SECRET", status)
			}))
			defer server.Close()
			client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "shared", NodeID: 7, NodeType: "vless"})
			if err != nil {
				t.Fatal(err)
			}
			// Repeated legacy misses must remain compatible until the first
			// successful snapshot, rather than marking fallback data as fetched.
			for attempt := 0; attempt < 2; attempt++ {
				counts, err := client.GetUserAlive()
				if err != nil || counts == nil || len(counts) != 0 {
					t.Fatalf("missing legacy endpoint failed cold start: counts=%v err=%v", counts, err)
				}
			}
		})
	}
}

func TestNativeAliveMissingEndpointPreservesSuccessfulSnapshot(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		for _, populated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/populated=%t", status, populated), func(t *testing.T) {
				var missing atomic.Bool
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if missing.Load() {
						http.Error(w, "MISSING_ENDPOINT_BODY_SECRET", status)
						return
					}
					if populated {
						fmt.Fprint(w, `{"alive":{"1":2},"ips":{"1":["192.0.2.1"]}}`)
					} else {
						fmt.Fprint(w, `{"alive":{},"ips":{}}`)
					}
				}))
				defer server.Close()
				client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "shared", NodeID: 7, NodeType: "vless"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := client.GetUserAlive(); err != nil {
					t.Fatal(err)
				}
				previous := client.AliveMap
				missing.Store(true)
				counts, err := client.GetUserAlive()
				if err == nil || counts != nil {
					t.Fatalf("missing endpoint replaced successful snapshot: counts=%v err=%v", counts, err)
				}
				if strings.Contains(err.Error(), "MISSING_ENDPOINT_BODY_SECRET") {
					t.Fatalf("missing endpoint leaked response body: %v", err)
				}
				if client.AliveMap != previous || client.AliveMap.Alive == nil || client.AliveIPs == nil {
					t.Fatal("missing endpoint changed previously published snapshot")
				}
				if populated && (client.AliveMap.Alive[1] != 2 || len(client.AliveIPs[1]) != 1) {
					t.Fatal("missing endpoint changed published counts or IP identities")
				}
			})
		}
	}
}

func TestNativeAliveColdStartRejectsAuthAndServerFailures(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "COLD_START_BODY_SECRET", status)
			}))
			defer server.Close()
			client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "shared", NodeID: 7, NodeType: "vless"})
			if err != nil {
				t.Fatal(err)
			}
			previous := client.AliveMap
			counts, err := client.GetUserAlive()
			if err == nil || counts != nil {
				t.Fatalf("HTTP failure was hidden as an empty successful snapshot: counts=%v err=%v", counts, err)
			}
			if strings.Contains(err.Error(), "COLD_START_BODY_SECRET") {
				t.Fatalf("cold-start failure leaked response body: %v", err)
			}
			if client.AliveMap != previous || client.AliveMap.Alive != nil || client.AliveIPs != nil {
				t.Fatal("failed cold-start request published device state")
			}
		})
	}
}

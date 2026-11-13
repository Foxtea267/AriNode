package node

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
)

func TestMachineDiscoveryPreservesHealthyNodesAcrossUpdatesAndFailures(t *testing.T) {
	var state atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/server/machine/nodes":
			switch state.Load() {
			case 0:
				fmt.Fprint(w, `{"nodes":[{"id":1,"type":"vless"}]}`)
			case 1:
				fmt.Fprint(w, `{"nodes":[{"id":1,"type":"vless"},{"id":2,"type":"vless"}]}`)
			case 2:
				fmt.Fprint(w, `{}`)
			case 3:
				fmt.Fprint(w, `{"nodes":[{"id":2,"type":"vless"}]}`)
			case 4:
				fmt.Fprint(w, `{"nodes":[]}`)
			}
		case "/api/v2/server/config":
			fmt.Fprint(w, `{"server_port":12345}`)
		case "/api/v2/server/user":
			fmt.Fprint(w, `{"users":[]}`)
		case "/api/v2/server/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	limiter.Init()
	n := New()
	n.retryInterval = 15 * time.Millisecond
	c := &fakeCore{}
	if err := n.Start([]conf.NodeConfig{testConfig(s.URL, "vless", 1, 22)}, c); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	healthy := n.entries[0].controller
	state.Store(1)
	deadline := time.Now().Add(2 * time.Second)
	for len(n.Snapshot()) != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(n.Snapshot()) != 2 {
		t.Fatal("new machine node was not automatically discovered")
	}
	n.mu.Lock()
	if n.entries[0].controller != healthy {
		n.mu.Unlock()
		t.Fatal("healthy node restarted")
	}
	n.mu.Unlock()
	state.Store(2)
	n.mu.Lock()
	n.reconcileLocked(n.discoverBindingsLocked())
	n.mu.Unlock()
	if len(n.Snapshot()) != 2 {
		t.Fatal("discovery failure removed existing nodes")
	}
	state.Store(3)
	n.mu.Lock()
	n.reconcileLocked(n.discoverBindingsLocked())
	n.mu.Unlock()
	if len(n.Snapshot()) != 1 || n.Snapshot()[0].NodeID != 2 {
		t.Fatal("unbound node was not removed")
	}
	state.Store(4)
	n.mu.Lock()
	n.reconcileLocked(n.discoverBindingsLocked())
	n.mu.Unlock()
	if len(n.Snapshot()) != 0 {
		t.Fatal("empty valid machine binding list was not applied")
	}
	state.Store(1)
	n.mu.Lock()
	n.reconcileLocked(n.discoverBindingsLocked())
	n.mu.Unlock()
	if len(n.Snapshot()) != 2 {
		t.Fatal("empty machine lost its discovery template")
	}
}

func TestClusterDiscoveryOnlyStartsExplicitSharedNode(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"nodes":[{"id":1,"type":"vless"},{"id":2,"type":"vless"}]}`)
	}))
	defer s.Close()
	shared := testConfig(s.URL, "vless", 1, 22)
	shared.ApiConfig.Cluster = &conf.ClusterConfig{Domain: "pool.example.com", MemberID: "hk-01"}
	n := New()
	n.configured = []conf.NodeConfig{shared}
	if bindings := n.discoverBindingsLocked(); len(bindings) != 1 || calls.Load() != 0 {
		t.Fatal("shared node discovered unrelated machine bindings")
	}
	n.configured = append(n.configured, testConfig(s.URL, "vless", 2, 22))
	if bindings := n.discoverBindingsLocked(); len(bindings) != 2 || bindings[0].ApiConfig.Cluster == nil || bindings[1].ApiConfig.Cluster != nil {
		t.Fatal("mixed bindings duplicated or changed the shared node")
	}
}

func TestRegisteredMachineDiscoversNodesWithoutLocalSeed(t *testing.T) {
	var assigned atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/server/machine/nodes":
			if r.URL.Query().Has("node_id") {
				t.Error("machine-only request sent a node ID")
			}
			if assigned.Load() {
				fmt.Fprint(w, `{"nodes":[{"id":3,"type":"vless"},{"id":2,"type":"vless"}]}`)
			} else {
				fmt.Fprint(w, `{"nodes":[]}`)
			}
		case "/api/v1/arinode/node/config":
			w.Header().Set("X-AriNode-Config", "1")
			fmt.Fprint(w, `{"server_port":12345}`)
		case "/api/v2/server/user":
			fmt.Fprint(w, `{"users":[]}`)
		case "/api/v2/server/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	other := nodePanel(t)
	defer other.Close()
	limiter.Init()
	n := New()
	managed := testConfig(server.URL, "", 0, 7)
	managed.Options.PanelConfigPath = t.TempDir()
	managed.ApiConfig.SingleMachineNode = true
	unrelated := testConfig(other.URL, "vless", 1, 0)
	if err := n.Start([]conf.NodeConfig{managed, unrelated}, &fakeCore{}); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	healthy := n.entries[0].controller
	if len(n.Snapshot()) != 1 {
		t.Fatal("empty machine started an inbound")
	}
	assigned.Store(true)
	if err := n.Reconcile([]conf.NodeConfig{managed, unrelated}); err != nil {
		t.Fatal(err)
	}
	if s := n.Snapshot(); len(s) != 2 || s[1].NodeID != 2 || s[1].State != "running" {
		t.Fatalf("single-node discovery failed: %+v", s)
	}
	first := n.entries[1].controller
	managed.ApiConfig.SingleMachineNode = false
	if err := n.Reconcile([]conf.NodeConfig{managed, unrelated}); err != nil {
		t.Fatal(err)
	}
	if len(n.Snapshot()) != 3 || n.entries[0].controller != healthy || n.entries[2].controller != first {
		t.Fatal("multi-node discovery disturbed the other panel")
	}
	if err := n.Reconcile([]conf.NodeConfig{unrelated}); err != nil {
		t.Fatal(err)
	}
	if len(n.Snapshot()) != 1 || n.entries[0].controller != healthy {
		t.Fatal("removing the registered machine affected a different panel")
	}
	if err := n.Reconcile(nil, true); err != nil || len(n.Snapshot()) != 0 {
		t.Fatalf("removing the last binding did not leave an idle service: %v", err)
	}
}

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

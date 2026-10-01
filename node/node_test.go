package node

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	vCore "github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
)

type fakeCore struct {
	mu       sync.Mutex
	added    []string
	removed  []string
	failNext bool
}

func (f *fakeCore) Start() error { return nil }
func (f *fakeCore) Close() error { return nil }
func (f *fakeCore) AddNode(tag string, _ *panel.NodeInfo, _ *conf.Options) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return fmt.Errorf("one node failed")
	}
	f.added = append(f.added, tag)
	return nil
}

func TestReloadFailureRestoresPreviousNode(t *testing.T) {
	server := nodePanel(t)
	defer server.Close()
	limiter.Init()
	core := &fakeCore{}
	n := New()
	if err := n.Start([]conf.NodeConfig{testConfig(server.URL, "vless", 1, 0)}, core); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	controller := n.entries[0].controller
	previous := controller.info
	core.mu.Lock()
	core.failNext = true
	core.mu.Unlock()
	replacement := *previous
	replacement.Id = 99
	if err := controller.reloadNode(&replacement, nil, nil); err == nil {
		t.Fatal("expected replacement failure")
	}
	if !controller.coreAdded || controller.info != previous {
		t.Fatal("previous node was not restored")
	}
	core.mu.Lock()
	added, removed := len(core.added), len(core.removed)
	core.mu.Unlock()
	if added != 2 || removed != 1 {
		t.Fatalf("unexpected core changes: added=%d removed=%d", added, removed)
	}
}
func (f *fakeCore) DelNode(tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, tag)
	return nil
}
func (f *fakeCore) AddUsers(_ *vCore.AddUsersParams) (int, error)                 { return 0, nil }
func (f *fakeCore) GetUserTrafficSlice(string, bool) ([]panel.UserTraffic, error) { return nil, nil }
func (f *fakeCore) DelUsers([]panel.UserInfo, string, *panel.NodeInfo) error      { return nil }
func (f *fakeCore) Protocols() []string                                           { return []string{"vless"} }
func (f *fakeCore) Type() string                                                  { return "fake" }

func nodePanel(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/server/UniProxy/config" || r.URL.Path == "/api/v2/server/config":
			_, _ = w.Write([]byte(`{"server_port":12345,"base_config":{"push_interval":60,"pull_interval":60}}`))
		case r.URL.Path == "/api/v1/server/UniProxy/user" || r.URL.Path == "/api/v2/server/user":
			_, _ = w.Write([]byte(`{"users":[]}`))
		case r.URL.Path == "/api/v1/server/UniProxy/alivelist" || r.URL.Path == "/api/v2/server/alivelist":
			_, _ = w.Write([]byte(`{"alive":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func testConfig(host, typ string, id, machineID int) conf.NodeConfig {
	return conf.NodeConfig{ApiConfig: conf.ApiConfig{APIHost: host, Key: "secret", NodeType: typ, NodeID: id, MachineID: machineID, Timeout: 2}, Options: conf.Options{Core: "fake", CertConfig: conf.NewCertConfig()}}
}

func TestFailedBindingDoesNotStopHealthyBinding(t *testing.T) {
	server := nodePanel(t)
	defer server.Close()
	limiter.Init()
	core := &fakeCore{}
	n := New()
	if err := n.Start([]conf.NodeConfig{testConfig(server.URL, "bad-type", 1, 0), testConfig(server.URL, "vless", 2, 0)}, core); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	status := n.Snapshot()
	if status[0].State != "retrying" || status[1].State != "running" {
		t.Fatalf("unexpected states: %+v", status)
	}
	if len(core.added) != 1 {
		t.Fatalf("healthy node was not started: %+v", core.added)
	}
	good := n.entries[1].controller
	if err := n.Reconcile([]conf.NodeConfig{testConfig(server.URL, "vless", 1, 0), testConfig(server.URL, "vless", 2, 0)}); err != nil {
		t.Fatal(err)
	}
	if n.entries[1].controller != good || len(core.removed) != 0 {
		t.Fatal("unchanged node was restarted")
	}
	if n.Snapshot()[0].State != "running" {
		t.Fatalf("recovered node did not start: %+v", n.Snapshot())
	}
}

func TestSameMachineIDOnDifferentPanelsGetsSeparateReporter(t *testing.T) {
	east := nodePanel(t)
	defer east.Close()
	west := nodePanel(t)
	defer west.Close()
	limiter.Init()
	n := New()
	if err := n.Start([]conf.NodeConfig{testConfig(east.URL, "vless", 1, 7), testConfig(west.URL, "vless", 2, 7)}, &fakeCore{}); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if !n.entries[0].controller.machinePrimary.Load() || !n.entries[1].controller.machinePrimary.Load() {
		t.Fatal("machine reporting collapsed across Xboard panels")
	}
}

func TestMachineReporterMovesWhenPrimaryBindingRemoved(t *testing.T) {
	server := nodePanel(t)
	defer server.Close()
	limiter.Init()
	n := New()
	first := testConfig(server.URL, "vless", 1, 7)
	second := testConfig(server.URL, "vless", 2, 7)
	if err := n.Start([]conf.NodeConfig{first, second}, &fakeCore{}); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if !n.entries[0].controller.machinePrimary.Load() || n.entries[1].controller.machinePrimary.Load() {
		t.Fatal("unexpected initial primary")
	}
	if err := n.Reconcile([]conf.NodeConfig{second}); err != nil {
		t.Fatal(err)
	}
	if !n.entries[0].controller.machinePrimary.Load() {
		t.Fatal("remaining node did not become machine reporter")
	}
}

func TestUnavailableNodeRetriesWithoutRestartingHealthyNode(t *testing.T) {
	var recovered atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/server/UniProxy/config" && r.URL.Query().Get("node_id") == "1" && !recovered.Load() {
			http.Error(w, "temporary failure", 503)
			return
		}
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/config":
			_, _ = w.Write([]byte(`{"server_port":12345,"base_config":{"push_interval":60,"pull_interval":60}}`))
		case "/api/v1/server/UniProxy/user":
			_, _ = w.Write([]byte(`{"users":[]}`))
		case "/api/v1/server/UniProxy/alivelist":
			_, _ = w.Write([]byte(`{"alive":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	limiter.Init()
	core := &fakeCore{}
	n := New()
	n.retryInterval = 20 * time.Millisecond
	if err := n.Start([]conf.NodeConfig{testConfig(server.URL, "vless", 1, 0), testConfig(server.URL, "vless", 2, 0)}, core); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if n.Snapshot()[0].State != "retrying" || n.Snapshot()[1].State != "running" {
		t.Fatalf("wrong initial states: %+v", n.Snapshot())
	}
	healthyController := n.entries[1].controller
	recovered.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n.Snapshot()[0].State == "running" {
			if n.entries[1].controller != healthyController {
				t.Fatal("healthy node restarted during retry")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("failed node did not recover: %+v", n.Snapshot())
}

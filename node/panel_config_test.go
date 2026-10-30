package node

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
)

func TestPluginConfigAutomaticallyReloadsOnlyChangedNodeAndPersists(t *testing.T) {
	var port atomic.Int32
	var usersBroken atomic.Bool
	port.Store(12000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/arinode/node/config":
			w.Header().Set("X-AriNode-Config", "1")
			value := int32(14000)
			if r.URL.Query().Get("node_id") == "1" {
				value = port.Load()
			}
			fmt.Fprintf(w, `{"tls":0,"server_port":%d}`, value)
		case "/api/v2/server/user":
			if usersBroken.Load() {
				http.Error(w, "temporarily unavailable", 500)
				return
			}
			fmt.Fprint(w, `{"users":[]}`)
		case "/api/v2/server/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	limiter.Init()
	directory := t.TempDir()
	configs := []conf.NodeConfig{testConfig(server.URL, "vless", 1, 39), testConfig(server.URL, "vless", 2, 39)}
	disabled := false
	for i := range configs {
		configs[i].ApiConfig.MachineAutoDiscover = &disabled
		configs[i].Options.PanelConfigPath = directory
	}
	manager := New()
	kernel := &fakeCore{}
	if err := manager.Start(configs, kernel); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	first, sibling := manager.entries[0].controller, manager.entries[1].controller
	paths, _ := filepath.Glob(filepath.Join(directory, "*.json"))
	if len(paths) != 2 {
		t.Fatal("per-node snapshots missing")
	}
	initial := map[string][]byte{}
	mtimes := map[string]int64{}
	for _, path := range paths {
		initial[path], _ = os.ReadFile(path)
		info, _ := os.Stat(path)
		mtimes[path] = info.ModTime().UnixNano()
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("snapshot is not private")
		}
	}
	for i := 0; i < 3; i++ {
		first.nodeInfoMonitor()
		sibling.nodeInfoMonitor()
	}
	for _, path := range paths {
		info, _ := os.Stat(path)
		if info.ModTime().UnixNano() != mtimes[path] {
			t.Fatal("unchanged snapshot rewritten")
		}
	}
	usersBroken.Store(true)
	port.Store(13000)
	first.nodeInfoMonitor()
	if first.info.Common.ServerPort != 13000 {
		t.Fatal("user endpoint failure blocked config delivery")
	}
	if manager.entries[1].controller != sibling || sibling.info.Common.ServerPort != 14000 {
		t.Fatal("sibling node affected")
	}
	if len(kernel.added) != 3 || len(kernel.removed) != 1 {
		t.Fatal("updated node did not reload exactly once")
	}
	first.nodeInfoMonitor()
	if len(kernel.added) != 3 {
		t.Fatal("unchanged config reloaded again")
	}
	lastGood := map[string][]byte{}
	for _, path := range paths {
		lastGood[path], _ = os.ReadFile(path)
	}
	kernel.failNext = true
	port.Store(15000)
	first.nodeInfoMonitor()
	if first.info.Common.ServerPort != 13000 || first.pendingNode == nil {
		t.Fatal("failed update lost last valid config")
	}
	for _, path := range paths {
		data, _ := os.ReadFile(path)
		if !bytes.Equal(data, lastGood[path]) {
			t.Fatal("failed update overwrote working snapshot")
		}
	}
	first.nodeInfoMonitor()
	if first.info.Common.ServerPort != 15000 {
		t.Fatal("failed update was not retried automatically")
	}
}

func TestPluginConfigPollingAppliesWithoutManualRestart(t *testing.T) {
	var port atomic.Int32
	port.Store(12000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/arinode/node/config":
			w.Header().Set("X-AriNode-Config", "1")
			fmt.Fprintf(w, `{"tls":0,"server_port":%d,"base_config":{"pull_interval":1,"push_interval":3600}}`, port.Load())
		case "/api/v2/server/user":
			fmt.Fprint(w, `{"users":[]}`)
		case "/api/v2/server/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	limiter.Init()
	cfg := testConfig(server.URL, "vless", 1, 39)
	disabled := false
	cfg.ApiConfig.MachineAutoDiscover = &disabled
	cfg.Options.PanelConfigPath = t.TempDir()
	manager := New()
	kernel := &fakeCore{}
	if err := manager.Start([]conf.NodeConfig{cfg}, kernel); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	paths, _ := filepath.Glob(filepath.Join(cfg.Options.PanelConfigPath, "*.json"))
	if len(paths) != 1 {
		t.Fatal("initial snapshot missing")
	}
	initial, _ := os.ReadFile(paths[0])
	if changed, err := writePanelConfig(paths[0], initial); err != nil || changed {
		t.Fatal("stable snapshot was not idempotent")
	}
	port.Store(13000)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(paths[0])
		if bytes.Contains(data, []byte("13000")) {
			kernel.mu.Lock()
			added, removed := len(kernel.added), len(kernel.removed)
			kernel.mu.Unlock()
			if added != 2 || removed != 1 {
				t.Fatal("automatic polling did not apply exactly once")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("panel edit was not applied by the background scheduler")
}

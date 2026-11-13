package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Foxtea267/AriNode/conf"
)

func enrollmentPanel(t *testing.T, token string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.RawQuery != "" {
			t.Errorf("unexpected enrollment request: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v1/arinode/enroll":
			var data struct{ Code string }
			if json.NewDecoder(r.Body).Decode(&data) != nil {
				t.Error("could not decode enrollment")
			}
			if data.Code == "invalid" {
				http.Error(w, "invalid enrollment", http.StatusForbidden)
				return
			}
			fmt.Fprintf(w, `{"machine_id":7,"token":%q,"core":"sing","multi_node":%t,"multi_panel":%t}`, token, data.Code != "single", data.Code != "exclusive")
		case "/api/v2/server/machine/nodes":
			var data struct {
				ID    int    `json:"machine_id"`
				Token string `json:"token"`
			}
			if json.NewDecoder(r.Body).Decode(&data) != nil || data.ID != 7 || data.Token != token {
				t.Error("machine discovery did not use the exchanged credential")
			}
			fmt.Fprint(w, `{"nodes":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestEnrollmentKeepsOtherPanelsAndLocalConfiguration(t *testing.T) {
	east := enrollmentPanel(t, "east-secret")
	west := enrollmentPanel(t, "west-secret")
	path := filepath.Join(t.TempDir(), "config.json")
	local := `{"Log":{"Level":"warn"},"Cores":[{"Type":"sing","Log":{"Disabled":true}}],"Extension":{"value":9007199254740993},"Nodes":[{"ApiHost":"https://legacy.example","ApiKey":"legacy","NodeID":1,"NodeType":"vless","CustomOption":"keep"}]}`
	if err := os.WriteFile(path, []byte(local), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, server := range []*httptest.Server{east, west} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := enrollPanel(context.Background(), path, server.URL, "valid"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := enrollPanel(context.Background(), path, east.URL, "single"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadPanelConfig(path)
	if err != nil || len(cfg.Machines) != 2 || len(cfg.NodeConfig) != 3 || len(cfg.BindingPanels()) != 3 {
		t.Fatalf("lost or duplicated panel bindings: %v", err)
	}
	before, _ := os.ReadFile(path)
	for _, value := range []string{"9007199254740993", "CustomOption", "keep", "Disabled", "warn"} {
		if !strings.Contains(string(before), value) {
			t.Fatalf("local configuration lost %q", value)
		}
	}
	if _, err := enrollPanel(context.Background(), path, east.URL, "exclusive"); err == nil {
		t.Fatal("single-panel enrollment replaced other panels")
	}
	if _, err := enrollPanel(context.Background(), path, west.URL, "invalid"); err == nil {
		t.Fatal("invalid enrollment modified the config")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed enrollment changed the config")
	}
	for _, m := range cfg.Machines {
		cache := m.CacheDir(filepath.Dir(path))
		if err := os.MkdirAll(cache, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cache, "fixture"), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	removed, legacy, err := removePanel(path, east.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := purgePanelFiles(path, removed, legacy); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadPanelConfig(path)
	if err != nil || len(cfg.Machines) != 1 || cfg.Machines[0].APIHost != west.URL || len(cfg.NodeConfig) != 2 {
		t.Fatalf("remove affected another binding: %v", err)
	}
	root := filepath.Dir(path)
	if _, err := os.Stat((conf.MachineBinding{APIHost: east.URL, MachineID: 7}).CacheDir(root)); !os.IsNotExist(err) {
		t.Fatal("removed panel still has downloaded files")
	}
	if _, err := os.Stat(filepath.Join(cfg.Machines[0].CacheDir(root), "fixture")); err != nil {
		t.Fatal("another panel's files were removed")
	}
}

func TestEnrollmentOfEmptyMachineAndRemovalOfLastPanel(t *testing.T) {
	s := enrollmentPanel(t, "secret")
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := enrollPanel(context.Background(), path, s.URL, "valid")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := removePanel(path, m.ID()); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadPanelConfig(path)
	if err != nil || len(cfg.NodeConfig) != 0 || !cfg.AllowEmpty {
		t.Fatalf("last panel removal should leave a valid idle configuration: %v", err)
	}
}

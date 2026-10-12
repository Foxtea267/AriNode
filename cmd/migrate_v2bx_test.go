package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testV2BXLayout(t *testing.T) v2bxLayout {
	t.Helper()
	root := t.TempDir()
	l := v2bxLayout{Config: filepath.Join(root, "etc", "V2bX", "config.json"), Program: filepath.Join(root, "usr", "local", "V2bX"), Unit: filepath.Join(root, "systemd", "V2bX.service"), Dropins: filepath.Join(root, "systemd", "V2bX.service.d"), Wrapper: filepath.Join(root, "usr", "bin", "V2bX"), Alias: filepath.Join(root, "usr", "bin", "v2bx"), AriConfig: filepath.Join(root, "etc", "arinode", "config.json"), AriUnit: filepath.Join(root, "systemd", "arinode.service"), AriBinary: filepath.Join(root, "usr", "local", "bin", "arinode"), AriCTL: filepath.Join(root, "usr", "local", "bin", "anctl"), Backups: filepath.Join(root, "var", "lib", "arinode", "migrations", "v2bx")}
	// Windows is case-insensitive; use distinct fixture names for the two Linux
	// management entry points instead of accidentally aliasing their snapshots.
	l.Alias = filepath.Join(root, "usr", "bin", "v2bx-alias")
	put := func(path, text string, mode os.FileMode) {
		t.Helper()
		os.MkdirAll(filepath.Dir(path), 0700)
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(l.Program, "V2bX"), l.Wrapper, l.Alias, l.AriBinary, l.AriCTL} {
		put(p, "saved-executable", 0700)
	}
	put(l.Unit, "original V2bX unit", 0600)
	put(l.AriUnit, "AriNode unit", 0600)
	put(filepath.Join(filepath.Dir(l.Config), "cert.pem"), "certificate", 0600)
	put(filepath.Join(filepath.Dir(l.Config), "native.json"), `{"tls":{"certificate_path":"`+filepath.ToSlash(filepath.Join(filepath.Dir(l.Config), "cert.pem"))+`"}}`, 0600)
	input := map[string]any{"Log": map[string]any{"Level": "info"}, "Cores": []any{map[string]any{"Type": "sing", "OriginalPath": filepath.ToSlash(filepath.Join(filepath.Dir(l.Config), "native.json"))}}, "Nodes": []any{map[string]any{"Core": "sing", "ApiHost": "https://panel.example.com", "ApiKey": "saved-token", "NodeID": 7, "NodeType": "vless"}}}
	data, _ := json.Marshal(input)
	put(l.Config, string(data), 0600)
	return l
}
func stubV2BXServices(t *testing.T, healthy bool) *[]string {
	t.Helper()
	old := systemctl
	oldAri := waitAriMigrationHealth
	oldV2 := waitV2BXMigrationHealth
	t.Cleanup(func() { systemctl = old; waitAriMigrationHealth = oldAri; waitV2BXMigrationHealth = oldV2 })
	var calls []string
	systemctl = func(args ...string) error { calls = append(calls, strings.Join(args, " ")); return nil }
	waitAriMigrationHealth = func(_ string, count int) error {
		if count != 1 {
			t.Fatal("incorrect health node count")
		}
		if !healthy {
			return errors.New("node retrying")
		}
		return nil
	}
	waitV2BXMigrationHealth = func() error { return nil }
	return &calls
}
func TestV2BXFullMigrationUninstallsAndRestoresCurrentConfig(t *testing.T) {
	l := testV2BXLayout(t)
	stubV2BXServices(t, true)
	if err := migrateV2BXInstallation(l, v2bxMigrationOptions{From: true, Force: true, Input: l.Config, Output: l.AriConfig}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{l.Config, l.Unit, filepath.Join(l.Program, "V2bX"), l.Wrapper, l.Alias} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("source not uninstalled: %s", p)
		}
	}
	data, _ := os.ReadFile(l.AriConfig)
	var root map[string]any
	json.Unmarshal(data, &root)
	core := root["Cores"].([]any)[0].(map[string]any)
	asset := core["OriginalPath"].(string)
	if _, err := os.Stat(asset); err != nil {
		t.Fatal("native Core asset was removed")
	}
	native, _ := os.ReadFile(asset)
	if strings.Contains(string(native), filepath.ToSlash(filepath.Dir(l.Config))) {
		t.Fatal("embedded native paths still refer to uninstalled config")
	}
	root["Nodes"].([]any)[0].(map[string]any)["NodeID"] = 8
	data, _ = json.Marshal(root)
	os.WriteFile(l.AriConfig, data, 0600)
	if err := migrateV2BXInstallation(l, v2bxMigrationOptions{From: false, Force: true, Input: l.AriConfig, Output: l.Config}); err != nil {
		t.Fatal(err)
	}
	bin, _ := os.ReadFile(filepath.Join(l.Program, "V2bX"))
	if string(bin) != "saved-executable" {
		t.Fatal("original executable not restored")
	}
	data, _ = os.ReadFile(l.Config)
	json.Unmarshal(data, &root)
	if root["Nodes"].([]any)[0].(map[string]any)["NodeID"] != float64(8) {
		t.Fatal("return restored a stale node config")
	}
	asset = root["Cores"].([]any)[0].(map[string]any)["OriginalPath"].(string)
	if _, err := os.Stat(asset); err != nil {
		t.Fatal("return removed referenced certificate/Core assets")
	}
	for _, p := range []string{l.AriConfig, l.AriUnit, l.AriBinary, l.AriCTL} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("AriNode source runtime not uninstalled")
		}
	}
}
func TestV2BXFailedHealthRetainsSourceAndRollsBackConfig(t *testing.T) {
	l := testV2BXLayout(t)
	calls := stubV2BXServices(t, false)
	old := []byte(`{"old":"protected"}`)
	os.MkdirAll(filepath.Dir(l.AriConfig), 0700)
	os.WriteFile(l.AriConfig, old, 0600)
	if err := migrateV2BXInstallation(l, v2bxMigrationOptions{From: true, Force: true, Input: l.Config, Output: l.AriConfig}); err == nil {
		t.Fatal("retrying nodes accepted as a successful migration")
	}
	data, _ := os.ReadFile(l.AriConfig)
	if string(data) != string(old) {
		t.Fatal("old AriNode config not restored")
	}
	if _, err := os.Stat(filepath.Join(l.Program, "V2bX")); err != nil {
		t.Fatal("failed migration uninstalled source")
	}
	if !strings.Contains(strings.Join(*calls, ","), "start V2bX.service") {
		t.Fatal("source service not restarted")
	}
}
func TestV2BXSnapshotTamperingAndPreview(t *testing.T) {
	l := testV2BXLayout(t)
	calls := stubV2BXServices(t, true)
	if err := migrateV2BXInstallation(l, v2bxMigrationOptions{From: true, DryRun: true, Input: l.Config, Output: l.AriConfig}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatal("preview changed system services")
	}
	if err := migrateV2BXInstallation(l, v2bxMigrationOptions{From: true, Force: true, Input: l.Config, Output: l.AriConfig}); err != nil {
		t.Fatal(err)
	}
	dir, _, err := loadV2BXSnapshot(l)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "program", "V2bX"), []byte("changed"), 0700)
	if _, _, err := loadV2BXSnapshot(l); err == nil {
		t.Fatal("tampered recovery executable accepted")
	}
}

func TestMigrationHealthWaitsForRunningNodes(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Write([]byte(`{"service":"AriNode","nodes":[{"state":"retrying"}]}`))
			return
		}
		w.Write([]byte(`{"service":"AriNode","nodes":[{"state":"running"}]}`))
	}))
	defer s.Close()
	if err := waitAriMigrationHealth(s.URL, 1); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatal("service liveness was mistaken for a healthy node")
	}
}
func TestV2BXManagementSymlinkSurvivesRecovery(t *testing.T) {
	l := testV2BXLayout(t)
	stubV2BXServices(t, true)
	os.Remove(l.Alias)
	if err := os.Symlink(l.Wrapper, l.Alias); err != nil {
		t.Skipf("host cannot create symlink: %v", err)
	}
	dir, err := saveV2BXSnapshot(l)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Backups, "latest"), []byte(filepath.Base(dir)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := uninstallV2BX(l); err != nil {
		t.Fatal(err)
	}
	dir, manifest, err := loadV2BXSnapshot(l)
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreV2BXSnapshot(l, dir, manifest); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(l.Alias)
	if err != nil || target != l.Wrapper {
		t.Fatal("management symlink lost")
	}
}

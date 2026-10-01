package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSwitchSystemdRollsBackOnStartFailure(t *testing.T) {
	previous := systemctl
	t.Cleanup(func() { systemctl = previous })
	var calls []string
	systemctl = func(args ...string) error {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if call == "restart arinode.service" {
			return errors.New("failed")
		}
		return nil
	}
	if err := switchSystemd("xboard-node.service", "arinode.service"); err == nil {
		t.Fatal("expected start failure")
	}
	got := strings.Join(calls, ", ")
	want := "stop xboard-node.service, restart arinode.service, start xboard-node.service"
	if got != want {
		t.Fatalf("calls: %s", got)
	}
}

func TestWriteMigratedBacksUpBeforeReplacing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := writeMigrated(path, []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := writeMigrated(path, []byte("second"), false); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	backup, err := writeMigrated(path, []byte("second"), true)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(backup)
	if err != nil || string(previous) != "first" {
		t.Fatalf("backup failed: %q %v", previous, err)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != "second" {
		t.Fatalf("replacement failed: %q %v", current, err)
	}
	if !strings.Contains(filepath.Base(backup), ".bak-") {
		t.Fatalf("unexpected backup name: %s", backup)
	}
}

func TestMigrationLoadsXBNodeCredentialsAndRestoresEnvironment(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "config.yml")
	output := filepath.Join(dir, "config.json")
	const key = "ARINODE_TEST_XBNODE_TOKEN"
	t.Setenv(key, "original")
	if err := os.WriteFile(input, []byte("panel:\n  url: https://panel.example.com\n  token_env: "+key+"\n  node_id: 7\n  node_type: vless\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.env"), []byte("# xbnode token\n"+key+"=secret-from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runMigration(input, output, output, "", false, false, true, false, true); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(key); got != "original" {
		t.Fatalf("environment not restored: %q", got)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Nodes []struct {
			APIKey string `json:"ApiKey"`
		} `json:"Nodes"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Nodes) != 1 || config.Nodes[0].APIKey != "secret-from-file" {
		t.Fatalf("token not migrated: %s", data)
	}
}

func TestCredentialFileDoesNotExecuteShellSyntax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.env")
	if err := os.WriteFile(path, []byte("TEST_SAFE_TOKEN=$(touch /tmp/should-not-exist)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	restore, err := loadXBNodeCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TEST_SAFE_TOKEN"); got != "$(touch /tmp/should-not-exist)" {
		t.Fatalf("value changed: %q", got)
	}
	restore()
	if _, ok := os.LookupEnv("TEST_SAFE_TOKEN"); ok {
		t.Fatal("environment was not restored")
	}
}

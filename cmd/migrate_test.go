package cmd

import (
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
		if call == "start arinode.service" {
			return errors.New("failed")
		}
		return nil
	}
	if err := switchSystemd("xboard-node.service", "arinode.service"); err == nil {
		t.Fatal("expected start failure")
	}
	got := strings.Join(calls, ", ")
	want := "stop xboard-node.service, start arinode.service, start xboard-node.service"
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

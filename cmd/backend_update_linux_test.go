package cmd

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
)

func TestPanelUpgradeClaimsBeforeInstalling(t *testing.T) {
	const next = "20261113-87654321"
	const agent = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const job = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	installFailure := errors.New("restart failed")
	for _, tc := range []struct {
		name      string
		claim     error
		installed string
		failure   error
		result    string
	}{
		{name: "already claimed", claim: panel.ErrUpdateJobUnavailable},
		{name: "updated", installed: next, result: "succeeded"},
		{name: "already current", installed: version, result: "current"},
		{name: "failed", failure: installFailure, result: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent []panel.BackendUpdateRequest
			installed := false
			err := executePanelUpgrade(t.Context(), agent, job, func(request panel.BackendUpdateRequest) error {
				sent = append(sent, request)
				if request.Action == "claim" {
					return tc.claim
				}
				return nil
			}, func() (string, error) {
				if len(sent) != 1 || sent[0].Action != "claim" {
					t.Fatal("installer ran before claiming job")
				}
				installed = true
				return tc.installed, tc.failure
			})
			if !errors.Is(err, tc.failure) {
				t.Fatalf("unexpected installer error: %v", err)
			}
			if len(sent) < 1 || sent[0].Job != job || sent[0].Agent != agent {
				t.Fatal("missing claim for the selected job")
			}
			if tc.claim != nil {
				if installed || len(sent) != 1 {
					t.Fatal("unavailable job was executed")
				}
				return
			}
			if !installed || len(sent) != 2 || sent[1].Action != "finish" || sent[1].Result != tc.result {
				t.Fatalf("wrong completion report: %+v", sent)
			}
			if tc.result == "succeeded" && sent[1].Version != next {
				t.Fatalf("completion reported %s, want %s", sent[1].Version, next)
			}
		})
	}
}

func TestBackendAgentIDPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".backend-agent-id")
	first, err := backendAgentID(path)
	if err != nil || !panel.UpdateJobID.MatchString(first) {
		t.Fatalf("failed to initialize agent ID: %v", err)
	}
	second, err := backendAgentID(path)
	if err != nil || first != second {
		t.Fatalf("agent ID did not survive a reload: %v", err)
	}
}

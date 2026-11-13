package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Foxtea267/AriNode/conf"
)

func TestBackendUpdateProtocol(t *testing.T) {
	const job = "0123456789abcdef0123456789abcdef"
	const agent = "abcdef0123456789abcdef0123456789"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/panel/api/v1/arinode/backend-update" || r.URL.RawQuery != "" {
			t.Errorf("unexpected backend update request: %s %s", r.Method, r.URL)
		}
		var request struct {
			BackendUpdateRequest
			MachineID int    `json:"machine_id"`
			NodeID    *int   `json:"node_id"`
			Token     string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode backend update request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.MachineID != 39 || request.NodeID == nil || *request.NodeID != 0 || request.Token != "test-machine-token" || request.Agent != agent {
			t.Error("missing machine authentication or host identity")
		}
		switch request.Action {
		case "poll":
			if request.Version == "invalid-job-response" {
				fmt.Fprint(w, `{"data":{"job":"arbitrary argument"}}`)
			} else {
				fmt.Fprintf(w, `{"data":{"job":%q}}`, job)
			}
		case "claim":
			if request.Job != job {
				t.Error("claim did not identify the queued job")
			}
			w.WriteHeader(http.StatusConflict)
		case "finish":
			if request.Job != job || request.Result != "succeeded" || request.Version != "20261113-12345678" {
				t.Error("completion did not report the installed version")
			}
			fmt.Fprint(w, `{"data":{"accepted":true}}`)
		}
	}))
	defer server.Close()

	binding := conf.MachineBinding{APIHost: server.URL + "/panel/", MachineID: 39, APIKey: "test-machine-token"}
	request := BackendUpdateRequest{Action: "poll", Agent: agent, Version: "20261113-12345678"}
	got, err := backendUpdate(t.Context(), server.Client(), binding, request)
	if err != nil || got != job {
		t.Fatalf("poll returned %q, %v", got, err)
	}
	request.Action, request.Job = "claim", got
	if _, err := backendUpdate(t.Context(), server.Client(), binding, request); !errors.Is(err, ErrUpdateJobUnavailable) {
		t.Fatalf("claim should surface conflict, got %v", err)
	}
	request.Action, request.Result = "finish", "succeeded"
	if _, err := backendUpdate(t.Context(), server.Client(), binding, request); err != nil {
		t.Fatalf("finish failed: %v", err)
	}
	request.Action, request.Version = "poll", "invalid-job-response"
	if _, err := backendUpdate(t.Context(), server.Client(), binding, request); err == nil {
		t.Fatal("arbitrary job ID was accepted")
	}
	binding.APIHost = strings.Replace(binding.APIHost, "https://", "http://", 1)
	if _, err := BackendUpdate(t.Context(), binding, request); err == nil {
		t.Fatal("cleartext endpoint was accepted")
	}
}

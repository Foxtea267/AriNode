package panel

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Foxtea267/AriNode/conf"
	"github.com/go-resty/resty/v2"
	"github.com/sirupsen/logrus"
)

// This changes a process-global writer and must not be used in parallel tests.
// Resty's default logger calls log.New(os.Stderr, ...) when New constructs it;
// log.SetOutput only changes the unrelated package-level standard logger.
func capturePanelStderr(t *testing.T) func() string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "panel-stderr-*.log")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = file
	t.Cleanup(func() {
		os.Stderr = previous
		if err := file.Close(); err != nil {
			t.Errorf("close stderr capture: %v", err)
		}
	})
	return func() string {
		t.Helper()
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

func TestPanelTLSFailureDoesNotExposeQueryCredentials(t *testing.T) {
	stderr := capturePanelStderr(t)
	authority := newTLSTestAuthority(t)
	leaf := authority.issueLeaf(t, 120, []string{"backend.novonet.cc"}, authority.rootYE.NotBefore, authority.rootYE.NotAfter)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	server.StartTLS()
	t.Cleanup(server.Close)
	// Verify that this captures Resty's private log.New(os.Stderr, ...) writer,
	// so replacing quietRestyLogger with the stock logger makes this test fail.
	control := resty.New().SetRetryCount(1)
	_, controlErr := control.R().Get(server.URL + "?token=STDERR_CAPTURE_CONTROL")
	if controlErr == nil || !strings.Contains(stderr(), "STDERR_CAPTURE_CONTROL") {
		t.Fatal("stderr capture did not observe Resty's stock retry logger")
	}

	panelAddress := server.URL + "?credential_in_base_url=BASE_URL_SECRET"
	client, err := New(&conf.ApiConfig{APIHost: panelAddress, Key: "PANEL_QUERY_SECRET", NodeID: 244, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	client.client.OnBeforeRequest(func(_ *resty.Client, _ *resty.Request) error {
		attempts.Add(1)
		return nil
	})
	client.client.SetTLSClientConfig(&tls.Config{ServerName: "backend.novonet.cc"})

	var logs bytes.Buffer
	previousOutput := logrus.StandardLogger().Out
	previousLevel := logrus.GetLevel()
	logrus.SetOutput(&logs)
	logrus.SetLevel(logrus.WarnLevel)
	t.Cleanup(func() {
		logrus.SetOutput(previousOutput)
		logrus.SetLevel(previousLevel)
	})
	_, requestErr := client.GetUserList()
	if requestErr == nil || !strings.Contains(requestErr.Error(), "tls_unknown_authority") {
		t.Fatalf("expected a sanitized TLS verification error, got %v", requestErr)
	}
	if strings.Contains(requestErr.Error(), "PANEL_QUERY_SECRET") || strings.Contains(requestErr.Error(), "BASE_URL_SECRET") || strings.Contains(requestErr.Error(), "?") {
		t.Fatalf("returned error contains URL credentials or query data: %v", requestErr)
	}
	if attempts.Load() != 4 {
		t.Fatalf("expected the initial request plus three retries, got %d attempts", attempts.Load())
	}
	output := logs.String()
	for _, secret := range []string{"PANEL_QUERY_SECRET", "BASE_URL_SECRET"} {
		if strings.Contains(output, secret) || strings.Contains(stderr(), secret) {
			t.Fatalf("TLS diagnostic logs exposed %s", secret)
		}
	}
	if !strings.Contains(output, "tls_unknown_authority") || !strings.Contains(output, "peer_cert_sha256") || !strings.Contains(output, "system_cert_pool_readable=") {
		t.Fatalf("TLS diagnostic fields missing from sanitized logs: %s", output)
	}

	var unknown x509.UnknownAuthorityError
	if strings.Contains(requestErr.Error(), unknown.Error()) {
		t.Fatal("returned error unexpectedly preserved raw certificate details")
	}
}

func TestPanelDebugDoesNotDumpCredentialsOrBodies(t *testing.T) {
	stderr := capturePanelStderr(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "DEBUG_QUERY_SECRET" || r.Header.Get("Authorization") != "Bearer DEBUG_HEADER_SECRET" {
			t.Error("debug test request lost authentication fields")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid":"DEBUG_RESPONSE_UUID_SECRET","password":"DEBUG_RESPONSE_PASSWORD_SECRET"}`))
	}))
	defer server.Close()
	client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "DEBUG_QUERY_SECRET", NodeID: 244, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	client.Debug()
	if client.client.Debug {
		t.Fatal("panel Debug enabled Resty's sensitive request/response formatter")
	}
	response, err := client.client.R().
		SetHeader("Authorization", "Bearer DEBUG_HEADER_SECRET").
		SetBody(map[string]string{"token": "DEBUG_BODY_TOKEN_SECRET", "uuid": "DEBUG_BODY_UUID_SECRET", "password": "DEBUG_BODY_PASSWORD_SECRET"}).
		Post("/api/v1/server/UniProxy/push")
	if err != nil || response == nil || response.StatusCode() != http.StatusOK {
		t.Fatalf("debug test request failed: %v", err)
	}
	output := stderr()
	for _, secret := range []string{"DEBUG_QUERY_SECRET", "DEBUG_HEADER_SECRET", "DEBUG_BODY_TOKEN_SECRET", "DEBUG_BODY_UUID_SECRET", "DEBUG_BODY_PASSWORD_SECRET", "DEBUG_RESPONSE_UUID_SECRET", "DEBUG_RESPONSE_PASSWORD_SECRET"} {
		if strings.Contains(output, secret) {
			t.Fatalf("debug dump exposed %s", secret)
		}
	}
	if strings.Contains(output, "~~~ REQUEST") || strings.Contains(output, "~~~ RESPONSE") {
		t.Fatal("panel Debug enabled a raw Resty request/response dump")
	}
}

func TestPanelDiagnosticAddressesAreSanitized(t *testing.T) {
	tests := []struct {
		name, input, panel, endpoint string
	}{
		{"base path", "https://panel.example.com/xboard/?token=QUERY_SECRET#FRAGMENT_SECRET", "https://panel.example.com/xboard/", "/xboard/"},
		{"IPv6", "https://[::1]:8443/panel?password=QUERY_SECRET", "https://[::1]:8443/panel", "/panel"},
		{"host only", "https://panel.example.com?token=QUERY_SECRET", "https://panel.example.com", "/"},
		{"relative endpoint", "/api/v2/server/user?token=QUERY_SECRET#FRAGMENT_SECRET", "<invalid-panel-url>", "/api/v2/server/user"},
		{"userinfo", "https://USER_SECRET:PASSWORD_SECRET@panel.example.com/api?token=QUERY_SECRET", "<invalid-panel-url>", "/api"},
		{"invalid escape", "https://panel.example.com/%zz?token=QUERY_SECRET", "<invalid-panel-url>", "<invalid-endpoint>"},
		{"invalid URL", "https://[::1?token=QUERY_SECRET", "<invalid-panel-url>", "<invalid-endpoint>"},
		{"query only", "?token=QUERY_SECRET", "<invalid-panel-url>", "/"},
		{"empty", "", "<invalid-panel-url>", "/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SafePanelAddress(test.input); got != test.panel {
				t.Errorf("panel address = %q, want %q", got, test.panel)
			}
			if got := safeEndpoint(test.input); got != test.endpoint {
				t.Errorf("endpoint = %q, want %q", got, test.endpoint)
			}
		})
	}
}

func TestPanelResponseErrorsOmitHostAndBody(t *testing.T) {
	const bodySecret = "RESPONSE_BODY_SECRET"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(bodySecret))
	}))
	defer server.Close()
	client, err := New(&conf.ApiConfig{APIHost: server.URL + "?credential_in_base_url=BASE_URL_SECRET", Key: "PANEL_QUERY_SECRET", NodeID: 244, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.client.R().Get("/api/v1/server/UniProxy/config")
	if err != nil {
		t.Fatal(err)
	}
	requestErr := client.checkResponse(response, "/api/v1/server/UniProxy/config", nil)
	if requestErr == nil || !strings.Contains(requestErr.Error(), "HTTP 500") {
		t.Fatalf("expected sanitized status error, got %v", requestErr)
	}
	for _, secret := range []string{bodySecret, "PANEL_QUERY_SECRET", "BASE_URL_SECRET"} {
		if strings.Contains(requestErr.Error(), secret) {
			t.Fatalf("response error exposed %s", secret)
		}
	}
}

func TestPanelUserDecodeErrorsOmitPayloadValues(t *testing.T) {
	tests := []struct {
		name, payload, secret string
	}{
		{"numeric UUID", `{"users":[{"id":1,"uuid":723698431056287194}]}`, "723698431056287194"},
		{"object UUID", `{"users":[{"id":1,"uuid":{"USER_UUID_BODY_SECRET":"USER_PASSWORD_BODY_SECRET"}}]}`, "USER_UUID_BODY_SECRET"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", "invalid-user-snapshot")
				_, _ = w.Write([]byte(test.payload))
			}))
			defer server.Close()
			client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "USER_QUERY_SECRET", NodeID: 244, NodeType: "vless"})
			if err != nil {
				t.Fatal(err)
			}
			client.userEtag = "last-valid-user-snapshot"
			users, err := client.GetUserList()
			if err == nil || users != nil {
				t.Fatalf("invalid UUID response accepted: users=%v err=%v", users, err)
			}
			if !strings.Contains(err.Error(), "decode user list") {
				t.Fatalf("decode error lost operation context: %v", err)
			}
			for _, secret := range []string{test.secret, "USER_PASSWORD_BODY_SECRET", "USER_QUERY_SECRET"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("user decode error exposed payload value %q: %v", secret, err)
				}
			}
			if client.userEtag != "last-valid-user-snapshot" {
				t.Fatal("invalid user response advanced the successful snapshot ETag")
			}
		})
	}
}

func TestPanelNodeDecodeErrorsOmitPayloadValues(t *testing.T) {
	tests := []struct {
		name, payload string
	}{
		{"tls", `{"tls":"NODE_TLS_BODY_SECRET","server_port":443}`},
		{"server port", `{"tls":0,"server_port":"NODE_PORT_BODY_SECRET"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.payload))
			}))
			defer server.Close()
			client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "NODE_QUERY_SECRET", NodeID: 244, NodeType: "vless"})
			if err != nil {
				t.Fatal(err)
			}
			node, err := client.GetNodeInfo()
			if err == nil || node != nil {
				t.Fatalf("invalid node fields accepted: node=%v err=%v", node, err)
			}
			if !strings.Contains(err.Error(), "decode") {
				t.Fatalf("node decode error lost operation context: %v", err)
			}
			for _, secret := range []string{"NODE_TLS_BODY_SECRET", "NODE_PORT_BODY_SECRET", "NODE_QUERY_SECRET"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("node decode error exposed payload value %q: %v", secret, err)
				}
			}
		})
	}
}

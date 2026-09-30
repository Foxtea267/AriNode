package migrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFromXBNodeDiscoversSingleNodeAndRoundTrips(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/server/UniProxy/config" || r.URL.Query().Get("node_id") != "7" || r.URL.Query().Get("token") != "secret" {
			t.Errorf("unexpected request: %s", r.URL)
			http.Error(w, "bad request", 400)
			return
		}
		_, _ = w.Write([]byte(`{"protocol":"vless"}`))
	}))
	defer server.Close()
	input := []byte("panel:\n  url: " + server.URL + "\n  token: secret\n  node_id: 7\nkernel:\n  type: singbox\n")
	result, err := FromXBNode(input, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var ar aRoot
	if err := json.Unmarshal(result.Data, &ar); err != nil {
		t.Fatal(err)
	}
	if result.Nodes != 1 || ar.Nodes[0].NodeType != "vless" || ar.Nodes[0].APIKey != "secret" || ar.Nodes[0].Core != "sing" {
		t.Fatalf("incorrect migration: %+v", ar)
	}
	reverse, err := ToXBNode(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reverse.Data), "node_id: 7") || !strings.Contains(string(reverse.Data), "node_type: vless") {
		t.Fatalf("incorrect reverse migration: %s", reverse.Data)
	}
}

func TestGenericV2RayTypeDiscoversActualProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"protocol":"vless"}`))
	}))
	defer server.Close()
	input := []byte("panel:\n  url: " + server.URL + "\n  token: secret\n  node_id: 7\n  node_type: v2ray\n")
	result, err := FromXBNode(input, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var ar aRoot
	if err := json.Unmarshal(result.Data, &ar); err != nil {
		t.Fatal(err)
	}
	if ar.Nodes[0].NodeType != "vless" {
		t.Fatalf("generic type was not resolved: %s", ar.Nodes[0].NodeType)
	}
}

func TestFromXBNodeMachineDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/server/machine/nodes" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.Error(w, "bad request", 400)
			return
		}
		var auth struct {
			MachineID int    `json:"machine_id"`
			Token     string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&auth); err != nil || auth.MachineID != 3 || auth.Token != "machine-secret" {
			t.Errorf("bad auth: %+v %v", auth, err)
		}
		_, _ = w.Write([]byte(`{"nodes":[{"id":8,"type":"trojan"},{"id":2,"type":"v2ray"}]}`))
	}))
	defer server.Close()
	input := []byte("panel:\n  url: " + server.URL + "\nmachine:\n  machine_id: 3\n  token: machine-secret\n")
	result, err := FromXBNode(input, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var ar aRoot
	if err := json.Unmarshal(result.Data, &ar); err != nil {
		t.Fatal(err)
	}
	if result.Nodes != 2 || ar.Nodes[0].NodeID != 2 || ar.Nodes[0].NodeType != "vmess" || ar.Nodes[1].MachineID != 3 {
		t.Fatalf("incorrect machine migration: %+v", ar.Nodes)
	}
	reverse, err := ToXBNode(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(reverse.Data), "machine_id: 3") != 1 {
		t.Fatalf("machine not grouped: %s", reverse.Data)
	}
	if len(reverse.Warnings) == 0 {
		t.Fatal("dynamic discovery warning missing")
	}
}

func TestFromXBNodeOfflineNeedsType(t *testing.T) {
	_, err := FromXBNode([]byte("panel:\n  url: https://panel.example.com\n  token: secret\n  node_id: 1\n"), Options{Offline: true})
	if err == nil || !strings.Contains(err.Error(), "node type missing") {
		t.Fatalf("unexpected error: %v", err)
	}
	result, err := FromXBNode([]byte("panel:\n  url: https://panel.example.com\n  token: secret\n  node_id: 1\n"), Options{Offline: true, NodeType: "vless"})
	if err != nil || result.Nodes != 1 {
		t.Fatalf("offline migration failed: %v", err)
	}
}

func TestMultiInstanceAndCert(t *testing.T) {
	input := []byte("kernel:\n  type: xray\ninstances:\n  - panel:\n      url: https://panel.example.com\n      token: a\n    nodes:\n      - node_id: 1\n        node_type: vless\n      - node_id: 2\n        node_type: trojan\n    cert:\n      cert_mode: file\n      cert_file: /cert.pem\n      key_file: /key.pem\n")
	result, err := FromXBNode(input, Options{Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	var ar aRoot
	if err := json.Unmarshal(result.Data, &ar); err != nil {
		t.Fatal(err)
	}
	if result.Nodes != 2 || ar.Nodes[0].Core != "xray" || ar.Nodes[1].CertConfig.CertMode != "file" {
		t.Fatalf("bad multi-instance migration: %+v", ar)
	}
}

func TestExportNamedPanelsAndWarnAboutKomari(t *testing.T) {
	input := []byte(`{"Cores":[{"Type":"sing"}],"Panels":[{"Name":"a","ApiHost":"https://a.example.com","ApiKey":"a-key"},{"Name":"b","ApiHost":"https://b.example.com","ApiKey":"b-key"}],"Komari":[{"Name":"monitor"}],"Nodes":[{"Panel":"a","Core":"sing","NodeID":1,"NodeType":"vless"},{"Panel":"b","Core":"sing","NodeID":2,"NodeType":"trojan"}]}`)
	result, err := ToXBNode(input)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result.Data)
	if result.Nodes != 2 || !strings.Contains(text, "a.example.com") || !strings.Contains(text, "b.example.com") || !strings.Contains(text, "a-key") || !strings.Contains(text, "b-key") {
		t.Fatalf("named panels not exported: %s", text)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "Komari") {
		t.Fatalf("Komari omission not reported: %v", result.Warnings)
	}
}

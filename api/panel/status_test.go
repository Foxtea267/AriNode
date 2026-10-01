package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Foxtea267/AriNode/common/sysstatus"
	"github.com/Foxtea267/AriNode/conf"
)

func TestReportStatusUsesUniProxyContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/server/UniProxy/status" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("node_type") != "vless" || q.Get("node_id") != "7" || q.Get("token") != "secret" {
			t.Errorf("unexpected auth query: %v", q)
		}
		var body sysstatus.Status
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.CPU != 12.5 || body.Mem.Total != 1024 || body.Disk.Used != 50 {
			t.Errorf("unexpected status body: %+v", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "secret", NodeType: "vless", NodeID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ReportStatus(sysstatus.Status{CPU: 12.5, Mem: sysstatus.Usage{Total: 1024}, Disk: sysstatus.Usage{Used: 50}}); err != nil {
		t.Fatal(err)
	}
}

func TestMachineBindingUsesV2Contract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("machine_id") != "3" || q.Get("node_id") != "7" || q.Get("token") != "machine-secret" || q.Get("node_type") != "" {
			t.Errorf("unexpected machine auth query: %v", q)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "machine-secret", NodeType: "vless", NodeID: 7, MachineID: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ReportStatus(sysstatus.Status{}); err != nil {
		t.Fatal(err)
	}
}

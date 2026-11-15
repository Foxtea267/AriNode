package panel

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/common/sysstatus"
	"github.com/Foxtea267/AriNode/conf"
)

// The fixture deliberately injects a private local CA. This tests real panel
// request/cache behavior across a TLS outage, not the production system-root
// discrepancy or exactly-once accounting after an ambiguous HTTP response.
func TestPanelTLSRecoveryPreservesSnapshotsAndReportsOnce(t *testing.T) {
	trusted := newTLSTestAuthority(t)
	untrusted := newTLSTestAuthority(t)
	now := time.Now()
	good := trusted.issueLeaf(t, 200, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	bad := untrusted.issueLeaf(t, 201, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	recovered := trusted.issueLeaf(t, 202, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	var certificate atomic.Pointer[tls.Certificate]
	certificate.Store(&good)
	var revision atomic.Int64
	revision.Store(1)
	var requests, pushes, statuses, machineStatuses atomic.Int64
	var reportMu sync.Mutex
	var reports []map[int][]int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/arinode/node/config":
			w.WriteHeader(http.StatusNotFound) // exercise native XBoard fallback
		case "/api/v2/server/config":
			expectedETag := fmt.Sprintf("config-%d", revision.Load())
			w.Header().Set("ETag", expectedETag)
			if r.Header.Get("If-None-Match") == expectedETag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = fmt.Fprintf(w, `{"server_port":%d,"tls":0}`, 12000+revision.Load())
		case "/api/v2/server/user":
			expectedETag := fmt.Sprintf("users-%d", revision.Load())
			w.Header().Set("ETag", expectedETag)
			if r.Header.Get("If-None-Match") == expectedETag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = fmt.Fprintf(w, `{"users":[{"id":17,"uuid":"00000000-0000-0000-0000-000000000017","speed_limit":%d,"device_limit":3}]}`, revision.Load()*100)
		case "/api/v2/server/alivelist":
			_, _ = fmt.Fprintf(w, `{"alive":{"17":%d},"ips":{"17":["192.0.2.17"]}}`, revision.Load())
		case "/api/v2/server/machine/nodes":
			_, _ = fmt.Fprintf(w, `{"nodes":[{"id":%d,"type":"vless"}]}`, 6+revision.Load())
		case "/api/v2/server/push":
			var report map[int][]int64
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Error("decode isolated traffic report:", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			reportMu.Lock()
			reports = append(reports, report)
			reportMu.Unlock()
			pushes.Add(1)
			_, _ = w.Write([]byte(`{}`))
		case "/api/v2/server/status":
			statuses.Add(1)
			_, _ = w.Write([]byte(`{}`))
		case "/api/v2/server/machine/status":
			machineStatuses.Add(1)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected isolated panel endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{good}, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return certificate.Load(), nil
	}}
	server.StartTLS()
	t.Cleanup(server.Close)
	newClient := func(nodeID int, nodeType string) *Client {
		client, err := New(&conf.ApiConfig{APIHost: server.URL, Key: "isolated-test-token", MachineID: 39, NodeID: nodeID, NodeType: nodeType})
		if err != nil {
			t.Fatal(err)
		}
		client.client.SetTLSClientConfig(&tls.Config{RootCAs: tlsTestRoots(trusted.rootX2), ServerName: "backend.novonet.cc", MinVersion: tls.VersionTLS12})
		return client
	}
	client := newClient(7, "vless")
	discovery := newClient(0, "")
	closeIdle := func() {
		client.client.GetClient().CloseIdleConnections()
		discovery.client.GetClient().CloseIdleConnections()
	}

	firstNode, err := client.GetNodeInfo()
	if err != nil || firstNode == nil || firstNode.Common.ServerPort != 12001 {
		t.Fatalf("initial node configuration failed: node=%+v err=%v", firstNode, err)
	}
	firstUsers, err := client.GetUserList()
	if err != nil || len(firstUsers) != 1 || firstUsers[0].Id != 17 || firstUsers[0].SpeedLimit != 100 {
		t.Fatalf("initial user snapshot failed: users=%+v err=%v", firstUsers, err)
	}
	firstAlive, err := client.GetUserAlive()
	if err != nil || firstAlive[17] != 1 || len(client.AliveIPs[17]) != 1 {
		t.Fatalf("initial alive snapshot failed: alive=%+v err=%v", firstAlive, err)
	}
	firstMachines, err := discovery.GetMachineNodes()
	if err != nil || len(firstMachines) != 1 || firstMachines[0].ID != 7 {
		t.Fatalf("initial machine discovery failed: nodes=%+v err=%v", firstMachines, err)
	}
	initialBody := append([]byte(nil), client.nodeBody...)
	initialConfigHash, initialNodeETag, initialUserETag := client.responseBodyHash, client.nodeEtag, client.userEtag
	initialIPs := map[int][]string{17: append([]string(nil), client.AliveIPs[17]...)}
	initialAlive := map[int]int{17: firstAlive[17]}
	initialRequests := requests.Load()
	pendingTraffic := []UserTraffic{{UID: 17, Upload: 128, Download: 512}}

	certificate.Store(&bad)
	closeIdle()
	if node, err := client.GetNodeInfo(); err == nil || node != nil {
		t.Errorf("untrusted panel returned a node configuration: node=%+v err=%v", node, err)
	}
	if users, err := client.GetUserList(); err == nil || users != nil {
		t.Errorf("untrusted panel returned a user snapshot: users=%+v err=%v", users, err)
	}
	if alive, err := client.GetUserAlive(); err == nil || alive != nil {
		t.Errorf("untrusted panel returned an alive snapshot: alive=%+v err=%v", alive, err)
	}
	if nodes, err := discovery.GetMachineNodes(); err == nil || nodes != nil {
		t.Errorf("untrusted panel returned a machine snapshot: nodes=%+v err=%v", nodes, err)
	}
	if err := client.ReportUserTraffic(pendingTraffic); err == nil {
		t.Error("traffic report accepted an untrusted panel certificate")
	}
	if err := client.ReportStatus(sysstatus.Status{}); err == nil {
		t.Error("node status accepted an untrusted panel certificate")
	}
	if err := client.ReportMachineStatus(sysstatus.Status{}); err == nil {
		t.Error("machine status accepted an untrusted panel certificate")
	}
	if requests.Load() != initialRequests || pushes.Load() != 0 || statuses.Load() != 0 || machineStatuses.Load() != 0 {
		t.Errorf("TLS failures entered the HTTP handler: requests=%d want=%d traffic=%d node_status=%d machine_status=%d", requests.Load(), initialRequests, pushes.Load(), statuses.Load(), machineStatuses.Load())
	}
	if !bytes.Equal(client.nodeBody, initialBody) || client.responseBodyHash != initialConfigHash || client.nodeEtag != initialNodeETag || client.userEtag != initialUserETag {
		t.Error("TLS failure altered cached node bytes/hash or config/user ETags")
	}
	if client.AliveMap == nil || !reflect.DeepEqual(client.AliveMap.Alive, initialAlive) || !reflect.DeepEqual(client.AliveIPs, initialIPs) {
		t.Errorf("TLS failure cleared the last valid alive cache: alive=%+v ips=%+v", client.AliveMap, client.AliveIPs)
	}
	if firstNode.Common.ServerPort != 12001 || firstUsers[0].SpeedLimit != 100 || firstAlive[17] != 1 || firstMachines[0].ID != 7 {
		t.Error("TLS failure modified a previously returned valid snapshot")
	}

	certificate.Store(&recovered)
	closeIdle()
	if node, err := client.GetNodeInfo(); err != nil || node != nil {
		t.Errorf("unchanged node configuration did not retain its ETag after recovery: node=%+v err=%v", node, err)
	}
	if users, err := client.GetUserList(); err != nil || users != nil {
		t.Errorf("unchanged user snapshot did not retain its ETag after recovery: users=%+v err=%v", users, err)
	}
	if client.nodeEtag != initialNodeETag || client.userEtag != initialUserETag || !bytes.Equal(client.nodeBody, initialBody) {
		t.Error("304 recovery response changed cached node bytes or ETags")
	}
	if alive, err := client.GetUserAlive(); err != nil || alive[17] != 1 {
		t.Errorf("alive synchronization did not recover: alive=%+v err=%v", alive, err)
	}
	if nodes, err := discovery.GetMachineNodes(); err != nil || len(nodes) != 1 || nodes[0].ID != 7 {
		t.Errorf("machine discovery did not recover: nodes=%+v err=%v", nodes, err)
	}
	if err := client.ReportUserTraffic(pendingTraffic); err != nil {
		t.Error("recovered traffic report failed:", err)
	}
	if err := client.ReportStatus(sysstatus.Status{}); err != nil {
		t.Error("recovered node status failed:", err)
	}
	if err := client.ReportMachineStatus(sysstatus.Status{}); err != nil {
		t.Error("recovered machine status failed:", err)
	}
	reportMu.Lock()
	actualReports := append([]map[int][]int64(nil), reports...)
	reportMu.Unlock()
	if pushes.Load() != 1 || len(actualReports) != 1 || !reflect.DeepEqual(actualReports[0], map[int][]int64{17: {128, 512}}) {
		t.Errorf("recovery report was lost or duplicated: pushes=%d reports=%+v", pushes.Load(), actualReports)
	}
	if statuses.Load() != 1 || machineStatuses.Load() != 1 {
		t.Errorf("recovery status was lost or duplicated: node_status=%d machine_status=%d", statuses.Load(), machineStatuses.Load())
	}

	revision.Store(2)
	if node, err := client.GetNodeInfo(); err != nil || node == nil || node.Common.ServerPort != 12002 || node.ConfigHash == initialConfigHash {
		t.Errorf("new node configuration did not synchronize after recovery: node=%+v err=%v", node, err)
	}
	if users, err := client.GetUserList(); err != nil || len(users) != 1 || users[0].SpeedLimit != 200 {
		t.Errorf("new user snapshot did not synchronize after recovery: users=%+v err=%v", users, err)
	}
	if alive, err := client.GetUserAlive(); err != nil || alive[17] != 2 {
		t.Errorf("new alive snapshot did not synchronize after recovery: alive=%+v err=%v", alive, err)
	}
	if nodes, err := discovery.GetMachineNodes(); err != nil || len(nodes) != 1 || nodes[0].ID != 8 {
		t.Errorf("new machine snapshot did not synchronize after recovery: nodes=%+v err=%v", nodes, err)
	}
	if client.nodeEtag != "config-2" || client.userEtag != "users-2" {
		t.Errorf("ETags did not advance after recovery: config=%q users=%q", client.nodeEtag, client.userEtag)
	}
}

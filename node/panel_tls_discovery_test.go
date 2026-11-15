package node

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/sirupsen/logrus"
)

func TestMachineDiscoveryTLSFailurePreservesExistingNodeAndRedactsCredentials(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("untrusted TLS request reached panel handler")
	}))
	t.Cleanup(server.Close)

	previous := testConfig(server.URL+"?bootstrap=BASE_URL_SECRET", "vless", 244, 39)
	previous.ApiConfig.APIHost = strings.TrimRight(server.URL, "/") + "/panel?bootstrap=BASE_URL_SECRET"
	previous.ApiConfig.Key = "PANEL_QUERY_SECRET"
	previous.Options.Name = "stable-machine-node"
	template := previous
	template.ApiConfig.NodeID = 0
	template.ApiConfig.NodeType = ""
	apiClient, err := panel.New(&template.ApiConfig)
	if err != nil {
		t.Fatal(err)
	}
	core := &fakeCore{}
	controller := NewController(core, apiClient, &previous.Options)
	controller.tag = "stable-machine-node"
	controller.coreAdded = true
	controller.info = &panel.NodeInfo{Id: 244, Type: "vless", Common: &panel.CommonNode{ServerPort: 12345}}
	entry := newManaged(previous)
	entry.controller = controller
	entry.runtimeTag = "stable-machine-node"
	entry.status.State = "running"
	manager := &Node{configured: []conf.NodeConfig{previous, template}, entries: []*managedNode{entry}, core: core}
	manager.snapshot.Store([]BindingStatus{entry.status})

	var logs bytes.Buffer
	previousOutput := logrus.StandardLogger().Out
	previousLevel := logrus.GetLevel()
	logrus.SetOutput(&logs)
	logrus.SetLevel(logrus.WarnLevel)
	t.Cleanup(func() {
		logrus.SetOutput(previousOutput)
		logrus.SetLevel(previousLevel)
	})
	desired := manager.discoverBindingsLocked()
	if len(desired) != 1 || desired[0].ApiConfig.NodeID != 244 || desired[0].Options.Name != "stable-machine-node" {
		t.Fatalf("TLS discovery failure did not preserve the existing binding: %+v", desired)
	}
	manager.reconcileLocked(desired)
	if len(manager.entries) != 1 || manager.entries[0].controller != controller || !controller.coreAdded || len(core.removed) != 0 {
		t.Fatal("TLS discovery failure removed the running node")
	}
	if snapshot := manager.Snapshot(); len(snapshot) != 1 || snapshot[0].State != "running" || snapshot[0].NodeID != 244 {
		t.Fatalf("running node status was not retained: %+v", snapshot)
	}
	output := logs.String()
	for _, secret := range []string{"PANEL_QUERY_SECRET", "BASE_URL_SECRET"} {
		if strings.Contains(output, secret) {
			t.Fatalf("discovery failure log exposed %s", secret)
		}
	}
	if !strings.Contains(output, "tls_unknown_authority") || !strings.Contains(output, "Machine discovery failed; preserving existing nodes") {
		t.Fatalf("expected sanitized TLS discovery diagnostic missing: %s", output)
	}
	if !strings.Contains(output, "peer_cert_sha256=") || !strings.Contains(output, "tls_roots=system_default") {
		t.Fatalf("structured TLS verification fields missing: %s", output)
	}
}

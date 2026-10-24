package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/cert"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
	log "github.com/sirupsen/logrus"
)

type certificateCore struct {
	fakeCore
	certificates []*conf.CertConfig
}

func (c *certificateCore) AddNode(tag string, info *panel.NodeInfo, options *conf.Options) error {
	if err := c.fakeCore.AddNode(tag, info, options); err != nil {
		return err
	}
	copy := *options.CertConfig
	c.certificates = append(c.certificates, &copy)
	return nil
}

func TestPanelCertificateReloadOnceAndPreserveMachineTemplate(t *testing.T) {
	var body atomic.Value
	var calls atomic.Int64
	certificate, key, _ := cert.SelfSigned("node.example.com")
	payload := map[string]any{"server_port": 25331, "tls_settings": map[string]any{"server_name": "node.example.com", "allow_insecure": false},
		"cert_config": map[string]any{"cert_mode": "content", "cert_content": strings.Join(certificate, ""), "key_content": strings.Join(key, "")}}
	set := func() { data, _ := json.Marshal(payload); body.Store(string(data)) }
	set()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/server/config":
			fmt.Fprintf(w, "{\"timestamp\":%d,%s}", calls.Add(1), strings.TrimPrefix(strings.TrimSuffix(body.Load().(string), "}"), "{"))
		case "/api/v2/server/machine/nodes":
			fmt.Fprint(w, `{"nodes":[{"id":244,"type":"anytls"}]}`)
		case "/api/v2/server/user":
			fmt.Fprint(w, `{"users":[]}`)
		case "/api/v2/server/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := testConfig(server.URL, "anytls", 244, 39)
	cfg.Options.CertStorePath = t.TempDir()
	baseline := *cfg.Options.CertConfig
	limiter.Init()
	kernel := &certificateCore{}
	manager := New()
	if err := manager.Start([]conf.NodeConfig{cfg}, kernel); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	c := manager.entries[0].controller
	if c == nil {
		t.Fatal("panel content certificate did not start")
	}
	first := kernel.certificates[0]
	if first.CertMode != "file" || first.CertFile == "" || first.KeyFile == "" {
		t.Fatal("kernel was not given certificate paths")
	}
	stamp := time.Unix(1600000000, 0)
	os.Chtimes(first.CertFile, stamp, stamp)
	for i := 0; i < 20; i++ {
		c.nodeInfoMonitor()
		if err := manager.Reconcile([]conf.NodeConfig{cfg}); err != nil {
			t.Fatal(err)
		}
	}
	if manager.entries[0].controller != c || len(kernel.added) != 1 || len(kernel.removed) != 0 {
		t.Fatal("unchanged machine node restarted")
	}
	if !reflect.DeepEqual(*cfg.Options.CertConfig, baseline) {
		t.Fatal("runtime certificate mutated shared machine template")
	}
	stat, _ := os.Stat(first.CertFile)
	if !stat.ModTime().Equal(stamp) {
		t.Fatal("unchanged certificate rewritten on poll")
	}
	cert2, key2, _ := cert.SelfSigned("node.example.com")
	certPayload := payload["cert_config"].(map[string]any)
	certPayload["cert_content"], certPayload["key_content"] = strings.Join(cert2, ""), strings.Join(key2, "")
	set()
	c.nodeInfoMonitor()
	for i := 0; i < 20; i++ {
		c.nodeInfoMonitor()
	}
	if len(kernel.added) != 2 || len(kernel.removed) != 1 {
		t.Fatal("certificate change did not reload exactly once")
	}
	active := c.runtimeOptions.Load()
	certPayload["key_content"] = "invalid key"
	set()
	c.nodeInfoMonitor()
	if len(kernel.removed) != 1 || !c.configError.Load() || c.runtimeOptions.Load() != active {
		t.Fatal("bad certificate replaced the working inbound")
	}
	certPayload["key_content"] = strings.Join(key2, "")
	set()
	c.nodeInfoMonitor()
	if len(kernel.added) != 2 || len(kernel.removed) != 1 || c.configError.Load() {
		t.Fatal("restoring active configuration caused a redundant reload")
	}
	if _, err := cert.Load(first.CertFile, first.KeyFile); err != nil {
		t.Fatal("old generation lost", err)
	}
}

func TestFailedCertificateReloadRestoresPreviousPaths(t *testing.T) {
	api, _ := panel.New(&conf.ApiConfig{APIHost: "https://panel.example.com", Key: "test", NodeID: 244, NodeType: "anytls"})
	opts := conf.Options{CertConfig: conf.NewCertConfig(), CertStorePath: t.TempDir()}
	c := NewController(&certificateCore{}, api, &opts)
	c.tag = "cert-rollback"
	limiter.Init()
	c.limiter = limiter.AddLimiter(c.tag, &conf.LimitConfig{}, nil, nil)
	defer limiter.DeleteLimiter(c.tag)
	makeInfo := func() *panel.NodeInfo {
		certificate, key, _ := cert.SelfSigned("node.example.com")
		return &panel.NodeInfo{Id: 244, Type: "anytls", Security: panel.Tls, Common: &panel.CommonNode{ServerPort: 25331,
			CertConfig: &cert.PanelConfig{CertMode: "content", CertContent: strings.Join(certificate, ""), KeyContent: strings.Join(key, "")}}, TLSSettings: panel.NativeTLSSettings{ServerName: "node.example.com"}}
	}
	c.info = makeInfo()
	active, _, _, err := c.prepareNodeOptions(c.info)
	if err != nil {
		t.Fatal(err)
	}
	c.runtimeOptions.Store(active)
	c.coreAdded = true
	kernel := c.server.(*certificateCore)
	kernel.failNext = true
	if err := c.reloadNode(makeInfo(), nil, nil); err == nil {
		t.Fatal("expected failed new inbound")
	}
	if !c.coreAdded || c.runtimeOptions.Load() != active || kernel.certificates[0].CertFile != active.CertConfig.CertFile {
		t.Fatal("rollback used new certificate paths")
	}
}

func TestLegacyCertificatesAndAutomaticFallback(t *testing.T) {
	api, _ := panel.New(&conf.ApiConfig{APIHost: "https://panel.example.com", Key: "test", NodeID: 244, NodeType: "anytls"})
	store := cert.Store{Root: t.TempDir()}
	certificate, key, _ := cert.SelfSigned("node.example.com")
	material, err := store.Save("legacy", strings.Join(certificate, ""), strings.Join(key, ""))
	if err != nil {
		t.Fatal(err)
	}
	info := &panel.NodeInfo{Id: 244, Type: "anytls", Security: panel.Tls, Common: &panel.CommonNode{}, TLSSettings: panel.NativeTLSSettings{ServerName: "node.example.com"}}
	for _, mode := range []string{"file", "self"} {
		opts := conf.Options{CertStorePath: t.TempDir(), CertConfig: &conf.CertConfig{CertMode: mode, CertDomain: "node.example.com", CertFile: material.CertPath, KeyFile: material.KeyPath}}
		c := NewController(&fakeCore{}, api, &opts)
		got, leaf, _, err := c.prepareNodeOptions(info)
		if err != nil || got.CertConfig.CertMode != mode || got.CertConfig.CertFile != material.CertPath || leaf.Fingerprint != material.Fingerprint {
			t.Fatal("legacy file/self certificate changed", err)
		}
	}
	opts := conf.Options{CertStorePath: t.TempDir(), CertConfig: conf.NewCertConfig()}
	c := NewController(&fakeCore{}, api, &opts)
	if _, _, _, err := c.prepareNodeOptions(info); err == nil {
		t.Fatal("automatic self-signed bypassed verified-client consent")
	}
	info.TLSSettings.AllowInsecure = true
	first, leaf, source, err := c.prepareNodeOptions(info)
	if err != nil || source != "self-signed" {
		t.Fatal("automatic fallback did not resolve", err)
	}
	for i := 0; i < 10; i++ {
		next, material, _, err := c.prepareNodeOptions(info)
		if err != nil || next.CertConfig.CertFile != first.CertConfig.CertFile || material.Fingerprint != leaf.Fingerprint {
			t.Fatal("fallback certificate regenerated", err)
		}
	}
	var output bytes.Buffer
	previous := log.StandardLogger().Out
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	c.logCertificate(info, leaf, source)
	if !strings.Contains(output.String(), "cert_source=self-signed") || !strings.Contains(output.String(), leaf.Fingerprint) || strings.Contains(output.String(), "PRIVATE KEY") {
		t.Fatal("certificate diagnostics missing or leaking PEM")
	}
}

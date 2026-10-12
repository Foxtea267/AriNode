package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Foxtea267/AriNode/conf"
)

func TestV2BXNativeConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	node := `{"ApiConfig":{"ApiHost":"https://panel.example.com","ApiKey":"secret","NodeID":7,"NodeType":"vless"},"Options":{"Core":"xray","ListenIP":"::","SendIP":"192.0.2.1","ReportMinTraffic":4,"LimitConfig":{"SpeedLimit":10},"CertConfig":{"CertMode":"file","CertFile":"/etc/V2bX/cert.pem","KeyFile":"/etc/V2bX/key.pem"}}}`
	if err := os.WriteFile(filepath.Join(dir, "node.json"), []byte(node), 0600); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"Log":{"Level":"debug"},"Cores":[{"Type":"xray","RouteConfigPath":"/etc/V2bX/route.json","OutboundConfigPath":"/etc/V2bX/outbounds.json","AssetPath":"/etc/V2bX"}],"Nodes":[{"Include":"node.json"}]}`)
	result, err := FromV2BX(input, V2BXOptions{SourceDir: dir, Cluster: &conf.ClusterConfig{Domain: "pool.example.com", MemberID: "hk-04"}})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	json.Unmarshal(result.Data, &root)
	n := root["Nodes"].([]any)[0].(map[string]any)
	if n["SendIP"] != "192.0.2.1" || n["ApiKey"] != "secret" || n["Include"] != nil || n["LimitConfig"] == nil || n["CertConfig"] == nil || n["Cluster"] == nil {
		t.Fatal("native or nested settings lost")
	}
	if _, err := ToV2BX(result.Data, V2BXOptions{}); err == nil {
		t.Fatal("silently discarded group aggregation")
	}
	reverse, err := ToV2BX(result.Data, V2BXOptions{DropCluster: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reverse.Data), "OutboundConfigPath") || !strings.Contains(string(reverse.Data), "ReportMinTraffic") || len(reverse.Warnings) == 0 {
		t.Fatal("round trip lost native config or warning")
	}
}
func TestV2BXIncludesRejectCyclesAndRemote(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "node.json"), []byte(`{"Include":"node.json"}`), 0600)
	for _, include := range []string{"node.json", "https://example.com/node.json"} {
		input := []byte(`{"Cores":[{"Type":"sing"}],"Nodes":[{"Include":"` + include + `"}]}`)
		if _, err := FromV2BX(input, V2BXOptions{SourceDir: dir}); err == nil {
			t.Fatal("unsafe Include accepted")
		}
	}
}
func TestV2BXAssetRebasingPreservesCredentialsAndArrays(t *testing.T) {
	data := []byte(`[{"certificate_path":"/etc/V2bX/cert.pem","password":"/etc/V2bX/password","private_key":"/etc/V2bX/key","ApiKey":"/etc/V2bX/token","server":"1.2.3.4"}]`)
	result, err := RebasePaths(data, "/etc/V2bX", "/etc/arinode/import")
	if err != nil {
		t.Fatal(err)
	}
	var values []map[string]any
	json.Unmarshal(result, &values)
	if values[0]["certificate_path"] != "/etc/arinode/import/cert.pem" || values[0]["password"] != "/etc/V2bX/password" || values[0]["ApiKey"] != "/etc/V2bX/token" || values[0]["private_key"] != "/etc/V2bX/key" {
		t.Fatal("paths or credentials corrupted")
	}
}
func TestV2BXRejectsIncompatibleExport(t *testing.T) {
	for _, node := range []string{`"NodeType":"anytls"`, `"NodeType":"vless","MachineID":4`, `"NodeType":"vless","CoreName":"missing"`} {
		data := []byte(`{"Cores":[{"Type":"sing"}],"Nodes":[{"ApiHost":"https://panel.example.com","ApiKey":"secret","NodeID":7,` + node + `}]}`)
		if _, err := ToV2BX(data, V2BXOptions{}); err == nil {
			t.Fatal("unsupported export silently accepted")
		}
	}
}

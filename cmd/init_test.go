package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteInitialConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("ARINODE_PANEL_TOKEN", "secret")
	if err := writeInitialConfig("https://panel.example.com/", "", "sing", path, []string{"vless:1", "trojan:2"}, 0, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Nodes []struct {
			APIHost string `json:"ApiHost"`
			APIKey  string `json:"ApiKey"`
			NodeID  int    `json:"NodeID"`
		} `json:"Nodes"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Nodes) != 2 || config.Nodes[0].APIHost != "https://panel.example.com" || config.Nodes[1].NodeID != 2 || config.Nodes[0].APIKey != "secret" {
		t.Fatalf("unexpected config: %+v", config)
	}
	if err := writeInitialConfig("https://panel.example.com", "secret", "sing", path, []string{"vless:1"}, 0, false); !os.IsExist(err) {
		t.Fatalf("expected existing file error, got %v", err)
	}
}

func TestWriteClusterConfigKeepsSharedNodeAndUniqueMember(t *testing.T) {
	for _, member := range []string{"hk-01", "hk-02"} {
		path := filepath.Join(t.TempDir(), "config.json")
		cluster, err := clusterValues("pool.example.com", member)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeInitialConfigWithCluster("https://panel.example.com", "shared", "sing", path, []string{"mieru:7"}, 22, false, cluster); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		var config struct {
			Nodes []struct {
				NodeID  int
				Cluster struct{ Domain, MemberID string }
			}
		}
		if json.Unmarshal(data, &config) != nil || config.Nodes[0].NodeID != 7 || config.Nodes[0].Cluster.MemberID != member {
			t.Fatal("replica identity not generated")
		}
	}
}

func TestWriteInitialConfigRejectsInvalidBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, nodes := range [][]string{{"vless:0"}, {"vless:1", "vless:1"}, {"invalid:1"}} {
		if err := writeInitialConfig("https://panel.example.com", "secret", "sing", path, nodes, 0, false); err == nil {
			t.Fatalf("accepted invalid nodes: %v", nodes)
		}
	}
}

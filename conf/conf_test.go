package conf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExampleConfig(t *testing.T) {
	c := New()
	if err := c.LoadFromPath("../example/arinode.config.json"); err != nil {
		t.Fatal(err)
	}
	if len(c.CoresConfig) != 1 || len(c.NodeConfig) != 1 || c.NodeConfig[0].ApiConfig.NodeID != 1 {
		t.Fatalf("unexpected example config: %+v", c)
	}
}

func TestDefaultSingCoreAndAliases(t *testing.T) {
	for _, config := range []string{
		`{"Nodes":[{"NodeID":1,"NodeType":"vless"}]}`,
		`{"Cores":[],"Nodes":[{"NodeID":1,"Options":{}}]}`,
		`{"Cores":[{}],"Nodes":[{"Core":"singbox"}]}`,
		`{"Cores":[{"Type":"sing-box"}],"Nodes":[{"Core":"SING-BOX"}]}`,
		`{"Cores":[{"Type":"xray"}],"Nodes":[{}]}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		c := New()
		if err := c.LoadFromPath(path); err != nil {
			t.Fatal(err)
		}
		o := c.NodeConfig[0].Options
		if o.Core != "sing" || o.SingOptions == nil || o.SingOptions.Multiplex == nil {
			t.Fatalf("missing sing options for %s", config)
		}
		found := false
		for _, core := range c.CoresConfig {
			if core.Type == "sing" && core.SingConfig != nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing default sing core for %s", config)
		}
	}
}

func TestNamedCoreIsHonored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"Cores":[{"Type":"xray","Name":"explicit"}],"Nodes":[{"CoreName":"explicit"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	c := New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	if c.NodeConfig[0].Options.Core != "xray" || c.NodeConfig[0].Options.XrayOptions == nil {
		t.Fatal("named xray core not initialized")
	}
}

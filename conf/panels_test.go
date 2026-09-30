package conf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMultipleNamedXboardPanels(t *testing.T) {
	t.Setenv("EAST_XBOARD_TOKEN", "east-secret")
	config := `{
  "Cores":[{"Type":"sing"}],
  "Panels":[
    {"Name":"east","ApiHost":"https://east.example.com/","ApiKeyEnv":"EAST_XBOARD_TOKEN","MachineID":7},
    {"Name":"west","ApiHost":"https://west.example.com","ApiKey":"west-secret","MachineID":7}
  ],
  "Nodes":[
    {"Panel":"east","Core":"sing","NodeID":1,"NodeType":"vless"},
    {"Panel":"west","Core":"sing","NodeID":2,"NodeType":"trojan"}
  ]
}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	c := New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	if len(c.NodeConfig) != 2 || c.NodeConfig[0].ApiConfig.APIHost != "https://east.example.com" || c.NodeConfig[0].ApiConfig.Key != "east-secret" || c.NodeConfig[1].ApiConfig.Key != "west-secret" || c.NodeConfig[0].ApiConfig.MachineID != 7 || c.NodeConfig[1].ApiConfig.MachineID != 7 {
		t.Fatalf("wrong panel resolution: %+v", c.NodeConfig)
	}
}

func TestUnknownPanelIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"Nodes":[{"Panel":"missing","NodeID":1,"NodeType":"vless"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := New().LoadFromPath(path); err == nil {
		t.Fatal("unknown panel accepted")
	}
}

func TestMultipleKomariBindings(t *testing.T) {
	t.Setenv("KOMARI_EAST_TOKEN", "east-token")
	config := `{"Komari":[{"Name":"east","Endpoint":"https://east.example.com/","TokenEnv":"KOMARI_EAST_TOKEN"},{"Name":"west","Endpoint":"https://west.example.com","Token":"west-token","Interval":8}]}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	c := New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	if len(c.Komari) != 2 || c.Komari[0].Token != "east-token" || c.Komari[0].Interval != 6 || c.Komari[1].Interval != 8 {
		t.Fatalf("wrong Komari resolution: %+v", c.Komari)
	}
}

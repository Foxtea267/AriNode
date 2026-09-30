package conf

import "testing"

func TestLoadExampleConfig(t *testing.T) {
	c := New()
	if err := c.LoadFromPath("../example/arinode.config.json"); err != nil {
		t.Fatal(err)
	}
	if len(c.CoresConfig) != 1 || len(c.NodeConfig) != 1 || c.NodeConfig[0].ApiConfig.NodeID != 1 {
		t.Fatalf("unexpected example config: %+v", c)
	}
}

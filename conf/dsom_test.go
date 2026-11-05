package conf

import (
	"path/filepath"
	"testing"
)

func TestDSOMExampleLoadsWithoutPanelCredentialsInJSON(t *testing.T) {
	t.Setenv("XBOARD_MACHINE_TOKEN", "example-test-token")
	c := New()
	if err := c.LoadFromPath(filepath.Join("..", "example", "dsom.config.json")); err != nil {
		t.Fatal(err)
	}
	if len(c.DynamicOutbounds) != 1 || c.DynamicOutbounds[0].Tag != "bd-residential" || len(c.NodeConfig) != 1 || len(c.NodeConfig[0].Options.DynamicRoutes) != 1 || c.NodeConfig[0].ApiConfig.MachineID != 39 {
		t.Fatal("documented configuration was not normalized")
	}
}

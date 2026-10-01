package core

import (
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
)

type selectionCore struct {
	kind                 string
	starts, closes, adds int
	options              *conf.Options
}

func (c *selectionCore) Type() string        { return c.kind }
func (c *selectionCore) Protocols() []string { return []string{"vless", "vmess"} }
func (c *selectionCore) Start() error        { c.starts++; return nil }
func (c *selectionCore) Close() error        { c.closes++; return nil }
func (c *selectionCore) AddNode(_ string, _ *panel.NodeInfo, options *conf.Options) error {
	c.adds++
	c.options = options
	return nil
}
func (c *selectionCore) DelNode(string) error                                     { return nil }
func (c *selectionCore) AddUsers(*AddUsersParams) (int, error)                    { return 0, nil }
func (c *selectionCore) DelUsers([]panel.UserInfo, string, *panel.NodeInfo) error { return nil }
func (c *selectionCore) GetUserTrafficSlice(string, bool) ([]panel.UserTraffic, error) {
	return nil, nil
}

func TestXHTTPCompatibilityCorePreservesDefault(t *testing.T) {
	sing := &selectionCore{kind: "sing"}
	xray := &selectionCore{kind: "xray"}
	oldFactory, existed := cores["xray"]
	created := 0
	cores["xray"] = func(config *conf.CoreConfig) (Core, error) {
		created++
		if config.XrayConfig == nil {
			t.Fatal("missing Xray defaults")
		}
		return xray, nil
	}
	t.Cleanup(func() {
		if existed {
			cores["xray"] = oldFactory
		} else {
			delete(cores, "xray")
		}
	})
	s := &Selector{cores: map[string]Core{"sing": sing}}
	options := &conf.Options{Core: "sing", ListenIP: "127.0.0.1"}
	for _, network := range []string{"xhttp", "splithttp"} {
		info := &panel.NodeInfo{Type: "vless", VAllss: &panel.VAllssNode{Network: network}}
		if err := s.AddNode(network, info, options); err != nil {
			t.Fatal(err)
		}
		if s.NodeCore(network) != "xray" || xray.options.XrayOptions == nil {
			t.Fatal("xhttp not routed to initialized Xray")
		}
	}
	if created != 1 || xray.starts != 1 {
		t.Fatal("compatibility core was not reused")
	}
	if options.Core != "sing" || options.XrayOptions != nil {
		t.Fatal("fallback changed the requested core")
	}
	if err := s.DelNode("xhttp"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNode("xhttp", &panel.NodeInfo{Type: "vless", VAllss: &panel.VAllssNode{Network: "tcp"}}, options); err != nil {
		t.Fatal(err)
	}
	if s.NodeCore("xhttp") != "sing" || sing.adds != 1 {
		t.Fatal("TCP did not return to sing-box")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if xray.closes != 1 {
		t.Fatal("compatibility core was not closed")
	}
}

func TestXHTTPUsesConfiguredXrayAndRespectsNamedSing(t *testing.T) {
	sing := &selectionCore{kind: "sing"}
	xray := &selectionCore{kind: "xray"}
	s := &Selector{cores: map[string]Core{"sing": sing, "custom-xray": xray}}
	info := &panel.NodeInfo{Type: "vless", VAllss: &panel.VAllssNode{Network: "xhttp"}}
	if err := s.AddNode("auto", info, &conf.Options{Core: "sing"}); err != nil {
		t.Fatal(err)
	}
	if s.NodeCore("auto") != "xray" || s.fallbackXray != nil {
		t.Fatal("did not reuse configured Xray")
	}
	if err := s.AddNode("pinned", info, &conf.Options{Core: "sing", CoreName: "sing"}); err == nil {
		t.Fatal("named sing core must report unsupported transport")
	}
	if err := s.AddNode("explicit", info, &conf.Options{Core: "xray", CoreName: "custom-xray", XrayOptions: conf.NewXrayOptions()}); err != nil {
		t.Fatal(err)
	}
}

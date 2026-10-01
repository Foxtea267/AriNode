package xray

import (
	"encoding/json"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
)

func TestXHTTPInboundAcceptsXboardEmptyObjects(t *testing.T) {
	options := &conf.Options{ListenIP: "127.0.0.1", XrayOptions: conf.NewXrayOptions()}
	// Native Xboard/PHP payload, including empty object fields in download settings.
	settings := json.RawMessage(`{"host":"example.com","path":"/xhttp","mode":"auto","extra":{"headers":[],"xmux":{"maxConcurrency":"16-32","cMaxReuseTimes":"64-128"},"downloadSettings":{"address":null,"port":443,"network":"xhttp","security":"tls","sockopt":[],"tlsSettings":[],"xhttpSettings":{"path":"/xhttp"}},"xPaddingBytes":"100-1000"}}`)
	for _, network := range []string{"xhttp", "splithttp"} {
		info := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: 25331}, VAllss: &panel.VAllssNode{Network: network, NetworkSettings: settings}}
		if _, err := buildInbound(options, info, "test"); err != nil {
			t.Fatalf("%s: %v", network, err)
		}
	}
	if _, err := normalizeXHTTPSettings(json.RawMessage(`{"mode":`)); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestXHTTPInboundWithoutNetworkSettings(t *testing.T) {
	for _, network := range []string{"tcp", "xhttp", "splithttp", ""} {
		info := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: 25331}, VAllss: &panel.VAllssNode{Network: network}}
		if _, err := buildInbound(&conf.Options{ListenIP: "127.0.0.1", XrayOptions: conf.NewXrayOptions()}, info, "test"); err != nil {
			t.Fatalf("%s: %v", network, err)
		}
	}
}

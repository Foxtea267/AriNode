package sing

import (
	"context"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/nodepolicy"
	"github.com/Foxtea267/AriNode/conf"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route"
	M "github.com/sagernet/sing/common/metadata"
)

func TestNativeSingPanelPolicyCompiler(t *testing.T) {
	server, err := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
	if err != nil {
		t.Fatal(err)
	}
	b := server.(*Sing)
	defer b.Close()
	info := &panel.NodeInfo{Common: &panel.CommonNode{CustomOutbounds: []panel.OutboundConfig{{Tag: "a", Protocol: "socks", Settings: map[string]any{"servers": []any{map[string]any{"address": "1.2.3.4", "port": 1080}}}}, {Tag: "b", Protocol: "socks", ProxyTag: "a", Settings: map[string]any{"server": "2.3.4.5", "server_port": 1080}}}, CustomRouteRules: []panel.CustomRouteRule{{Name: "claude", Match: panel.RouteMatch{Domains: []string{"*.claude.com"}, Ports: []string{"443", "8000-9000"}, Networks: []string{"tcp"}, SourceCIDRs: []string{"192.0.2.0/24"}, SourcePorts: []string{"1000-60000"}}, Action: panel.RouteAction{Type: "route", Target: "b"}}}}}
	compiled, err := b.compilePolicy("HK01", "1", info, &conf.Options{})
	if err != nil {
		t.Fatal(err)
	}
	first := compiled.Outbounds[2].Options.(*option.SOCKSOutboundOptions)
	second := compiled.Outbounds[3].Options.(*option.SOCKSOutboundOptions)
	if first.Server != "1.2.3.4" || first.ServerPort != 1080 || second.Detour != compiled.Outbounds[2].Tag {
		t.Fatal("flat socks settings or chain mapping lost")
	}
	rule := compiled.Rules[0].DefaultOptions
	if len(rule.DomainRegex) != 0 || rule.DomainSuffix[0] != "claude.com" || rule.Port[0] != 443 || rule.PortRange[0] != "8000:9000" || rule.SourcePortRange[0] != "1000:60000" || rule.RouteOptions.Outbound != compiled.Outbounds[3].Tag {
		t.Fatal("invalid native rule")
	}
	native := route.NewRouter(b.ctx, b.logFactory, option.RouteOptions{}, option.DNSOptions{})
	defer native.Close()
	if err := native.Initialize(compiled.Rules, nil); err != nil {
		t.Fatal(err)
	}
	metadata := adapter.InboundContext{Destination: M.ParseSocksaddr("sub.claude.com:443"), Source: M.ParseSocksaddr("192.0.2.1:1234"), Network: "tcp"}
	if !native.Rules()[0].Match(&metadata) {
		t.Fatal("native route did not match")
	}
	metadata.ResetRuleCache()
	metadata.Destination.Port = 80
	if native.Rules()[0].Match(&metadata) {
		t.Fatal("port condition broadened to OR")
	}
	// Domains + IP CIDRs also require AND, unlike sing-box's default grouping.
	info.Common.CustomRouteRules[0].Match.IPCIDRs = []string{"203.0.113.0/24"}
	compiled, err = b.compilePolicy("HK01", "2", info, &conf.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Rules[1].Type != "logical" || compiled.Rules[1].LogicalOptions.Mode != "and" || compiled.Rules[0].DefaultOptions.Action != "resolve" {
		t.Fatal("domain/IP categories broadened to OR")
	}
	logical := route.NewRouter(b.ctx, b.logFactory, option.RouteOptions{}, option.DNSOptions{})
	defer logical.Close()
	if err := logical.Initialize(compiled.Rules, nil); err != nil {
		t.Fatal(err)
	}
	metadata = adapter.InboundContext{Domain: "claude.com", Destination: M.ParseSocksaddr("203.0.113.1:443"), Source: M.ParseSocksaddr("192.0.2.1:1234"), Network: "tcp"}
	if !logical.Rules()[1].Match(&metadata) {
		t.Fatal("AND rule failed when all categories match")
	}
	metadata.ResetRuleCache()
	metadata.Destination = M.ParseSocksaddr("198.51.100.1:443")
	if logical.Rules()[1].Match(&metadata) {
		t.Fatal("domain match bypassed destination CIDR")
	}
	metadata.ResetRuleCache()
	metadata.Destination = M.ParseSocksaddr("203.0.113.1:443")
	metadata.Domain = "other.example"
	if logical.Rules()[1].Match(&metadata) {
		t.Fatal("destination CIDR bypassed domain match")
	}
}
func TestSingWireGuardAndUnsupportedProtocol(t *testing.T) {
	ctx := box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry())
	o := nodepolicy.OutboundConfig{Tag: "wg", Protocol: "wireguard", Settings: map[string]any{"secretKey": "key", "address": []string{"10.0.0.2/32"}, "peers": []any{map[string]any{"endpoint": "[2001:db8::1]:51820", "publicKey": "pub", "allowedIPs": []string{"0.0.0.0/0"}}}}}
	out, err := singOutbound(ctx, o, nodepolicy.Tags("node", "1", []nodepolicy.OutboundConfig{o}))
	if err != nil {
		t.Fatal(err)
	}
	wg := out.Options.(*option.WireGuardEndpointOptions)
	if !out.Endpoint || wg.PrivateKey != "key" || wg.Peers[0].Address != "2001:db8::1" || wg.Peers[0].Port != 51820 {
		t.Fatal("wireguard translation lost")
	}
	for _, protocol := range []string{"naive", "not-real"} {
		o.Protocol = protocol
		if _, err := singOutbound(ctx, o, map[string]string{"wg": "internal"}); err == nil {
			t.Fatalf("unsupported protocol %s was accepted", protocol)
		}
	}
	o.Protocol = "socks"
	o.Settings = map[string]any{"server": "1.2.3.4", "server_port": 70000}
	if _, err := singOutbound(ctx, o, map[string]string{"wg": "internal"}); err == nil {
		t.Fatal("out of range port accepted")
	}
}
func TestPanelMultiplexOverridesLocal(t *testing.T) {
	info := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: 12345}, VAllss: &panel.VAllssNode{Network: "tcp"}}
	options := &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions()}
	options.SingOptions.Multiplex = &conf.MultiplexConfig{Enabled: true, Padding: true}
	check := func(enabled, padding bool) {
		t.Helper()
		in, err := getInboundOptions("node", info, options)
		if err != nil {
			t.Fatal(err)
		}
		mux := in.Options.(*option.VLESSInboundOptions).Multiplex
		if mux == nil || mux.Enabled != enabled || mux.Padding != padding {
			t.Fatal("wrong effective mux configuration")
		}
	}
	check(true, true)
	info.Common.Multiplex = &panel.MultiplexConfig{Enabled: false}
	check(false, false)
	info.Common.Multiplex = &panel.MultiplexConfig{Enabled: true, Protocol: "smux", MaxConnections: 4, MinStreams: 4, Padding: true, Brutal: &panel.BrutalConfig{Enabled: true, UpMbps: 10, DownMbps: 20}}
	check(true, true)
	in, _ := getInboundOptions("node", info, options)
	brutal := in.Options.(*option.VLESSInboundOptions).Multiplex.Brutal
	if brutal == nil || !brutal.Enabled || brutal.UpMbps != 10 {
		t.Fatal("brutal was not applied")
	}
	info.Common.Multiplex.Protocol = "bad"
	if _, err := getInboundOptions("node", info, options); err == nil {
		t.Fatal("bad mux protocol accepted")
	}
}

//go:build with_quic && with_grpc

package sing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
)

func protocolClient(t *testing.T, outbound map[string]any) *box.Box {
	t.Helper()
	ctx := box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry())
	data, err := json.Marshal(map[string]any{"log": map[string]any{"disabled": true}, "outbounds": []any{outbound}})
	if err != nil {
		t.Fatal(err)
	}
	options, err := sjson.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	c, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		c.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAllSingProtocolsAuthenticatedForwarding(t *testing.T) {
	cases := []struct {
		name, protocol, network, cipher string
		udp                             bool
	}{
		{"vmess-tcp", "vmess", "tcp", "", true}, {"vmess-ws", "vmess", "ws", "", false},
		{"vless-tcp", "vless", "tcp", "", true}, {"vless-ws", "vless", "ws", "", false}, {"vless-grpc", "vless", "grpc", "", false}, {"vless-httpupgrade", "vless", "httpupgrade", "", false},
		{"vless-h2", "vless", "h2", "", false},
		{"trojan-tls", "trojan", "tcp", "", true}, {"trojan-ws", "trojan", "ws", "", false},
		{"trojan-grpc", "trojan", "grpc", "", false}, {"trojan-h2", "trojan", "h2", "", false},
		{"ss-classic", "shadowsocks", "", "aes-128-gcm", true}, {"ss-2022-128", "shadowsocks", "", "2022-blake3-aes-128-gcm", true}, {"ss-2022-256", "shadowsocks", "", "2022-blake3-aes-256-gcm", true},
		{"ss-aes256", "shadowsocks", "", "aes-256-gcm", true}, {"ss-chacha20", "shadowsocks", "", "chacha20-ietf-poly1305", true},
		{"hy1", "hysteria", "", "", true}, {"hy2", "hysteria2", "", "", true}, {"tuic", "tuic", "", "", true}, {"anytls", "anytls", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const uuid = "11111111-1111-4111-8111-111111111111"
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := l.Addr().(*net.TCPAddr).Port
			l.Close()
			info := &panel.NodeInfo{Type: tc.protocol, Common: &panel.CommonNode{ServerPort: port}, TLSSettings: panel.NativeTLSSettings{ServerName: "arinode.local", AllowInsecure: true}}
			out := map[string]any{"type": tc.protocol, "server": "127.0.0.1", "server_port": port}
			if tc.network != "tcp" && tc.network != "" {
				transport := map[string]any{"type": tc.network}
				if tc.network == "h2" {
					transport["type"] = "http"
				}
				if tc.network == "grpc" {
					transport["service_name"] = "arinode-test"
				} else {
					transport["path"] = "/arinode-test"
				}
				out["transport"] = transport
			}
			switch tc.protocol {
			case "vless", "vmess":
				settings := json.RawMessage(`{"path":"/arinode-test","headers":[],"serviceName":"arinode-test"}`)
				if tc.network == "tcp" {
					settings = nil
				}
				info.VAllss = &panel.VAllssNode{Network: tc.network, NetworkSettings: settings}
				out["uuid"] = uuid
				if tc.protocol == "vmess" {
					out["security"] = "auto"
				}
			case "trojan":
				info.Security = panel.Tls
				info.Trojan = &panel.TrojanNode{Network: tc.network, NetworkSettings: json.RawMessage(`{"path":"/arinode-test","headers":[],"serviceName":"arinode-test"}`)}
				out["password"] = uuid
			case "shadowsocks":
				key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
				password := uuid
				if tc.cipher == "2022-blake3-aes-128-gcm" {
					password = key + ":" + base64.StdEncoding.EncodeToString([]byte(uuid[:16]))
				}
				if tc.cipher == "2022-blake3-aes-256-gcm" {
					key = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
					password = key + ":" + base64.StdEncoding.EncodeToString([]byte(uuid[:32]))
				}
				info.Shadowsocks = &panel.ShadowsocksNode{Cipher: tc.cipher, ServerKey: key}
				out["method"], out["password"] = tc.cipher, password
			case "hysteria":
				info.Security = panel.Tls
				info.Hysteria = &panel.HysteriaNode{UpMbps: 100, DownMbps: 100}
				out["auth_str"] = uuid
				out["up_mbps"], out["down_mbps"] = 10, 10
			case "hysteria2":
				info.Security = panel.Tls
				info.Hysteria2 = &panel.Hysteria2Node{}
				out["password"] = uuid
			case "tuic":
				info.Security = panel.Tls
				info.Tuic = &panel.TuicNode{CongestionControl: "bbr"}
				out["uuid"], out["password"], out["congestion_control"] = uuid, uuid, "bbr"
			case "anytls":
				info.Security = panel.Tls
				info.AnyTls = &panel.AnyTlsNode{}
				out["password"] = uuid
			}
			if info.Security == panel.Tls {
				out["tls"] = map[string]any{"enabled": true, "server_name": "arinode.local", "insecure": true}
				if tc.protocol == "tuic" {
					out["tls"].(map[string]any)["alpn"] = []string{"h3"}
				}
			}
			sc := conf.NewSingConfig()
			sc.LogConfig.Disabled = true
			serverCore, err := New(&conf.CoreConfig{SingConfig: sc})
			if err != nil {
				t.Fatal(err)
			}
			server := serverCore.(*Sing)
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			limiter.Init()
			users := []panel.UserInfo{{Id: 1, Uuid: uuid}}
			limiter.AddLimiter(tc.name, &conf.LimitConfig{}, users, map[int]int{})
			defer limiter.DeleteLimiter(tc.name)
			if err := server.AddNode(tc.name, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()}); err != nil {
				t.Fatal(err)
			}
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: tc.name, NodeInfo: info, Users: users}); err != nil {
				t.Fatal(err)
			}
			client := protocolClient(t, out)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			stop := context.AfterFunc(ctx, func() { client.Close() })
			defer stop()
			echo, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer echo.Close()
			go func() {
				conn, err := echo.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(8 * time.Second))
				io.Copy(conn, conn)
			}()
			conn, err := client.Outbound().Default().DialContext(ctx, "tcp", M.ParseSocksaddr(echo.Addr().String()))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(8 * time.Second))
			payload := []byte("authenticated AriNode " + tc.name)
			if _, err := conn.Write(payload); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, response); err != nil {
				t.Fatal(err)
			}
			if string(response) != string(payload) {
				t.Fatal("TCP proxy response mismatch")
			}
			trafficDeadline := time.Now().Add(200 * time.Millisecond)
			for {
				up, down := server.GetUserTraffic(tc.name, uuid, false)
				if up > 0 && down > 0 {
					break
				}
				if time.Now().After(trafficDeadline) {
					t.Fatalf("missing user traffic: up=%d down=%d", up, down)
				}
				time.Sleep(time.Millisecond)
			}
			if tc.protocol != "hysteria" {
				verifyPanelOutbound(t, server, tc.name+"-panel", tc.protocol, uuid, out)
			}
			if !tc.udp {
				return
			}
			udpEcho, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer udpEcho.Close()
			go func() {
				b := make([]byte, 2048)
				n, addr, err := udpEcho.ReadFrom(b)
				if err == nil {
					udpEcho.WriteTo(b[:n], addr)
				}
			}()
			packet, err := client.Outbound().Default().ListenPacket(ctx, M.ParseSocksaddr(udpEcho.LocalAddr().String()))
			if err != nil {
				t.Fatal(err)
			}
			defer packet.Close()
			packet.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := packet.WriteTo(payload, udpEcho.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			b := make([]byte, 2048)
			n, _, err := packet.ReadFrom(b)
			if err != nil {
				t.Fatal(err)
			}
			if string(b[:n]) != string(payload) {
				t.Fatal("UDP proxy response mismatch")
			}
		})
	}
}

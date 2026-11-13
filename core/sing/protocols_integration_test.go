//go:build with_quic && with_grpc

package sing

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/juju/ratelimit"
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
		speedLimit                      int
	}{
		{"vmess-tcp", "vmess", "tcp", "", true, 0}, {"vmess-ws", "vmess", "ws", "", false, 0},
		{"vless-tcp", "vless", "tcp", "", true, 0}, {"vless-ws", "vless", "ws", "", false, 0}, {"vless-grpc", "vless", "grpc", "", false, 0}, {"vless-httpupgrade", "vless", "httpupgrade", "", false, 0},
		{"vless-h2", "vless", "h2", "", false, 0},
		{"trojan-tls", "trojan", "tcp", "", true, 0}, {"trojan-ws", "trojan", "ws", "", false, 0},
		{"trojan-grpc", "trojan", "grpc", "", false, 0}, {"trojan-h2", "trojan", "h2", "", false, 0},
		{"trojan-httpupgrade", "trojan", "httpupgrade", "", true, 0},
		{"ss-classic", "shadowsocks", "", "aes-128-gcm", true, 0}, {"ss-2022-128", "shadowsocks", "", "2022-blake3-aes-128-gcm", true, 0}, {"ss-2022-256", "shadowsocks", "", "2022-blake3-aes-256-gcm", true, 0},
		{"ss-aes256", "shadowsocks", "", "aes-256-gcm", true, 0}, {"ss-chacha20", "shadowsocks", "", "chacha20-ietf-poly1305", true, 0},
		{"hy1", "hysteria", "", "", true, 0}, {"hy2", "hysteria2", "", "", true, 0}, {"tuic", "tuic", "", "", true, 0}, {"anytls", "anytls", "", "", true, 0},
		{"vless-udp-limited", "vless", "tcp", "", true, 300},
		{"ss-udp-limited", "shadowsocks", "", "aes-128-gcm", true, 300},
		{"hy2-udp-limited", "hysteria2", "", "", true, 300},
		{"tuic-udp-limited", "tuic", "", "", true, 300},
		{"anytls-udp-limited", "anytls", "", "", true, 300},
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
			users := []panel.UserInfo{{Id: 1, Uuid: uuid, SpeedLimit: tc.speedLimit}}
			nodeLimiter := limiter.AddLimiter(tc.name, &conf.LimitConfig{}, users, map[int]int{})
			defer limiter.DeleteLimiter(tc.name)
			if err := server.AddNode(tc.name, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()}); err != nil {
				t.Fatal(err)
			}
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: tc.name, NodeInfo: info, Users: users}); err != nil {
				t.Fatal(err)
			}
			client := protocolClient(t, out)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
			mieruWaitTraffic(t, server, tc.name, uuid, int64(len(payload)))
			// The unlimited cases retain the panel-outbound coverage. Keep the
			// limited cases' traffic isolated to this client so their UDP deltas
			// cannot be satisfied by deferred accounting from another client.
			if tc.protocol != "hysteria" && tc.speedLimit == 0 {
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
			var testBucket *ratelimit.Bucket
			userTag := format.UserTag(tc.name, uuid)
			if tc.speedLimit > 0 {
				// Keep the initial TCP exchange on the configured 300 Mbps
				// bucket. Only a new UDP association uses the drained low-rate
				// fixture, making a datagram-copy bypass observable locally.
				originalBucket, rejected := nodeLimiter.CheckLimit(userTag, "127.0.0.1", false, false)
				if rejected || originalBucket == nil || originalBucket.Rate() != 37_500_000 {
					t.Fatalf("speed_limit=300: bucket=%v rejected=%v, want 37500000 B/s", originalBucket, rejected)
				}
				testBucket = ratelimit.NewBucketWithRate(512, 128)
				testBucket.TakeAvailable(testBucket.Capacity())
				nodeLimiter.SpeedLimiter.Store(userTag, testBucket)
				defer nodeLimiter.SpeedLimiter.Store(userTag, originalBucket)
			}
			packet, err := client.Outbound().Default().ListenPacket(ctx, M.ParseSocksaddr(udpEcho.LocalAddr().String()))
			if err != nil {
				t.Fatal(err)
			}
			defer packet.Close()
			packet.SetDeadline(time.Now().Add(5 * time.Second))
			udpPayload := payload
			if testBucket != nil {
				udpPayload = bytes.Repeat([]byte("limited payload/"), 8) // 128 bytes in each direction.
				// Discard refill accrued while ListenPacket negotiated this
				// protocol's transport. The payload must reserve tokens now.
				testBucket.TakeAvailable(testBucket.Capacity())
			}
			upBefore, downBefore := server.GetUserTraffic(tc.name, uuid, false)
			transferStarted := time.Now()
			if _, err := packet.WriteTo(udpPayload, udpEcho.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			b := make([]byte, 2048)
			n, _, err := packet.ReadFrom(b)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(b[:n], udpPayload) {
				t.Fatal("UDP proxy response mismatch")
			}
			// CopyPacket records counters after its destination write returns;
			// require actual new payload in both directions before checking
			// elapsed time so receipt cannot conceal a pending limiter wait.
			mieruWaitTrafficTotals(t, server, tc.name, uuid, upBefore+int64(len(udpPayload)), downBefore+int64(len(udpPayload)))
			if testBucket != nil {
				if elapsed := time.Since(transferStarted); elapsed < 375*time.Millisecond {
					t.Fatalf("%s UDP round trip bypassed the shared 512 B/s bucket: %d bytes each way in %s", tc.protocol, len(udpPayload), elapsed)
				} else {
					t.Logf("%s UDP round trip and bidirectional accounting completed in %s (%d bytes each way)", tc.protocol, elapsed, len(udpPayload))
				}
				if stored, ok := nodeLimiter.SpeedLimiter.Load(userTag); !ok || stored != testBucket {
					t.Fatal("UDP association replaced the user's shared test bucket")
				}
			}
		})
	}
}

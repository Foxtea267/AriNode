//go:build with_quic && with_grpc

package sing

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/core/xray"
	"github.com/Foxtea267/AriNode/limiter"
)

func TestLegacyProtocolActiveRevocationTCPUDP(t *testing.T) {
	for _, name := range []string{"vmess", "trojan", "shadowsocks", "ss2022-aes128", "ss2022-aes256", "xray-ss2022-aes128", "xray-ss2022-aes256"} {
		t.Run(name, func(t *testing.T) {
			protocol, method, serverKey := name, "aes-128-gcm", ""
			switch name {
			case "ss2022-aes128", "xray-ss2022-aes128":
				protocol, method, serverKey = "shadowsocks", "2022-blake3-aes-128-gcm", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 16))
			case "ss2022-aes256", "xray-ss2022-aes256":
				protocol, method, serverKey = "shadowsocks", "2022-blake3-aes-256-gcm", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
			}
			limiter.Init()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			tag := "legacy-revoke-" + protocol
			info := &panel.NodeInfo{Type: protocol, Common: &panel.CommonNode{ServerPort: port}}
			out := func(uuid string) map[string]any {
				result := map[string]any{"type": protocol, "server": "127.0.0.1", "server_port": port}
				switch protocol {
				case "vmess":
					result["uuid"], result["security"] = uuid, "auto"
				case "trojan":
					result["password"] = uuid
					result["tls"] = map[string]any{"enabled": true, "server_name": "arinode.local", "insecure": true}
				case "shadowsocks":
					password, err := format.ShadowsocksUserPassword(method, uuid)
					if err != nil {
						t.Fatal(err)
					}
					if serverKey != "" {
						password = serverKey + ":" + password
					}
					result["method"], result["password"] = method, password
				}
				return result
			}
			switch protocol {
			case "vmess":
				info.VAllss = &panel.VAllssNode{Network: "tcp"}
			case "trojan":
				info.Trojan = &panel.TrojanNode{Network: "tcp"}
				info.Security = panel.Tls
				info.TLSSettings = panel.NativeTLSSettings{ServerName: "arinode.local", AllowInsecure: true}
			case "shadowsocks":
				info.Shadowsocks = &panel.ShadowsocksNode{Cipher: method, ServerKey: serverKey}
			}
			config := conf.NewSingConfig()
			config.LogConfig.Disabled = true
			var server core.Core
			if name == "xray-ss2022-aes128" || name == "xray-ss2022-aes256" {
				xc := conf.NewXrayConfig()
				xc.LogConfig.Level = "none"
				server, err = xray.New(&conf.CoreConfig{XrayConfig: xc})
			} else {
				server, err = New(&conf.CoreConfig{SingConfig: config})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			users := []panel.UserInfo{{Id: 41, Uuid: revokeVLESSUUID}, {Id: 73, Uuid: revokeHealthyUUID}}
			userLimiter := limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
			t.Cleanup(func() { limiter.DeleteLimiter(tag) })
			if err := server.AddNode(tag, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), XrayOptions: conf.NewXrayOptions(), CertConfig: conf.NewCertConfig()}); err != nil {
				t.Fatal(err)
			}
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: info, Users: users}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			target := quicFixedResponseTarget(t)
			expiredClient, healthyClient := protocolClient(t, out(revokeVLESSUUID)).Outbound().Default(), protocolClient(t, out(revokeHealthyUUID)).Outbound().Default()
			expired, err := expiredClient.DialContext(ctx, "tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			defer expired.Close()
			healthy, err := healthyClient.DialContext(ctx, "tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			defer healthy.Close()
			if err := quicFixedRoundTrip(expired); err != nil {
				t.Fatal(err)
			}
			waitLegacyTraffic(t, server, tag, users[0].Id, 128, 512)
			if row := legacyTraffic(t, server, tag, users[1].Id); row.Upload != 0 || row.Download != 0 {
				t.Fatalf("idle healthy user charged another user: %+v", row)
			}
			if err := quicFixedRoundTrip(healthy); err != nil {
				t.Fatal(err)
			}
			udpTarget := startRevocationUDPTarget(t)
			expiredPacket := revocationUDPAssociation(t, expiredClient, ctx, udpTarget.LocalAddr())
			healthyPacket := revocationUDPAssociation(t, healthyClient, ctx, udpTarget.LocalAddr())
			if err := legacyRevocationUDPRoundTrip(expiredPacket, udpTarget.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			if err := legacyRevocationUDPRoundTrip(healthyPacket, udpTarget.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			waitLegacyTraffic(t, server, tag, users[0].Id, 256, 1024)
			waitLegacyTraffic(t, server, tag, users[1].Id, 256, 1024)
			if err := server.DelUsers(users[:1], tag, info); err != nil {
				t.Fatal(err)
			}
			userLimiter.UpdateUser(tag, nil, users[:1])
			if err := quicFixedRoundTrip(expired); err == nil {
				t.Error("revoked legacy TCP connection still forwards")
			}
			if err := legacyRevocationUDPRoundTrip(expiredPacket, udpTarget.LocalAddr()); err == nil {
				t.Error("revoked legacy UDP association still forwards")
			}
			if err := quicFixedRoundTrip(healthy); err != nil {
				t.Fatalf("revocation closed healthy legacy TCP: %v", err)
			}
			if err := legacyRevocationUDPRoundTrip(healthyPacket, udpTarget.LocalAddr()); err != nil {
				t.Fatalf("revocation closed healthy legacy UDP: %v", err)
			}
			rows, err := server.GetUserTrafficSlice(tag, true)
			if err != nil {
				t.Fatal(err)
			}
			var revokedUp, revokedDown int64
			for _, row := range rows {
				if row.UID == users[0].Id {
					revokedUp += row.Upload
					revokedDown += row.Download
				}
			}
			if revokedUp != 256 || revokedDown != 1024 {
				t.Fatalf("revoked legacy user's final accounting lost: %d/%d, rows=%+v", revokedUp, revokedDown, rows)
			}
		})
	}
}

func legacyTraffic(t *testing.T, server core.Core, tag string, uid int) panel.UserTraffic {
	t.Helper()
	rows, err := server.GetUserTrafficSlice(tag, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.UID == uid {
			return row
		}
	}
	return panel.UserTraffic{UID: uid}
}

func waitLegacyTraffic(t *testing.T, server core.Core, tag string, uid int, up, down int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		row := legacyTraffic(t, server, tag, uid)
		if row.Upload == up && row.Download == down {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("uid %d traffic = %d/%d, want %d/%d", uid, row.Upload, row.Download, up, down)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func legacyRevocationUDPRoundTrip(packet net.PacketConn, target net.Addr) error {
	if err := packet.SetDeadline(time.Now().Add(600 * time.Millisecond)); err != nil {
		return err
	}
	if _, err := packet.WriteTo(bytes.Repeat([]byte{'u'}, 128), target); err != nil {
		return err
	}
	response := make([]byte, 4096)
	n, _, err := packet.ReadFrom(response)
	if err == nil && !bytes.Equal(response[:n], bytes.Repeat([]byte{'d'}, 512)) {
		return errors.New("UDP payload changed")
	}
	return err
}

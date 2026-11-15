//go:build with_quic && with_grpc

package sing

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/bufio/deadline"
	M "github.com/sagernet/sing/common/metadata"
)

const (
	revokeVLESSUUID   = "11111111-1111-4111-8111-111111111111"
	revokeHealthyUUID = "22222222-2222-4222-8222-222222222222"
)

func startRevocationSing(t *testing.T, tag, network string, users []panel.UserInfo) (*Sing, *panel.NodeInfo, int, *limiter.Limiter) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	info := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: port}, VAllss: &panel.VAllssNode{Network: network}}
	if network == "ws" || network == "httpupgrade" {
		info.VAllss.NetworkSettings = []byte(`{"path":"/revoke"}`)
	}
	config := conf.NewSingConfig()
	config.LogConfig.Disabled = true
	instance, err := New(&conf.CoreConfig{SingConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	server := instance.(*Sing)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	userLimiter := limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	options := &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()}
	if err := server.AddNode(tag, info, options); err != nil {
		t.Fatal(err)
	}
	if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: info, Users: users}); err != nil {
		t.Fatal(err)
	}
	return server, info, port, userLimiter
}

func revocationVLESSClient(t *testing.T, port int, uuid, network string) interface {
	DialContext(context.Context, string, M.Socksaddr) (net.Conn, error)
	ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error)
} {
	t.Helper()
	outbound := map[string]any{"type": "vless", "server": "127.0.0.1", "server_port": port, "uuid": uuid}
	if network != "tcp" {
		transport := map[string]any{"type": network}
		if network == "ws" || network == "httpupgrade" {
			transport["path"] = "/revoke"
		}
		outbound["transport"] = transport
	}
	boxClient := protocolClient(t, outbound)
	return boxClient.Outbound().Default()
}

func revocationFixedRoundTrip(t *testing.T, ctx context.Context, client interface {
	DialContext(context.Context, string, M.Socksaddr) (net.Conn, error)
}, destination M.Socksaddr) error {
	t.Helper()
	conn, err := client.DialContext(ctx, "tcp", destination)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := time.AfterFunc(time.Second, func() { _ = conn.Close() })
	defer stop.Stop()
	return quicFixedRoundTrip(conn)
}

func waitRevocationTraffic(t *testing.T, server *Sing, tag, uuid string, wantUp, wantDown int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		up, down := server.GetUserTraffic(tag, uuid, false)
		if up == wantUp && down == wantDown {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	up, down := server.GetUserTraffic(tag, uuid, false)
	t.Fatalf("traffic counters = %d/%d, want exact %d/%d", up, down, wantUp, wantDown)
}

func revocationUDPAssociation(t *testing.T, client interface {
	ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error)
}, ctx context.Context, echo net.Addr) net.PacketConn {
	t.Helper()
	packet, err := client.ListenPacket(ctx, M.ParseSocksaddr(echo.String()))
	if err != nil {
		t.Fatal(err)
	}
	// WebSocket deliberately has no native deadlines; use sing's supported
	// client-side deadline reader rather than treating ErrInvalid as failure.
	packet = deadline.NewPacketConn(bufio.NewPacketConn(packet))
	t.Cleanup(func() { _ = packet.Close() })
	return packet
}

func revocationUDPRoundTrip(packet net.PacketConn, target net.Addr) error {
	if err := packet.SetReadDeadline(time.Now().Add(600 * time.Millisecond)); err != nil {
		return err
	}
	upload := bytes.Repeat([]byte{'u'}, 128)
	if _, err := packet.WriteTo(upload, target); err != nil {
		return err
	}
	response := make([]byte, 512)
	n, _, err := packet.ReadFrom(response)
	if err != nil {
		return err
	}
	if !bytes.Equal(response[:n], bytes.Repeat([]byte{'d'}, 512)) {
		return net.ErrClosed
	}
	return nil
}

func startRevocationUDPTarget(t *testing.T) net.PacketConn {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		request := make([]byte, 2048)
		for {
			_, source, err := conn.ReadFrom(request)
			if err != nil {
				return
			}
			_, _ = conn.WriteTo(bytes.Repeat([]byte{'d'}, 512), source)
		}
	}()
	return conn
}

func TestVLESSActiveRevocationTCPUDPAccountingAndNodeIsolation(t *testing.T) {
	for _, transport := range []string{"tcp", "ws", "httpupgrade"} {
		t.Run(transport, func(t *testing.T) {
			limiter.Init()
			tag := "vless-revoke-" + transport
			users := []panel.UserInfo{{Id: 41, Uuid: revokeVLESSUUID}, {Id: 73, Uuid: revokeHealthyUUID}}
			server, info, port, userLimiter := startRevocationSing(t, tag, transport, users)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			target := quicFixedResponseTarget(t)
			revokedClient := revocationVLESSClient(t, port, revokeVLESSUUID, transport)
			healthyClient := revocationVLESSClient(t, port, revokeHealthyUUID, transport)
			revokedStream, err := revokedClient.DialContext(ctx, "tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			defer revokedStream.Close()
			healthyStream, err := healthyClient.DialContext(ctx, "tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			defer healthyStream.Close()
			if err := quicFixedRoundTrip(revokedStream); err != nil {
				t.Fatal(err)
			}
			waitRevocationTraffic(t, server, tag, revokeVLESSUUID, 128, 512)
			udpTarget := startRevocationUDPTarget(t)
			revokedPacket := revocationUDPAssociation(t, revokedClient, ctx, udpTarget.LocalAddr())
			healthyPacket := revocationUDPAssociation(t, healthyClient, ctx, udpTarget.LocalAddr())
			if err := revocationUDPRoundTrip(revokedPacket, udpTarget.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			waitRevocationTraffic(t, server, tag, revokeVLESSUUID, 256, 1024)
			if up, down := server.GetUserTraffic(tag, revokeHealthyUUID, false); up != 0 || down != 0 {
				t.Fatalf("idle healthy user charged other user's transfer: %d/%d", up, down)
			}
			if err := quicFixedRoundTrip(healthyStream); err != nil {
				t.Fatal(err)
			}
			// Retain a healthy client's authenticated mux parent but delay its
			// first UDP child until after deletion compacts the old user slice.
			lateHealthyPacket := revocationUDPAssociation(t, healthyClient, ctx, udpTarget.LocalAddr())
			if err := revocationUDPRoundTrip(healthyPacket, udpTarget.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			waitRevocationTraffic(t, server, tag, revokeHealthyUUID, 256, 1024)

			// A second node may use the same UUID with a different panel ID.
			otherTag := tag + "-other"
			otherUsers := []panel.UserInfo{{Id: 909, Uuid: revokeVLESSUUID}}
			otherListener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			otherPort := otherListener.Addr().(*net.TCPAddr).Port
			_ = otherListener.Close()
			otherInfo := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: otherPort}, VAllss: &panel.VAllssNode{Network: "tcp"}}
			limiter.AddLimiter(otherTag, &conf.LimitConfig{}, otherUsers, nil)
			t.Cleanup(func() { limiter.DeleteLimiter(otherTag) })
			if err := server.AddNode(otherTag, otherInfo, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()}); err != nil {
				t.Fatal(err)
			}
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: otherTag, NodeInfo: otherInfo, Users: otherUsers}); err != nil {
				t.Fatal(err)
			}
			otherClient := revocationVLESSClient(t, otherPort, revokeVLESSUUID, "tcp")
			otherStream, err := otherClient.DialContext(ctx, "tcp", target)
			if err != nil {
				t.Fatal(err)
			}
			defer otherStream.Close()
			if err := quicFixedRoundTrip(otherStream); err != nil {
				t.Fatal(err)
			}
			waitRevocationTraffic(t, server, otherTag, revokeVLESSUUID, 128, 512)

			if err := server.DelUsers(users[:1], tag, info); err != nil {
				t.Fatal(err)
			}
			userLimiter.UpdateUser(tag, nil, users[:1])
			deletedTraffic, err := server.GetUserTrafficSlice(tag, true)
			var deletedUpload, deletedDownload int64
			for _, traffic := range deletedTraffic {
				if traffic.UID == users[0].Id {
					deletedUpload += traffic.Upload
					deletedDownload += traffic.Download
				}
			}
			if err != nil || deletedUpload != 256 || deletedDownload != 1024 {
				t.Errorf("active user's pre-revocation bytes were lost or misattributed: %+v err=%v", deletedTraffic, err)
			}
			secondDrain, err := server.GetUserTrafficSlice(tag, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, traffic := range secondDrain {
				if traffic.UID == users[0].Id {
					t.Errorf("revoked user's final billing replayed on second drain: %+v", traffic)
				}
			}
			if err := quicFixedRoundTrip(otherStream); err != nil {
				t.Errorf("deleting same UUID on one node closed another node: %v", err)
			}
			waitRevocationTraffic(t, server, otherTag, revokeVLESSUUID, 256, 1024)
			if err := server.DelUsers(otherUsers, otherTag, otherInfo); err != nil {
				t.Fatal(err)
			}
			if err := quicFixedRoundTrip(revokedStream); err == nil {
				t.Error("revoked VLESS TCP stream remained active")
			}
			if err := revocationUDPRoundTrip(revokedPacket, udpTarget.LocalAddr()); err == nil {
				t.Error("revoked VLESS UDP association remained active")
			}
			if err := revocationFixedRoundTrip(t, ctx, revocationVLESSClient(t, port, revokeVLESSUUID, transport), target); err == nil {
				t.Error("revoked user authenticated on a fresh VLESS connection")
			}
			if err := quicFixedRoundTrip(healthyStream); err != nil {
				t.Fatalf("revocation closed healthy user's TCP stream: %v", err)
			}
			if err := revocationUDPRoundTrip(healthyPacket, udpTarget.LocalAddr()); err != nil {
				t.Fatalf("revocation closed healthy user's UDP association: %v", err)
			}
			if err := revocationUDPRoundTrip(lateHealthyPacket, udpTarget.LocalAddr()); err != nil {
				t.Fatalf("whitelist compaction invalidated healthy user's delayed UDP child: %v", err)
			}
			if up, down := server.GetUserTraffic(tag, revokeVLESSUUID, false); up != 0 || down != 0 {
				t.Fatalf("revoked user's counter store was not removed: %d/%d", up, down)
			}
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: info, Users: users[:1]}); err != nil {
				t.Fatal(err)
			}
			userLimiter.UpdateUser(tag, users[:1], nil)
			if err := quicFixedRoundTrip(revokedStream); err == nil {
				t.Error("re-adding a UUID revived its revoked TCP stream")
			}
			if err := revocationUDPRoundTrip(revokedPacket, udpTarget.LocalAddr()); err == nil {
				t.Error("re-adding a UUID revived its revoked UDP association")
			}
			freshClient := revocationVLESSClient(t, port, revokeVLESSUUID, transport)
			if err := revocationFixedRoundTrip(t, ctx, freshClient, target); err != nil {
				t.Errorf("fresh connection after re-adding UUID failed: %v", err)
			}
			if err := revocationUDPRoundTrip(healthyPacket, udpTarget.LocalAddr()); err != nil {
				t.Fatalf("updating another user's authorization closed healthy UDP: %v", err)
			}
			waitRevocationTraffic(t, server, tag, revokeHealthyUUID, 512, 2048)
		})
	}
}

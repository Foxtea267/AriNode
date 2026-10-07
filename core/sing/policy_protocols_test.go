//go:build with_quic && with_grpc

package sing

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	M "github.com/sagernet/sing/common/metadata"
)

// Adds HK to the same live core and routes through an authenticated SG inbound.
// The HK client uses smux, proving panel inbound multiplex is effective too.
func verifyPanelOutbound(t *testing.T, server *Sing, tag, protocol, uuid string, settings map[string]any) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	flat := map[string]any{}
	for key, value := range settings {
		if key != "type" {
			flat[key] = value
		}
	}
	hk := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: port, CustomOutbounds: []panel.OutboundConfig{{Tag: "sg01", Protocol: protocol, Settings: flat}}, Routes: []panel.Route{{Id: 31, Match: []string{"127.0.0.1"}, Action: "proxy", ActionValue: "sg01"}}, Multiplex: &panel.MultiplexConfig{Enabled: true, Protocol: "smux", MaxConnections: 4, MinStreams: 4}}, VAllss: &panel.VAllssNode{Network: "tcp"}}
	users := []panel.UserInfo{{Id: 1, Uuid: uuid}}
	limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
	defer limiter.DeleteLimiter(tag)
	options := &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions()}
	if err := server.AddNode(tag, hk, options); err != nil {
		t.Fatal(err)
	}
	if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: hk, Users: users}); err != nil {
		t.Fatal(err)
	}
	client := protocolClient(t, map[string]any{"type": "vless", "server": "127.0.0.1", "server_port": port, "uuid": uuid, "multiplex": map[string]any{"enabled": true, "protocol": "smux", "max_connections": 4, "min_streams": 4}})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	echo := revocationEcho(t)
	conn, err := client.Outbound().Default().DialContext(ctx, "tcp", M.ParseSocksaddr(echo.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	payload := []byte("HK-panel-SG-" + protocol)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("panel %s TCP: %v", protocol, err)
	}
	if string(response) != string(payload) {
		t.Fatal("SG TCP response mismatch")
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			n, addr, err := udp.ReadFrom(b)
			if err != nil {
				return
			}
			udp.WriteTo(b[:n], addr)
		}
	}()
	packet, err := client.Outbound().Default().ListenPacket(ctx, M.ParseSocksaddr(udp.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	packet.SetDeadline(time.Now().Add(7 * time.Second))
	if _, err := packet.WriteTo(payload, udp.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	n, _, err := packet.ReadFrom(buffer)
	if err != nil {
		t.Fatalf("panel %s UDP: %v", protocol, err)
	}
	if string(buffer[:n]) != string(payload) {
		t.Fatal("SG UDP response mismatch")
	}
}

func TestMieruPanelOutboundAndMultiplex(t *testing.T) {
	for _, transport := range []string{"tcp", "udp"} {
		t.Run(transport, func(t *testing.T) {
			const uuid = "11111111-1111-4111-8111-111111111111"
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			port := l.Addr().(*net.TCPAddr).Port
			l.Close()
			cfg := conf.NewSingConfig()
			cfg.LogConfig.Disabled = true
			instance, err := New(&conf.CoreConfig{SingConfig: cfg})
			if err != nil {
				t.Fatal(err)
			}
			server := instance.(*Sing)
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			info := &panel.NodeInfo{Type: "mieru", Common: &panel.CommonNode{ServerPort: port}, Mieru: &panel.MieruNode{Transport: transport}}
			limiter.Init()
			users := []panel.UserInfo{{Id: 1, Uuid: uuid}}
			tag := "SG-mieru-" + transport
			limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
			defer limiter.DeleteLimiter(tag)
			if err := server.AddNode(tag, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions()}); err != nil {
				t.Fatal(err)
			}
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: info, Users: users}); err != nil {
				t.Fatal(err)
			}
			verifyPanelOutbound(t, server, "HK-mieru-"+transport, "mieru", uuid, map[string]any{"server": "127.0.0.1", "server_port": port, "username": uuid, "password": uuid, "transport": transport})
		})
	}
}

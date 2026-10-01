package sing

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/trafficpattern"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"google.golang.org/protobuf/proto"
)

func mieruTestClient(t *testing.T, port int, transport, uuid, pattern string) client.Client {
	t.Helper()
	protocol := appctlpb.TransportProtocol_TCP
	if transport == "udp" {
		protocol = appctlpb.TransportProtocol_UDP
	}
	p, err := trafficpattern.Decode(pattern)
	if err != nil {
		t.Fatal(err)
	}
	c := client.NewClient()
	err = c.Store(&client.ClientConfig{Profile: &appctlpb.ClientProfile{
		ProfileName: proto.String("test"), User: &appctlpb.User{Name: proto.String(uuid), Password: proto.String(uuid)},
		HandshakeMode: appctlpb.HandshakeMode_HANDSHAKE_STANDARD.Enum(), TrafficPattern: p,
		Servers: []*appctlpb.ServerEndpoint{{IpAddress: proto.String("127.0.0.1"), PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: &protocol}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop() })
	return c
}

func TestMieruAuthenticatedForwarding(t *testing.T) {
	for _, transport := range []string{"tcp", "udp"} {
		t.Run(transport, func(t *testing.T) {
			echo, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer echo.Close()
			go func() {
				for {
					c, err := echo.Accept()
					if err != nil {
						return
					}
					go func() { defer c.Close(); c.SetDeadline(time.Now().Add(15 * time.Second)); io.Copy(c, c) }()
				}
			}()
			serverCore, err := New(&conf.CoreConfig{SingConfig: &conf.SingConfig{}})
			if err != nil {
				t.Fatal(err)
			}
			server := serverCore.(*Sing)
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			tag := "mieru-test-" + transport
			const uuid = "11111111-1111-4111-8111-111111111111"
			users := []panel.UserInfo{{Id: 1, Uuid: uuid}}
			limiter.Init()
			limiter.AddLimiter(tag, &conf.LimitConfig{}, users, map[int]int{})
			defer limiter.DeleteLimiter(tag)
			info := &panel.NodeInfo{Type: "mieru", Common: &panel.CommonNode{}, Mieru: &panel.MieruNode{Transport: transport, TrafficPattern: "CgYIiwEQwAM="}}
			if err := server.AddNode(tag, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions()}); err != nil {
				t.Fatal(err)
			}
			in, _ := server.box.Inbound().Get(tag)
			h := in.(*mieruInbound)
			var port int
			if transport == "tcp" {
				port = h.listener.Addr().(*net.TCPAddr).Port
			} else {
				port = h.packet.LocalAddr().(*net.UDPAddr).Port
			}
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: info, Users: users}); err != nil {
				t.Fatal(err)
			}
			c := mieruTestClient(t, port, transport, uuid, info.Mieru.TrafficPattern)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			stop := context.AfterFunc(ctx, func() { c.Stop() })
			defer stop()
			conn, err := c.DialContext(ctx, echo.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			payload := []byte("AriNode Mieru authenticated forwarding")
			if _, err := conn.Write(payload); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, response); err != nil {
				t.Fatal(err)
			}
			if string(response) != string(payload) {
				t.Fatal("incorrect TCP response")
			}
			up, down := server.GetUserTraffic(tag, uuid, false)
			if up == 0 || down == 0 {
				t.Fatalf("missing traffic: up=%d down=%d", up, down)
			}

			udpEcho, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer udpEcho.Close()
			go func() {
				b := make([]byte, 2048)
				for {
					n, addr, err := udpEcho.ReadFrom(b)
					if err != nil {
						return
					}
					udpEcho.WriteTo(b[:n], addr)
				}
			}()
			udpStream, err := c.DialContext(ctx, &net.UDPAddr{IP: net.IPv4zero})
			if err != nil {
				t.Fatal(err)
			}
			defer udpStream.Close()
			udpStream.SetDeadline(time.Now().Add(5 * time.Second))
			packet := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(udpStream))
			if _, err := packet.WriteTo(payload, udpEcho.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			b := make([]byte, 2048)
			n, _, err := packet.ReadFrom(b)
			if err != nil {
				t.Fatal(err)
			}
			if string(b[:n]) != string(payload) {
				t.Fatal("incorrect UDP response")
			}
			if err := server.DelUsers(users, tag, info); err != nil {
				t.Fatal(err)
			}
			conn.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := conn.Read(b); err == nil {
				t.Fatal("revoked session remains open")
			}
			// Existing multiplexed transports must not create sessions for revoked users.
			rejected, err := c.DialContext(ctx, echo.Addr())
			if err == nil {
				rejected.Close()
				t.Fatal("revoked user reconnected")
			}
		})
	}
}

func TestMieruRejectsInvalidConfigurationAndPortConflict(t *testing.T) {
	if _, err := newMieruInbound(context.Background(), nil, nil, "test", mieruInboundOptions{Transport: "invalid"}); err == nil {
		t.Fatal("accepted invalid transport")
	}
	if _, err := newMieruInbound(context.Background(), nil, nil, "test", mieruInboundOptions{TrafficPattern: "invalid!"}); err == nil {
		t.Fatal("accepted invalid traffic pattern")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	addr := netip.MustParseAddr("127.0.0.1")
	h, err := newMieruInbound(context.Background(), nil, nil, "test", mieruInboundOptions{ListenOptions: option.ListenOptions{Listen: (*badoption.Addr)(&addr), ListenPort: uint16(l.Addr().(*net.TCPAddr).Port)}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.Start(adapter.StartStateStart); err == nil {
		t.Fatal("port conflict reported healthy")
	}
}

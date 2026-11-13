package sing

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/format"
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
		for _, speedLimit := range []int{0, 300} {
			name := "unlimited"
			if speedLimit > 0 {
				name = "limited"
			}
			t.Run(transport+"_"+name, func(t *testing.T) {
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
				const healthyUUID = "22222222-2222-4222-8222-222222222222"
				users := []panel.UserInfo{{Id: 1, Uuid: uuid, SpeedLimit: speedLimit}, {Id: 2, Uuid: healthyUUID}}
				limiter.Init()
				nodeLimiter := limiter.AddLimiter(tag, &conf.LimitConfig{}, users, map[int]int{})
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
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
				mieruWaitTraffic(t, server, tag, uuid, int64(len(payload)))
				_, hasBucket := nodeLimiter.SpeedLimiter.Load(format.UserTag(tag, uuid))
				if hasBucket != (speedLimit > 0) {
					t.Fatalf("speed_limit=%d: active bucket=%v", speedLimit, hasBucket)
				}
				mieruWaitDevices(t, nodeLimiter, 1)
				// More than one read/write must accumulate traffic on the same session.
				for round := 2; round <= 3; round++ {
					if _, err := conn.Write(payload); err != nil {
						t.Fatal(err)
					}
					if _, err := io.ReadFull(conn, response); err != nil {
						t.Fatal(err)
					}
					if string(response) != string(payload) {
						t.Fatal("incorrect repeated TCP response")
					}
					mieruWaitTraffic(t, server, tag, uuid, int64(round*len(payload)))
				}

				// This is Mieru's internal target-connect response. A refused target
				// must produce a failure reply, never a success followed by timeout.
				closedTarget, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				failedAddress := closedTarget.Addr()
				closedTarget.Close()
				started := time.Now()
				failedConn, dialErr := c.DialContext(ctx, failedAddress)
				if failedConn != nil {
					failedConn.Close()
				}
				if dialErr == nil || !strings.Contains(dialErr.Error(), "server returned socks5 error code 1") {
					t.Fatalf("target failure did not return Mieru failure reply: %v", dialErr)
				}
				if elapsed := time.Since(started); elapsed >= 5*time.Second {
					t.Fatalf("target failure reply took %s instead of returning promptly", elapsed)
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
				healthy := mieruTestClient(t, port, transport, healthyUUID, info.Mieru.TrafficPattern)
				healthyConn, err := healthy.DialContext(ctx, echo.Addr())
				if err != nil {
					t.Fatal(err)
				}
				defer healthyConn.Close()
				healthyConn.SetDeadline(time.Now().Add(time.Second))
				if _, err := healthyConn.Write(payload); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(healthyConn, response); err != nil {
					t.Fatal(err)
				}
				mieruWaitDevices(t, nodeLimiter, 1, 2)
				if err := server.DelUsers(users[:1], tag, info); err != nil {
					t.Fatal(err)
				}
				conn.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := conn.Read(b); err == nil {
					t.Fatal("revoked session remains open")
				}
				udpStream.SetReadDeadline(time.Now().Add(time.Second))
				if _, _, err := packet.ReadFrom(b); err == nil {
					t.Fatal("revoked UDP association remains open")
				}
				healthyConn.SetDeadline(time.Now().Add(time.Second))
				if _, err := healthyConn.Write(payload); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(healthyConn, response); err != nil {
					t.Fatalf("healthy user's session closed: %v", err)
				}
				// Clear the one-shot admission snapshot, then verify active devices:
				// revoked streams are gone while the healthy user's session survives.
				nodeLimiter.GetOnlineDevice()
				mieruWaitDevices(t, nodeLimiter, 2)
				mieruWaitTraffic(t, server, tag, healthyUUID, int64(2*len(payload)))
				// Existing multiplexed transports must not create sessions for revoked users.
				rejected, err := c.DialContext(ctx, echo.Addr())
				if err == nil {
					rejected.Close()
					t.Fatal("revoked user reconnected")
				}
				if err := server.DelUsers(users[1:], tag, info); err != nil {
					t.Fatal(err)
				}
				healthyConn.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := healthyConn.Read(b); err == nil {
					t.Fatal("last user's session remains open after empty snapshot")
				}
				nodeLimiter.GetOnlineDevice()
				mieruWaitDevices(t, nodeLimiter)
				fresh := mieruTestClient(t, port, transport, uuid, info.Mieru.TrafficPattern)
				freshCtx, stopFresh := context.WithTimeout(context.Background(), 3*time.Second)
				defer stopFresh()
				stopOnTimeout := context.AfterFunc(freshCtx, func() { fresh.Stop() })
				defer stopOnTimeout()
				if conn, err := fresh.DialContext(freshCtx, echo.Addr()); err == nil {
					conn.Close()
					t.Fatal("revoked user authenticated through fresh transport")
				}
			})
		}
	}
}

// sing's Copy increments the upload counter after destination.WriteBuffer
// returns. The target can already echo its bytes and the client receive them
// before that goroutine records the upload. Wait for accounting, but require
// both directions to include every application byte within a bounded interval.
func mieruWaitTraffic(t *testing.T, server *Sing, tag, uuid string, minimum int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	initialUp, initialDown := server.GetUserTraffic(tag, uuid, false)
	up, down := initialUp, initialDown
	for up < minimum || down < minimum {
		if time.Now().After(deadline) {
			t.Fatalf("missing cumulative traffic: up=%d down=%d want each >=%d (initial up=%d down=%d)", up, down, minimum, initialUp, initialDown)
		}
		time.Sleep(5 * time.Millisecond)
		up, down = server.GetUserTraffic(tag, uuid, false)
	}
	if initialUp < minimum || initialDown < minimum {
		t.Logf("accounting became visible: initial up=%d down=%d; final up=%d down=%d", initialUp, initialDown, up, down)
	}
}

func mieruWaitDevices(t *testing.T, l *limiter.Limiter, want ...int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		online, err := l.GetOnlineDevice()
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[int]bool)
		for _, device := range *online {
			if device.IP != "127.0.0.1" {
				t.Fatalf("unexpected device address %q", device.IP)
			}
			seen[device.UID] = true
		}
		match := len(*online) == len(want) && len(seen) == len(want)
		for _, uid := range want {
			match = match && seen[uid]
		}
		if match {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(fmt.Sprintf("online devices=%+v, want UIDs %v", *online, want))
		}
		time.Sleep(5 * time.Millisecond)
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

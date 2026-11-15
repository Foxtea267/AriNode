//go:build with_quic && with_grpc

package sing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/gofrs/uuid/v5"
	boxTLS "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	hy1Client "github.com/sagernet/sing-quic/hysteria"
	hyClient "github.com/sagernet/sing-quic/hysteria2"
	tuicClient "github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

const (
	quicExpiredUUID = "11111111-1111-4111-8111-111111111111"
	quicHealthyUUID = "22222222-2222-4222-8222-222222222222"
	quicRenewedUUID = "33333333-3333-4333-8333-333333333333"
)

type revocationQUICClient interface {
	DialConn(context.Context, M.Socksaddr) (net.Conn, error)
	ListenPacket(context.Context) (net.PacketConn, error)
	CloseWithError(error) error
}

type revocationHY1Client struct{ *hy1Client.Client }

func (c *revocationHY1Client) ListenPacket(ctx context.Context) (net.PacketConn, error) {
	return c.Client.ListenPacket(ctx, M.Socksaddr{})
}

// A reused client normally reconnects automatically. Block future transport
// dials after authentication so a denial proves the old QUIC transport cannot
// authorize a new stream, rather than testing only fresh authentication.
type revocationDialGate struct {
	blocked atomic.Bool
	dials   atomic.Int32
}

func (d *revocationDialGate) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if d.blocked.Load() {
		return nil, errors.New("test forbids replacement QUIC transport")
	}
	d.dials.Add(1)
	return (&net.Dialer{}).DialContext(ctx, network, destination.String())
}

func (d *revocationDialGate) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	if d.blocked.Load() {
		return nil, errors.New("test forbids replacement QUIC transport")
	}
	d.dials.Add(1)
	return (&net.ListenConfig{}).ListenPacket(ctx, "udp", "127.0.0.1:0")
}

type revocationQUICFixture struct {
	server *Sing
	limit  *limiter.Limiter
	info   *panel.NodeInfo
	tag    string
	port   int
	users  []panel.UserInfo
}

func newRevocationQUICFixture(t *testing.T, protocol string) *revocationQUICFixture {
	t.Helper()
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.LocalAddr().(*net.UDPAddr).Port
	listener.Close()
	tag := "quic-revocation-" + protocol
	users := []panel.UserInfo{{Id: 41, Uuid: quicExpiredUUID}, {Id: 73, Uuid: quicHealthyUUID}}
	limiter.Init()
	limit := limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	config := conf.NewSingConfig()
	config.LogConfig.Disabled = true
	backend, err := New(&conf.CoreConfig{SingConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	server := backend.(*Sing)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	info := &panel.NodeInfo{Type: protocol, Common: &panel.CommonNode{ServerPort: port}, Security: panel.Tls,
		TLSSettings: panel.NativeTLSSettings{ServerName: "arinode.local", AllowInsecure: true}}
	if protocol == "hysteria" {
		info.Hysteria = &panel.HysteriaNode{UpMbps: 100, DownMbps: 100}
	} else if protocol == "hysteria2" {
		info.Hysteria2 = &panel.Hysteria2Node{}
	} else {
		info.Tuic = &panel.TuicNode{CongestionControl: "bbr"}
	}
	if err := server.AddNode(tag, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: info, Users: users}); err != nil {
		t.Fatal(err)
	}
	return &revocationQUICFixture{server: server, limit: limit, info: info, tag: tag, port: port, users: users}
}

func (f *revocationQUICFixture) addUsers(t *testing.T, users []panel.UserInfo) {
	t.Helper()
	if _, err := f.server.AddUsers(&core.AddUsersParams{Tag: f.tag, NodeInfo: f.info, Users: users}); err != nil {
		t.Fatal(err)
	}
	// Match Controller.applyUserList: core authorization is committed before
	// updating the limiter's independent admission/user snapshot.
	f.limit.UpdateUser(f.tag, users, nil)
}

func (f *revocationQUICFixture) client(t *testing.T, credential string) (revocationQUICClient, *revocationDialGate) {
	t.Helper()
	gate := &revocationDialGate{}
	tlsConfig, err := boxTLS.NewClient(context.Background(), logger.NOP(), "127.0.0.1", option.OutboundTLSOptions{
		Enabled: true, ServerName: "arinode.local", Insecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := M.ParseSocksaddr(fmt.Sprintf("127.0.0.1:%d", f.port))
	var client revocationQUICClient
	if f.info.Type == "hysteria" {
		var hy1 *hy1Client.Client
		hy1, err = hy1Client.NewClient(hy1Client.ClientOptions{
			Context: context.Background(), Dialer: gate, Logger: logger.NOP(), ServerAddress: destination, Password: credential, TLSConfig: tlsConfig,
			SendBPS: 100 * hy1Client.MbpsToBps, ReceiveBPS: 100 * hy1Client.MbpsToBps, XPlusPassword: f.info.Hysteria.Obfs,
		})
		client = &revocationHY1Client{hy1}
	} else if f.info.Type == "hysteria2" {
		client, err = hyClient.NewClient(hyClient.ClientOptions{
			Context: context.Background(), Dialer: gate, Logger: logger.NOP(), ServerAddress: destination, Password: credential, TLSConfig: tlsConfig,
		})
	} else {
		tlsConfig.SetNextProtos([]string{"h3"})
		client, err = tuicClient.NewClient(tuicClient.ClientOptions{
			Context: context.Background(), Dialer: gate, ServerAddress: destination, UUID: uuid.Must(uuid.FromString(credential)), Password: credential,
			TLSConfig: tlsConfig, CongestionControl: "bbr",
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.CloseWithError(net.ErrClosed) })
	return client, gate
}

func quicFixedResponseTarget(t *testing.T) M.Socksaddr {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(20 * time.Second))
				request := make([]byte, 128)
				for {
					if _, err := io.ReadFull(conn, request); err != nil {
						return
					}
					if _, err := conn.Write(bytes.Repeat([]byte{'d'}, 512)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return M.ParseSocksaddr(listener.Addr().String())
}

func quicFixedRoundTrip(conn net.Conn) error {
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write(bytes.Repeat([]byte{'u'}, 128)); err != nil {
		return err
	}
	response := make([]byte, 512)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if !bytes.Equal(response, bytes.Repeat([]byte{'d'}, 512)) {
		return errors.New("QUIC stream response content changed")
	}
	return nil
}

func quicDialVerified(t *testing.T, client revocationQUICClient, destination M.Socksaddr) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := client.DialConn(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := quicFixedRoundTrip(conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

func quicAssertReusedDenied(t *testing.T, client revocationQUICClient, destination M.Socksaddr) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := client.DialConn(ctx, destination)
	if err != nil {
		return
	}
	defer conn.Close()
	if err := quicFixedRoundTrip(conn); err == nil {
		t.Error("revoked authentication opened a new stream on its old QUIC transport")
	}
}

// Recover only at the API boundary being asserted: a panic here must fail the
// test explicitly, while allowing the other protocol subtests to run too.
func quicDeleteWithoutPanic(f *revocationQUICFixture, users []panel.UserInfo) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("DelUsers panicked with active QUIC sessions: %v", recovered)
		}
	}()
	if err := f.server.DelUsers(users, f.tag, f.info); err != nil {
		return err
	}
	f.limit.UpdateUser(f.tag, nil, users)
	return nil
}

func TestQUICRevocationAndAccounting(t *testing.T) {
	for _, protocol := range []string{"hysteria", "hysteria2", "tuic"} {
		t.Run(protocol, func(t *testing.T) {
			t.Run("whitelist_attribution", func(t *testing.T) {
				f := newRevocationQUICFixture(t, protocol)
				client, _ := f.client(t, quicExpiredUUID)
				conn := quicDialVerified(t, client, quicFixedResponseTarget(t))
				mieruWaitTrafficTotals(t, f.server, f.tag, quicExpiredUUID, 128, 512)
				if up, down := f.server.GetUserTraffic(f.tag, quicExpiredUUID, false); up != 128 || down != 512 {
					t.Fatalf("initial accounting up=%d down=%d want=128/512", up, down)
				}
				// Add/remove another real panel ID, rather than array indexes.
				other := []panel.UserInfo{{Id: 109, Uuid: quicRenewedUUID}}
				f.addUsers(t, other)
				if err := quicDeleteWithoutPanic(f, other); err != nil {
					t.Fatal(err)
				}
				if err := quicFixedRoundTrip(conn); err != nil {
					t.Fatalf("unrelated whitelist update broke existing user: %v", err)
				}
				mieruWaitTrafficTotals(t, f.server, f.tag, quicExpiredUUID, 256, 1024)
				if up, down := f.server.GetUserTraffic(f.tag, quicExpiredUUID, false); up != 256 || down != 1024 {
					t.Fatalf("whitelist update changed cumulative accounting: %d/%d", up, down)
				}
				if up, down := f.server.GetUserTraffic(f.tag, quicHealthyUUID, false); up != 0 || down != 0 {
					t.Fatalf("idle user charged another user's transfer: %d/%d", up, down)
				}
			})
			t.Run("uuid_rotation_and_readd", func(t *testing.T) {
				f := newRevocationQUICFixture(t, protocol)
				client, gate := f.client(t, quicExpiredUUID)
				healthy, _ := f.client(t, quicHealthyUUID)
				destination := quicFixedResponseTarget(t)
				old := quicDialVerified(t, client, destination)
				healthyConn := quicDialVerified(t, healthy, destination)
				gate.blocked.Store(true)
				if gate.dials.Load() != 1 {
					t.Fatalf("reuse fixture opened %d transports, want one", gate.dials.Load())
				}
				if err := quicDeleteWithoutPanic(f, f.users[:1]); err != nil {
					t.Fatal(err)
				}
				renewed := []panel.UserInfo{{Id: 41, Uuid: quicRenewedUUID}}
				f.addUsers(t, renewed)
				if err := quicFixedRoundTrip(old); err == nil {
					t.Error("UUID reset left the old stream forwarding")
				}
				quicAssertReusedDenied(t, client, destination)
				if err := quicFixedRoundTrip(healthyConn); err != nil {
					t.Fatalf("UUID reset closed the healthy user's stream: %v", err)
				}
				fresh, _ := f.client(t, quicRenewedUUID)
				quicDialVerified(t, fresh, destination)
				if err := quicDeleteWithoutPanic(f, renewed); err != nil {
					t.Fatal(err)
				}
				f.addUsers(t, f.users[:1])
				quicAssertReusedDenied(t, client, destination)
				freshReadded, _ := f.client(t, quicExpiredUUID)
				quicDialVerified(t, freshReadded, destination)
				quicDialVerified(t, healthy, destination)
			})
			t.Run("active_udp_revoke_and_node_isolation", func(t *testing.T) {
				f := newRevocationQUICFixture(t, protocol)
				client, gate := f.client(t, quicExpiredUUID)
				healthy, _ := f.client(t, quicHealthyUUID)
				udpEcho, err := net.ListenPacket("udp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { udpEcho.Close() })
				go func() {
					buffer := make([]byte, 2048)
					for {
						n, addr, err := udpEcho.ReadFrom(buffer)
						if err != nil {
							return
						}
						udpEcho.WriteTo(buffer[:n], addr)
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				packet, err := client.ListenPacket(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { packet.Close() })
				healthyPacket, err := healthy.ListenPacket(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { healthyPacket.Close() })
				roundTrip := func(packet net.PacketConn) error {
					if err := packet.SetReadDeadline(time.Now().Add(400 * time.Millisecond)); err != nil {
						return err
					}
					payload := []byte("quic UDP revoke")
					if _, err := packet.WriteTo(payload, udpEcho.LocalAddr()); err != nil {
						return err
					}
					buffer := make([]byte, 128)
					n, _, err := packet.ReadFrom(buffer)
					if err == nil && !bytes.Equal(buffer[:n], payload) {
						err = errors.New("UDP payload changed")
					}
					return err
				}
				if err := roundTrip(packet); err != nil {
					t.Fatal(err)
				}
				if err := roundTrip(healthyPacket); err != nil {
					t.Fatal(err)
				}
				// Another node has the same UUID but its own numeric user identity.
				otherInfo := &panel.NodeInfo{Type: "mieru", Common: &panel.CommonNode{}, Mieru: &panel.MieruNode{Transport: "tcp"}}
				if err := f.server.AddNode("quic-other-panel", otherInfo, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions()}); err != nil {
					t.Fatal(err)
				}
				otherUsers := []panel.UserInfo{{Id: 909, Uuid: quicHealthyUUID}}
				if _, err := f.server.AddUsers(&core.AddUsersParams{Tag: "quic-other-panel", NodeInfo: otherInfo, Users: otherUsers}); err != nil {
					t.Fatal(err)
				}
				if err := f.server.DelUsers(otherUsers, "quic-other-panel", otherInfo); err != nil {
					t.Fatal(err)
				}
				gate.blocked.Store(true)
				if err := quicDeleteWithoutPanic(f, f.users[:1]); err != nil {
					t.Fatal(err)
				}
				if err := roundTrip(packet); err == nil {
					t.Error("revoked user's existing QUIC UDP association still forwards")
				}
				if err := roundTrip(healthyPacket); err != nil {
					t.Fatalf("healthy user's UDP association closed: %v", err)
				}
				f.addUsers(t, f.users[:1])
				if err := roundTrip(packet); err == nil {
					t.Error("readding UUID revived its old QUIC UDP association")
				}
				quicAssertReusedDenied(t, client, quicFixedResponseTarget(t))
				fresh, _ := f.client(t, quicExpiredUUID)
				quicDialVerified(t, fresh, quicFixedResponseTarget(t))
				if err := quicDeleteWithoutPanic(f, f.users); err != nil {
					t.Fatal(err)
				}
				if err := roundTrip(healthyPacket); err == nil {
					t.Error("empty user snapshot left healthy UDP association forwarding")
				}
			})
			t.Run("node_delete_closes_parent", func(t *testing.T) {
				f := newRevocationQUICFixture(t, protocol)
				client, gate := f.client(t, quicExpiredUUID)
				destination := quicFixedResponseTarget(t)
				stream := quicDialVerified(t, client, destination)
				gate.blocked.Store(true)
				if err := f.server.DelNode(f.tag); err != nil {
					t.Fatal(err)
				}
				if err := quicFixedRoundTrip(stream); err == nil {
					t.Error("removed node left its existing QUIC stream forwarding")
				}
				quicAssertReusedDenied(t, client, destination)
			})
		})
	}
}

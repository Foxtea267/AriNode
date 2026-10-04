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

func revocationEcho(t *testing.T) net.Listener {
	t.Helper()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); conn.SetDeadline(time.Now().Add(30 * time.Second)); io.Copy(conn, conn) }()
		}
	}()
	return echo
}

func revocationRoundTrip(conn net.Conn) error {
	conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := conn.Write([]byte("revoke")); err != nil {
		return err
	}
	var response [6]byte
	_, err := io.ReadFull(conn, response[:])
	return err
}

func TestAnyTLSRevokesActiveAndReusedSessions(t *testing.T) {
	const tag = "anytls-revoke"
	const expired = "11111111-1111-4111-8111-111111111111"
	const healthy = "22222222-2222-4222-8222-222222222222"
	users := []panel.UserInfo{{Id: 1, Uuid: expired}, {Id: 2, Uuid: healthy}}
	limiter.Init()
	limiter.AddLimiter(tag, &conf.LimitConfig{}, users, map[int]int{})
	defer limiter.DeleteLimiter(tag)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	sc := conf.NewSingConfig()
	sc.LogConfig.Disabled = true
	c, err := New(&conf.CoreConfig{SingConfig: sc})
	if err != nil {
		t.Fatal(err)
	}
	server := c.(*Sing)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info := &panel.NodeInfo{Type: "anytls", Common: &panel.CommonNode{ServerPort: port}, Security: panel.Tls, AnyTls: &panel.AnyTlsNode{}, TLSSettings: panel.NativeTLSSettings{ServerName: "arinode.local", AllowInsecure: true}}
	if err := server.AddNode(tag, info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: info, Users: users}); err != nil {
		t.Fatal(err)
	}
	out := func(uuid string) map[string]any {
		return map[string]any{"type": "anytls", "server": "127.0.0.1", "server_port": port, "password": uuid, "tls": map[string]any{"enabled": true, "server_name": "arinode.local", "insecure": true}}
	}
	expiredClient, healthyClient := protocolClient(t, out(expired)), protocolClient(t, out(healthy))
	echo := revocationEcho(t)
	dst := M.ParseSocksaddr(echo.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dial := func(client interface {
		DialContext(context.Context, string, M.Socksaddr) (net.Conn, error)
	}) net.Conn {
		conn, err := client.DialContext(ctx, "tcp", dst)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		if err := revocationRoundTrip(conn); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	expiredConn := dial(expiredClient.Outbound().Default())
	healthyConn := dial(healthyClient.Outbound().Default())
	// Closing a child stream must not untrack the still-pooled TLS connection.
	dial(expiredClient.Outbound().Default()).Close()
	udpEcho, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close()
	go func() {
		b := make([]byte, 128)
		for {
			n, addr, err := udpEcho.ReadFrom(b)
			if err != nil {
				return
			}
			udpEcho.WriteTo(b[:n], addr)
		}
	}()
	packet, err := expiredClient.Outbound().Default().ListenPacket(ctx, M.ParseSocksaddr(udpEcho.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	udpRoundTrip := func() error {
		packet.SetDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := packet.WriteTo([]byte("udp"), udpEcho.LocalAddr()); err != nil {
			return err
		}
		var response [128]byte
		_, _, err := packet.ReadFrom(response[:])
		return err
	}
	if err := udpRoundTrip(); err != nil {
		t.Fatal(err)
	}
	if err := server.DelUsers(users[:1], tag, info); err != nil {
		t.Fatal(err)
	}
	if err := revocationRoundTrip(expiredConn); err == nil {
		t.Fatal("revoked user's established stream still forwards")
	}
	if err := udpRoundTrip(); err == nil {
		t.Fatal("revoked UDP-over-TCP association still forwards")
	}
	// The same pooled client can open streams without another password handshake.
	if conn, err := expiredClient.Outbound().Default().DialContext(ctx, "tcp", dst); err == nil {
		defer conn.Close()
		if err := revocationRoundTrip(conn); err == nil {
			t.Fatal("revoked user opened a stream on a reused session")
		}
	}
	if err := revocationRoundTrip(healthyConn); err != nil {
		t.Fatalf("healthy user's existing stream closed: %v", err)
	}
	dial(healthyClient.Outbound().Default())
	// A separate node/panel may reuse a UUID with another numeric user ID.
	// Deleting it there must not erase this node's accounting identity.
	otherInfo := &panel.NodeInfo{Type: "mieru", Common: &panel.CommonNode{}, Mieru: &panel.MieruNode{Transport: "tcp"}}
	if err := server.AddNode("other-panel", otherInfo, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions()}); err != nil {
		t.Fatal(err)
	}
	otherUsers := []panel.UserInfo{{Id: 99, Uuid: healthy}}
	if _, err := server.AddUsers(&core.AddUsersParams{Tag: "other-panel", NodeInfo: otherInfo, Users: otherUsers}); err != nil {
		t.Fatal(err)
	}
	if err := server.DelUsers(otherUsers, "other-panel", otherInfo); err != nil {
		t.Fatal(err)
	}
	if err := revocationRoundTrip(healthyConn); err != nil {
		t.Fatal(err)
	}
	traffic, err := server.GetUserTrafficSlice(tag, false)
	if err != nil || len(traffic) != 1 || traffic[0].UID != 2 {
		t.Fatalf("another binding erased healthy user's accounting: %+v %v", traffic, err)
	}
	fresh := protocolClient(t, out(expired))
	if conn, err := fresh.Outbound().Default().DialContext(ctx, "tcp", dst); err == nil {
		defer conn.Close()
		if err := revocationRoundTrip(conn); err == nil {
			t.Fatal("revoked user authenticated on a fresh connection")
		}
	}
	if err := server.DelUsers(users[1:], tag, info); err != nil {
		t.Fatal(err)
	}
	if err := revocationRoundTrip(healthyConn); err == nil {
		t.Fatal("last user's stream still forwards after empty snapshot")
	}
	// Renewal permits fresh authentication, while expired transports stay closed.
	if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: info, Users: users[:1]}); err != nil {
		t.Fatal(err)
	}
	dial(protocolClient(t, out(expired)).Outbound().Default())
}

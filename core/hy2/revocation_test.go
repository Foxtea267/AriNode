package hy2

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/conf"
	vCore "github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/apernet/hysteria/core/v2/client"
	"github.com/juju/ratelimit"
	"go.uber.org/zap"
)

func TestStandaloneHY2NodeCredentialsAndRevokedTransport(t *testing.T) {
	h := &Hysteria2{Hy2nodes: make(map[string]Hysteria2node), Logger: zap.NewNop()}
	defer h.Close()
	limiter.Init()
	opts := &conf.Options{ListenIP: "127.0.0.1", CertConfig: &conf.CertConfig{CertMode: "auto"}}
	first := []panel.UserInfo{{Id: 41, Uuid: "standalone-test-first", SpeedLimit: 300}}
	second := []panel.UserInfo{{Id: 82, Uuid: "standalone-test-second"}}
	add := func(tag string, info *panel.NodeInfo, users []panel.UserInfo) {
		t.Helper()
		if _, err := h.AddUsers(&vCore.AddUsersParams{Tag: tag, Users: users, NodeInfo: info}); err != nil {
			t.Fatal(err)
		}
	}
	start := func(tag string, users []panel.UserInfo) (*panel.NodeInfo, *net.UDPAddr, *limiter.Limiter) {
		t.Helper()
		p, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := p.LocalAddr().(*net.UDPAddr)
		_ = p.Close()
		info := &panel.NodeInfo{Type: "hysteria2", Common: &panel.CommonNode{ServerPort: addr.Port},
			Hysteria2: &panel.Hysteria2Node{}, TLSSettings: panel.NativeTLSSettings{ServerName: "arinode.local", AllowInsecure: true}}
		l := limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
		t.Cleanup(func() { limiter.DeleteLimiter(tag) })
		if err := h.AddNode(tag, info, opts); err != nil {
			t.Fatal(err)
		}
		add(tag, info, users)
		return info, addr, l
	}
	info, addrA, l := start("standalone-a", first)
	_, addrB, _ := start("standalone-b", second)
	dial := func(addr net.Addr, credential string) (client.Client, error) {
		c, _, err := client.NewClient(&client.Config{ServerAddr: addr, Auth: credential,
			TLSConfig: client.TLSConfig{ServerName: "arinode.local", InsecureSkipVerify: true}})
		if err == nil {
			t.Cleanup(func() { _ = c.Close() })
		}
		return c, err
	}
	if _, err := dial(addrB, first[0].Uuid); err == nil {
		t.Fatal("node B accepted a credential registered only on node A")
	}
	old, err := dial(addrA, first[0].Uuid)
	if err != nil {
		t.Fatal(err)
	}
	key := format.UserTag("standalone-a", first[0].Uuid)
	actual, ok := l.SpeedLimiter.Load(key)
	if !ok || actual.(*ratelimit.Bucket).Rate() != 37500000 {
		t.Fatal("positive speed_limit did not produce the configured Mbps bucket")
	}
	// Same real data path with smaller deterministic chunks. Test both
	// directions over the real QUIC transport, not merely bucket allocation.
	drain := func() {
		bucket := ratelimit.NewBucketWithQuantum(250*time.Millisecond, 4, 4)
		bucket.Take(4)
		l.SpeedLimiter.Store(key, bucket)
	}
	healthy, err := dial(addrB, second[0].Uuid)
	if err != nil {
		t.Fatal(err)
	}
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
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	roundTrip := func(c net.Conn) error {
		_ = c.SetDeadline(time.Now().Add(time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			return err
		}
		_, err := io.ReadFull(c, make([]byte, 4))
		return err
	}
	oldStream, err := old.TCP(echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer oldStream.Close()
	drain()
	started := time.Now()
	if err := roundTrip(oldStream); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 375*time.Millisecond {
		t.Fatalf("TCP forwarding bypassed standalone limit: %s", elapsed)
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		buf := make([]byte, 256)
		for {
			n, source, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(buf[:n], source)
		}
	}()
	association, err := old.UDP()
	if err != nil {
		t.Fatal(err)
	}
	defer association.Close()
	drain()
	started = time.Now()
	if err := association.Send([]byte("ping"), udp.LocalAddr().String()); err != nil {
		t.Fatal(err)
	}
	if data, _, err := association.Receive(); err != nil || string(data) != "ping" {
		t.Fatalf("initial UDP: %q %v", data, err)
	}
	if elapsed := time.Since(started); elapsed < 375*time.Millisecond {
		t.Fatalf("UDP forwarding bypassed standalone limit: %s", elapsed)
	}
	if err := h.DelUsers(first, "standalone-a", info); err != nil {
		t.Fatal(err)
	}
	renewed := []panel.UserInfo{{Id: 91, Uuid: first[0].Uuid}}
	l.UpdateUser("standalone-a", renewed, first)
	add("standalone-a", info, renewed)
	if roundTrip(oldStream) == nil {
		t.Fatal("revoked TCP stream still forwards")
	}
	if err := association.Send([]byte("ping"), udp.LocalAddr().String()); err == nil {
		delivery := make(chan bool, 1)
		go func() { _, _, err := association.Receive(); delivery <- err == nil }()
		select {
		case forwarded := <-delivery:
			if forwarded {
				t.Fatal("revoked UDP association still forwards")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("revoked UDP transport was not disconnected on next datagram")
		}
	}
	for _, c := range []client.Client{healthy, func() client.Client {
		c, err := dial(addrA, renewed[0].Uuid)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}()} {
		stream, err := c.TCP(echo.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		if err := roundTrip(stream); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := h.GetUserTrafficSlice("standalone-a", true)
	got := make(map[int][2]int64)
	for _, row := range rows {
		got[row.UID] = [2]int64{row.Upload, row.Download}
	}
	if err != nil || len(rows) != 2 || got[41] != [2]int64{8, 8} || got[91] != [2]int64{4, 4} {
		t.Fatalf("standalone accounting lost or reassigned: %+v %v", rows, err)
	}
}

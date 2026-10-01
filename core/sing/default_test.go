package sing

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	vCore "github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/sagernet/sing-vmess/vless"
	M "github.com/sagernet/sing/common/metadata"
)

func TestAutoTLSRequiresPanelConsent(t *testing.T) {
	info := &panel.NodeInfo{Type: "trojan", Security: panel.Tls, Common: &panel.CommonNode{ServerPort: 12345}, Trojan: &panel.TrojanNode{Network: "tcp"}}
	_, err := getInboundOptions("tls-consent", info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()})
	if err == nil {
		t.Fatal("generated an automatic certificate for a verified client")
	}
}

// Verify an omitted core produces an authenticated proxy that forwards bytes,
// rather than merely accepting a configuration or listening on a port.
func TestDefaultCoreForwardsVLESSConnection(t *testing.T) {
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
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.Copy(conn, conn)
	}()
	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenPort := port.Addr().(*net.TCPAddr).Port
	address := port.Addr().String()
	port.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"Nodes":[{"ListenIP":"127.0.0.1","NodeType":"vless","NodeID":1}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	c := conf.New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	server, err := vCore.NewCore(c.CoresConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info := &panel.NodeInfo{Id: 1, Type: "vless", Common: &panel.CommonNode{ServerPort: listenPort}, VAllss: &panel.VAllssNode{Network: "tcp"}}
	const uuid = "11111111-1111-4111-8111-111111111111"
	users := []panel.UserInfo{{Id: 1, Uuid: uuid}}
	limiter.Init()
	l := limiter.AddLimiter("default-test", &conf.LimitConfig{}, users, map[int]int{})
	if err := l.UpdateRule(&panel.Rules{Match: []string{"*.blocked.example"}}); err != nil {
		t.Fatal(err)
	}
	defer limiter.DeleteLimiter("default-test")
	if err := server.AddNode("default-test", info, &c.NodeConfig[0].Options); err != nil {
		t.Fatal(err)
	}
	if _, err := server.AddUsers(&vCore.AddUsersParams{Tag: "default-test", Users: users, NodeInfo: info}); err != nil {
		t.Fatal(err)
	}
	raw, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	client, err := vless.NewClient(uuid, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	destination := M.ParseSocksaddr(echo.Addr().String())
	conn, err := client.DialConn(raw, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const payload = "arinode default sing-box proxy"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != payload {
		t.Fatalf("unexpected response: %q", response)
	}
}

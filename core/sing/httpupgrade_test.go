package sing

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
)

func TestHTTPUpgradePreservesPipelinedVLESSHeaderAndRevocation(t *testing.T) {
	const uuid = "11111111-1111-4111-8111-111111111111"
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
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
	info := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: port}, VAllss: &panel.VAllssNode{Network: "httpupgrade", NetworkSettings: json.RawMessage(`{"path":"/upgrade"}`)}}
	users := []panel.UserInfo{{Id: 1, Uuid: uuid}}
	limiter.Init()
	limiter.AddLimiter("early", &conf.LimitConfig{}, users, nil)
	defer limiter.DeleteLimiter("early")
	if err := server.AddNode("early", info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions()}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.AddUsers(&core.AddUsersParams{Tag: "early", NodeInfo: info, Users: users}); err != nil {
		t.Fatal(err)
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	userBytes, _ := hex.DecodeString(strings.ReplaceAll(uuid, "-", ""))
	// Send HTTP headers and the entire VLESS request in one TCP write. This
	// deliberately puts protocol bytes in HTTP's read-ahead buffer.
	packet := []byte("GET /upgrade HTTP/1.1\r\nHost: arinode.local\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	packet = append(packet, 0)
	packet = append(packet, userBytes...)
	packet = append(packet, 0, 1)
	var targetPort [2]byte
	binary.BigEndian.PutUint16(targetPort[:], uint16(echo.Addr().(*net.TCPAddr).Port))
	packet = append(packet, targetPort[:]...)
	packet = append(packet, 1, 127, 0, 0, 1)
	packet = append(packet, []byte("ping")...)
	request := func() error {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write(packet); err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			return err
		}
		if response.StatusCode != 101 {
			return fmt.Errorf("HTTP upgrade status %d", response.StatusCode)
		}
		var body [6]byte
		if _, err := io.ReadFull(reader, body[:]); err != nil {
			return err
		}
		if body[0] != 0 || body[1] != 0 || string(body[2:]) != "ping" {
			return fmt.Errorf("corrupted pipelined protocol bytes")
		}
		return nil
	}
	for i := 0; i < 16; i++ {
		if err := request(); err != nil {
			t.Fatalf("early header lost on request %d: %v", i, err)
		}
	}
	if err := server.DelUsers(users, "early", info); err != nil {
		t.Fatal(err)
	}
	if err := request(); err == nil {
		t.Fatal("revoked user authenticated through the HTTP frontend")
	}
	if err := server.DelNode("early"); err != nil {
		t.Fatalf("HTTPUpgrade node removal failed: %v", err)
	}
}

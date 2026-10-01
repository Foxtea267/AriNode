package core_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	_ "github.com/Foxtea267/AriNode/core/sing"
	_ "github.com/Foxtea267/AriNode/core/xray"
	"github.com/Foxtea267/AriNode/limiter"
	XN "github.com/xtls/xray-core/common/net"
	XC "github.com/xtls/xray-core/core"
	XConf "github.com/xtls/xray-core/infra/conf"
)

func TestDefaultCoreForwardsXHTTP(t *testing.T) {
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
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		io.Copy(conn, conn)
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	server, err := core.NewCore(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info := &panel.NodeInfo{Id: 1, Type: "vless", Common: &panel.CommonNode{ServerPort: port}, VAllss: &panel.VAllssNode{Network: "xhttp", NetworkSettings: json.RawMessage(`{"path":"/test","mode":"auto","extra":{"headers":[]}}`)}}
	const uuid = "11111111-1111-4111-8111-111111111111"
	users := []panel.UserInfo{{Id: 1, Uuid: uuid}}
	limiter.Init()
	limiter.AddLimiter("xhttp-integration", &conf.LimitConfig{}, users, map[int]int{})
	defer limiter.DeleteLimiter("xhttp-integration")
	options := &conf.Options{Core: "sing", ListenIP: "127.0.0.1"}
	if err := server.AddNode("xhttp-integration", info, options); err != nil {
		t.Fatal(err)
	}
	if _, err := server.AddUsers(&core.AddUsersParams{Tag: "xhttp-integration", Users: users, NodeInfo: info}); err != nil {
		t.Fatal(err)
	}
	if options.Core != "sing" {
		t.Fatal("changed the requested core")
	}
	configJSON, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "none"}, "outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{map[string]any{"id": uuid, "encryption": "none"}}}}}, "streamSettings": map[string]any{"network": "xhttp", "xhttpSettings": map[string]any{"path": "/test", "mode": "packet-up"}}}}})
	var config XConf.Config
	if err := json.Unmarshal(configJSON, &config); err != nil {
		t.Fatal(err)
	}
	built, err := config.Build()
	if err != nil {
		t.Fatal(err)
	}
	client, err := XC.New(built)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := XC.Dial(ctx, client, XN.TCPDestination(XN.ParseAddress("127.0.0.1"), XN.Port(echo.Addr().(*net.TCPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Xray's virtual connection has no socket deadline; explicitly close on timeout.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	const payload = "arinode xhttp authenticated forwarding"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != payload {
		t.Fatal("unexpected proxy response")
	}
}

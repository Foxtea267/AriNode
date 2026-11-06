package sing

import (
	"context"
	"net"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
)

func TestHookTracksActualConnectionAcrossReports(t *testing.T) {
	limiter.Init()
	l := limiter.AddLimiter("device-hook", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user"}}, nil)
	defer limiter.DeleteLimiter("device-hook")
	client, peer := net.Pipe()
	defer peer.Close()
	hook := &HookServer{}
	conn := hook.RoutedConnection(context.Background(), client, adapter.InboundContext{
		Inbound: "device-hook", User: "user", Source: M.ParseSocksaddr("[::ffff:192.0.2.1]:54321"),
	}, nil, nil)
	defer conn.Close()
	for i := 0; i < 3; i++ {
		online, _ := l.GetOnlineDevice()
		if len(*online) != 1 || (*online)[0].IP != "192.0.2.1" {
			t.Fatalf("long connection lost or counted twice: %v", online)
		}
	}
	conn.Close()
	online, _ := l.GetOnlineDevice()
	if len(*online) != 0 {
		t.Fatal("closed connection remained in device reports")
	}
}

func TestHookReleasesPacketSessionFromDeviceReports(t *testing.T) {
	limiter.Init()
	l := limiter.AddLimiter("packet-device", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user"}}, nil)
	defer limiter.DeleteLimiter("packet-device")
	raw, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hook := &HookServer{}
	conn := hook.RoutedPacketConnection(context.Background(), bufio.NewPacketConn(raw), adapter.InboundContext{
		Inbound: "packet-device", User: "user", Source: M.ParseSocksaddr("192.0.2.1:54321"),
	}, nil, nil)
	defer conn.Close()
	for i := 0; i < 3; i++ {
		online, _ := l.GetOnlineDevice()
		if len(*online) != 1 {
			t.Fatal("live UDP session was not tracked")
		}
	}
	conn.Close()
	conn.Close()
	online, _ := l.GetOnlineDevice()
	if len(*online) != 0 {
		t.Fatal("closed UDP session remained online")
	}
}

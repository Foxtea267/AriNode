package core_test

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
)

func TestNativeDeviceLifecycleWithRealVLESSConnections(t *testing.T) {
	for _, kernel := range []string{"sing", "xray"} {
		t.Run(kernel, func(t *testing.T) {
			config := conf.CoreConfig{Type: kernel}
			if kernel == "sing" {
				config.SingConfig = conf.NewSingConfig()
				config.SingConfig.LogConfig.Disabled = true
			} else {
				config.XrayConfig = conf.NewXrayConfig()
			}
			server, err := core.NewCore([]conf.CoreConfig{config})
			if err != nil {
				t.Fatal(err)
			}
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			options := &conf.Options{Core: kernel, ListenIP: "127.0.0.1"}
			if err := options.UseCore(kernel); err != nil {
				t.Fatal(err)
			}
			info := &panel.NodeInfo{Id: 1, Type: "vless", Common: &panel.CommonNode{ServerPort: policyPort(t)}, VAllss: &panel.VAllssNode{Network: "tcp"}}
			users := []panel.UserInfo{{Id: 1, Uuid: policyUUID, DeviceLimit: 1}}
			limiter.Init()
			l := limiter.AddLimiter("device-native", &conf.LimitConfig{}, users, nil)
			defer limiter.DeleteLimiter("device-native")
			if err := server.AddNode("device-native", info, options); err != nil {
				t.Fatal(err)
			}
			defer server.DelNode("device-native")
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: "device-native", NodeInfo: info, Users: users}); err != nil {
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
					go func() { defer conn.Close(); io.Copy(conn, conn) }()
				}
			}()
			a := policyDial(t, info.Common.ServerPort, echo.Addr().String(), "ping")
			defer a.Close()
			b := policyDial(t, info.Common.ServerPort, echo.Addr().String(), "ping")
			defer b.Close()
			for i := 0; i < 4; i++ {
				online, _ := l.GetOnlineDevice()
				if len(*online) != 1 {
					t.Fatalf("native connections duplicated or disappeared: %v", online)
				}
			}
			a.Close()
			for i := 0; i < 3; i++ {
				online, _ := l.GetOnlineDevice()
				if len(*online) != 1 {
					t.Fatal("one closed native connection removed another live one")
				}
			}
			b.Close()
			// Xray retains the download half for its configured DownlinkOnly
			// timeout (4 seconds by default), even after the client sends EOF.
			deadline := time.Now().Add(6 * time.Second)
			for time.Now().Before(deadline) {
				online, _ := l.GetOnlineDevice()
				if len(*online) == 0 {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("all native client connections closed but the device remained online")
		})
	}
}

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

func TestXrayActiveVLESSRevocationKeepsOriginalUIDTraffic(t *testing.T) {
	cfg := conf.NewXrayConfig()
	server, err := core.NewCore([]conf.CoreConfig{{Type: "xray", XrayConfig: cfg}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	const tag = "xray-revoke-live"
	info := &panel.NodeInfo{Type: "vless", Common: &panel.CommonNode{ServerPort: policyPort(t)}, VAllss: &panel.VAllssNode{Network: "tcp"}}
	opts := &conf.Options{Core: "xray", ListenIP: "127.0.0.1"}
	if err := opts.UseCore("xray"); err != nil {
		t.Fatal(err)
	}
	first := []panel.UserInfo{{Id: 41, Uuid: policyUUID}}
	renewed := []panel.UserInfo{{Id: 82, Uuid: policyUUID}}
	limiter.Init()
	limit := limiter.AddLimiter(tag, &conf.LimitConfig{}, first, nil)
	defer limiter.DeleteLimiter(tag)
	if err := server.AddNode(tag, info, opts); err != nil {
		t.Fatal(err)
	}
	defer server.DelNode(tag)
	add := func(users []panel.UserInfo) {
		t.Helper()
		if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: info, Users: users}); err != nil {
			t.Fatal(err)
		}
	}
	add(first)
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
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	old := policyDial(t, info.Common.ServerPort, echo.Addr().String(), "ping")
	defer old.Close()
	if err := server.DelUsers(first, tag, info); err != nil {
		t.Fatal(err)
	}
	limit.UpdateUser(tag, renewed, first)
	add(renewed)
	fresh := policyDial(t, info.Common.ServerPort, echo.Addr().String(), "ping")
	defer fresh.Close()
	_ = old.SetDeadline(time.Now().Add(time.Second))
	_, _ = old.Write([]byte("ping"))
	if _, err := io.ReadFull(old, make([]byte, 4)); err == nil {
		t.Fatal("old VLESS connection still forwarded after re-add")
	}
	rows, err := server.GetUserTrafficSlice(tag, true)
	got := make(map[int][2]int64)
	for _, row := range rows {
		got[row.UID] = [2]int64{row.Upload, row.Download}
	}
	if err != nil || len(rows) != 2 || got[41] != [2]int64{4, 4} || got[82] != [2]int64{4, 4} {
		t.Fatalf("traffic lost or assigned to the new UID: %+v, %v", rows, err)
	}
	rows, err = server.GetUserTrafficSlice(tag, true)
	if err != nil || len(rows) != 0 {
		t.Fatalf("drained traffic reported twice: %+v, %v", rows, err)
	}
}

package limiter

import (
	"sync"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
)

func TestActiveDevicesSurviveSnapshotsAndDeduplicateConnections(t *testing.T) {
	Init()
	l := AddLimiter("devices", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "a", DeviceLimit: 1}}, nil)
	defer DeleteLimiter("devices")
	first := l.TrackDeviceIP("devices|a", "192.0.2.1")
	second := l.TrackDeviceIP("devices|a", "::ffff:192.0.2.1")
	for i := 0; i < 4; i++ {
		online, _ := l.GetOnlineDevice()
		if len(*online) != 1 || (*online)[0].IP != "192.0.2.1" {
			t.Fatalf("long-lived/multiple connections changed device count: %v", online)
		}
	}
	first()
	first()
	online, _ := l.GetOnlineDevice()
	if len(*online) != 1 {
		t.Fatal("one closed connection removed another live connection")
	}
	if _, reject := l.CheckLimit("devices|a", "192.0.2.2", true, true); !reject {
		t.Fatal("active connection bypassed the one-device limit")
	}
	second()
	online, _ = l.GetOnlineDevice()
	if len(*online) != 0 {
		t.Fatal("disconnected device remained active")
	}
}

func TestActiveDeviceRevocationAndOldConnectionRelease(t *testing.T) {
	Init()
	user := panel.UserInfo{Id: 1, Uuid: "a"}
	l := AddLimiter("revoke", &conf.LimitConfig{}, []panel.UserInfo{user}, nil)
	defer DeleteLimiter("revoke")
	old := l.TrackDeviceIP("revoke|a", "192.0.2.1")
	l.UpdateUser("revoke", nil, []panel.UserInfo{user})
	online, _ := l.GetOnlineDevice()
	if len(*online) != 0 {
		t.Fatal("revoked connection remained reportable")
	}
	l.UpdateUser("revoke", []panel.UserInfo{user}, nil)
	next := l.TrackDeviceIP("revoke|a", "192.0.2.1")
	old()
	online, _ = l.GetOnlineDevice()
	if len(*online) != 1 {
		t.Fatal("old connection removed reauthorized session")
	}
	next()
}

func TestRecentDevicesAreIsolatedByUserBehindSameNAT(t *testing.T) {
	Init()
	l := AddLimiter("nat", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "a", DeviceLimit: 1}, {Id: 2, Uuid: "b", DeviceLimit: 1}}, nil)
	defer DeleteLimiter("nat")
	l.CheckLimit("nat|a", "192.0.2.1", true, true)
	l.CheckLimit("nat|b", "192.0.2.1", true, true)
	online, _ := l.GetOnlineDevice()
	if len(*online) != 2 {
		t.Fatal("different users sharing an IP were merged")
	}
	for _, user := range []string{"a", "b"} {
		if _, reject := l.CheckLimit("nat|"+user, "192.0.2.2", true, true); !reject {
			t.Fatal("another user's IP overwrote the recent device identity")
		}
	}
}

func TestConcurrentDeviceSessionsAndSnapshots(t *testing.T) {
	Init()
	l := AddLimiter("concurrent", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "a"}}, nil)
	defer DeleteLimiter("concurrent")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				done := l.TrackDeviceIP("concurrent|a", "2001:db8::1")
				online, _ := l.GetOnlineDevice()
				if len(*online) != 1 {
					t.Error("duplicate or missing concurrent device")
				}
				done()
				done()
			}
		}()
	}
	wg.Wait()
	online, _ := l.GetOnlineDevice()
	if len(*online) != 0 {
		t.Fatal("session references leaked")
	}
}

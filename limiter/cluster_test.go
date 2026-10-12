package limiter

import (
	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/conf"
	"testing"
)

func TestClusterKnownDeviceCanSwitchReplicaAtLimit(t *testing.T) {
	Init()
	l := AddLimiter("group", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "same", DeviceLimit: 1}}, map[int]int{})
	defer DeleteLimiter("group")
	l.SetAliveList(map[int]int{1: 1}, map[int][]string{1: {"192.0.2.1"}})
	if _, rejected := l.CheckLimit(format.UserTag("group", "same"), "192.0.2.1", true, true); rejected {
		t.Fatal("existing group device rejected on another replica")
	}
	if _, rejected := l.CheckLimit(format.UserTag("group", "same"), "192.0.2.2", true, true); !rejected {
		t.Fatal("new device bypassed aggregate limit")
	}
	// Local admissions expire through the reporting windows as well as the
	// remote snapshot; an empty remote set cannot erase a just-admitted client.
	l.GetOnlineDevice()
	l.GetOnlineDevice()
	l.SetAliveList(map[int]int{}, nil)
	if _, rejected := l.CheckLimit(format.UserTag("group", "same"), "192.0.2.2", true, true); rejected {
		t.Fatal("expired devices retained")
	}
}

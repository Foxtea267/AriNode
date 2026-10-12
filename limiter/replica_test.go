package limiter

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/conf"
)

func TestFourReplicasAcceptKnownClientAtThreeDeviceLimit(t *testing.T) {
	Init()
	for i := 1; i <= 4; i++ {
		tag := fmt.Sprintf("replica-%d", i)
		l := AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user", DeviceLimit: 3}}, map[int]int{1: 3})
		defer DeleteLimiter(tag)
		l.SetAliveList(map[int]int{1: 3}, map[int][]string{1: {"192.0.2.1", "192.0.2.2", "2001:db8:0:0::3"}})
		if _, reject := l.CheckLimit(format.UserTag(tag, "user"), "::ffff:192.0.2.1", true, true); reject {
			t.Fatalf("known client rejected on replica %d", i)
		}
		if _, reject := l.CheckLimit(format.UserTag(tag, "user"), "2001:db8::3", true, true); reject {
			t.Fatal("IPv6 aliases counted as new devices")
		}
		if _, reject := l.CheckLimit(format.UserTag(tag, "user"), "192.0.2.4", true, true); !reject {
			t.Fatal("a genuinely new fourth device bypassed the package limit")
		}
	}
}
func TestDeviceAdmissionIsAtomicBeforePanelSync(t *testing.T) {
	Init()
	l := AddLimiter("local", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user", DeviceLimit: 3}}, nil)
	defer DeleteLimiter("local")
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, reject := l.CheckLimit("local|user", fmt.Sprintf("192.0.2.%d", i), true, true); !reject {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatalf("concurrent admission accepted %d devices", accepted.Load())
	}
}

package limiter

import (
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
)

func TestExpiredDynamicLimitKeepsUserAndRestoresRate(t *testing.T) {
	const tag = "dynamic-expiration"
	Init()
	l := AddLimiter(tag, &conf.LimitConfig{SpeedLimit: 16}, []panel.UserInfo{{Id: 4, Uuid: "user", DeviceLimit: 1}}, nil)
	defer DeleteLimiter(tag)
	normal, rejected := l.CheckLimit(tag+"|user", "192.0.2.1", true, true)
	if rejected || normal == nil || normal.Rate() != 2_000_000 {
		t.Fatalf("unexpected base bucket: rejected=%v bucket=%v", rejected, normal)
	}
	if err := l.UpdateDynamicSpeedLimit(tag, "user", 8, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	limited, rejected := l.CheckLimit(tag+"|user", "192.0.2.1", true, true)
	if rejected || limited == nil || limited == normal || limited.Rate() != 1_000_000 {
		t.Fatalf("dynamic policy reused a stale rate: rejected=%v bucket=%v", rejected, limited)
	}
	if err := l.UpdateDynamicSpeedLimit(tag, "user", 8, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		restored, rejected := l.CheckLimit(tag+"|user", "192.0.2.1", true, true)
		if rejected || restored == nil || restored.Rate() != normal.Rate() {
			t.Fatalf("expired policy removed user or retained old rate: rejected=%v bucket=%v", rejected, restored)
		}
	}
	if _, rejected := l.CheckLimit(tag+"|user", "192.0.2.2", true, true); !rejected {
		t.Fatal("dynamic expiry discarded the user's device limit")
	}
}

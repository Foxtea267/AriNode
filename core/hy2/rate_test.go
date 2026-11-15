package hy2

import (
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/juju/ratelimit"
)

func TestStandaloneRateSharedAndRevocationCancelsWait(t *testing.T) {
	const tag = "standalone-rate"
	limiter.Init()
	users := []panel.UserInfo{{Id: 1, Uuid: "limited", SpeedLimit: 1}, {Id: 2, Uuid: "healthy"}}
	l := limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(tag) })
	auth := &V2bX{tag: tag}
	hook := &HookServer{Tag: tag, auth: auth}
	h := &Hysteria2{Hy2nodes: map[string]Hysteria2node{tag: {Auth: auth, TrafficLogger: hook}}}
	if _, err := h.AddUsers(&core.AddUsersParams{Tag: tag, Users: users}); err != nil {
		t.Fatal(err)
	}
	_, first := auth.Authenticate(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, "limited", 0)
	_, second := auth.Authenticate(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 2)}, "limited", 0)
	_, healthy := auth.Authenticate(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, "healthy", 0)
	if first == "" || first != second || healthy == "" || healthy == first {
		t.Fatal("invalid test authentication")
	}
	bucket := ratelimit.NewBucketWithQuantum(10*time.Second, 8, 8)
	l.SpeedLimiter.Store(format.UserTag(tag, "limited"), bucket)
	if !hook.LogTraffic(first, 4, 4) {
		t.Fatal("initial shared allowance was rejected")
	}
	result := make(chan bool, 1)
	go func() { result <- hook.LogTraffic(second, 1, 0) }()
	deadline := time.Now().Add(2 * time.Second)
	for bucket.Available() >= 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if bucket.Available() != -1 {
		t.Fatal("second peer did not consume the same quota")
	}
	if !hook.LogTraffic(healthy, 2, 3) {
		t.Fatal("unlimited user was affected by another user's wait")
	}
	if err := h.DelUsers(users[:1], tag, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case forwarded := <-result:
		if forwarded {
			t.Fatal("revoked traffic was forwarded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not cancel token wait")
	}
	rows, err := h.GetUserTrafficSlice(tag, true)
	byUID := make(map[int][2]int64)
	for _, row := range rows {
		byUID[row.UID] = [2]int64{row.Upload, row.Download}
	}
	if err != nil || len(rows) != 2 || byUID[1] != [2]int64{4, 4} || byUID[2] != [2]int64{2, 3} {
		t.Fatalf("revocation changed traffic accounting: %+v, %v", rows, err)
	}
}

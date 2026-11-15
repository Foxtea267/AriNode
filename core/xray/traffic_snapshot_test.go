package xray

import (
	"testing"

	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/core/xray/app/dispatcher"
)

func TestXrayTrafficGetterThresholdDrainAndUserIsolation(t *testing.T) {
	const tag, otherTag = "traffic-node", "other-node"
	user, otherUser := format.UserTag(tag, "user"), format.UserTag(otherTag, "user")
	x := &Xray{
		dispatcher:                &dispatcher.DefaultDispatcher{},
		users:                     &UserMap{uidMap: map[string]int{user: 1, otherUser: 2}},
		nodeReportMinTrafficBytes: map[string]int64{tag: 64},
	}
	traffic, otherTraffic := counter.NewTrafficCounter(), counter.NewTrafficCounter()
	x.dispatcher.Counter.Store(tag, traffic)
	x.dispatcher.Counter.Store(otherTag, otherTraffic)
	traffic.Tx(user, 32)
	traffic.Rx(user, 32)
	otherTraffic.Tx(otherUser, 17)
	otherTraffic.Rx(otherUser, 29)
	if rows, err := x.GetUserTrafficSlice(tag, true); err != nil || len(rows) != 0 {
		t.Fatalf("threshold equality should remain unreported: %+v %v", rows, err)
	}
	if up, down := traffic.GetTraffic(user, false); up != 32 || down != 32 {
		t.Fatalf("below-threshold collection discarded bytes: %d/%d", up, down)
	}
	traffic.Tx(user, 96)
	traffic.Rx(user, 480)
	for _, reset := range []bool{false, false, true} {
		rows, err := x.GetUserTrafficSlice(tag, reset)
		if err != nil || len(rows) != 1 || rows[0].UID != 1 || rows[0].Upload != 128 || rows[0].Download != 512 {
			t.Fatalf("reset=%v traffic lost/misattributed: %+v %v", reset, rows, err)
		}
	}
	if rows, err := x.GetUserTrafficSlice(tag, true); err != nil || len(rows) != 0 {
		t.Fatalf("repeat reset duplicated billing: %+v %v", rows, err)
	}
	traffic.Tx(user, 300)
	traffic.Rx(user, 1)
	rows, err := x.GetUserTrafficSlice(tag, true)
	if err != nil || len(rows) != 1 || rows[0].Upload != 300 || rows[0].Download != 1 {
		t.Fatalf("new bytes after reset lost: %+v %v", rows, err)
	}
	rows, err = x.GetUserTrafficSlice(otherTag, true)
	if err != nil || len(rows) != 1 || rows[0].UID != 2 || rows[0].Upload != 17 || rows[0].Download != 29 {
		t.Fatalf("another node's UUID traffic changed: %+v %v", rows, err)
	}
}

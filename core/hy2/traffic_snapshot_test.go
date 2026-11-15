package hy2

import (
	"testing"

	"github.com/Foxtea267/AriNode/common/counter"
)

func TestHY2TrafficGetterThresholdDrainAndUserIsolation(t *testing.T) {
	const tag, user, other = "traffic-node", "user", "other"
	hook := &HookServer{ReportMinTrafficBytes: 64}
	traffic := counter.NewTrafficCounter()
	hook.Counter.Store(tag, traffic)
	h := &Hysteria2{
		Hy2nodes: map[string]Hysteria2node{tag: {TrafficLogger: hook, Auth: &V2bX{active: map[string]authenticatedUser{user: {uid: 1}, other: {uid: 2}}}}},
	}
	traffic.Tx(user, 32)
	traffic.Rx(user, 32)
	traffic.Tx(other, 70)
	traffic.Rx(other, 80)
	rows, err := h.GetUserTrafficSlice(tag, true)
	if err != nil || len(rows) != 1 || rows[0].UID != 2 || rows[0].Upload != 70 || rows[0].Download != 80 {
		t.Fatalf("independent user's billing lost: %+v %v", rows, err)
	}
	if up, down := traffic.GetTraffic(user, false); up != 32 || down != 32 {
		t.Fatalf("threshold equality discarded bytes: %d/%d", up, down)
	}
	traffic.Tx(user, 96)
	traffic.Rx(user, 480)
	for _, reset := range []bool{false, false, true} {
		rows, err := h.GetUserTrafficSlice(tag, reset)
		if err != nil || len(rows) != 1 || rows[0].UID != 1 || rows[0].Upload != 128 || rows[0].Download != 512 {
			t.Fatalf("reset=%v traffic lost/misattributed: %+v %v", reset, rows, err)
		}
	}
	if rows, err := h.GetUserTrafficSlice(tag, true); err != nil || len(rows) != 0 {
		t.Fatalf("repeat reset duplicated billing: %+v %v", rows, err)
	}
	traffic.Tx(user, 300)
	traffic.Rx(user, 1)
	rows, err = h.GetUserTrafficSlice(tag, true)
	if err != nil || len(rows) != 1 || rows[0].Upload != 300 || rows[0].Download != 1 {
		t.Fatalf("new bytes after reset lost: %+v %v", rows, err)
	}
}

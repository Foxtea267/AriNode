package sing

import (
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/counter"
)

func retiredTrafficTestServer(tag string, spool *counter.RetiredUserTraffic, active map[string]int) (*Sing, *counter.TrafficCounter) {
	hook := &HookServer{}
	traffic := counter.NewTrafficCounter()
	hook.counter.Store(tag, traffic)
	return &Sing{
		hookServer: hook,
		users: &UserMap{
			uidMap:         map[string]map[string]int{tag: active},
			retiredTraffic: map[string]*counter.RetiredUserTraffic{tag: spool},
		},
		nodeReportMinTrafficBytes: map[string]int64{tag: 0},
	}, traffic
}

func retiredTrafficTotals(rows []panel.UserTraffic) map[int][2]int64 {
	result := make(map[int][2]int64)
	for _, row := range rows {
		current := result[row.UID]
		result[row.UID] = [2]int64{current[0] + row.Upload, current[1] + row.Download}
	}
	return result
}

func requireRetiredTrafficSnapshot(t *testing.T, server *Sing, tag string, reset bool, expected map[int][2]int64) {
	t.Helper()
	rows, err := server.GetUserTrafficSlice(tag, reset)
	if err != nil {
		t.Fatal(err)
	}
	if actual := retiredTrafficTotals(rows); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("traffic snapshot(reset=%t) = %+v, want %+v (rows=%+v)", reset, actual, expected, rows)
	}
}

func TestRetiredVLESSTrafficKeepsOriginalUIDAfterDrainAndReadd(t *testing.T) {
	const tag, credential = "retired-vless", "same-uuid-after-renewal"
	const originalUID, renewedUID = 41, 909
	spool := &counter.RetiredUserTraffic{}
	server, liveCounters := retiredTrafficTestServer(tag, spool, map[string]int{credential: originalUID})
	old := liveCounters.GetCounter(credential)
	old.AddUp(128)
	old.AddDown(512)
	// DelUsers captures the original numeric panel ID before removing its
	// identity mapping. The real protocol test covers that production call.
	old.RetireToFunc(func(up, down int64) { spool.Add(originalUID, up, down) })
	liveCounters.Delete(credential)
	server.users.uidMap[tag][credential] = renewedUID
	renewed := liveCounters.GetCounter(credential)
	renewed.AddUp(7)
	renewed.AddDown(9)

	expected := map[int][2]int64{originalUID: {128, 512}, renewedUID: {7, 9}}
	requireRetiredTrafficSnapshot(t, server, tag, false, expected)
	requireRetiredTrafficSnapshot(t, server, tag, false, expected)
	requireRetiredTrafficSnapshot(t, server, tag, true, expected)
	requireRetiredTrafficSnapshot(t, server, tag, true, map[int][2]int64{})
	if len(spool.Snapshot(false)) != 0 {
		t.Fatal("drained spool retained historical UID entries")
	}

	// Copy counters may complete after the first report and after the UUID was
	// re-added under another panel UID. Such bytes must recreate only the old
	// UID's pending report, and must never enter the renewed account's bucket.
	for i := 0; i < 3; i++ {
		old.AddUp(5)
		old.AddDown(7)
	}
	renewed.AddUp(13)
	renewed.AddDown(17)
	expected = map[int][2]int64{originalUID: {15, 21}, renewedUID: {13, 17}}
	requireRetiredTrafficSnapshot(t, server, tag, false, expected)
	requireRetiredTrafficSnapshot(t, server, tag, true, expected)
	requireRetiredTrafficSnapshot(t, server, tag, false, map[int][2]int64{})
	if up, down := old.Snapshot(false); up != 0 || down != 0 {
		t.Fatalf("retired source retained bytes after redirect: %d/%d", up, down)
	}
	if len(spool.Snapshot(false)) != 0 {
		t.Fatal("second drain retained historical UIDs")
	}
}

func TestRetiredVLESSTrafficDoesNotRetainDrainedHistoricalUIDs(t *testing.T) {
	const tag, generations = "retired-history", 256
	spool := &counter.RetiredUserTraffic{}
	server, _ := retiredTrafficTestServer(tag, spool, nil)
	sources := make([]*counter.TrafficStorage, generations)
	expected := make(map[int][2]int64, generations)
	spool.Add(1234, 0, 0)
	if len(spool.Snapshot(false)) != 0 {
		t.Fatal("zero-byte callback allocated a historical UID entry")
	}
	for i := range sources {
		uid := 1000 + i
		sources[i] = &counter.TrafficStorage{}
		sources[i].AddUp(int64(i + 1))
		sources[i].AddDown(int64(2 * (i + 1)))
		sources[i].RetireToFunc(func(up, down int64) { spool.Add(uid, up, down) })
		expected[uid] = [2]int64{int64(i + 1), int64(2 * (i + 1))}
	}
	requireRetiredTrafficSnapshot(t, server, tag, true, expected)
	if len(spool.Snapshot(false)) != 0 {
		t.Fatalf("drain retained %d historical UID entries", len(spool.Snapshot(false)))
	}
	// A very old live callback can still report after history has been drained.
	// Its immutable sink must remain usable without retaining all past UIDs in
	// the spool itself.
	sources[0].AddUp(11)
	sources[generations-1].AddDown(23)
	if len(spool.Snapshot(false)) != 2 {
		t.Fatalf("late callbacks restored %d historical entries, want only 2", len(spool.Snapshot(false)))
	}
	requireRetiredTrafficSnapshot(t, server, tag, true, map[int][2]int64{1000: {11, 0}, 1000 + generations - 1: {0, 23}})
	requireRetiredTrafficSnapshot(t, server, tag, true, map[int][2]int64{})
	if len(spool.Snapshot(false)) != 0 {
		t.Fatal("late callback drain retained zero-valued historical entries")
	}
}

func TestRetiredVLESSTrafficMergesRenewedCredentialWithSameUID(t *testing.T) {
	const tag, credential, uid = "retired-same-uid", "renewed-uuid", 41
	spool := &counter.RetiredUserTraffic{}
	server, live := retiredTrafficTestServer(tag, spool, map[string]int{credential: uid})
	old := &counter.TrafficStorage{}
	old.AddUp(128)
	old.AddDown(512)
	old.RetireToFunc(func(up, down int64) { spool.Add(uid, up, down) })
	current := live.GetCounter(credential)
	current.AddUp(17)
	current.AddDown(31)
	for _, reset := range []bool{false, false, true} {
		rows, err := server.GetUserTrafficSlice(tag, reset)
		if err != nil {
			t.Fatal(err)
		}
		// ReportUserTraffic builds a UID-keyed object, so duplicate UID rows
		// overwrite each other rather than adding. Require the actual collector
		// to return one already-merged row, not only an aggregate assertion.
		if len(rows) != 1 || rows[0] != (panel.UserTraffic{UID: uid, Upload: 145, Download: 543}) {
			t.Fatalf("same UID produced unmerged billing rows(reset=%t): %+v", reset, rows)
		}
	}
	requireRetiredTrafficSnapshot(t, server, tag, true, map[int][2]int64{})
	old.AddUp(5)
	current.AddDown(11)
	rows, err := server.GetUserTrafficSlice(tag, true)
	if err != nil || len(rows) != 1 || rows[0] != (panel.UserTraffic{UID: uid, Upload: 5, Download: 11}) {
		t.Fatalf("late bytes and same UID current session were not merged: rows=%+v err=%v", rows, err)
	}
	requireRetiredTrafficSnapshot(t, server, tag, true, map[int][2]int64{})
}

func TestRetiredVLESSTrafficConcurrentCallbacksAndReports(t *testing.T) {
	const tag, credential = "retired-concurrent", "renewed-same-uuid"
	const oldUID, currentUID = 41, 909
	const workers, iterations, collectors = 8, 400, 2
	spool := &counter.RetiredUserTraffic{}
	server, live := retiredTrafficTestServer(tag, spool, map[string]int{credential: currentUID})
	current := live.GetCounter(credential)
	sources := make([]*counter.TrafficStorage, workers)
	for i := range sources {
		sources[i] = &counter.TrafficStorage{}
		sources[i].RetireToFunc(func(up, down int64) { spool.Add(oldUID, up, down) })
	}
	start, finished := make(chan struct{}), make(chan struct{})
	var producers sync.WaitGroup
	producers.Add(workers + 1)
	for _, source := range sources {
		go func(storage *counter.TrafficStorage) {
			defer producers.Done()
			<-start
			for i := 0; i < iterations; i++ {
				storage.AddUp(3)
				storage.AddDown(7)
				if i%17 == 0 {
					runtime.Gosched()
				}
			}
		}(source)
	}
	go func() {
		defer producers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			current.AddUp(5)
			current.AddDown(11)
		}
	}()
	results := make(chan map[int][2]int64, collectors)
	errors := make(chan error, collectors)
	for i := 0; i < collectors; i++ {
		go func() {
			<-start
			total := make(map[int][2]int64)
			for {
				rows, err := server.GetUserTrafficSlice(tag, true)
				if err != nil {
					errors <- err
					results <- total
					return
				}
				for uid, value := range retiredTrafficTotals(rows) {
					previous := total[uid]
					total[uid] = [2]int64{previous[0] + value[0], previous[1] + value[1]}
				}
				select {
				case <-finished:
					results <- total
					return
				default:
					runtime.Gosched()
				}
			}
		}()
	}
	close(start)
	producers.Wait()
	close(finished)
	total := make(map[int][2]int64)
	for i := 0; i < collectors; i++ {
		for uid, value := range <-results {
			previous := total[uid]
			total[uid] = [2]int64{previous[0] + value[0], previous[1] + value[1]}
		}
	}
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	// The producers finished before the final drain. No timeout/retry can
	// explain away missing bytes; every callback must appear exactly once.
	remaining, err := server.GetUserTrafficSlice(tag, true)
	if err != nil {
		t.Fatal(err)
	}
	for uid, value := range retiredTrafficTotals(remaining) {
		previous := total[uid]
		total[uid] = [2]int64{previous[0] + value[0], previous[1] + value[1]}
	}
	expected := map[int][2]int64{
		oldUID:     {workers * iterations * 3, workers * iterations * 7},
		currentUID: {iterations * 5, iterations * 11},
	}
	if !reflect.DeepEqual(total, expected) {
		t.Fatalf("concurrent reports lost/replayed/misattributed bytes: got %+v want %+v", total, expected)
	}
	requireRetiredTrafficSnapshot(t, server, tag, true, map[int][2]int64{})
	if len(spool.Snapshot(false)) != 0 {
		t.Fatal("concurrent report drain retained historical UID entries")
	}
}

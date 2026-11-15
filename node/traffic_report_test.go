package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
)

type trafficReportSnapshot struct {
	traffic []panel.UserTraffic
	err     error
}

type trafficReportCore struct {
	fakeCore
	reportMu  sync.Mutex
	snapshots []trafficReportSnapshot
	drains    int
}

func (c *trafficReportCore) GetUserTrafficSlice(_ string, reset bool) ([]panel.UserTraffic, error) {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	if !reset {
		return nil, errors.New("reporting must drain one fresh snapshot")
	}
	c.drains++
	if len(c.snapshots) == 0 {
		return nil, nil
	}
	snapshot := c.snapshots[0]
	c.snapshots = c.snapshots[1:]
	return snapshot.traffic, snapshot.err
}

func newTrafficReportController(t *testing.T, core *trafficReportCore, handler http.HandlerFunc) *Controller {
	return newTrafficReportControllerWithPlugin(t, core, nil, handler)
}

func newTrafficReportControllerWithPlugin(t *testing.T, core *trafficReportCore, plugin, handler http.HandlerFunc) *Controller {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/arinode/traffic/info", "/api/v1/arinode/traffic/report":
			if plugin != nil {
				plugin(w, r)
			} else {
				http.NotFound(w, r)
			}
			return
		}
		if r.URL.Path == "/api/v1/server/UniProxy/status" {
			fmt.Fprint(w, `{"data":true}`)
			return
		}
		if r.URL.Path != "/api/v1/server/UniProxy/push" {
			t.Errorf("unexpected reporting endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	api, err := panel.New(&conf.ApiConfig{APIHost: server.URL, Key: "test", NodeType: "vless", NodeID: 7, Timeout: 2})
	if err != nil {
		t.Fatal(err)
	}
	limiter.Init()
	l := limiter.AddLimiter(t.Name(), &conf.LimitConfig{}, nil, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(t.Name()) })
	c := NewController(core, api, &conf.Options{})
	c.tag, c.limiter = t.Name(), l
	return c
}

func decodeTrafficReport(t *testing.T, r *http.Request) map[int][]int64 {
	t.Helper()
	var traffic map[int][]int64
	if err := json.NewDecoder(r.Body).Decode(&traffic); err != nil {
		t.Error(err)
	}
	return traffic
}

func TestNativeTrafficReportRetainsFailureAndCombinesOnlyFreshBytes(t *testing.T) {
	core := &trafficReportCore{snapshots: []trafficReportSnapshot{
		{traffic: []panel.UserTraffic{{UID: 1, Upload: 300, Download: 512}}},
		{}, // An empty/sub-threshold core drain must not discard the failed batch.
		{traffic: []panel.UserTraffic{{UID: 1, Upload: 20, Download: 30}, {UID: 2, Upload: 7, Download: 11}}},
		{traffic: []panel.UserTraffic{{UID: 1, Upload: 5, Download: 9}}},
		{},
	}}
	var received []map[int][]int64
	c := newTrafficReportController(t, core, func(w http.ResponseWriter, r *http.Request) {
		received = append(received, decodeTrafficReport(t, r))
		if len(received) <= 2 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":true}`)
	})
	for range len(core.snapshots) {
		if err := c.reportUserTrafficTask(); err != nil {
			t.Fatal(err)
		}
	}
	want := []map[int][]int64{
		{1: {300, 512}},
		{1: {300, 512}},
		{1: {320, 542}, 2: {7, 11}},
		{1: {5, 9}},
	}
	if !reflect.DeepEqual(received, want) {
		t.Fatalf("failed report lost bytes or retried bytes were added again: got=%v want=%v", received, want)
	}
	if core.drains != 5 {
		t.Fatalf("fresh traffic must be drained once per interval: %d", core.drains)
	}
}

func TestNativeTrafficReportRetriesPendingWhenCoreReadFails(t *testing.T) {
	core := &trafficReportCore{snapshots: []trafficReportSnapshot{
		{traffic: []panel.UserTraffic{{UID: 1, Upload: 17, Download: 29}}},
		{err: errors.New("temporary core collector failure")},
		{},
	}}
	var received []map[int][]int64
	c := newTrafficReportController(t, core, func(w http.ResponseWriter, r *http.Request) {
		received = append(received, decodeTrafficReport(t, r))
		if len(received) == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":true}`)
	})
	for range 3 {
		if err := c.reportUserTrafficTask(); err != nil {
			t.Fatal(err)
		}
	}
	if want := []map[int][]int64{{1: {17, 29}}, {1: {17, 29}}}; !reflect.DeepEqual(received, want) {
		t.Fatalf("collector failure discarded pending traffic: got=%v want=%v", received, want)
	}
}

func TestNativeTrafficReportsSerializePendingSnapshot(t *testing.T) {
	core := &trafficReportCore{snapshots: []trafficReportSnapshot{
		{traffic: []panel.UserTraffic{{UID: 1, Upload: 100, Download: 200}}},
		{traffic: []panel.UserTraffic{{UID: 1, Upload: 3, Download: 5}}},
		{},
	}}
	firstEntered, releaseFirst := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var received []map[int][]int64
	c := newTrafficReportController(t, core, func(w http.ResponseWriter, r *http.Request) {
		traffic := decodeTrafficReport(t, r)
		mu.Lock()
		received = append(received, traffic)
		index := len(received)
		mu.Unlock()
		if index == 1 {
			close(firstEntered)
			<-releaseFirst
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":true}`)
	})
	var reports sync.WaitGroup
	reports.Add(1)
	go func() { defer reports.Done(); c.reportUserTrafficTask() }()
	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		close(releaseFirst)
		t.Fatal("first report never reached the panel")
	}
	secondStarted := make(chan struct{})
	reports.Add(1)
	go func() {
		defer reports.Done()
		close(secondStarted)
		c.reportUserTrafficTask()
	}()
	<-secondStarted
	close(releaseFirst)
	reports.Wait()
	if err := c.reportUserTrafficTask(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []map[int][]int64{{1: {100, 200}}, {1: {103, 205}}}; !reflect.DeepEqual(received, want) {
		t.Fatalf("overlapping report calls lost or duplicated pending bytes: got=%v want=%v", received, want)
	}
}

func TestNativeTrafficRetryDoesNotReplayOnlineActivity(t *testing.T) {
	var alive []map[int][]string
	pushes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/status":
		case "/api/v1/arinode/traffic/info":
			http.NotFound(w, r)
			return
		case "/api/v1/server/UniProxy/push":
			pushes++
			if pushes == 1 {
				http.Error(w, "temporary failure", http.StatusServiceUnavailable)
				return
			}
		case "/api/v1/server/UniProxy/alive":
			var data map[int][]string
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				t.Error(err)
			}
			alive = append(alive, data)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":true}`)
	}))
	defer server.Close()
	api, err := panel.New(&conf.ApiConfig{APIHost: server.URL, Key: "test", NodeType: "hysteria2", NodeID: 7})
	if err != nil {
		t.Fatal(err)
	}
	limiter.Init()
	l := limiter.AddLimiter(t.Name(), &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user"}}, nil)
	defer limiter.DeleteLimiter(t.Name())
	core := &trafficReportCore{snapshots: []trafficReportSnapshot{
		{traffic: []panel.UserTraffic{{UID: 1, Upload: 10, Download: 20}}},
		{},
	}}
	c := NewController(core, api, &conf.Options{Core: "hysteria2"})
	c.tag, c.limiter = t.Name(), l
	release := l.TrackDeviceIP(t.Name()+"|user", "192.0.2.1")
	defer release()
	if err := c.reportUserTrafficTask(); err != nil {
		t.Fatal(err)
	}
	release()
	if err := c.reportUserTrafficTask(); err != nil {
		t.Fatal(err)
	}
	if pushes != 2 || len(alive) != 2 || len(alive[0][1]) != 1 {
		t.Fatalf("test did not report then retry the active user's traffic: pushes=%d alive=%v", pushes, alive)
	}
	if ips, exists := alive[1][1]; !exists || ips == nil || len(ips) != 0 {
		t.Fatalf("replayed billing bytes incorrectly kept an inactive device online: %v", alive[1])
	}
}

func TestPluginTrafficLostAckKeepsSealedBatchAndNeverDowngrades(t *testing.T) {
	core := &trafficReportCore{snapshots: []trafficReportSnapshot{
		{traffic: []panel.UserTraffic{{UID: 1, Upload: 300, Download: 512}}},
		{traffic: []panel.UserTraffic{{UID: 1, Upload: 20, Download: 30}}},
		{},
		{},
	}}
	var phase atomic.Int32
	var mu sync.Mutex
	var batches []panel.TrafficReport
	charged := make(map[string]map[int][2]int64)
	c := newTrafficReportControllerWithPlugin(t, core, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/arinode/traffic/info" {
			fmt.Fprint(w, `{"version":1,"node_id":7}`)
			return
		}
		var report panel.TrafficReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		batches = append(batches, report)
		switch phase.Load() {
		case 0:
			// The panel accepted the upload, but the connection died before
			// the node read the response (including Resty's same-ID retries).
			charged[report.ReportID] = report.Traffic
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		case 1:
			http.NotFound(w, r) // plugin briefly absent; must not use native POST
		default:
			if old, ok := charged[report.ReportID]; ok && !reflect.DeepEqual(old, report.Traffic) {
				t.Error("same batch ID reused with different traffic")
			}
			charged[report.ReportID] = report.Traffic
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "report_id": report.ReportID})
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		t.Error("keyed report was downgraded to additive native push")
	})
	for _, step := range []int32{0, 1, 2, 2} {
		phase.Store(step)
		if err := c.reportUserTrafficTask(); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(batches) < 4 || len(charged) != 2 || c.userTrafficReport != nil || len(c.pendingUserTraffic) != 0 {
		t.Fatalf("sealed batches did not finish: submissions=%d, receipts=%d", len(batches), len(charged))
	}
	first := batches[0]
	for _, retry := range batches[:len(batches)-1] {
		if first.ReportID != retry.ReportID || !reflect.DeepEqual(first.Traffic, retry.Traffic) {
			t.Fatalf("frozen batch changed over retry: %+v vs %+v", first, retry)
		}
	}
	if charged[first.ReportID][1] != [2]int64{300, 512} || charged[batches[len(batches)-1].ReportID][1] != [2]int64{20, 30} {
		t.Fatalf("incorrect accounting batches: %+v", charged)
	}
}

package node

import (
	"encoding/json"
	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type clusterCore struct {
	fakeCore
	drains int
}

func (c *clusterCore) GetUserTrafficSlice(_ string, reset bool) ([]panel.UserTraffic, error) {
	c.drains++
	return []panel.UserTraffic{{UID: 1, Upload: 10, Download: 20}}, nil
}
func TestClusterRetriesExactSnapshotAndClearsExpiredUserIPs(t *testing.T) {
	var received []panel.ClusterReport
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/arinode/cluster/report" {
			t.Errorf("native overwrite endpoint called: %s", r.URL.Path)
		}
		var report panel.ClusterReport
		json.NewDecoder(r.Body).Decode(&report)
		received = append(received, report)
		if len(received) == 1 {
			http.Error(w, "temporary failure", 503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"accepted": true, "report_id": report.ReportID})
	}))
	defer s.Close()
	api, err := panel.New(&conf.ApiConfig{APIHost: s.URL, Key: "shared", NodeType: "vless", NodeID: 7, Cluster: &conf.ClusterConfig{Domain: "pool.example.com", MemberID: "hk-01"}})
	if err != nil {
		t.Fatal(err)
	}
	core := &clusterCore{}
	limiter.Init()
	l := limiter.AddLimiter("group", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "same"}}, map[int]int{})
	defer limiter.DeleteLimiter("group")
	c := NewController(core, api, &conf.Options{})
	c.tag = "group"
	c.limiter = l
	c.info = &panel.NodeInfo{PushInterval: time.Minute}
	c.clusterAlive = map[int]map[string]time.Time{1: {"192.0.2.1": time.Now()}}
	c.reportUserTrafficTask()
	c.reportUserTrafficTask()
	if core.drains != 1 || len(received) != 2 || !reflect.DeepEqual(received[0], received[1]) {
		t.Fatal("retry consumed another snapshot or changed request ID")
	}
	l.UpdateUser(c.tag, nil, []panel.UserInfo{{Id: 1, Uuid: "same"}})
	c.reportUserTrafficTask()
	if len(received[2].Alive) != 0 {
		t.Fatal("empty member snapshot did not remove expired user's IP")
	}
}

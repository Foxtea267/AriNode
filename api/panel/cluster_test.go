package panel

import (
	"encoding/json"
	"github.com/Foxtea267/AriNode/conf"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClusterNativeReadsAndAcknowledgedReports(t *testing.T) {
	reports := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "shared" || r.URL.Query().Get("node_id") != "7" {
			t.Error("missing native auth")
		}
		switch r.URL.Path {
		case clusterBase + "info":
			json.NewEncoder(w).Encode(map[string]any{"version": 1, "node_id": 7})
		case "/api/v1/server/UniProxy/user":
			w.Write([]byte(`{"users":[]}`))
		case clusterBase + "alivelist":
			w.Write([]byte(`{"alive":{"1":2},"ips":{"1":["192.0.2.1","192.0.2.2"]}}`))
		case clusterBase + "report":
			var report ClusterReport
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Fatal(err)
			}
			if report.MemberID != "hk-01" || report.Domain != "pool.example.com" || report.Traffic[1] != [2]int64{30, 50} || len(report.ReportID) != 32 {
				t.Errorf("bad report %+v", report)
			}
			reports++
			json.NewEncoder(w).Encode(map[string]any{"accepted": true, "report_id": report.ReportID})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c, err := New(&conf.ApiConfig{APIHost: s.URL, Key: "shared", NodeID: 7, NodeType: "vless", Cluster: &conf.ClusterConfig{MemberID: "hk-01", Domain: "pool.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CheckCluster(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetUserList(); err != nil {
		t.Fatal(err)
	}
	alive, err := c.GetUserAlive()
	if err != nil || alive[1] != 2 || len(c.AliveIPs[1]) != 2 {
		t.Fatal("group alive data lost")
	}
	r, err := c.NewClusterReport([]UserTraffic{{UID: 1, Upload: 10, Download: 20}, {UID: 1, Upload: 20, Download: 30}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ReportCluster(r); err != nil {
		t.Fatal(err)
	}
	if reports != 1 {
		t.Fatal("report missing")
	}
}

func TestClusterDoesNotAcceptMissingPluginOrIncorrectAck(t *testing.T) {
	for _, body := range []string{`{}`, `{"accepted":true,"report_id":"wrong"}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		c, _ := New(&conf.ApiConfig{APIHost: s.URL, Key: "shared", NodeID: 7, NodeType: "vless", Cluster: &conf.ClusterConfig{MemberID: "hk-01", Domain: "pool.example.com"}})
		if c.CheckCluster() == nil {
			t.Fatal("missing plugin accepted")
		}
		r, _ := c.NewClusterReport(nil, nil, nil)
		if c.ReportCluster(r) == nil {
			t.Fatal("incorrect acknowledgement accepted")
		}
		s.Close()
	}
}

func TestClusterMalformedAliveDoesNotDiscardCachedDeviceIPs(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer s.Close()
	c, _ := New(&conf.ApiConfig{APIHost: s.URL, Key: "shared", NodeID: 7, NodeType: "vless", Cluster: &conf.ClusterConfig{MemberID: "hk-01", Domain: "pool.example.com"}})
	c.AliveIPs = map[int][]string{1: {"192.0.2.1"}}
	if _, err := c.GetUserAlive(); err == nil || len(c.AliveIPs[1]) != 1 {
		t.Fatal("malformed response replaced cached group devices")
	}
}

package node

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
)

func TestNativeReportsClearEightOldDeviceIPsAndRetry(t *testing.T) {
	for _, machine := range []int{0, 39} {
		t.Run(fmt.Sprint(machine), func(t *testing.T) {
			var snapshots []map[int][]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := "/api/v1/server/UniProxy/alive"
				if machine != 0 {
					path = "/api/v2/server/alive"
				}
				if r.URL.Path != path {
					t.Errorf("unexpected report path %s", r.URL.Path)
				}
				var data map[int][]string
				if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
					t.Error(err)
				}
				snapshots = append(snapshots, data)
				if len(snapshots) == 2 {
					http.Error(w, "temporary failure", 503)
					return
				}
				fmt.Fprint(w, `{"data":true}`)
			}))
			defer server.Close()
			api, err := panel.New(&conf.ApiConfig{APIHost: server.URL, Key: "test", NodeType: "anytls", NodeID: 244, MachineID: machine})
			if err != nil {
				t.Fatal(err)
			}
			limiter.Init()
			l := limiter.AddLimiter("native", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user"}}, nil)
			defer limiter.DeleteLimiter("native")
			for i := 1; i <= 8; i++ {
				l.CheckLimit("native|user", fmt.Sprintf("192.0.2.%d", i), true, true)
			}
			c := NewController(&fakeCore{}, api, &conf.Options{})
			c.limiter = l
			if err := c.reportOnlineDevices(nil); err != nil {
				t.Fatal(err)
			}
			if len(snapshots) != 1 || len(snapshots[0][1]) != 8 {
				t.Fatal("test did not establish an eight-IP panel snapshot")
			}
			if err := c.reportOnlineDevices(nil); err == nil {
				t.Fatal("failed clear was not retained")
			}
			if err := c.reportOnlineDevices(nil); err != nil {
				t.Fatal(err)
			}
			for _, data := range snapshots[1:] {
				ips, exists := data[1]
				if !exists || ips == nil || len(ips) != 0 {
					t.Fatalf("offline user must be explicitly cleared with []: %v", data)
				}
			}
			if err := c.reportOnlineDevices(nil); err != nil || len(snapshots) != 3 {
				t.Fatal("successful empty report was not acknowledged")
			}
		})
	}
}

func TestNativeLongConnectionIsReportedWithoutNewRequests(t *testing.T) {
	var counts []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[int][]string
		json.NewDecoder(r.Body).Decode(&data)
		counts = append(counts, len(data[1]))
		fmt.Fprint(w, `{"data":true}`)
	}))
	defer server.Close()
	api, _ := panel.New(&conf.ApiConfig{APIHost: server.URL, Key: "test", NodeType: "vless", NodeID: 1})
	limiter.Init()
	l := limiter.AddLimiter("long", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user"}}, nil)
	defer limiter.DeleteLimiter("long")
	c := NewController(&fakeCore{}, api, &conf.Options{})
	c.limiter = l
	done := l.TrackDeviceIP("long|user", "192.0.2.1")
	for i := 0; i < 4; i++ {
		if err := c.reportOnlineDevices(nil); err != nil {
			t.Fatal(err)
		}
		if counts[i] != 1 {
			t.Fatal("open connection was cleared between reports")
		}
	}
	done()
	if err := c.reportOnlineDevices(nil); err != nil {
		t.Fatal(err)
	}
	if counts[4] != 0 {
		t.Fatal("closed connection was not cleared")
	}
}

func TestStandaloneHY2TrafficDoesNotRenewUnidentifiedHistoricalIPs(t *testing.T) {
	var snapshots []map[int][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[int][]string
		json.NewDecoder(r.Body).Decode(&data)
		snapshots = append(snapshots, data)
		fmt.Fprint(w, `{"data":true}`)
	}))
	defer server.Close()
	api, _ := panel.New(&conf.ApiConfig{APIHost: server.URL, Key: "test", NodeType: "hysteria2", NodeID: 1})
	limiter.Init()
	l := limiter.AddLimiter("legacy", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user"}}, nil)
	defer limiter.DeleteLimiter("legacy")
	c := NewController(&fakeCore{}, api, &conf.Options{Core: "hysteria2"})
	c.limiter = l
	l.CheckLimit("legacy|user", "192.0.2.1", true, true)
	if err := c.reportOnlineDevices(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.reportOnlineDevices([]panel.UserTraffic{{UID: 1, Upload: 10}}); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || !c.reportedOnline[1] {
		t.Fatal("aggregate traffic renewed an unidentified old IP or declared the user offline")
	}
	if err := c.reportOnlineDevices(nil); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || len(snapshots[1][1]) != 0 {
		t.Fatal("legacy offline user was not cleared after traffic stopped")
	}
}

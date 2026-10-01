package komari

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/common/sysstatus"
	"github.com/Foxtea267/AriNode/conf"
)

func TestIndependentKomariBindings(t *testing.T) {
	previous := readStatus
	readStatus = func() (sysstatus.Status, error) {
		return sysstatus.Status{CPU: 12.5, Mem: sysstatus.Usage{Total: 100, Used: 20}, Disk: sysstatus.Usage{Total: 200, Used: 30}}, nil
	}
	t.Cleanup(func() { readStatus = previous })
	var healthyReports atomic.Int32
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/clients/v2/rpc" || r.URL.Query().Get("token") != "good-token" {
			t.Errorf("bad request: %s", r.URL)
			http.Error(w, "bad", 400)
			return
		}
		var message struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
		}
		if string(message.ID) != "null" {
			t.Errorf("monitor-only request must be a notification, got id %s", message.ID)
		}
		if message.Method == "agent.report" {
			healthyReports.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer healthy.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unavailable", 503) }))
	defer broken.Close()
	manager := New()
	manager.Reconcile([]conf.KomariBinding{{Name: "bad", Endpoint: broken.URL, Token: "bad-token", Interval: 5}, {Name: "good", Endpoint: healthy.URL, Token: "good-token", Interval: 5}})
	defer manager.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if healthyReports.Load() > 0 {
			states := manager.Snapshot()
			if len(states) == 2 {
				var good, bad string
				for _, state := range states {
					if state.Name == "good" {
						good = state.State
					} else {
						bad = state.State
					}
				}
				if good == "reporting" && bad == "retrying" {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("healthy binding did not continue: reports=%d states=%+v", healthyReports.Load(), manager.Snapshot())
}

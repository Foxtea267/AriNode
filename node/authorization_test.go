package node

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	vCore "github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
)

type authorizationCore struct {
	fakeCore
	deleteCalls         int
	failDelete, failAdd bool
	lastAdded           []panel.UserInfo
}

func (c *authorizationCore) DelUsers([]panel.UserInfo, string, *panel.NodeInfo) error {
	c.deleteCalls++
	if c.failDelete {
		c.failDelete = false
		return fmt.Errorf("temporary delete failure")
	}
	return nil
}
func (c *authorizationCore) AddUsers(p *vCore.AddUsersParams) (int, error) {
	if c.failAdd {
		c.failAdd = false
		return 0, fmt.Errorf("temporary add failure")
	}
	c.lastAdded = append([]panel.UserInfo{}, p.Users...)
	return len(p.Users), nil
}

func authorizationController(t *testing.T, host string, core *authorizationCore, users []panel.UserInfo) *Controller {
	t.Helper()
	cfg := testConfig(host, "vless", 1, 0)
	api, err := panel.New(&cfg.ApiConfig)
	if err != nil {
		t.Fatal(err)
	}
	limiter.Init()
	c := NewController(core, api, &cfg.Options)
	c.tag = "authorization-test"
	c.info = &panel.NodeInfo{Id: 1, Type: "vless", Common: &panel.CommonNode{ServerPort: 12345}, VAllss: &panel.VAllssNode{}, PullInterval: time.Minute, PushInterval: time.Minute}
	c.userList = users
	c.limiter = limiter.AddLimiter(c.tag, &conf.LimitConfig{}, users, map[int]int{})
	t.Cleanup(func() { limiter.DeleteLimiter(c.tag) })
	return c
}

func TestRevocationRetriesSnapshotAfterCoreFailureAndConfigError(t *testing.T) {
	userRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/user":
			userRequests++
			if r.Header.Get("If-None-Match") == "expired" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", "expired")
			fmt.Fprint(w, `{"users":[]}`)
		case "/api/v1/server/UniProxy/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		case "/api/v1/server/UniProxy/config":
			http.Error(w, "broken node config", 500)
		}
	}))
	defer server.Close()
	core := &authorizationCore{failDelete: true}
	c := authorizationController(t, server.URL, core, []panel.UserInfo{{Id: 1, Uuid: "expired-user"}})
	c.nodeInfoMonitor()
	if len(c.userList) != 1 {
		t.Fatal("failed deletion was committed")
	}
	c.nodeInfoMonitor()
	if len(c.userList) != 0 || core.deleteCalls != 2 {
		t.Fatalf("304 skipped retry: users=%v deleteCalls=%d", c.userList, core.deleteCalls)
	}
	if userRequests != 2 {
		t.Fatal("unexpected user polling")
	}
	c.nodeInfoMonitor()
	if core.deleteCalls != 2 {
		t.Fatal("unchanged snapshot retried successful deletion")
	}
}

func TestFailedReloadCannotRestoreRevokedUsers(t *testing.T) {
	server := nodePanel(t)
	defer server.Close()
	core := &authorizationCore{}
	c := authorizationController(t, server.URL, core, []panel.UserInfo{{Id: 1, Uuid: "expired-user"}})
	c.coreAdded = true
	core.failNext = true
	c.nodeInfoMonitor()
	if len(c.userList) != 0 || len(core.lastAdded) != 0 || core.deleteCalls != 1 {
		t.Fatal("failed reload restored expired credentials")
	}
	if !c.coreAdded || c.pendingNode == nil {
		t.Fatal("failed reload did not restore node and retain pending config")
	}
}

func TestRevocationRemainsCommittedWhenAddingUserFails(t *testing.T) {
	server := nodePanel(t)
	defer server.Close()
	core := &authorizationCore{failAdd: true}
	c := authorizationController(t, server.URL, core, []panel.UserInfo{{Id: 1, Uuid: "expired-user"}})
	next := []panel.UserInfo{{Id: 2, Uuid: "healthy-user"}}
	if err := c.applyUserList(next, nil); err == nil {
		t.Fatal("expected add failure")
	}
	if len(c.userList) != 0 {
		t.Fatal("successful revocation rolled back")
	}
	if err := c.applyUserList(next, nil); err != nil {
		t.Fatal(err)
	}
	if core.deleteCalls != 1 || len(c.userList) != 1 {
		t.Fatal("deleted user retried or replacement not added")
	}
}

func TestUserChangeIncludesIdentityAndDeviceLimit(t *testing.T) {
	old := []panel.UserInfo{{Id: 1, Uuid: "user", DeviceLimit: 1}}
	for _, updated := range []panel.UserInfo{{Id: 2, Uuid: "user", DeviceLimit: 1}, {Id: 1, Uuid: "user", DeviceLimit: 2}} {
		del, add := compareUserList(old, []panel.UserInfo{updated})
		if len(del) != 1 || len(add) != 1 {
			t.Fatal("identity/device limit update ignored")
		}
	}
	if authorizationInterval(time.Minute) != 30*time.Second || authorizationInterval(time.Second) != time.Second {
		t.Fatal("authorization polling cap not applied")
	}
}

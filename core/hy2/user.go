package hy2

import (
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/common/format"
	vCore "github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/apernet/hysteria/core/v2/server"
)

var _ server.Authenticator = &V2bX{}

type authenticatedUser struct {
	uid        int
	credential string
	id         string
	done       chan struct{}
}

type V2bX struct {
	mutex      sync.RWMutex
	users      map[string]authenticatedUser
	active     map[string]authenticatedUser
	generation uint64
	tag        string
	closed     bool
}

func (v *V2bX) Authenticate(addr net.Addr, auth string, _ uint64) (bool, string) {
	v.mutex.RLock()
	defer v.mutex.RUnlock()
	user, ok := v.users[auth]
	if !ok || v.closed {
		return false, ""
	}
	l, err := limiter.GetLimiter(v.tag)
	if err != nil {
		return false, ""
	}
	if _, reject := l.CheckLimit(format.UserTag(v.tag, auth), extractIPFromAddr(addr), false, true); reject {
		return false, ""
	}
	return true, user.id
}

func (v *V2bX) close() {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	v.closed = true
	for _, entry := range v.active {
		close(entry.done)
	}
	clear(v.users)
	clear(v.active)
}

func (v *V2bX) credential(id string) string {
	v.mutex.RLock()
	defer v.mutex.RUnlock()
	return v.active[id].credential
}

func (h *Hysteria2) AddUsers(p *vCore.AddUsersParams) (int, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n, ok := h.Hy2nodes[p.Tag]
	if !ok {
		return 0, fmt.Errorf("Hysteria2 node not found")
	}
	auth := n.Auth
	auth.mutex.Lock()
	defer auth.mutex.Unlock()
	for _, u := range p.Users {
		if u.Uuid == "" || u.Id <= 0 {
			return 0, fmt.Errorf("invalid Hysteria2 user")
		}
		if current, exists := auth.users[u.Uuid]; exists && current.uid != u.Id {
			return 0, fmt.Errorf("Hysteria2 user must be removed before reassigning its credential")
		}
	}
	if auth.users == nil {
		auth.users = make(map[string]authenticatedUser)
		auth.active = make(map[string]authenticatedUser)
	}
	for _, u := range p.Users {
		if _, exists := auth.users[u.Uuid]; exists {
			continue
		}
		auth.generation++
		entry := authenticatedUser{uid: u.Id, credential: u.Uuid, id: strconv.FormatUint(auth.generation, 10), done: make(chan struct{})}
		auth.users[u.Uuid], auth.active[entry.id] = entry, entry
	}
	return len(p.Users), nil
}

func (h *Hysteria2) DelUsers(users []panel.UserInfo, tag string, _ *panel.NodeInfo) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n, ok := h.Hy2nodes[tag]
	if !ok {
		return fmt.Errorf("Hysteria2 node not found")
	}
	hook := n.TrafficLogger.(*HookServer)
	auth := n.Auth
	auth.mutex.Lock()
	revoked := make(map[string]bool)
	for _, u := range users {
		entry, exists := auth.users[u.Uuid]
		if !exists {
			continue
		}
		delete(auth.users, u.Uuid)
		delete(auth.active, entry.id)
		close(entry.done)
		revoked[entry.id] = true
		if value, ok := hook.Counter.Load(tag); ok {
			traffic := value.(*counter.TrafficCounter)
			if counts, ok := traffic.Counters.Load(entry.id); ok {
				up, down := counts.(*counter.TrafficStorage).Snapshot(true)
				hook.retired.Add(entry.uid, up, down)
				traffic.Delete(entry.id)
			}
		}
	}
	auth.mutex.Unlock()
	hook.closeRevokedStreams(revoked)
	return nil
}

func (h *Hysteria2) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n, ok := h.Hy2nodes[tag]
	if !ok {
		return nil, nil
	}
	auth := n.Auth
	auth.mutex.Lock()
	defer auth.mutex.Unlock()
	hook := n.TrafficLogger.(*HookServer)
	totals := make(map[int]panel.UserTraffic)
	merge := func(uid int, up, down int64) {
		if uid == 0 || (up == 0 && down == 0) {
			return
		}
		t := totals[uid]
		t.UID, t.Upload, t.Download = uid, t.Upload+up, t.Download+down
		totals[uid] = t
	}
	for _, t := range hook.retired.Snapshot(reset) {
		merge(t.UID, t.Upload, t.Download)
	}
	if v, ok := hook.Counter.Load(tag); ok {
		v.(*counter.TrafficCounter).Counters.Range(func(key, value any) bool {
			entry, active := auth.active[key.(string)]
			if !active {
				return true
			}
			counts := value.(*counter.TrafficStorage)
			up, down := counts.Snapshot(false)
			if up+down > hook.ReportMinTrafficBytes {
				if reset {
					up, down = counts.Snapshot(true)
				}
				merge(entry.uid, up, down)
			}
			return true
		})
	}
	var result []panel.UserTraffic
	for _, t := range totals {
		result = append(result, t)
	}
	return result, nil
}

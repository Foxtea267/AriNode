package counter

import "sync"

type UserTraffic struct {
	UID              int
	Upload, Download int64
}

// Only pending bytes occupy this spool. Late copy callbacks can insert their
// original UID again after a drain; zero-valued retired users are not retained.
type RetiredUserTraffic struct {
	mu      sync.Mutex
	pending map[int]UserTraffic
}

func (r *RetiredUserTraffic) Add(uid int, up, down int64) {
	if up == 0 && down == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = make(map[int]UserTraffic)
	}
	t := r.pending[uid]
	t.UID, t.Upload, t.Download = uid, t.Upload+up, t.Download+down
	r.pending[uid] = t
}

func (r *RetiredUserTraffic) Snapshot(reset bool) []UserTraffic {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]UserTraffic, 0, len(r.pending))
	for _, t := range r.pending {
		result = append(result, t)
	}
	if reset {
		r.pending = nil
	}
	return result
}

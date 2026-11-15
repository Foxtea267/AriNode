package hy2

import (
	"math"
	"sync"
	"time"

	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/apernet/hysteria/core/v2/server"
)

var _ server.TrafficLogger = (*HookServer)(nil)

type HookServer struct {
	Tag                   string
	auth                  *V2bX
	Counter               sync.Map
	ReportMinTrafficBytes int64
	retired               counter.RetiredUserTraffic
	streamMu              sync.Mutex
	streams               map[server.HyStream]string
}

func (h *HookServer) TraceStream(stream server.HyStream, stats *server.StreamStats) {
	h.auth.mutex.RLock()
	_, active := h.auth.active[stats.AuthID]
	if active {
		h.streamMu.Lock()
		if h.streams == nil {
			h.streams = make(map[server.HyStream]string)
		}
		h.streams[stream] = stats.AuthID
		h.streamMu.Unlock()
	}
	h.auth.mutex.RUnlock()
	if !active {
		_ = stream.Close()
	}
}

func (h *HookServer) UntraceStream(stream server.HyStream) {
	h.streamMu.Lock()
	delete(h.streams, stream)
	h.streamMu.Unlock()
}

func (h *HookServer) closeRevokedStreams(ids map[string]bool) {
	h.streamMu.Lock()
	var streams []server.HyStream
	for stream, id := range h.streams {
		if ids[id] {
			streams = append(streams, stream)
			delete(h.streams, stream)
		}
	}
	h.streamMu.Unlock()
	for _, stream := range streams {
		_ = stream.Close()
	}
}

func (h *HookServer) LogTraffic(id string, tx, rx uint64) bool {
	if tx > math.MaxInt64 || rx > math.MaxInt64-tx {
		return false
	}
	h.auth.mutex.RLock()
	user, active := h.auth.active[id]
	if !active {
		h.auth.mutex.RUnlock()
		return false // the SDK drops the payload and disconnects the transport
	}
	l, err := limiter.GetLimiter(h.Tag)
	if err != nil {
		h.auth.mutex.RUnlock()
		return false
	}
	key := format.UserTag(h.Tag, user.credential)
	v, ok := l.UserLimitInfo.Load(key)
	if !ok || v.(*limiter.UserLimitInfo).OverLimit.Swap(false) {
		h.auth.mutex.RUnlock()
		return false
	}
	// The SDK calls this before forwarding both TCP and UDP payloads.
	// All transports/directions use the same limiter bucket. Never hold
	// the authentication lock while waiting for tokens.
	bucket, reject := l.CheckLimit(key, "", false, false)
	if reject {
		h.auth.mutex.RUnlock()
		return false
	}
	var wait time.Duration
	if bucket != nil && tx+rx != 0 {
		wait = bucket.Take(int64(tx + rx))
	}
	h.auth.mutex.RUnlock()
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-user.done:
			return false
		}
	}
	h.auth.mutex.RLock()
	defer h.auth.mutex.RUnlock()
	if _, active := h.auth.active[id]; !active {
		return false
	}
	counts, _ := h.Counter.LoadOrStore(h.Tag, counter.NewTrafficCounter())
	storage := counts.(*counter.TrafficCounter).GetCounter(id)
	storage.AddUp(int64(tx))
	storage.AddDown(int64(rx))
	return true
}

func (h *HookServer) LogOnlineState(string, bool) {}

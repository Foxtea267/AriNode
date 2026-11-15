package counter

import (
	"sync"
	"sync/atomic"
)

type TrafficCounter struct {
	Counters sync.Map
}

type TrafficStorage struct {
	UpCounter   atomic.Int64
	DownCounter atomic.Int64
	retireMu    sync.Mutex
	retiredTo   func(int64, int64)
}

// AddUp/AddDown keep in-flight copy callbacks billable after credentials are
// revoked. RetireTo changes their destination without discarding late bytes.
func (s *TrafficStorage) AddUp(n int64) {
	s.retireMu.Lock()
	if s.retiredTo != nil {
		s.retiredTo(n, 0)
	} else {
		s.UpCounter.Add(n)
	}
	s.retireMu.Unlock()
}

func (s *TrafficStorage) AddDown(n int64) {
	s.retireMu.Lock()
	if s.retiredTo != nil {
		s.retiredTo(0, n)
	} else {
		s.DownCounter.Add(n)
	}
	s.retireMu.Unlock()
}

// destination is a dedicated per-UID report accumulator, never another retired
// session counter. The caller serializes retirement and report collection.
func (s *TrafficStorage) RetireTo(destination *TrafficStorage) {
	if destination == nil || destination == s {
		return
	}
	s.RetireToFunc(func(up, down int64) {
		destination.UpCounter.Add(up)
		destination.DownCounter.Add(down)
	})
}

// RetireToFunc redirects final byte callbacks into a report spool. The callback
// must not call back into this storage; it is serialized with its Read/Write
// counts and can safely republish a UID after a preceding report was drained.
func (s *TrafficStorage) RetireToFunc(destination func(int64, int64)) {
	if destination == nil {
		return
	}
	s.retireMu.Lock()
	defer s.retireMu.Unlock()
	if s.retiredTo != nil {
		return
	}
	s.retiredTo = destination
	destination(s.UpCounter.Swap(0), s.DownCounter.Swap(0))
}

// Snapshot reads the current totals, or atomically drains each direction when
// reset is true. Additions racing a drain are either returned by this call or
// retained for the next one; never read totals followed by a separate Reset.
// The two directions are independent counters, not a transaction across them.
func (s *TrafficStorage) Snapshot(reset bool) (up, down int64) {
	if reset {
		return s.UpCounter.Swap(0), s.DownCounter.Swap(0)
	}
	return s.UpCounter.Load(), s.DownCounter.Load()
}

func NewTrafficCounter() *TrafficCounter {
	return &TrafficCounter{}
}

func (c *TrafficCounter) GetCounter(uuid string) *TrafficStorage {
	if cts, ok := c.Counters.Load(uuid); ok {
		return cts.(*TrafficStorage)
	}
	newStorage := &TrafficStorage{}
	if cts, loaded := c.Counters.LoadOrStore(uuid, newStorage); loaded {
		return cts.(*TrafficStorage)
	}
	return newStorage
}

func (c *TrafficCounter) GetUpCount(uuid string) int64 {
	if cts, ok := c.Counters.Load(uuid); ok {
		return cts.(*TrafficStorage).UpCounter.Load()
	}
	return 0
}

func (c *TrafficCounter) GetDownCount(uuid string) int64 {
	if cts, ok := c.Counters.Load(uuid); ok {
		return cts.(*TrafficStorage).DownCounter.Load()
	}
	return 0
}

// GetTraffic does not create a counter for an unknown user.
func (c *TrafficCounter) GetTraffic(uuid string, reset bool) (up, down int64) {
	if storage, ok := c.Counters.Load(uuid); ok {
		return storage.(*TrafficStorage).Snapshot(reset)
	}
	return 0, 0
}

func (c *TrafficCounter) Len() int {
	length := 0
	c.Counters.Range(func(_, _ interface{}) bool {
		length++
		return true
	})
	return length
}

func (c *TrafficCounter) Reset(uuid string) {
	c.GetTraffic(uuid, true)
}

func (c *TrafficCounter) Delete(uuid string) {
	c.Counters.Delete(uuid)
}

func (c *TrafficCounter) Rx(uuid string, n int) {
	cts := c.GetCounter(uuid)
	cts.DownCounter.Add(int64(n))
}

func (c *TrafficCounter) Tx(uuid string, n int) {
	cts := c.GetCounter(uuid)
	cts.UpCounter.Add(int64(n))
}

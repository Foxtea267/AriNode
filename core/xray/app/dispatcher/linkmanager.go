package dispatcher

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/protocol"
)

type ManagedWriter struct {
	writer  buf.Writer
	manager *LinkManager
	abort   func()
	halves  atomic.Int32
}

func (w *ManagedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	return w.writer.WriteMultiBuffer(mb)
}

func (w *ManagedWriter) Close() error {
	return common.Close(w.writer)
}

func (w *ManagedWriter) Interrupt() { common.Interrupt(w.writer) }

func (w *ManagedWriter) trackHalf(release func()) func() {
	w.halves.Add(1)
	return func() {
		release()
		if w.halves.Add(-1) == 0 {
			w.manager.RemoveWriter(w)
		}
	}
}

type LinkManager struct {
	links  map[*ManagedWriter]buf.Reader
	mu     sync.Mutex
	user   *protocol.MemoryUser
	closed bool
}

// RegisterUser publishes the exact authentication record before the core can
// hand it to the dispatcher. An equivalent credential added later has a
// different record and must not authorize a callback from the old one.
func (d *DefaultDispatcher) RegisterUser(user *protocol.MemoryUser) error {
	_, exists := d.LinkManagers.LoadOrStore(user.Email, &LinkManager{user: user})
	if exists {
		return fmt.Errorf("user already registered in dispatcher")
	}
	return nil
}

func (d *DefaultDispatcher) RevokeUser(email string) {
	if value, ok := d.LinkManagers.LoadAndDelete(email); ok {
		value.(*LinkManager).CloseAll()
	}
}

func (d *DefaultDispatcher) registerLink(tag string, user *protocol.MemoryUser, writer *ManagedWriter, reader buf.Reader, setup func(*counter.TrafficStorage)) bool {
	key := user.Email
	managed := strings.HasPrefix(key, tag+"|")
	if !managed {
		key = tag + "\x00" + key
	}
	value, ok := d.LinkManagers.Load(key)
	if !ok {
		// AriNode credentials always use tag|uuid. Never re-create a
		// removed registration from a previously authenticated connection.
		if managed {
			return false
		}
		// Keep legacy local inbounds (InboundConfigPath) working.
		value, _ = d.LinkManagers.LoadOrStore(key, &LinkManager{})
	}
	m := value.(*LinkManager)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || (m.user != nil && m.user != user) {
		return false
	}
	if m.links == nil {
		m.links = make(map[*ManagedWriter]buf.Reader)
	}
	writer.manager = m
	value, _ = d.Counter.LoadOrStore(tag, counter.NewTrafficCounter())
	setup(value.(*counter.TrafficCounter).GetCounter(user.Email))
	m.links[writer] = reader
	return true
}

func (d *DefaultDispatcher) closeAllLinks() {
	d.LinkManagers.Range(func(key, value any) bool {
		if d.LinkManagers.CompareAndDelete(key, value) {
			value.(*LinkManager).CloseAll()
		}
		return true
	})
}

func (m *LinkManager) RemoveWriter(writer *ManagedWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.links, writer)
}

func (m *LinkManager) CloseAll() {
	m.mu.Lock()
	links := m.links
	m.links = nil
	m.closed = true
	m.mu.Unlock()
	// Closing a writer calls RemoveWriter. Snapshot under the lock, then close
	// outside it so user/node removal cannot race map iteration or deadlock.
	for w, r := range links {
		common.Interrupt(w)
		common.Interrupt(r)
		if w.abort != nil {
			w.abort()
		}
	}
}

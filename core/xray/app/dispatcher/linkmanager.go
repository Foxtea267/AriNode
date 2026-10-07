package dispatcher

import (
	sync "sync"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

type ManagedWriter struct {
	writer  buf.Writer
	manager *LinkManager
	abort   func()
}

func (w *ManagedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	return w.writer.WriteMultiBuffer(mb)
}

func (w *ManagedWriter) Close() error {
	w.manager.RemoveWriter(w)
	return common.Close(w.writer)
}

type LinkManager struct {
	links map[*ManagedWriter]buf.Reader
	mu    sync.Mutex
}

func (m *LinkManager) AddLink(writer *ManagedWriter, reader buf.Reader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links[writer] = reader
}

func (m *LinkManager) RemoveWriter(writer *ManagedWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.links, writer)
}

func (m *LinkManager) CloseAll() {
	m.mu.Lock()
	links := m.links
	m.links = make(map[*ManagedWriter]buf.Reader)
	m.mu.Unlock()
	// Closing a writer calls RemoveWriter. Snapshot under the lock, then close
	// outside it so user/node removal cannot race map iteration or deadlock.
	for w, r := range links {
		common.Close(w)
		common.Interrupt(r)
		if w.abort != nil {
			w.abort()
		}
	}
}

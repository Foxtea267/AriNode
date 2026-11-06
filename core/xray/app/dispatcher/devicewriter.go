package dispatcher

import (
	"sync"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

// Each direction holds its own reference: closing the upload half must not make
// an ongoing download disappear from online-device reports.
type deviceWriter struct {
	writer  buf.Writer
	release func()
	once    sync.Once
}

func (w *deviceWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	err := w.writer.WriteMultiBuffer(mb)
	if err != nil {
		w.once.Do(w.release)
	}
	return err
}

func (w *deviceWriter) Close() error {
	defer w.once.Do(w.release)
	return common.Close(w.writer)
}

func (w *deviceWriter) Interrupt() {
	defer w.once.Do(w.release)
	common.Interrupt(w.writer)
}

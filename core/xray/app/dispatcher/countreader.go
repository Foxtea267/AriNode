package dispatcher

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

var _ buf.TimeoutReader = (*CounterReader)(nil)

type CounterReader struct {
	Reader  buf.TimeoutReader
	Counter *atomic.Int64
	Release func()
	once    sync.Once
}

func (c *CounterReader) release() {
	if c.Release != nil {
		c.once.Do(c.Release)
	}
}

func (c *CounterReader) Interrupt() {
	defer c.release()
	common.Interrupt(c.Reader)
}

func (c *CounterReader) ReadMultiBufferTimeout(time.Duration) (buf.MultiBuffer, error) {
	mb, err := c.Reader.ReadMultiBufferTimeout(time.Second)
	if err == io.EOF {
		c.release()
	}
	if err != nil {
		return nil, err
	}
	if mb.Len() > 0 {
		c.Counter.Add(int64(mb.Len()))
	}
	return mb, nil
}

func (c *CounterReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := c.Reader.ReadMultiBuffer()
	if err != nil {
		c.release()
	}
	if err != nil {
		return nil, err
	}
	if mb.Len() > 0 {
		c.Counter.Add(int64(mb.Len()))
	}
	return mb, nil
}

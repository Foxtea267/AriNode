package dispatcher

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/juju/ratelimit"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

var _ buf.TimeoutReader = (*CounterReader)(nil)

type CounterReader struct {
	Reader  buf.TimeoutReader
	Counter *atomic.Int64
	Add     func(int64)
	Source  buf.Reader
	Bucket  *ratelimit.Bucket
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
	if c.Source != nil {
		common.Interrupt(c.Source)
		return
	}
	common.Interrupt(c.Reader)
}

func (c *CounterReader) account(mb buf.MultiBuffer) {
	if n := int64(mb.Len()); n > 0 {
		if c.Bucket != nil {
			c.Bucket.Wait(n)
		}
		if c.Add != nil {
			c.Add(n)
		} else if c.Counter != nil {
			c.Counter.Add(n)
		}
	}
}

func (c *CounterReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	mb, err := c.Reader.ReadMultiBufferTimeout(timeout)
	if err == io.EOF {
		c.release()
	}
	c.account(mb)
	return mb, err
}

func (c *CounterReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := c.Reader.ReadMultiBuffer()
	if err != nil {
		c.release()
	}
	c.account(mb)
	return mb, err
}

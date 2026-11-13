package rate

import (
	"net"
	"sync"
	"time"

	"github.com/juju/ratelimit"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// PacketConn charges both directions to the existing user bucket. Embed only
// PacketConn: promoting cached reads, packet waiters or raw socket methods would
// let optimized copies bypass ReadPacket/WritePacket and their rate limit.
type PacketConn struct {
	N.PacketConn
	limiter   *ratelimit.Bucket
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

var _ N.PacketConn = (*PacketConn)(nil)

func NewPacketConnRateLimiter(conn N.PacketConn, bucket *ratelimit.Bucket) *PacketConn {
	return &PacketConn{PacketConn: conn, limiter: bucket, done: make(chan struct{})}
}

func (c *PacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	destination, err := c.PacketConn.ReadPacket(buffer)
	if err != nil {
		return destination, err
	}
	return destination, c.wait(buffer.Len())
}

func (c *PacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	// Wait before handing off the packet; the underlying writer owns and may
	// release the buffer. Never inspect or release it after that handoff.
	if err := c.wait(buffer.Len()); err != nil {
		buffer.Release()
		return err
	}
	return c.PacketConn.WritePacket(buffer, destination)
}

func (c *PacketConn) wait(size int) error {
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	// Take is the same reservation used by Bucket.Wait. Reservations cannot be
	// refunded safely on cancellation because TCP and other UDP sessions share
	// this bucket. Cancel only the wait, without changing its accounting.
	delay := c.limiter.Take(int64(size))
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-c.done:
		return net.ErrClosed
	case <-timer.C:
		return nil
	}
}

func (c *PacketConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		c.closeErr = c.PacketConn.Close()
	})
	return c.closeErr
}

// Keep handshake/NAT control interfaces discoverable, while keeping sing's
// data-copy optimizations on the rate-limited packet methods.
func (c *PacketConn) Upstream() any           { return c.PacketConn }
func (c *PacketConn) ReaderReplaceable() bool { return false }
func (c *PacketConn) WriterReplaceable() bool { return false }

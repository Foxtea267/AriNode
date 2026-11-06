package dispatcher

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/xtls/xray-core/common/buf"
)

type deviceTestIO struct{}

func (*deviceTestIO) WriteMultiBuffer(buf.MultiBuffer) error    { return nil }
func (*deviceTestIO) Close() error                              { return nil }
func (*deviceTestIO) ReadMultiBuffer() (buf.MultiBuffer, error) { return nil, io.EOF }
func (*deviceTestIO) ReadMultiBufferTimeout(time.Duration) (buf.MultiBuffer, error) {
	return nil, io.EOF
}

func TestDeviceLinkHalfCloseKeepsOtherDirectionOnline(t *testing.T) {
	limiter.Init()
	l := limiter.AddLimiter("device-link", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "user"}}, nil)
	defer limiter.DeleteLimiter("device-link")
	up := &deviceWriter{writer: &deviceTestIO{}, release: l.TrackDeviceIP("device-link|user", "192.0.2.1")}
	down := &deviceWriter{writer: &deviceTestIO{}, release: l.TrackDeviceIP("device-link|user", "192.0.2.1")}
	up.Close()
	up.Interrupt()
	for i := 0; i < 3; i++ {
		online, _ := l.GetOnlineDevice()
		if len(*online) != 1 {
			t.Fatal("upload half-close removed ongoing download")
		}
	}
	down.Interrupt()
	down.Close()
	online, _ := l.GetOnlineDevice()
	if len(*online) != 0 {
		t.Fatal("closed link leaked a device reference")
	}
}

func TestDeviceCounterReaderReleasesOnEOFAndInterrupt(t *testing.T) {
	var released atomic.Int64
	reader := &CounterReader{Reader: &deviceTestIO{}, Counter: &atomic.Int64{}, Release: func() { released.Add(1) }}
	reader.ReadMultiBufferTimeout(time.Second)
	reader.ReadMultiBuffer()
	reader.Interrupt()
	if released.Load() != 1 {
		t.Fatal("reader released a device multiple times")
	}
}

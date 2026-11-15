package counter_test

import (
	"bytes"
	"io"
	"sync"
	"testing"

	"github.com/Foxtea267/AriNode/common/counter"
	N "github.com/sagernet/sing/common/network"
)

func TestRetiredTrafficKeepsLateCopyCallbacksAndOriginalUIDTotals(t *testing.T) {
	storage, reported := &counter.TrafficStorage{}, &counter.TrafficStorage{}
	storage.AddUp(128)
	storage.AddDown(512)
	conn := counter.NewConnCounter(&countingTestConn{reader: bytes.NewReader([]byte("late"))}, storage)
	_, reads := conn.(interface {
		UnwrapReader() (io.Reader, []N.CountFunc)
	}).UnwrapReader()
	_, writes := conn.(interface {
		UnwrapWriter() (io.Writer, []N.CountFunc)
	}).UnwrapWriter()
	storage.RetireTo(reported)
	storage.RetireTo(reported) // repeated removal cannot replay already drained bytes
	if up, down := reported.Snapshot(true); up != 128 || down != 512 {
		t.Fatalf("first report=%d/%d want128/512", up, down)
	}
	// Optimized copies may finish their byte callbacks after Close/DelUsers.
	_ = conn.Close()
	reads[0](17)
	writes[0](29)
	if up, down := reported.Snapshot(true); up != 17 || down != 29 {
		t.Fatalf("late report=%d/%d want17/29", up, down)
	}
	if up, down := storage.Snapshot(true); up != 0 || down != 0 {
		t.Fatalf("retired source retained double-countable bytes: %d/%d", up, down)
	}
}

func TestRetiredTrafficConcurrentAddsAndReportsDoNotLoseBytes(t *testing.T) {
	const workers, rounds = 6, 2000
	source, destination := &counter.TrafficStorage{}, &counter.TrafficStorage{}
	start, done := make(chan struct{}), make(chan struct{})
	var writers sync.WaitGroup
	for range workers {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			for range rounds {
				source.AddUp(3)
				source.AddDown(7)
			}
		}()
	}
	result := make(chan [2]int64, 1)
	go func() {
		var total [2]int64
		for {
			up, down := destination.Snapshot(true)
			total[0] += up
			total[1] += down
			select {
			case <-done:
				up, down := destination.Snapshot(true)
				result <- [2]int64{total[0] + up, total[1] + down}
				return
			default:
			}
		}
	}()
	close(start)
	source.RetireTo(destination)
	writers.Wait()
	close(done)
	got := <-result
	if got != [2]int64{workers * rounds * 3, workers * rounds * 7} {
		t.Fatalf("concurrent retirement lost/replayed bytes: %v", got)
	}
}

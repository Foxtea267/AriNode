package counter_test

import (
	"runtime"
	"sync"
	"testing"

	"github.com/Foxtea267/AriNode/common/counter"
)

func TestTrafficSnapshotPreservesConcurrentAddsAcrossDrains(t *testing.T) {
	c := counter.NewTrafficCounter()
	if up, down := c.GetTraffic("absent", true); up != 0 || down != 0 || c.Len() != 0 {
		t.Fatalf("unknown user created traffic: %d/%d len=%d", up, down, c.Len())
	}
	s := c.GetCounter("user")
	other := c.GetCounter("other")
	other.UpCounter.Add(17)
	other.DownCounter.Add(29)
	const writers, collectors, rounds = 4, 3, 12000
	start := make(chan struct{})
	finished := make(chan struct{})
	var producers, drains sync.WaitGroup
	results := make(chan [2]int64, collectors)
	for range writers {
		producers.Add(1)
		go func() {
			defer producers.Done()
			<-start
			for i := 0; i < rounds; i++ {
				s.UpCounter.Add(3)
				s.DownCounter.Add(7)
				if i%37 == 0 {
					runtime.Gosched()
				}
			}
		}()
	}
	for range collectors {
		drains.Add(1)
		go func() {
			defer drains.Done()
			<-start
			var total [2]int64
			for {
				up, down := c.GetTraffic("user", true)
				total[0] += up
				total[1] += down
				select {
				case <-finished:
					results <- total
					return
				default:
					runtime.Gosched()
				}
			}
		}()
	}
	close(start)
	producers.Wait()
	close(finished)
	drains.Wait()
	close(results)
	up, down := c.GetTraffic("user", true)
	for result := range results {
		up += result[0]
		down += result[1]
	}
	if wantUp, wantDown := int64(writers*rounds*3), int64(writers*rounds*7); up != wantUp || down != wantDown {
		t.Fatalf("concurrent drain lost or duplicated traffic: %d/%d want=%d/%d", up, down, wantUp, wantDown)
	}
	if up, down := c.GetTraffic("user", true); up != 0 || down != 0 {
		t.Fatalf("second billing drain duplicated bytes: %d/%d", up, down)
	}
	if up, down := c.GetTraffic("other", false); up != 17 || down != 29 {
		t.Fatalf("another user's traffic changed: %d/%d", up, down)
	}
}

func TestTrafficSnapshotsAreNonDestructiveUntilDrained(t *testing.T) {
	s := &counter.TrafficStorage{}
	s.UpCounter.Add(128)
	s.DownCounter.Add(512)
	for range 2 {
		if up, down := s.Snapshot(false); up != 128 || down != 512 {
			t.Fatalf("snapshot changed traffic: %d/%d", up, down)
		}
	}
	if up, down := s.Snapshot(true); up != 128 || down != 512 {
		t.Fatalf("drain changed traffic: %d/%d", up, down)
	}
	s.UpCounter.Add(300)
	s.DownCounter.Add(1)
	if up, down := s.Snapshot(true); up != 300 || down != 1 {
		t.Fatalf("new bytes after drain lost: %d/%d", up, down)
	}
}

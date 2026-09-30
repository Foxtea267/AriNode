package task

import (
	"fmt"
	"log"
	"sync"
	"time"
)

type Task struct {
	Interval time.Duration
	Execute  func() error
	access   sync.Mutex
	running  bool
	stop     chan struct{}
	done     chan struct{}
}

func (t *Task) Start(first bool) error {
	t.access.Lock()
	if t.Interval <= 0 {
		t.access.Unlock()
		return fmt.Errorf("task interval must be positive")
	}
	if t.running {
		t.access.Unlock()
		return nil
	}
	t.running = true
	t.stop = make(chan struct{})
	t.done = make(chan struct{})
	stop, done := t.stop, t.done
	t.access.Unlock()

	go func() {
		defer close(done)
		if first {
			if err := t.executeSafely(); err != nil {
				t.access.Lock()
				if t.running && t.stop == stop {
					t.running = false
					close(stop)
				}
				t.access.Unlock()
				return
			}
		}

		for {
			t.access.Lock()
			interval := t.Interval
			t.access.Unlock()
			select {
			case <-time.After(interval):
			case <-stop:
				return
			}
			select {
			case <-stop:
				return
			default:
			}

			if err := t.executeSafely(); err != nil {
				t.access.Lock()
				if t.running && t.stop == stop {
					t.running = false
					close(stop)
				}
				t.access.Unlock()
				return
			}
		}
	}()

	return nil
}

func (t *Task) Close() {
	t.access.Lock()
	done := t.done
	if t.running {
		t.running = false
		close(t.stop)
	}
	t.access.Unlock()
	if done != nil {
		<-done
	}
}

func (t *Task) SetInterval(interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("task interval must be positive")
	}
	t.access.Lock()
	t.Interval = interval
	t.access.Unlock()
	return nil
}

func (t *Task) executeSafely() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("background task recovered from panic: %v", recovered)
			// Keep the scheduler alive so a transient node failure can recover.
			err = nil
		}
	}()
	return t.Execute()
}

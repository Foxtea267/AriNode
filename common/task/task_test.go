package task

import (
	"log"
	"sync/atomic"
	"testing"
	"time"
)

func TestTask(t *testing.T) {
	ts := Task{Execute: func() error {
		log.Println("q")
		return nil
	}, Interval: time.Second}
	ts.Start(false)
}

func TestPanicInTaskDoesNotStopScheduler(t *testing.T) {
	var calls atomic.Int32
	task := Task{Interval: 10 * time.Millisecond, Execute: func() error {
		if calls.Add(1) == 1 {
			panic("one node failed")
		}
		return nil
	}}
	if err := task.Start(true); err != nil {
		t.Fatal(err)
	}
	defer task.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task stopped after one panic")
}

func TestCloseWaitsForActiveExecution(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	closed := make(chan struct{})
	task := Task{Interval: time.Second, Execute: func() error { close(entered); <-release; return nil }}
	if err := task.Start(true); err != nil {
		t.Fatal(err)
	}
	<-entered
	go func() { task.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while task was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish")
	}
}

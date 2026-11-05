//go:build linux

package dsom

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPrivateFilesDoNotBlockOnFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-json-file")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 1)
	go func() {
		var value Endpoint
		if err := readPrivateJSON(path, &value); err == nil {
			t.Error("FIFO accepted as credentials/endpoint JSON")
		}
		if loadHistory(path) != nil {
			t.Error("FIFO accepted as history")
		}
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("configuration file read blocked graceful shutdown")
	}
}

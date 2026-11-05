package conf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchAtomicReplacementAndRapidSecondEdit(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	write := func(id int) {
		t.Helper()
		source := filepath.Join(directory, "next.json")
		data := fmt.Sprintf(`{"Nodes":[{"ApiHost":"http://127.0.0.1","ApiKey":"test","NodeID":%d,"NodeType":"vless"}]}`, id)
		if err := os.WriteFile(source, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(source, path); err != nil {
			t.Fatal(err)
		}
	}
	write(1)
	c := New()
	if err := c.LoadFromPath(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updated := make(chan int, 4)
	if err := c.WatchContext(ctx, path, "", "", func(next *Conf) error { updated <- next.NodeConfig[0].ApiConfig.NodeID; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{2, 3} {
		write(id)
		select {
		case got := <-updated:
			if got != id {
				t.Fatalf("got %d want %d", got, id)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("atomic replacement lost the file watch or a rapid edit")
		}
	}
}

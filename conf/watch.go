package conf

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

func (p *Conf) Watch(filePath, xDnsPath string, sDnsPath string, reload func(*Conf) error) error {
	return p.WatchContext(context.Background(), filePath, xDnsPath, sDnsPath, reload)
}

// Watch directories, not file inodes: editors and configuration managers commonly
// publish by rename. Debouncing retains the latest event instead of discarding
// all edits in a ten-second window.
func (p *Conf) WatchContext(ctx context.Context, filePath, xDnsPath, sDnsPath string, reload func(*Conf) error) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("new watcher error: %s", err)
	}
	targets, dirs := map[string]bool{}, map[string]bool{}
	for _, path := range []string{filePath, xDnsPath, sDnsPath} {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			watcher.Close()
			return err
		}
		targets[filepath.Clean(absolute)] = true
		dirs[filepath.Dir(absolute)] = true
	}
	for dir := range dirs {
		if err := watcher.Add(dir); err != nil {
			watcher.Close()
			return fmt.Errorf("watch directory error: %s", err)
		}
	}
	go func() {
		defer watcher.Close()
		var timer *time.Timer
		var tick <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-watcher.Events:
				if !ok {
					return
				}
				absolute, _ := filepath.Abs(e.Name)
				if !targets[filepath.Clean(absolute)] || e.Op == fsnotify.Chmod {
					continue
				}
				if timer == nil {
					timer = time.NewTimer(250 * time.Millisecond)
				} else {
					timer.Reset(250 * time.Millisecond)
				}
				tick = timer.C
			case <-tick:
				tick = nil
				if ctx.Err() != nil {
					return
				}
				next := New()
				if err := next.LoadFromPath(filePath); err != nil {
					log.Printf("reload config error: %s", err)
					continue
				}
				if err := reload(next); err != nil {
					log.Printf("reload rejected: %s", err)
					continue
				}
				*p = *next
				log.Println("reload config success")
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				if err != nil {
					log.Printf("File watcher error: %s", err)
				}
			}
		}
	}()
	return nil
}

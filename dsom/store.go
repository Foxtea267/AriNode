package dsom

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

func loadHistory(path string) []Change {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 131072 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 131073))
	if err != nil || len(data) > 131072 {
		return nil
	}
	var changes []Change
	if json.Unmarshal(data, &changes) != nil || len(changes) > 100 {
		return nil
	}
	for _, change := range changes {
		if change.At.IsZero() || len(change.IPs) > 2 || len(change.IPs) == 0 {
			return nil
		}
		for _, ip := range change.IPs {
			if _, ok := publicIP(ip); !ok {
				return nil
			}
		}
		if change.Via != "gateway" && change.Via != "direct" && change.Via != "fallback-1" && change.Via != "fallback-2" && change.Via != "fallback-3" && change.Via != "fallback-4" {
			return nil
		}
	}
	return changes
}

func saveHistory(path string, changes []Change) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".history-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

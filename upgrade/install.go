package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var binaryNames = []string{"arinode", "anctl"}

// ReplaceBinaries installs both executables from staging and keeps the old
// files until the optional service restart succeeds. On failure it restores
// the pair and attempts to restart the old service.
func ReplaceBinaries(staging, directory string, restart func() error) error {
	for _, name := range binaryNames {
		for _, path := range []string{filepath.Join(staging, name), filepath.Join(directory, name)} {
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file", path)
			}
		}
	}
	backupDir := filepath.Join(staging, "backup")
	if err := os.Mkdir(backupDir, 0700); err != nil {
		return err
	}
	installed := make([]string, 0, 2)
	backedUp := make([]string, 0, 2)
	rollback := func(cause error) error {
		var failures []error
		for i := len(installed) - 1; i >= 0; i-- {
			name := installed[i]
			if err := os.Rename(filepath.Join(directory, name), filepath.Join(staging, "failed-"+name)); err != nil {
				failures = append(failures, fmt.Errorf("remove failed %s: %w", name, err))
			}
		}
		for i := len(backedUp) - 1; i >= 0; i-- {
			name := backedUp[i]
			if err := os.Rename(filepath.Join(backupDir, name), filepath.Join(directory, name)); err != nil {
				failures = append(failures, fmt.Errorf("restore %s: %w", name, err))
			}
		}
		if restart != nil {
			if err := restart(); err != nil {
				failures = append(failures, fmt.Errorf("restart previous service: %w", err))
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("upgrade failed: %w; rollback files retained at %s", errors.Join(append([]error{cause}, failures...)...), staging)
		}
		return cause
	}
	for _, name := range binaryNames {
		if err := os.Rename(filepath.Join(directory, name), filepath.Join(backupDir, name)); err != nil {
			return rollback(fmt.Errorf("back up %s: %w", name, err))
		}
		backedUp = append(backedUp, name)
		if err := os.Rename(filepath.Join(staging, name), filepath.Join(directory, name)); err != nil {
			return rollback(fmt.Errorf("install %s: %w", name, err))
		}
		installed = append(installed, name)
	}
	if restart != nil {
		if err := restart(); err != nil {
			return rollback(fmt.Errorf("restart updated service: %w", err))
		}
	}
	return nil
}

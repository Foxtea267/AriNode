package node

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Foxtea267/AriNode/api/panel"
	log "github.com/sirupsen/logrus"
)

// Persist only successfully applied plugin snapshots. Never replace the local
// bootstrap config, which may contain other panels, cores and Komari bindings.
func (c *Controller) savePanelConfig(info *panel.NodeInfo) {
	if info == nil || !info.PanelManaged || len(info.RawConfig) == 0 {
		return
	}
	root := c.PanelConfigPath
	if root == "" {
		root = "/etc/arinode/panel-config"
	}
	namespace := fmt.Sprintf("%s|machine=%d|node=%d", c.apiClient.APIHost, c.apiClient.MachineID, info.Id)
	digest := sha256.Sum256([]byte(namespace))
	path := filepath.Join(root, fmt.Sprintf("%x.json", digest))
	changed, err := writePanelConfig(path, append(append([]byte(nil), info.RawConfig...), '\n'))
	if err != nil {
		log.WithError(err).WithField("node_id", info.Id).Warn("Node configuration applied but local snapshot could not be saved; will retry")
		return
	}
	if changed {
		log.WithFields(log.Fields{"node_id": info.Id, "config_hash": info.ConfigHash, "config_path": path}).Info("Panel configuration synchronized and applied")
	}
}

func writePanelConfig(path string, data []byte) (bool, error) {
	previous, err := os.ReadFile(path)
	if err == nil && bytes.Equal(previous, data) {
		return false, os.Chmod(path, 0600)
	}
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return false, err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return false, err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return false, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(name, path); err != nil {
		return false, err
	}
	return true, nil
}

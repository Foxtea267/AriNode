//go:build !linux

package cmd

import "github.com/Foxtea267/AriNode/conf"

func finishPanelRemoval(path string, removed conf.PanelSummary, legacy []conf.NodeConfig) error {
	return purgePanelFiles(path, removed, legacy)
}

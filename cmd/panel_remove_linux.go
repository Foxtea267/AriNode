package cmd

import (
	"fmt"

	"github.com/Foxtea267/AriNode/conf"
)

func finishPanelRemoval(path string, removed conf.PanelSummary, legacy []conf.NodeConfig) error {
	if path == defaultPanelConfig && systemctl("is-active", "--quiet", "arinode.service") == nil {
		fmt.Println("The running service will unload this binding and clear its downloaded files when it reloads the configuration.")
		return nil
	}
	return purgePanelFiles(path, removed, legacy)
}

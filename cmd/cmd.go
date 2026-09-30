package cmd

import (
	"os"

	log "github.com/sirupsen/logrus"

	_ "github.com/Foxtea267/ariNode/core/imports"
	"github.com/spf13/cobra"
)

var command = &cobra.Command{
	Use: "arinode",
}

func Run() {
	err := command.Execute()
	if err != nil {
		log.WithField("err", err).Error("Execute command failed")
		os.Exit(1)
	}
}

// RunAs keeps the service and management binaries on the same command tree.
func RunAs(name string) {
	command.Use = name
	Run()
}

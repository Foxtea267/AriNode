package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var (
	version  = "dev"
	codename = "AriNode"
	intro    = "Xboard and Komari integrated node service, based on V2bX"
)

var versionCommand = cobra.Command{
	Use:   "version",
	Short: "Print version info",
	Run:   func(_ *cobra.Command, _ []string) { showVersion() },
}

func init() { command.AddCommand(&versionCommand) }

func showVersion() { fmt.Printf("%s %s (%s)\n", codename, version, intro) }

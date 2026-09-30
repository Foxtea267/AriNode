package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func init() {
	for _, action := range []string{"start", "stop", "restart"} {
		action := action
		command.AddCommand(&cobra.Command{
			Use:   action,
			Short: fmt.Sprintf("%s the AriNode systemd service", action),
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				if err := systemctl(action, "arinode.service"); err != nil {
					return err
				}
				if action != "stop" {
					if err := systemctl("is-active", "--quiet", "arinode.service"); err != nil {
						return fmt.Errorf("arinode.service is not active: %w", err)
					}
				}
				fmt.Fprintf(os.Stdout, "arinode.service: %s complete\n", action)
				return nil
			},
		})
	}
	command.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show AriNode systemd service status",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			process := exec.Command("systemctl", "status", "arinode.service", "--no-pager")
			process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
			return process.Run()
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "log",
		Short: "Follow AriNode journal logs",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			process := exec.Command("journalctl", "-u", "arinode.service", "-e", "--no-pager", "-f")
			process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
			return process.Run()
		},
	})
}

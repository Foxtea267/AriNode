package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Foxtea267/AriNode/upgrade"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

const upgradeUnitDir = "/etc/systemd/system"
const upgradeTimerName = "arinode-upgrade.timer"
const upgradeServiceName = "arinode-upgrade.service"

var systemdPathPattern = regexp.MustCompile(`^/[A-Za-z0-9/_.-]+$`)

func init() {
	parent := &cobra.Command{
		Use:   "upgrade",
		Short: "Install the latest official stable AriNode release",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return installLatestRelease()
		},
	}
	parent.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Check the latest official stable release",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			release, _, err := upgrade.NewClient().Latest(ctx, runtime.GOARCH)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "installed: %s\nlatest stable: %s\nupgrade available: %t\n", version, release.Tag, upgrade.Newer(version, release.Tag))
			return nil
		},
	})
	auto := &cobra.Command{Use: "auto", Short: "Manage the optional daily systemd upgrade timer"}
	auto.AddCommand(&cobra.Command{Use: "enable", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error { return setAutoUpgrade(true) }})
	auto.AddCommand(&cobra.Command{Use: "disable", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error { return setAutoUpgrade(false) }})
	auto.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		output, err := exec.Command("systemctl", "is-enabled", upgradeTimerName).CombinedOutput()
		if err != nil {
			fmt.Fprintln(os.Stdout, "automatic upgrades: disabled")
			return nil
		}
		fmt.Fprintf(os.Stdout, "automatic upgrades: %s\n", strings.TrimSpace(string(output)))
		return nil
	}})
	parent.AddCommand(auto)
	command.AddCommand(parent)
}

func installLatestRelease() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("installing an upgrade requires root (use sudo anctl upgrade)")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	directory := filepath.Dir(resolved)
	lock, err := os.OpenFile(filepath.Join(directory, ".arinode-upgrade.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("another AriNode upgrade is running: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := upgrade.NewClient()
	release, asset, err := client.Latest(ctx, runtime.GOARCH)
	if err != nil {
		return err
	}
	if !upgrade.Newer(version, release.Tag) {
		fmt.Fprintf(os.Stdout, "AriNode %s is current; latest stable is %s\n", version, release.Tag)
		return nil
	}
	client.HTTP.Timeout = 5 * time.Minute
	archive, err := client.Download(ctx, asset, directory)
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	staging, err := upgrade.ExtractBinaries(archive, directory)
	if err != nil {
		return err
	}
	cleanStaging := false
	defer func() {
		if cleanStaging {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := verifyReleaseBinaries(ctx, staging, release.Tag); err != nil {
		return err
	}
	var restart func() error
	if systemctl("is-active", "--quiet", "arinode.service") == nil {
		restart = func() error {
			if err := systemctl("restart", "arinode.service"); err != nil {
				return err
			}
			return systemctl("is-active", "--quiet", "arinode.service")
		}
	}
	if err := upgrade.ReplaceBinaries(staging, directory, restart); err != nil {
		return err
	}
	cleanStaging = true
	fmt.Fprintf(os.Stdout, "AriNode upgraded to %s\n", release.Tag)
	return nil
}

func verifyReleaseBinaries(ctx context.Context, staging, tag string) error {
	for _, name := range []string{"arinode", "anctl"} {
		checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		output, err := exec.CommandContext(checkCtx, filepath.Join(staging, name), "version").CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("verify %s version: %w", name, err)
		}
		if !strings.HasPrefix(string(output), "AriNode "+tag+" ") {
			return fmt.Errorf("%s version does not match release %s", name, tag)
		}
	}
	return nil
}

func setAutoUpgrade(enable bool) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("changing automatic upgrades requires root")
	}
	servicePath := filepath.Join(upgradeUnitDir, upgradeServiceName)
	timerPath := filepath.Join(upgradeUnitDir, upgradeTimerName)
	if !enable {
		_ = systemctl("disable", "--now", upgradeTimerName)
		for _, path := range []string{timerPath, servicePath} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := systemctl("daemon-reload"); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "automatic upgrades: disabled")
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	if !systemdPathPattern.MatchString(executable) {
		return fmt.Errorf("anctl path cannot be used in a systemd unit: %s", executable)
	}
	service := fmt.Sprintf("[Unit]\nDescription=Upgrade AriNode to the latest stable release\nWants=network-online.target\nAfter=network-online.target\n\n[Service]\nType=oneshot\nExecStart=%s upgrade\n", executable)
	timer := "[Unit]\nDescription=Check for a stable AriNode upgrade daily\n\n[Timer]\nOnCalendar=daily\nPersistent=true\nRandomizedDelaySec=1h\nUnit=arinode-upgrade.service\n\n[Install]\nWantedBy=timers.target\n"
	if err := os.WriteFile(servicePath, []byte(service), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(timerPath, []byte(timer), 0644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", "--now", upgradeTimerName); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "automatic upgrades: enabled (daily stable releases)")
	return nil
}

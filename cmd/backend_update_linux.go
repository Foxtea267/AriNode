package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/upgrade"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const backendUpdateUnit = "arinode-panel-upgrade.service"
const defaultHealthListen = "127.0.0.1:18086"
const healthURL = "http://" + defaultHealthListen + "/healthz"
const panelUpgradeBinary = "/usr/local/bin/anctl"
const panelServerBinary = "/usr/local/bin/arinode"

func init() {
	command.AddCommand(&cobra.Command{
		Use: "panel-upgrade BINDING_ID JOB_ID", Hidden: true, Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPanelUpgrade(cmd.Context(), args[0], args[1])
		},
	})
}

func startBackendUpdates(cfg *conf.Conf, path, listen string) (func(*conf.Conf), func()) {
	noop := func(*conf.Conf) {}
	if os.Geteuid() != 0 || !upgrade.ValidVersion(version) || filepath.Clean(path) != defaultPanelConfig || listen != defaultHealthListen {
		return noop, func() {}
	}
	if !managedPanelService() {
		return noop, func() {}
	}
	agent, err := backendAgentID(filepath.Join(filepath.Dir(path), ".backend-agent-id"))
	if err != nil {
		log.WithError(err).Warn("Panel update identity unavailable")
		return noop, func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var machines atomic.Value
	reload := func(next *conf.Conf) { machines.Store(next.UpdateMachines()) }
	reload(cfg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			for _, binding := range machines.Load().([]conf.MachineBinding) {
				if ctx.Err() != nil {
					return
				}
				job, err := panel.BackendUpdate(ctx, binding, panel.BackendUpdateRequest{Action: "poll", Agent: agent, Version: version})
				if err != nil {
					if !errors.Is(err, panel.ErrUpdateJobUnavailable) {
						log.WithField("panel", binding.APIHost).WithError(err).Debug("Panel backend update poll failed")
					}
					continue
				}
				if job != "" {
					if err := launchPanelUpgrade(ctx, binding.ID(), job); err != nil {
						log.WithError(err).Warn("Could not start panel update service")
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return reload, func() { cancel(); <-done }
}

func managedPanelService() bool {
	path, err := os.Executable()
	if err != nil || path != panelServerBinary {
		return false
	}
	binary, err := os.Stat(panelUpgradeBinary)
	if err != nil || !binary.Mode().IsRegular() || binary.Mode().Perm()&0100 == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemctl", "show", "--property=MainPID", "--value", "arinode.service").Output()
	return err == nil && strings.TrimSpace(string(output)) == strconv.Itoa(os.Getpid())
}

func launchPanelUpgrade(ctx context.Context, binding, job string) error {
	if !panel.UpdateJobID.MatchString(job) {
		return fmt.Errorf("invalid backend update job")
	}
	// The transient unit is outside AriNode's cgroup, so it survives a restart.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemd-run", "--quiet", "--no-block", "--collect",
		"--unit="+backendUpdateUnit, "--property=Type=oneshot", "--property=TimeoutStartSec=12min",
		panelUpgradeBinary, "panel-upgrade", binding, job).Run()
}

func backendAgentID(path string) (string, error) {
	read := func() (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		id := strings.TrimSpace(string(data))
		if !panel.UpdateJobID.MatchString(id) {
			return "", fmt.Errorf("invalid backend update identity")
		}
		return id, nil
	}
	if id, err := read(); !os.IsNotExist(err) {
		return id, err
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(key[:])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return read()
	}
	if err != nil {
		return "", err
	}
	_, err = file.WriteString(id + "\n")
	if err := errors.Join(err, file.Close()); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return id, nil
}

func runPanelUpgrade(ctx context.Context, bindingID, job string) error {
	if os.Geteuid() != 0 || !panel.UpdateJobID.MatchString(job) {
		return fmt.Errorf("invalid panel upgrade invocation")
	}
	cfg, err := loadPanelConfig(defaultPanelConfig)
	if err != nil {
		return err
	}
	agent, err := backendAgentID(filepath.Join(filepath.Dir(defaultPanelConfig), ".backend-agent-id"))
	if err != nil {
		return err
	}
	for _, binding := range cfg.UpdateMachines() {
		if binding.ID() == bindingID {
			return executePanelUpgrade(ctx, agent, job, func(request panel.BackendUpdateRequest) error {
				_, err := panel.BackendUpdate(ctx, binding, request)
				return err
			}, performPanelUpgrade)
		}
	}
	return fmt.Errorf("panel update binding no longer exists")
}

func executePanelUpgrade(ctx context.Context, agent, job string, send func(panel.BackendUpdateRequest) error, install func() (string, error)) error {
	request := panel.BackendUpdateRequest{Action: "claim", Agent: agent, Version: version, Job: job}
	if err := send(request); err != nil {
		if errors.Is(err, panel.ErrUpdateJobUnavailable) {
			return nil
		}
		return err
	}
	installed, installErr := install()
	request.Action = "finish"
	request.Result = "current"
	switch {
	case installErr != nil:
		request.Result = "failed"
	case installed != version:
		request.Result, request.Version = "succeeded", installed
	}
	var reportErr error
	for i := 0; i < 3; i++ {
		reportErr = send(request)
		if reportErr == nil || errors.Is(reportErr, panel.ErrUpdateJobUnavailable) || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	return errors.Join(installErr, reportErr)
}

func performPanelUpgrade() (string, error) {
	tag, err := readHealthyVersion()
	if err != nil {
		return "", fmt.Errorf("AriNode health check is unavailable: %w", err)
	}
	if tag != version {
		return "", fmt.Errorf("running AriNode version differs from anctl")
	}
	return installStableRelease(waitForPanelUpgradeHealth)
}

func readHealthyVersion() (string, error) {
	client := http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DisableKeepAlives: true},
	}
	res, err := client.Get(healthURL)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	tag := res.Header.Get("X-AriNode-Version")
	if res.StatusCode != http.StatusOK || !upgrade.ValidVersion(tag) {
		return "", fmt.Errorf("AriNode health endpoint is not ready")
	}
	return tag, nil
}

func waitForPanelUpgradeHealth() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, panelServerBinary, "version").Output()
	parts := strings.Fields(string(output))
	if err != nil || len(parts) < 2 || !upgrade.ValidVersion(parts[1]) {
		return fmt.Errorf("cannot read installed AriNode version")
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if tag, err := readHealthyVersion(); err == nil && tag == parts[1] {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("AriNode %s did not become healthy within 60 seconds", parts[1])
}

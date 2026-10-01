package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Foxtea267/AriNode/migrate"
	"github.com/spf13/cobra"
)

func init() {
	parent := &cobra.Command{Use: "migrate", Short: "Convert configurations between AriNode and Xboard-Node"}
	parent.AddCommand(newMigrationCommand("from-xbnode", "/etc/xboard-node/config.yml", "/etc/arinode/config.json", true))
	parent.AddCommand(newMigrationCommand("to-xbnode", "/etc/arinode/config.json", "/etc/xboard-node/config.yml", false))
	command.AddCommand(parent)
}

func newMigrationCommand(name, defaultInput, defaultOutput string, from bool) *cobra.Command {
	var input, output, nodeType string
	var force, dryRun, offline, switchService bool
	cmd := &cobra.Command{
		Use:   name,
		Short: map[bool]string{true: "Migrate Xboard-Node configuration to AriNode", false: "Migrate AriNode configuration to Xboard-Node"}[from],
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMigration(input, output, defaultOutput, nodeType, force, dryRun, offline, switchService, from)
		},
	}
	cmd.Flags().StringVar(&input, "input", defaultInput, "source configuration")
	cmd.Flags().StringVar(&output, "output", defaultOutput, "destination configuration")
	cmd.Flags().BoolVar(&force, "force", false, "replace existing destination after backing it up")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show result without writing secrets")
	cmd.Flags().BoolVar(&switchService, "switch", false, "switch Linux systemd services after writing config")
	if from {
		cmd.Flags().BoolVar(&offline, "offline", false, "do not query Xboard for missing node types")
		cmd.Flags().StringVar(&nodeType, "node-type", "", "fallback node type for an Xboard-Node config without node_type")
	}
	return cmd
}

func runMigration(input, output, defaultOutput, nodeType string, force, dryRun, offline, switchService, from bool) error {
	inputAbs, err := filepath.Abs(input)
	if err != nil {
		return err
	}
	outputAbs, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if inputAbs == outputAbs {
		return fmt.Errorf("input and output must be different files")
	}
	if switchService {
		if runtime.GOOS != "linux" {
			return fmt.Errorf("--switch requires Linux systemd")
		}
		if output != defaultOutput {
			return fmt.Errorf("--switch requires the service config path %s", defaultOutput)
		}
		if !dryRun {
			target := "xboard-node.service"
			if from {
				target = "arinode.service"
			}
			if err := systemctl("cat", target); err != nil {
				return fmt.Errorf("target service is not installed: %w", err)
			}
		}
	}
	source, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("read %s: %w", input, err)
	}
	if from {
		restore, err := loadXBNodeCredentials(filepath.Join(filepath.Dir(inputAbs), "credentials.env"))
		if err != nil {
			return err
		}
		defer restore()
	}
	var result migrate.Result
	if from {
		result, err = migrate.FromXBNode(source, migrate.Options{NodeType: nodeType, Offline: offline})
	} else {
		result, err = migrate.ToXBNode(source)
	}
	if err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		fmt.Fprintln(os.Stderr, "WARNING:", warning)
	}
	if dryRun {
		fmt.Fprintf(os.Stdout, "Preview: %d node(s), output %s (%d bytes); no file written\n", result.Nodes, output, len(result.Data))
		return nil
	}
	backup, err := writeMigrated(output, result.Data, force)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Migrated %d node(s) to %s\n", result.Nodes, output)
	if backup != "" {
		fmt.Fprintf(os.Stdout, "Previous config backed up to %s\n", backup)
	}
	if switchService {
		sourceService, targetService := "arinode.service", "xboard-node.service"
		if from {
			sourceService, targetService = targetService, sourceService
		}
		if err := switchSystemd(sourceService, targetService); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Switched service from %s to %s\n", sourceService, targetService)
	}
	return nil
}

var credentialKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// loadXBNodeCredentials reads systemd EnvironmentFile assignments as literal
// values. It never executes shell syntax and restores the process environment.
func loadXBNodeCredentials(path string) (func(), error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Xboard-Node credentials %s: %w", path, err)
	}
	type previous struct {
		value   string
		present bool
	}
	before := make(map[string]previous)
	restore := func() {
		for key, old := range before {
			if old.present {
				_ = os.Setenv(key, old.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !credentialKey.MatchString(key) {
			restore()
			return nil, fmt.Errorf("invalid assignment in Xboard-Node credentials %s", path)
		}
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := before[key]; !exists {
			old, present := os.LookupEnv(key)
			before[key] = previous{old, present}
		}
		if err := os.Setenv(key, value); err != nil {
			restore()
			return nil, err
		}
	}
	return restore, nil
}

var systemctl = func(args ...string) error {
	output, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func switchSystemd(source, target string) error {
	if err := systemctl("stop", source); err != nil {
		return fmt.Errorf("stop %s: %w", source, err)
	}
	if err := systemctl("restart", target); err != nil {
		rollback := systemctl("start", source)
		if rollback != nil {
			return fmt.Errorf("restart %s: %w; rollback start %s also failed: %v", target, err, source, rollback)
		}
		return fmt.Errorf("restart %s: %w; %s restarted", target, err, source)
	}
	if err := systemctl("enable", target); err != nil {
		return fmt.Errorf("%s is running, but enable failed: %w", target, err)
	}
	if err := systemctl("disable", source); err != nil {
		return fmt.Errorf("%s is running, but disable %s failed: %w", target, source, err)
	}
	return nil
}

func writeMigrated(output string, data []byte, force bool) (string, error) {
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return "", err
	}
	backup := ""
	if previous, err := os.ReadFile(output); err == nil {
		if !force {
			return "", fmt.Errorf("%s already exists; use --force to back it up and replace", output)
		}
		backup = fmt.Sprintf("%s.bak-%s", output, time.Now().UTC().Format("20060102T150405.000000000Z"))
		if err := os.WriteFile(backup, previous, 0600); err != nil {
			return "", fmt.Errorf("write backup: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if force {
		tmp, err := os.CreateTemp(filepath.Dir(output), ".arinode-migrate-*")
		if err != nil {
			return backup, err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if err := tmp.Chmod(0600); err != nil {
			tmp.Close()
			return backup, err
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return backup, err
		}
		if err := tmp.Close(); err != nil {
			return backup, err
		}
		if err := os.Rename(tmpName, output); err != nil {
			return backup, fmt.Errorf("replace %s: %w", output, err)
		}
		return backup, nil
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return backup, err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return backup, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return backup, err
	}
	if err := f.Close(); err != nil {
		return backup, err
	}
	return backup, nil
}

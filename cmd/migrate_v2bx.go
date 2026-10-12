package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/migrate"
	"github.com/spf13/cobra"
)

const v2bxConfig = "/etc/V2bX/config.json"

type v2bxLayout struct {
	Config, Program, Unit, Dropins, Wrapper, Alias string
	AriConfig, AriUnit, AriBinary, AriCTL, Backups string
}

var v2bxPaths = v2bxLayout{v2bxConfig, "/usr/local/V2bX", "/etc/systemd/system/V2bX.service", "/etc/systemd/system/V2bX.service.d", "/usr/bin/V2bX", "/usr/bin/v2bx", "/etc/arinode/config.json", "/etc/systemd/system/arinode.service", "/usr/local/bin/arinode", "/usr/local/bin/anctl", "/var/lib/arinode/migrations/v2bx"}

type v2bxSnapshot struct {
	Version      int      `json:"version"`
	Arch         string   `json:"arch"`
	BinarySHA256 string   `json:"binary_sha256"`
	Entries      []string `json:"entries"`
}
type v2bxMigrationOptions struct {
	Input, Output, Domain, Member, StatusURL     string
	From, ConfigOnly, DryRun, Force, DropCluster bool
}

func newV2BXMigrationCommand(from bool) *cobra.Command {
	opts := v2bxMigrationOptions{From: from}
	name, input, output := "to-v2bx", v2bxPaths.AriConfig, v2bxPaths.Config
	if from {
		name, input, output = "from-v2bx", output, input
	}
	c := &cobra.Command{Use: name, Short: "Migrate, verify and uninstall the source service; keep a recovery snapshot", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if !opts.ConfigOnly && !opts.DryRun && (runtime.GOOS != "linux" || os.Geteuid() != 0) {
				return fmt.Errorf("full migration requires root and Linux systemd; use --config-only for conversion")
			}
			return migrateV2BXInstallation(v2bxPaths, opts)
		}}
	c.Flags().StringVar(&opts.Input, "input", input, "source configuration")
	c.Flags().StringVar(&opts.Output, "output", output, "destination configuration")
	c.Flags().BoolVar(&opts.ConfigOnly, "config-only", false, "convert only; do not switch, uninstall or copy installation assets")
	c.Flags().BoolVar(&opts.DryRun, "dry-run", false, "preview without writing files or stopping services")
	c.Flags().BoolVar(&opts.Force, "force", true, "back up and replace existing destination config")
	c.Flags().StringVar(&opts.StatusURL, "status-url", "http://127.0.0.1:18086/v1/status", "local AriNode status URL for migration health checks")
	if from {
		c.Flags().StringVar(&opts.Domain, "cluster-domain", "", "enable shared-node reporting for migrated nodes (requires XBoard plugin)")
		c.Flags().StringVar(&opts.Member, "cluster-member", "", "unique server ID for shared-node reporting")
	} else {
		c.Flags().BoolVar(&opts.DropCluster, "drop-cluster", false, "explicitly remove unsupported Cluster reporting when returning to V2bX")
	}
	return c
}
func v2bxComponents(l v2bxLayout) map[string]string {
	return map[string]string{"config": filepath.Dir(l.Config), "program": l.Program, "unit": l.Unit, "dropins": l.Dropins, "wrapper": l.Wrapper, "alias": l.Alias}
}
func copyMigrationAsset(source, target string, preserveLink bool) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if preserveLink {
			link, err := os.Readlink(source)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		info, err = os.Stat(source)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("symlinked installation directories are not supported: %s", source)
		}
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, 0700); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyMigrationAsset(filepath.Join(source, e.Name()), filepath.Join(target, e.Name()), preserveLink); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported installation file: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := os.FileMode(0600)
	if info.Mode().Perm()&0111 != 0 {
		mode = 0700
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func migrationBinaryHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func saveV2BXSnapshot(l v2bxLayout) (string, error) {
	if err := os.MkdirAll(l.Backups, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(l.Backups, 0700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(l.Backups, "snapshot-")
	if err != nil {
		return "", err
	}
	manifest := v2bxSnapshot{Version: 1, Arch: runtime.GOARCH}
	for name, source := range v2bxComponents(l) {
		if _, err := os.Lstat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", err
		}
		if err := copyMigrationAsset(source, filepath.Join(dir, name), name == "wrapper" || name == "alias"); err != nil {
			return "", err
		}
		manifest.Entries = append(manifest.Entries, name)
	}
	manifest.BinarySHA256, err = migrationBinaryHash(filepath.Join(dir, "program", "V2bX"))
	if err != nil {
		return "", fmt.Errorf("standard V2bX executable is required for recoverable uninstall: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "unit")); err != nil {
		return "", fmt.Errorf("standard V2bX systemd unit is required: %w", err)
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
		return "", err
	}
	return dir, nil
}
func loadV2BXSnapshot(l v2bxLayout) (string, v2bxSnapshot, error) {
	var manifest v2bxSnapshot
	name, err := os.ReadFile(filepath.Join(l.Backups, "latest"))
	if err != nil {
		return "", manifest, fmt.Errorf("no saved V2bX installation; migrate from V2bX first, or use --config-only after installing V2bX")
	}
	base := strings.TrimSpace(string(name))
	if filepath.Base(base) != base || !strings.HasPrefix(base, "snapshot-") {
		return "", manifest, fmt.Errorf("invalid V2bX snapshot pointer")
	}
	dir := filepath.Join(l.Backups, base)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Version != 1 || manifest.Arch != runtime.GOARCH {
		return "", manifest, fmt.Errorf("invalid or incompatible V2bX snapshot")
	}
	actual, err := migrationBinaryHash(filepath.Join(dir, "program", "V2bX"))
	if err != nil || actual != manifest.BinarySHA256 {
		return "", manifest, fmt.Errorf("saved V2bX executable checksum mismatch")
	}
	return dir, manifest, nil
}
func restoreV2BXSnapshot(l v2bxLayout, dir string, manifest v2bxSnapshot) (resultErr error) {
	components := v2bxComponents(l)
	seen := map[string]bool{}
	for _, name := range manifest.Entries {
		target, ok := components[name]
		if !ok || seen[name] {
			return fmt.Errorf("invalid snapshot component")
		}
		seen[name] = true
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("V2bX component already exists: %s; restore it separately or use --config-only", target)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, name := range []string{"config", "program", "unit"} {
		if !seen[name] {
			return fmt.Errorf("incomplete recovery snapshot")
		}
	}
	var created []string
	defer func() {
		if resultErr != nil {
			for _, name := range created {
				_ = os.RemoveAll(components[name])
			}
			_ = systemctl("daemon-reload")
		}
	}()
	for _, name := range manifest.Entries {
		created = append(created, name)
		if err := copyMigrationAsset(filepath.Join(dir, name), components[name], name == "wrapper" || name == "alias"); err != nil {
			return err
		}
	}
	return systemctl("daemon-reload")
}
func uninstallV2BX(l v2bxLayout) error {
	// These paths come from the fixed installation layout, never from panel JSON,
	// a shell uninstall script, an Include value or a snapshot manifest.
	for _, p := range v2bxComponents(l) {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return systemctl("daemon-reload")
}
func restoreMigrationConfig(path string, previous []byte, existed bool) error {
	if !existed {
		return os.Remove(path)
	}
	_, err := writeMigrated(path, previous, true)
	return err
}

var waitAriMigrationHealth = func(address string, expected int) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return fmt.Errorf("migration health URL must use local loopback HTTP")
	}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest("GET", address, nil)
		if token := os.Getenv("ARINODE_STATUS_TOKEN"); token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err == nil {
			var status struct {
				Service string `json:"service"`
				Nodes   []struct {
					State string `json:"state"`
				} `json:"nodes"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&status)
			response.Body.Close()
			ready := decodeErr == nil && response.StatusCode == 200 && status.Service == "AriNode" && len(status.Nodes) >= expected
			for _, n := range status.Nodes {
				ready = ready && n.State == "running"
			}
			if ready {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("AriNode nodes did not become healthy; V2bX will be retained and restarted")
}
var waitV2BXMigrationHealth = func() error {
	for i := 0; i < 5; i++ {
		time.Sleep(time.Second)
		if err := systemctl("is-active", "--quiet", "V2bX.service"); err != nil {
			return err
		}
	}
	return nil
}

func migrateV2BXInstallation(l v2bxLayout, opts v2bxMigrationOptions) error {
	source, err := os.ReadFile(opts.Input)
	if err != nil {
		return err
	}
	input, err := filepath.Abs(opts.Input)
	if err != nil {
		return err
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return err
	}
	if input == output {
		return fmt.Errorf("input and output must differ")
	}
	var cluster *conf.ClusterConfig
	if opts.Domain != "" || opts.Member != "" {
		cluster = &conf.ClusterConfig{Domain: opts.Domain, MemberID: opts.Member}
	}
	convertOpts := migrate.V2BXOptions{SourceDir: filepath.Dir(input), Cluster: cluster, DropCluster: opts.DropCluster}
	if !opts.ConfigOnly && opts.From {
		convertOpts.WorkingDir = l.Program
	}
	var result migrate.Result
	if opts.From {
		result, err = migrate.FromV2BX(source, convertOpts)
	} else {
		result, err = migrate.ToV2BX(source, convertOpts)
	}
	if err != nil {
		return err
	}
	for _, w := range result.Warnings {
		fmt.Fprintln(os.Stderr, "WARNING:", w)
	}
	if opts.DryRun {
		fmt.Printf("Preview: %d node(s); full migration switches and uninstalls the source after health checks; no changes made\n", result.Nodes)
		return nil
	}
	if opts.ConfigOnly {
		_, err = writeMigrated(opts.Output, result.Data, opts.Force)
		return err
	}
	expectedInput, expectedOutput := l.AriConfig, l.Config
	if opts.From {
		expectedInput, expectedOutput = expectedOutput, expectedInput
	}
	if filepath.Clean(input) != filepath.Clean(expectedInput) || filepath.Clean(output) != filepath.Clean(expectedOutput) {
		return fmt.Errorf("full migration requires the standard service config paths; use --config-only for custom paths")
	}
	sourceService, targetService := "arinode.service", "V2bX.service"
	if opts.From {
		sourceService, targetService = targetService, sourceService
	}
	sourceEnabled := systemctl("is-enabled", "--quiet", sourceService) == nil
	targetEnabled := systemctl("is-enabled", "--quiet", targetService) == nil
	targetActive := systemctl("is-active", "--quiet", targetService) == nil
	previous, readErr := os.ReadFile(output)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	existed := readErr == nil
	if existed && !opts.Force {
		return fmt.Errorf("destination exists; use --force")
	}
	var snapshot string
	restored := false
	committed := false
	defer func() {
		if restored && !committed {
			_ = uninstallV2BX(l)
		}
	}()
	if opts.From {
		if err = systemctl("cat", "arinode.service"); err != nil {
			return fmt.Errorf("install AriNode first, or use install.sh --migrate-v2bx")
		}
		snapshot, err = saveV2BXSnapshot(l)
		if err != nil {
			return err
		}
		assets := filepath.Join(filepath.Dir(l.AriConfig), "v2bx-assets", filepath.Base(snapshot))
		if err = copyMigrationAsset(filepath.Join(snapshot, "config"), assets, false); err != nil {
			return err
		}
		if err = copyMigrationAsset(filepath.Join(snapshot, "program"), filepath.Join(assets, "program"), false); err != nil {
			return err
		}
		result.Data, err = migrate.RebasePaths(result.Data, filepath.Dir(l.Config), assets)
		if err != nil {
			return err
		}
		result.Data, err = migrate.RebasePaths(result.Data, l.Program, filepath.Join(assets, "program"))
		if err != nil {
			return err
		}
		if err = rebaseMigrationJSON(assets, filepath.Dir(l.Config), assets); err != nil {
			return err
		}
		if err = rebaseMigrationJSON(assets, l.Program, filepath.Join(assets, "program")); err != nil {
			return err
		}
	} else {
		var manifest v2bxSnapshot
		snapshot, manifest, err = loadV2BXSnapshot(l)
		if err != nil {
			return err
		}
		if err = restoreV2BXSnapshot(l, snapshot, manifest); err != nil {
			return err
		}
		restored = true
		assets := filepath.Join(filepath.Dir(l.Config), "arinode-import", time.Now().UTC().Format("20060102T150405.000000000Z"))
		if err = copyMigrationAsset(filepath.Dir(l.AriConfig), assets, false); err != nil {
			return err
		}
		result.Data, err = migrate.RebasePaths(result.Data, filepath.Dir(l.AriConfig), assets)
		if err != nil {
			return err
		}
		if err = rebaseMigrationJSON(assets, filepath.Dir(l.AriConfig), assets); err != nil {
			return err
		}
	}
	if _, err = writeMigrated(output, result.Data, true); err != nil {
		return err
	}
	if opts.From {
		parsed := conf.New()
		if err = parsed.LoadFromPath(output); err != nil {
			_ = restoreMigrationConfig(output, previous, existed)
			return fmt.Errorf("migrated config validation failed: %w", err)
		}
	}
	if err = systemctl("stop", sourceService); err != nil {
		_ = restoreMigrationConfig(output, previous, existed)
		return err
	}
	rollback := func(cause error) error {
		_ = systemctl("stop", targetService)
		restoreErr := restoreMigrationConfig(output, previous, existed)
		startErr := systemctl("start", sourceService)
		if sourceEnabled {
			_ = systemctl("enable", sourceService)
		}
		if !targetEnabled {
			_ = systemctl("disable", targetService)
		}
		if targetActive && existed {
			_ = systemctl("restart", targetService)
		}
		return fmt.Errorf("migration failed: %w; source restart=%v, config restore=%v; snapshot=%s", cause, startErr, restoreErr, snapshot)
	}
	if err = systemctl("restart", targetService); err != nil {
		return rollback(err)
	}
	if opts.From {
		err = waitAriMigrationHealth(opts.StatusURL, result.Nodes)
	} else {
		err = waitV2BXMigrationHealth()
	}
	if err != nil {
		return rollback(err)
	}
	if err = systemctl("enable", targetService); err != nil {
		return rollback(err)
	}
	if err = systemctl("disable", sourceService); err != nil {
		return rollback(err)
	}
	committed = true
	if opts.From {
		if err = os.WriteFile(filepath.Join(l.Backups, "latest"), []byte(filepath.Base(snapshot)+"\n"), 0600); err != nil {
			return rollback(err)
		}
		if err = uninstallV2BX(l); err != nil {
			return fmt.Errorf("AriNode is healthy; V2bX cleanup failed: %w; recovery snapshot=%s", err, snapshot)
		}
	} else {
		// Save AriNode configuration and executables before uninstalling its runtime.
		ariBackup := filepath.Join(l.Backups, "arinode-return-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
		ariFiles := map[string]string{"config": filepath.Dir(l.AriConfig), "arinode": l.AriBinary, "anctl": l.AriCTL, "unit": l.AriUnit, "dropins": l.AriUnit + ".d", "upgrade-timer": filepath.Join(filepath.Dir(l.AriUnit), "arinode-upgrade.timer"), "upgrade-service": filepath.Join(filepath.Dir(l.AriUnit), "arinode-upgrade.service")}
		for name, path := range ariFiles {
			if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
				continue
			} else if statErr != nil {
				return statErr
			}
			if err = copyMigrationAsset(path, filepath.Join(ariBackup, name), false); err != nil {
				return fmt.Errorf("V2bX is healthy; AriNode retained because backup failed: %w", err)
			}
		}
		if err = systemctl("cat", "arinode-upgrade.timer"); err == nil {
			if err = systemctl("disable", "--now", "arinode-upgrade.timer"); err != nil {
				return err
			}
		}
		for _, path := range ariFiles {
			if err = os.RemoveAll(path); err != nil {
				return err
			}
		}
		if err = systemctl("daemon-reload"); err != nil {
			return err
		}
	}
	fmt.Printf("Migrated %d node(s); %s verified, %s uninstalled. Recovery snapshot: %s\n", result.Nodes, targetService, sourceService, snapshot)
	return nil
}
func rebaseMigrationJSON(dir, from, to string) error {
	return filepath.WalkDir(dir, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.ToLower(filepath.Ext(file)) != ".json" {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		encoded, err := migrate.RebasePaths(data, from, to)
		if err != nil {
			return fmt.Errorf("cannot preserve native JSON asset %s: %w", filepath.Base(file), err)
		}
		return os.WriteFile(file, encoded, 0600)
	})
}

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/json5"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/node"
	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
)

const defaultPanelConfig = "/etc/arinode/config.json"

func init() {
	var path string
	parent := &cobra.Command{Use: "panel", Short: "Manage XBoard panel bindings"}
	parent.PersistentFlags().StringVar(&path, "config", defaultPanelConfig, "AriNode configuration file")
	parent.AddCommand(
		&cobra.Command{Use: "list", Short: "Show panel addresses and binding IDs", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, err := loadPanelConfig(path)
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "ID\tPANEL\tMACHINE\tNODES")
				for _, p := range cfg.BindingPanels() {
					fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", p.ID, p.Host, p.MachineID, p.Mode)
				}
				return w.Flush()
			}},
		&cobra.Command{Use: "enroll URL [CODE]", Short: "Register a machine and pull bindings from XBoard", Args: cobra.RangeArgs(1, 2),
			RunE: func(cmd *cobra.Command, args []string) error {
				code := os.Getenv("ARINODE_ENROLL_CODE")
				if len(args) == 2 {
					code = args[1]
				}
				host, err := conf.PanelURL(args[0])
				if err != nil {
					return err
				}
				if len(code) == 0 || len(code) > 1024 {
					return fmt.Errorf("an enrollment code from the XBoard plugin is required")
				}
				binding, err := enrollPanel(cmd.Context(), path, host, code)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Registered %s (machine %d, binding %s)\n", binding.APIHost, binding.MachineID, binding.ID())
				return nil
			}},
		&cobra.Command{Use: "remove ID|URL", Short: "Disconnect one panel binding and clear its downloaded files", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				removed, legacy, err := removePanel(path, args[0])
				if err != nil {
					return err
				}
				if err := finishPanelRemoval(path, removed, legacy); err != nil {
					return fmt.Errorf("binding removed from configuration: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Removed %s (machine %d, binding %s)\n", removed.Host, removed.MachineID, removed.ID)
				return nil
			}},
	)
	command.AddCommand(parent)
}

func loadPanelConfig(path string) (*conf.Conf, error) {
	cfg := conf.New()
	if err := cfg.LoadFromPath(path); err != nil {
		return nil, fmt.Errorf("could not load AriNode configuration at %s", path)
	}
	return cfg, nil
}

func enrollPanel(ctx context.Context, path, host, code string) (conf.MachineBinding, error) {
	ticket, err := panel.Enroll(ctx, host, code)
	if err != nil {
		return conf.MachineBinding{}, err
	}
	binding := ticket.Binding(host)
	a := conf.ApiConfig{APIHost: host, Key: binding.APIKey, MachineID: binding.MachineID, Timeout: 5}
	client, err := panel.New(&a)
	if err != nil {
		return binding, err
	}
	if _, err := client.GetMachineNodes(); err != nil {
		return binding, err
	}
	err = editPanelConfig(path, true, func(raw map[string]json.RawMessage, cfg *conf.Conf) error {
		if !*ticket.MultiPanel {
			for _, existing := range cfg.BindingPanels() {
				if existing.ID != binding.ID() {
					return fmt.Errorf("another binding already exists; enable multi-panel mode in XBoard to add this machine")
				}
			}
		}
		machines := append([]conf.MachineBinding(nil), cfg.Machines...)
		for i, existing := range machines {
			if existing.ID() == binding.ID() {
				machines[i] = binding
				return setConfigField(raw, "Machines", machines)
			}
		}
		return setConfigField(raw, "Machines", append(machines, binding))
	})
	return binding, err
}

func removePanel(path, selector string) (conf.PanelSummary, []conf.NodeConfig, error) {
	var removed conf.PanelSummary
	var legacy []conf.NodeConfig
	err := editPanelConfig(path, false, func(raw map[string]json.RawMessage, cfg *conf.Conf) error {
		host, _ := conf.PanelURL(selector)
		for _, existing := range cfg.BindingPanels() {
			if existing.ID == selector || existing.Host == host {
				if removed.ID != "" {
					return fmt.Errorf("panel address has multiple machines; specify the binding ID from anctl panel list")
				}
				removed = existing
			}
		}
		if removed.ID == "" {
			return fmt.Errorf("panel binding not found; run anctl panel list")
		}
		var machines []conf.MachineBinding
		for _, m := range cfg.Machines {
			if m.ID() != removed.ID {
				machines = append(machines, m)
			}
		}
		if err := setConfigField(raw, "Machines", machines); err != nil {
			return err
		}
		var nodes []json.RawMessage
		if value := raw["Nodes"]; len(value) > 0 {
			if err := json.Unmarshal(value, &nodes); err != nil || len(nodes) != len(cfg.NodeConfig)-len(cfg.Machines) {
				return fmt.Errorf("cannot identify local node entries for removal")
			}
		}
		remaining := make([]json.RawMessage, 0, len(nodes))
		used := make(map[string]bool)
		removedNames := make(map[string]bool)
		for i, entry := range nodes {
			configured := cfg.NodeConfig[i]
			a := configured.ApiConfig
			if conf.PanelBindingID(a.APIHost, a.MachineID) == removed.ID {
				legacy = append(legacy, configured)
				removedNames[configured.Panel] = true
			} else {
				remaining = append(remaining, entry)
				used[configured.Panel] = true
			}
		}
		if err := setConfigField(raw, "Nodes", remaining); err != nil {
			return err
		}
		var panels []json.RawMessage
		if value := raw["Panels"]; len(value) > 0 {
			if err := json.Unmarshal(value, &panels); err != nil || len(panels) != len(cfg.Panels) {
				return fmt.Errorf("cannot identify named panel entries for removal")
			}
		}
		keep := make([]json.RawMessage, 0, len(panels))
		for i, entry := range panels {
			p := cfg.Panels[i]
			if !used[p.Name] && (removedNames[p.Name] || conf.PanelBindingID(p.APIHost, p.MachineID) == removed.ID) {
				continue
			}
			keep = append(keep, entry)
		}
		return setConfigField(raw, "Panels", keep)
	})
	return removed, legacy, err
}

func purgePanelFiles(path string, removed conf.PanelSummary, legacy []conf.NodeConfig) error {
	binding := conf.MachineBinding{APIHost: removed.Host, MachineID: removed.MachineID}
	failures := []error{binding.RemoveCache(filepath.Dir(path))}
	for _, n := range legacy {
		failures = append(failures, node.RemovePanelSnapshot(n))
	}
	return errors.Join(failures...)
}

func setConfigField(raw map[string]json.RawMessage, name string, value any) error {
	b, err := json.Marshal(value)
	if err == nil {
		raw[name] = b
	}
	return err
}

func editPanelConfig(path string, create bool, edit func(map[string]json.RawMessage, *conf.Conf) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock AriNode configuration: %w", err)
	}
	defer lock.Close()
	cfg := conf.New()
	raw := make(map[string]json.RawMessage)
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("AriNode configuration must be a regular file")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(json5.NewTrimNodeReader(f))
		f.Close()
		if err != nil || json.Unmarshal(data, &raw) != nil || raw == nil {
			return fmt.Errorf("invalid AriNode configuration object")
		}
		cfg, err = loadPanelConfig(path)
		if err != nil {
			return err
		}
	case !create || !os.IsNotExist(err):
		return err
	}
	if err := edit(raw, cfg); err != nil {
		return err
	}
	if err := setConfigField(raw, "AllowEmpty", true); err != nil {
		return err
	}
	// Preserve the effective default core when removing an explicit sing-box
	// node from a local configuration that only listed other core types.
	if len(raw["Cores"]) > 0 {
		var cores []json.RawMessage
		if err := json.Unmarshal(raw["Cores"], &cores); err != nil {
			return err
		}
		if len(cores) > 0 && len(cfg.CoresConfig) > len(cores) {
			for _, c := range cfg.CoresConfig[len(cores):] {
				value, err := json.Marshal(map[string]string{"Type": c.Type})
				if err != nil {
					return err
				}
				cores = append(cores, value)
			}
			if err := setConfigField(raw, "Cores", cores); err != nil {
				return err
			}
		}
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".panel-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if info != nil {
		previous, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(previous) == string(append(data, '\n')) {
			return os.Chmod(path, 0600)
		}
	}
	next, err := loadPanelConfig(f.Name())
	if err != nil {
		return err
	}
	if err := node.ValidateBindings(next.NodeConfig, next.AllowEmpty); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

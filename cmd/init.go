package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var (
	initPanel     string
	initToken     string
	initCore      string
	initOutput    string
	initForce     bool
	initNodes     []string
	initMachineID int
)

var initCommand = &cobra.Command{
	Use:   "init",
	Short: "Generate an Xboard node or machine binding configuration",
	RunE: func(_ *cobra.Command, _ []string) error {
		return writeInitialConfig(initPanel, initToken, initCore, initOutput, initNodes, initMachineID, initForce)
	},
}

func init() {
	initCommand.Flags().StringVar(&initPanel, "panel", "", "Xboard panel URL")
	initCommand.Flags().StringVar(&initToken, "token", "", "Xboard server token (prefer ARINODE_PANEL_TOKEN)")
	initCommand.Flags().StringVar(&initCore, "core", "sing", "core: sing or xray")
	initCommand.Flags().StringVar(&initOutput, "output", "config.json", "output path")
	initCommand.Flags().StringArrayVar(&initNodes, "node", nil, "node binding as type:id; repeat for multiple nodes")
	initCommand.Flags().IntVar(&initMachineID, "machine-id", 0, "Xboard machine ID for machine-token authentication")
	initCommand.Flags().BoolVar(&initForce, "force", false, "replace an existing config")
	command.AddCommand(initCommand)
}

func writeInitialConfig(panel, token, core, output string, nodes []string, machineID int, force bool) error {
	panel = strings.TrimRight(strings.TrimSpace(panel), "/")
	u, err := url.Parse(panel)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("--panel must be an absolute http(s) URL without credentials, query or fragment")
	}
	if token == "" {
		token = os.Getenv("ARINODE_PANEL_TOKEN")
	}
	if token == "" {
		return errors.New("panel token is required via ARINODE_PANEL_TOKEN or --token")
	}
	if core != "sing" && core != "xray" {
		return errors.New("--core must be sing or xray")
	}
	if len(nodes) == 0 {
		return errors.New("at least one --node type:id is required")
	}
	if machineID < 0 {
		return errors.New("--machine-id cannot be negative")
	}
	type binding struct {
		Core      string `json:"Core"`
		APIHost   string `json:"ApiHost"`
		APIKey    string `json:"ApiKey"`
		NodeID    int    `json:"NodeID"`
		MachineID int    `json:"MachineID,omitempty"`
		NodeType  string `json:"NodeType"`
		Timeout   int    `json:"Timeout"`
	}
	bindings := make([]binding, 0, len(nodes))
	seen := map[string]bool{}
	for _, raw := range nodes {
		parts := strings.Split(raw, ":")
		if len(parts) != 2 {
			return fmt.Errorf("invalid node %q: expected type:id", raw)
		}
		typ := strings.ToLower(parts[0])
		switch typ {
		case "vmess", "vless", "trojan", "shadowsocks", "hysteria", "hysteria2", "tuic", "anytls":
		default:
			return fmt.Errorf("unsupported node type %q", typ)
		}
		id, err := strconv.Atoi(parts[1])
		if err != nil || id <= 0 {
			return fmt.Errorf("invalid node ID in %q", raw)
		}
		key := fmt.Sprintf("%s:%d", typ, id)
		if seen[key] {
			return fmt.Errorf("duplicate node %s", key)
		}
		seen[key] = true
		bindings = append(bindings, binding{Core: core, APIHost: panel, APIKey: token, NodeID: id, MachineID: machineID, NodeType: typ, Timeout: 30})
	}
	config := struct {
		Log   map[string]string   `json:"Log"`
		Cores []map[string]string `json:"Cores"`
		Nodes []binding           `json:"Nodes"`
	}{map[string]string{"Level": "info"}, []map[string]string{{"Type": core}}, bindings}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(output, flags, 0600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Chmod(output, 0600)
}

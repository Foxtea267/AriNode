package conf

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type MachineBinding struct {
	APIHost    string `json:"ApiHost"`
	APIKey     string `json:"ApiKey"`
	MachineID  int    `json:"MachineID"`
	Core       string `json:"Core"`
	SingleNode bool   `json:"SingleNode,omitempty"`
}

type PanelSummary struct {
	ID        string `json:"id"`
	Host      string `json:"address"`
	MachineID int    `json:"machine_id"`
	Mode      string `json:"mode"`
}

func (p *Conf) BindingPanels() []PanelSummary {
	result := make([]PanelSummary, 0)
	seen := make(map[string]bool)
	referenced := make(map[string]bool)
	add := func(host string, machine int, mode string) {
		id := PanelBindingID(host, machine)
		if canonical, err := PanelURL(host); err == nil {
			host = canonical
		} else {
			host = "(invalid address)"
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, PanelSummary{ID: id, Host: host, MachineID: machine, Mode: mode})
		}
	}
	for _, m := range p.Machines {
		mode := "all"
		if m.SingleNode {
			mode = "single"
		}
		add(m.APIHost, m.MachineID, mode)
	}
	for _, n := range p.NodeConfig {
		add(n.ApiConfig.APIHost, n.ApiConfig.MachineID, "local")
		referenced[n.Panel] = true
	}
	for _, binding := range p.Panels {
		if !referenced[binding.Name] {
			add(binding.APIHost, binding.MachineID, "local")
		}
	}
	return result
}

func (m MachineBinding) ValidateCache(root string) error {
	dir := m.CacheDir(root)
	for _, path := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed panel cache must be a local directory")
		}
	}
	return nil
}

func PanelURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("panel address must be an absolute http(s) URL")
	}
	u.Host = strings.ToLower(u.Host)
	return strings.TrimRight(u.String(), "/"), nil
}

func PanelBindingID(host string, machine int) string {
	if canonical, err := PanelURL(host); err == nil {
		host = canonical
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", host, machine))))[:24]
}

func (m MachineBinding) ID() string { return PanelBindingID(m.APIHost, m.MachineID) }

func (m MachineBinding) CacheDir(root string) string {
	return filepath.Join(root, "managed-panels", m.ID())
}

func (m MachineBinding) RemoveCache(root string) error {
	dir := m.CacheDir(root)
	parent, err := os.Lstat(filepath.Dir(dir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed-panels must be a local directory")
	}
	return os.RemoveAll(dir)
}

func (p *Conf) resolveMachines(root string) error {
	explicit := len(p.NodeConfig)
	seen := make(map[string]bool, len(p.Machines))
	for i := range p.Machines {
		m := &p.Machines[i]
		host, err := PanelURL(m.APIHost)
		if err != nil || m.APIKey == "" || m.MachineID <= 0 {
			return fmt.Errorf("Machines[%d]: panel address, machine ID and credential are required", i)
		}
		m.APIHost = host
		m.Core = NormalizeCoreType(m.Core)
		if m.Core != "sing" && m.Core != "xray" {
			return fmt.Errorf("Machines[%d]: unsupported core", i)
		}
		id := m.ID()
		if seen[id] {
			return fmt.Errorf("Machines[%d]: duplicate machine binding", i)
		}
		seen[id] = true
		if err := m.ValidateCache(root); err != nil {
			return err
		}
		cache := m.CacheDir(root)
		node := NodeConfig{
			ApiConfig: ApiConfig{APIHost: host, Key: m.APIKey, MachineID: m.MachineID, Timeout: 5, SingleMachineNode: m.SingleNode},
			Options: Options{ManagedPanel: true, ListenIP: "0.0.0.0", SendIP: "0.0.0.0", CertConfig: NewCertConfig(),
				PanelConfigPath: filepath.Join(cache, "nodes"), CertStorePath: filepath.Join(cache, "certs")},
		}
		if err := node.Options.UseCore(m.Core); err != nil {
			return err
		}
		for j := 0; j < explicit; j++ {
			local := &p.NodeConfig[j]
			a := &local.ApiConfig
			if PanelBindingID(a.APIHost, a.MachineID) != id {
				continue
			}
			if a.Cluster != nil || (a.MachineAutoDiscover != nil && !*a.MachineAutoDiscover) {
				return fmt.Errorf("Machines[%d]: existing binding uses cluster or manual node discovery", i)
			}
			a.APIHost, a.Key, a.SingleMachineNode = host, m.APIKey, m.SingleNode
		}
		p.NodeConfig = append(p.NodeConfig, node)
	}
	return nil
}

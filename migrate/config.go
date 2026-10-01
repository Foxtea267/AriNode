package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Foxtea267/AriNode/common/json5"
	"gopkg.in/yaml.v3"
)

type xRoot struct {
	ID         string    `yaml:"id,omitempty"`
	Panel      xPanel    `yaml:"panel,omitempty"`
	Nodes      []xNode   `yaml:"nodes,omitempty"`
	Machine    *xMachine `yaml:"machine,omitempty"`
	Kernel     xKernel   `yaml:"kernel,omitempty"`
	Cert       xCert     `yaml:"cert,omitempty"`
	Log        xLog      `yaml:"log,omitempty"`
	Instances  []xRoot   `yaml:"instances,omitempty"`
	Standalone any       `yaml:"standalone,omitempty"`
	Runtime    any       `yaml:"runtime,omitempty"`
	WS         any       `yaml:"ws,omitempty"`
	Node       any       `yaml:"node,omitempty"`
	HealthPort int       `yaml:"health_port,omitempty"`
}

type xPanel struct {
	URL      string `yaml:"url,omitempty"`
	Token    string `yaml:"token,omitempty"`
	TokenEnv string `yaml:"token_env,omitempty"`
	NodeID   int    `yaml:"node_id,omitempty"`
	NodeType string `yaml:"node_type,omitempty"`
}
type xNode struct {
	NodeID   int      `yaml:"node_id"`
	NodeType string   `yaml:"node_type,omitempty"`
	Kernel   *xKernel `yaml:"kernel,omitempty"`
	Cert     *xCert   `yaml:"cert,omitempty"`
}
type xMachine struct {
	MachineID int    `yaml:"machine_id"`
	Token     string `yaml:"token,omitempty"`
	TokenEnv  string `yaml:"token_env,omitempty"`
}
type xKernel struct {
	Type           string `yaml:"type,omitempty"`
	CustomConfig   string `yaml:"custom_config,omitempty"`
	ConfigDir      string `yaml:"config_dir,omitempty"`
	GeoDataDir     string `yaml:"geo_data_dir,omitempty"`
	LogLevel       string `yaml:"log_level,omitempty"`
	CustomOutbound any    `yaml:"custom_outbound,omitempty"`
	CustomRoute    any    `yaml:"custom_route,omitempty"`
}
type xCert struct {
	AutoTLS     bool              `yaml:"auto_tls,omitempty"`
	Domain      string            `yaml:"domain,omitempty"`
	Email       string            `yaml:"email,omitempty"`
	CertFile    string            `yaml:"cert_file,omitempty"`
	KeyFile     string            `yaml:"key_file,omitempty"`
	CertMode    string            `yaml:"cert_mode,omitempty"`
	DNSProvider string            `yaml:"dns_provider,omitempty"`
	DNSEnv      map[string]string `yaml:"dns_env,omitempty"`
	CertDir     string            `yaml:"cert_dir,omitempty"`
	HTTPPort    int               `yaml:"http_port,omitempty"`
	CertContent string            `yaml:"cert_content,omitempty"`
	KeyContent  string            `yaml:"key_content,omitempty"`
}
type xLog struct {
	Level  string `yaml:"level,omitempty"`
	Output string `yaml:"output,omitempty"`
}

type aRoot struct {
	Log    aLog              `json:"Log"`
	Cores  []aCore           `json:"Cores"`
	Panels []aPanel          `json:"Panels,omitempty"`
	Komari []json.RawMessage `json:"Komari,omitempty"`
	Nodes  []aNode           `json:"Nodes"`
}
type aPanel struct {
	Name      string `json:"Name"`
	APIHost   string `json:"ApiHost"`
	APIKey    string `json:"ApiKey"`
	APIKeyEnv string `json:"ApiKeyEnv"`
	MachineID int    `json:"MachineID"`
}
type aLog struct {
	Level  string `json:"Level,omitempty"`
	Output string `json:"Output,omitempty"`
}
type aCore struct {
	Type         string `json:"Type"`
	Name         string `json:"Name,omitempty"`
	OriginalPath string `json:"OriginalPath,omitempty"`
}
type aNode struct {
	Panel      string `json:"Panel,omitempty"`
	Core       string `json:"Core"`
	CoreName   string `json:"CoreName,omitempty"`
	Include    string `json:"Include,omitempty"`
	APIHost    string `json:"ApiHost"`
	APIKey     string `json:"ApiKey"`
	NodeID     int    `json:"NodeID"`
	MachineID  int    `json:"MachineID,omitempty"`
	NodeType   string `json:"NodeType"`
	Timeout    int    `json:"Timeout"`
	CertConfig *aCert `json:"CertConfig,omitempty"`
}
type aCert struct {
	RejectUnknownSni bool              `json:"RejectUnknownSni,omitempty"`
	CertMode         string            `json:"CertMode"`
	CertDomain       string            `json:"CertDomain,omitempty"`
	CertFile         string            `json:"CertFile,omitempty"`
	KeyFile          string            `json:"KeyFile,omitempty"`
	Provider         string            `json:"Provider,omitempty"`
	Email            string            `json:"Email,omitempty"`
	DNSEnv           map[string]string `json:"DNSEnv,omitempty"`
}

type Options struct {
	NodeType string
	Offline  bool
	Client   *http.Client
}

type Result struct {
	Data     []byte
	Nodes    int
	Warnings []string
}

func FromXBNode(data []byte, opts Options) (Result, error) {
	var root xRoot
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&root); err != nil {
		return Result{}, fmt.Errorf("parse Xboard-Node YAML: %w", err)
	}
	instances := root.Instances
	if len(instances) == 0 {
		instances = []xRoot{root}
	}
	out := aRoot{Log: aLog{Level: "info"}, Cores: []aCore{}, Nodes: []aNode{}}
	cores := map[string]bool{}
	seen := map[string]bool{}
	var warnings []string
	for i, inst := range instances {
		inst.inherit(root)
		if inst.Standalone != nil {
			return Result{}, fmt.Errorf("instances[%d]: standalone mode has no Xboard binding", i)
		}
		if inst.Panel.URL == "" {
			return Result{}, fmt.Errorf("instances[%d]: panel.url is required", i)
		}
		if err := validPanelURL(inst.Panel.URL); err != nil {
			return Result{}, fmt.Errorf("instances[%d]: %w", i, err)
		}
		core, err := normalizeCore(inst.Kernel.Type)
		if err != nil {
			return Result{}, fmt.Errorf("instances[%d]: %w", i, err)
		}
		if !cores[core] {
			out.Cores = append(out.Cores, aCore{Type: core})
			cores[core] = true
		}
		if inst.Kernel.CustomConfig != "" || inst.Kernel.CustomOutbound != nil || inst.Kernel.CustomRoute != nil || inst.Kernel.LogLevel != "" || inst.Kernel.GeoDataDir != "" {
			warnings = append(warnings, fmt.Sprintf("instances[%d]: kernel custom config, routes, assets or logging require manual porting", i))
		}
		if inst.Runtime != nil || inst.WS != nil || inst.Node != nil || inst.HealthPort != 0 {
			warnings = append(warnings, fmt.Sprintf("instances[%d]: runtime/WS/polling/health settings are not equivalent in AriNode", i))
		}
		if inst.Log.Level != "" {
			out.Log.Level = inst.Log.Level
		}
		if inst.Log.Output != "" && inst.Log.Output != "stdout" {
			out.Log.Output = inst.Log.Output
		}
		if inst.Machine != nil {
			if inst.Machine.MachineID <= 0 {
				return Result{}, fmt.Errorf("instances[%d]: machine_id must be positive", i)
			}
			token, err := resolveToken(inst.Machine.Token, inst.Machine.TokenEnv)
			if err != nil {
				return Result{}, fmt.Errorf("instances[%d]: machine: %w", i, err)
			}
			if opts.Offline {
				return Result{}, fmt.Errorf("instances[%d]: machine mode requires panel discovery; remove --offline", i)
			}
			nodes, err := discoverMachine(opts.Client, inst.Panel.URL, token, inst.Machine.MachineID)
			if err != nil {
				return Result{}, fmt.Errorf("instances[%d]: %w", i, err)
			}
			if len(nodes) == 0 {
				return Result{}, fmt.Errorf("instances[%d]: machine has no active nodes", i)
			}
			for _, n := range nodes {
				if err := addNode(&out, seen, inst.Panel.URL, token, core, n.ID, inst.Machine.MachineID, n.Type, inst.Cert); err != nil {
					return Result{}, fmt.Errorf("instances[%d]: %w", i, err)
				}
			}
			warnings = append(warnings, fmt.Sprintf("instances[%d]: machine node list is a snapshot; rerun migration after bindings change", i))
			continue
		}
		token, err := resolveToken(inst.Panel.Token, inst.Panel.TokenEnv)
		if err != nil {
			return Result{}, fmt.Errorf("instances[%d]: panel: %w", i, err)
		}
		nodes := inst.Nodes
		if len(nodes) == 0 {
			nodes = []xNode{{NodeID: inst.Panel.NodeID, NodeType: inst.Panel.NodeType}}
		}
		for _, n := range nodes {
			typ := first(n.NodeType, inst.Panel.NodeType)
			if typ == "" || strings.EqualFold(typ, "v2ray") {
				if opts.Offline {
					if opts.NodeType == "" {
						return Result{}, fmt.Errorf("instances[%d] node %d: node type missing or generic v2ray; use --node-type or remove --offline", i, n.NodeID)
					}
					typ = opts.NodeType
				} else {
					typ, err = discoverType(opts.Client, inst.Panel.URL, token, n.NodeID)
					if err != nil {
						return Result{}, fmt.Errorf("instances[%d] node %d: %w", i, n.NodeID, err)
					}
				}
			}
			cert := inst.Cert
			if n.Cert != nil {
				cert = *n.Cert
			}
			if n.Kernel != nil && (n.Kernel.CustomConfig != "" || n.Kernel.ConfigDir != "" || n.Kernel.GeoDataDir != "" || n.Kernel.LogLevel != "") {
				warnings = append(warnings, fmt.Sprintf("instances[%d] node %d: kernel overrides require manual porting", i, n.NodeID))
			}
			if err := addNode(&out, seen, inst.Panel.URL, token, core, n.NodeID, 0, typ, cert); err != nil {
				return Result{}, fmt.Errorf("instances[%d]: %w", i, err)
			}
		}
	}
	if len(out.Nodes) == 0 {
		return Result{}, errors.New("no nodes found")
	}
	sort.Strings(warnings)
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return Result{}, err
	}
	return Result{Data: append(encoded, '\n'), Nodes: len(out.Nodes), Warnings: warnings}, nil
}

func ToXBNode(data []byte) (Result, error) {
	var in aRoot
	clean, err := io.ReadAll(json5.NewTrimNodeReader(bytes.NewReader(data)))
	if err != nil {
		return Result{}, fmt.Errorf("read AriNode JSON: %w", err)
	}
	if err := json.Unmarshal(clean, &in); err != nil {
		return Result{}, fmt.Errorf("parse AriNode JSON: %w", err)
	}
	var raw struct {
		Cores []map[string]json.RawMessage `json:"Cores"`
		Nodes []map[string]json.RawMessage `json:"Nodes"`
	}
	if err := json.Unmarshal(clean, &raw); err != nil {
		return Result{}, err
	}
	if len(in.Nodes) == 0 {
		return Result{}, errors.New("AriNode config has no nodes")
	}
	root := xRoot{Log: xLog{Level: first(in.Log.Level, "info"), Output: in.Log.Output}}
	var warnings []string
	if len(in.Komari) > 0 {
		warnings = append(warnings, "Komari bindings are AriNode-only and were not exported to Xboard-Node")
	}
	panels := map[string]aPanel{}
	for i, binding := range in.Panels {
		if binding.Name == "" {
			return Result{}, fmt.Errorf("Panels[%d]: Name is required", i)
		}
		if _, exists := panels[binding.Name]; exists {
			return Result{}, fmt.Errorf("Panels[%d]: duplicate Name", i)
		}
		panels[binding.Name] = binding
	}
	for i, core := range in.Cores {
		if core.OriginalPath != "" {
			warnings = append(warnings, fmt.Sprintf("Cores[%d]: OriginalPath requires manual porting to Xboard-Node custom_config", i))
		}
		if i < len(raw.Cores) {
			for field := range raw.Cores[i] {
				switch strings.ToLower(field) {
				case "type", "name", "originalpath":
				default:
					warnings = append(warnings, fmt.Sprintf("Cores[%d]: %s requires manual review", i, field))
				}
			}
		}
	}
	seen := map[string]bool{}
	machines := map[string]int{}
	machineTokens := map[string]string{}
	for i, n := range in.Nodes {
		if n.Panel != "" {
			binding, ok := panels[n.Panel]
			if !ok {
				return Result{}, fmt.Errorf("Nodes[%d]: unknown Panel %q", i, n.Panel)
			}
			n.APIHost = binding.APIHost
			n.APIKey = binding.APIKey
			if n.APIKey == "" && binding.APIKeyEnv != "" {
				n.APIKey = os.Getenv(binding.APIKeyEnv)
			}
			if n.MachineID == 0 {
				n.MachineID = binding.MachineID
			}
		}
		if n.Include != "" {
			return Result{}, fmt.Errorf("Nodes[%d]: Include must be expanded before migration", i)
		}
		if n.NodeID <= 0 || n.APIKey == "" {
			return Result{}, fmt.Errorf("Nodes[%d]: positive NodeID and ApiKey required", i)
		}
		if err := validPanelURL(n.APIHost); err != nil {
			return Result{}, fmt.Errorf("Nodes[%d]: %w", i, err)
		}
		coreType := n.Core
		if n.CoreName != "" {
			found := false
			for _, config := range in.Cores {
				if config.Name == n.CoreName {
					coreType = config.Type
					found = true
					break
				}
			}
			if !found {
				return Result{}, fmt.Errorf("Nodes[%d]: CoreName %q is not defined", i, n.CoreName)
			}
		} else if coreType == "" {
			if len(in.Cores) != 1 {
				return Result{}, fmt.Errorf("Nodes[%d]: Core is empty and exactly one configured core is required", i)
			}
			coreType = in.Cores[0].Type
		}
		core, err := normalizeCore(coreType)
		if err != nil {
			return Result{}, fmt.Errorf("Nodes[%d]: %w", i, err)
		}
		if n.CoreName != "" {
			warnings = append(warnings, fmt.Sprintf("Nodes[%d]: named core configuration requires manual review", i))
		}
		if i < len(raw.Nodes) {
			for field := range raw.Nodes[i] {
				switch strings.ToLower(field) {
				case "panel", "core", "corename", "apihost", "apikey", "nodeid", "machineid", "nodetype", "timeout", "certconfig":
				default:
					warnings = append(warnings, fmt.Sprintf("Nodes[%d]: %s requires manual review", i, field))
				}
			}
		}
		typ, err := normalizeType(n.NodeType)
		if err != nil {
			return Result{}, fmt.Errorf("Nodes[%d]: %w", i, err)
		}
		key := fmt.Sprintf("%s/%d", strings.TrimRight(n.APIHost, "/"), n.NodeID)
		if seen[key] {
			return Result{}, fmt.Errorf("Nodes[%d]: duplicate binding %s", i, key)
		}
		seen[key] = true
		inst := xRoot{Panel: xPanel{URL: strings.TrimRight(n.APIHost, "/")}, Kernel: xKernel{Type: xCore(core)}}
		if n.CertConfig != nil {
			if n.CertConfig.RejectUnknownSni {
				return Result{}, fmt.Errorf("Nodes[%d]: RejectUnknownSni has no Xboard-Node equivalent", i)
			}
			if i < len(raw.Nodes) {
				for field, value := range raw.Nodes[i] {
					if strings.EqualFold(field, "CertConfig") {
						var rawCert map[string]json.RawMessage
						if err := json.Unmarshal(value, &rawCert); err == nil {
							for certField := range rawCert {
								switch strings.ToLower(certField) {
								case "certmode", "certdomain", "certfile", "keyfile", "provider", "email", "dnsenv", "rejectunknownsni":
								default:
									warnings = append(warnings, fmt.Sprintf("Nodes[%d].CertConfig: %s requires manual review", i, certField))
								}
							}
						}
					}
				}
			}
			inst.Cert, err = toXCert(*n.CertConfig)
			if err != nil {
				return Result{}, fmt.Errorf("Nodes[%d]: %w", i, err)
			}
		}
		if n.MachineID > 0 {
			identity := fmt.Sprintf("%s/%d", inst.Panel.URL, n.MachineID)
			if previous, ok := machineTokens[identity]; ok && previous != n.APIKey {
				return Result{}, fmt.Errorf("Nodes[%d]: same machine ID has different tokens", i)
			}
			machineTokens[identity] = n.APIKey
			mkey := fmt.Sprintf("%s/%d/%s", inst.Panel.URL, n.MachineID, n.APIKey)
			if index, ok := machines[mkey]; ok {
				if root.Instances[index].Kernel.Type != inst.Kernel.Type {
					return Result{}, fmt.Errorf("Nodes[%d]: same machine uses multiple cores", i)
				}
				if !sameCert(root.Instances[index].Cert, inst.Cert) {
					return Result{}, fmt.Errorf("Nodes[%d]: same machine uses different cert settings", i)
				}
				continue
			}
			inst.Machine = &xMachine{MachineID: n.MachineID, Token: n.APIKey}
			machines[mkey] = len(root.Instances)
		} else {
			inst.Panel.Token = n.APIKey
			inst.Panel.NodeID = n.NodeID
			inst.Panel.NodeType = typ
		}
		root.Instances = append(root.Instances, inst)
	}
	if len(machines) > 0 {
		warnings = append(warnings, "machine mode uses Xboard-Node dynamic discovery; panel bindings may include nodes beyond this AriNode config")
	}
	sort.Strings(warnings)
	if len(root.Instances) == 1 {
		root.Panel, root.Kernel, root.Cert, root.Machine = root.Instances[0].Panel, root.Instances[0].Kernel, root.Instances[0].Cert, root.Instances[0].Machine
		root.Instances = nil
	}
	encoded, err := yaml.Marshal(root)
	if err != nil {
		return Result{}, err
	}
	return Result{Data: encoded, Nodes: len(in.Nodes), Warnings: warnings}, nil
}

func (x *xRoot) inherit(parent xRoot) {
	if x.Panel.URL == "" {
		x.Panel.URL = parent.Panel.URL
	}
	if x.Panel.Token == "" && x.Panel.TokenEnv == "" {
		x.Panel.Token, x.Panel.TokenEnv = parent.Panel.Token, parent.Panel.TokenEnv
	}
	if x.Panel.NodeType == "" {
		x.Panel.NodeType = parent.Panel.NodeType
	}
	if x.Kernel.Type == "" {
		x.Kernel.Type = parent.Kernel.Type
	}
	if x.Kernel.CustomConfig == "" {
		x.Kernel.CustomConfig = parent.Kernel.CustomConfig
	}
	if x.Kernel.CustomOutbound == nil {
		x.Kernel.CustomOutbound = parent.Kernel.CustomOutbound
	}
	if x.Kernel.CustomRoute == nil {
		x.Kernel.CustomRoute = parent.Kernel.CustomRoute
	}
	childHasCert := x.Cert.CertMode != "" || x.Cert.CertFile != "" || x.Cert.CertContent != ""
	if x.Cert.CertMode == "" {
		x.Cert.CertMode = parent.Cert.CertMode
	}
	if x.Cert.Domain == "" {
		x.Cert.Domain = parent.Cert.Domain
	}
	if x.Cert.Email == "" {
		x.Cert.Email = parent.Cert.Email
	}
	if x.Cert.CertFile == "" {
		x.Cert.CertFile = parent.Cert.CertFile
	}
	if x.Cert.KeyFile == "" {
		x.Cert.KeyFile = parent.Cert.KeyFile
	}
	if x.Cert.DNSProvider == "" {
		x.Cert.DNSProvider = parent.Cert.DNSProvider
	}
	if len(x.Cert.DNSEnv) == 0 {
		x.Cert.DNSEnv = parent.Cert.DNSEnv
	}
	if x.Cert.HTTPPort == 0 {
		x.Cert.HTTPPort = parent.Cert.HTTPPort
	}
	if x.Cert.CertContent == "" {
		x.Cert.CertContent = parent.Cert.CertContent
	}
	if x.Cert.KeyContent == "" {
		x.Cert.KeyContent = parent.Cert.KeyContent
	}
	if !childHasCert && !x.Cert.AutoTLS {
		x.Cert.AutoTLS = parent.Cert.AutoTLS
	}
	if x.Log.Level == "" {
		x.Log.Level = parent.Log.Level
	}
	if x.Log.Output == "" {
		x.Log.Output = parent.Log.Output
	}
	if x.Runtime == nil {
		x.Runtime = parent.Runtime
	}
	if x.WS == nil {
		x.WS = parent.WS
	}
	if x.Node == nil {
		x.Node = parent.Node
	}
	if x.HealthPort == 0 {
		x.HealthPort = parent.HealthPort
	}
}

func addNode(out *aRoot, seen map[string]bool, panel, token, core string, id, machineID int, typ string, cert xCert) error {
	if id <= 0 {
		return errors.New("node ID must be positive")
	}
	typ, err := normalizeType(typ)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s/%d", strings.TrimRight(panel, "/"), id)
	if seen[key] {
		return fmt.Errorf("duplicate binding %s", key)
	}
	seen[key] = true
	ac, err := toACert(cert)
	if err != nil {
		return err
	}
	out.Nodes = append(out.Nodes, aNode{Core: core, APIHost: strings.TrimRight(panel, "/"), APIKey: token, NodeID: id, MachineID: machineID, NodeType: typ, Timeout: 30, CertConfig: ac})
	return nil
}

func toACert(c xCert) (*aCert, error) {
	if c.CertContent != "" || c.KeyContent != "" {
		return nil, errors.New("inline certificate content requires manual migration")
	}
	mode := strings.ToLower(c.CertMode)
	if mode == "" {
		switch {
		case c.CertFile != "":
			mode = "file"
		case c.AutoTLS:
			mode = "http"
		default:
			return nil, nil
		}
	}
	if mode == "none" {
		return nil, nil
	}
	if mode == "self" && c.CertFile == "" && c.KeyFile == "" {
		return nil, nil
	}
	if mode != "file" && mode != "http" && mode != "dns" && mode != "self" {
		return nil, fmt.Errorf("unsupported certificate mode %q; migrate certificates manually", mode)
	}
	if mode == "http" && c.HTTPPort != 0 && c.HTTPPort != 80 {
		return nil, fmt.Errorf("custom ACME HTTP port %d is not supported", c.HTTPPort)
	}
	return &aCert{CertMode: mode, CertDomain: c.Domain, CertFile: c.CertFile, KeyFile: c.KeyFile, Provider: c.DNSProvider, Email: c.Email, DNSEnv: c.DNSEnv}, nil
}

func toXCert(c aCert) (xCert, error) {
	mode := strings.ToLower(c.CertMode)
	if mode == "auto" {
		return xCert{CertMode: "self", Domain: c.CertDomain}, nil
	}
	if mode == "none" || mode == "" {
		return xCert{}, nil
	}
	if mode != "file" && mode != "http" && mode != "dns" && mode != "self" {
		return xCert{}, fmt.Errorf("unsupported certificate mode %q", mode)
	}
	return xCert{CertMode: mode, AutoTLS: mode == "http", Domain: c.CertDomain, CertFile: c.CertFile, KeyFile: c.KeyFile, DNSProvider: c.Provider, Email: c.Email, DNSEnv: c.DNSEnv}, nil
}

func sameCert(a, b xCert) bool { return reflect.DeepEqual(a, b) }
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func normalizeCore(value string) (string, error) {
	switch strings.ToLower(value) {
	case "", "sing", "singbox", "sing-box":
		return "sing", nil
	case "xray":
		return "xray", nil
	default:
		return "", fmt.Errorf("unsupported kernel %q", value)
	}
}
func xCore(value string) string {
	if value == "sing" {
		return "singbox"
	}
	return value
}
func normalizeType(value string) (string, error) {
	switch strings.ToLower(value) {
	case "v2ray", "vmess":
		return "vmess", nil
	case "vless", "trojan", "shadowsocks", "hysteria", "hysteria2", "tuic", "anytls", "mieru":
		return strings.ToLower(value), nil
	default:
		return "", fmt.Errorf("unsupported or missing node type %q", value)
	}
}
func validPanelURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid panel URL %q", raw)
	}
	return nil
}
func resolveToken(token, env string) (string, error) {
	if token == "" && env != "" {
		token = os.Getenv(env)
	}
	if token == "" {
		return "", errors.New("token is missing (set token or token_env environment variable)")
	}
	return token, nil
}

type machineNode struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
}

func discoverMachine(client *http.Client, panel, token string, machineID int) ([]machineNode, error) {
	var response struct {
		Nodes []machineNode `json:"nodes"`
	}
	err := panelPOST(client, panel, "/api/v2/server/machine/nodes", map[string]any{"machine_id": machineID, "token": token}, &response)
	if err != nil {
		return nil, fmt.Errorf("discover machine nodes: %w", err)
	}
	sort.Slice(response.Nodes, func(i, j int) bool { return response.Nodes[i].ID < response.Nodes[j].ID })
	return response.Nodes, nil
}
func discoverType(client *http.Client, panel, token string, id int) (string, error) {
	u, _ := url.Parse(strings.TrimRight(panel, "/") + "/api/v1/server/UniProxy/config")
	q := u.Query()
	q.Set("node_id", fmt.Sprint(id))
	q.Set("token", token)
	u.RawQuery = q.Encode()
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Get(u.String())
	if err != nil {
		return "", fmt.Errorf("discover node type: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discover node type: panel returned HTTP %d", resp.StatusCode)
	}
	var value struct {
		Protocol string `json:"protocol"`
		Type     string `json:"type"`
		Data     struct {
			Protocol string `json:"protocol"`
			Type     string `json:"type"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&value); err != nil {
		return "", err
	}
	return first(value.Protocol, value.Type, value.Data.Protocol, value.Data.Type), nil
}
func panelPOST(client *http.Client, panel, path string, body any, out any) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := client.Post(strings.TrimRight(panel, "/")+path, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("panel returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

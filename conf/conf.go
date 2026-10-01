package conf

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/Foxtea267/AriNode/common/json5"

	"encoding/json/v2"
)

type Conf struct {
	LogConfig   LogConfig       `json:"Log"`
	CoresConfig []CoreConfig    `json:"Cores"`
	Panels      []PanelBinding  `json:"Panels,omitempty"`
	Komari      []KomariBinding `json:"Komari,omitempty"`
	NodeConfig  []NodeConfig    `json:"Nodes"`
}

type PanelBinding struct {
	Name      string `json:"Name"`
	APIHost   string `json:"ApiHost"`
	APIKey    string `json:"ApiKey,omitempty"`
	APIKeyEnv string `json:"ApiKeyEnv,omitempty"`
	MachineID int    `json:"MachineID,omitempty"`
	Timeout   int    `json:"Timeout,omitempty"`
}

type KomariBinding struct {
	Name     string `json:"Name"`
	Endpoint string `json:"Endpoint"`
	Token    string `json:"Token,omitempty"`
	TokenEnv string `json:"TokenEnv,omitempty"`
	Interval int    `json:"Interval,omitempty"`
}

func New() *Conf {
	return &Conf{
		LogConfig: LogConfig{
			Level:  "info",
			Output: "",
		},
	}
}

func (p *Conf) LoadFromPath(filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open config file error: %s", err)
	}
	defer f.Close()

	reader := json5.NewTrimNodeReader(f)
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read config file error: %s", err)
	}

	err = json.Unmarshal(data, p)
	if err != nil {
		return fmt.Errorf("unmarshal config error: %s", err)
	}
	if err := p.resolveCores(); err != nil {
		return err
	}
	if err := p.resolvePanels(); err != nil {
		return err
	}
	return p.resolveKomari()
}

func (p *Conf) resolveCores() error {
	if len(p.CoresConfig) == 0 {
		p.CoresConfig = []CoreConfig{DefaultCoreConfig()}
	}
	hasSing := false
	for _, c := range p.CoresConfig {
		hasSing = hasSing || c.Type == "sing"
	}
	for i := range p.NodeConfig {
		o := &p.NodeConfig[i].Options
		if o.CoreName != "" {
			for _, c := range p.CoresConfig {
				if c.Name == o.CoreName && o.Core == "" {
					if err := o.UseCore(c.Type); err != nil {
						return fmt.Errorf("Nodes[%d]: %w", i, err)
					}
					break
				}
			}
		} else if o.Core == "sing" && !hasSing {
			p.CoresConfig = append(p.CoresConfig, DefaultCoreConfig())
			hasSing = true
		}
	}
	return nil
}

func (p *Conf) resolveKomari() error {
	seen := map[string]bool{}
	for i := range p.Komari {
		binding := &p.Komari[i]
		if binding.Name == "" || seen[binding.Name] {
			return fmt.Errorf("Komari[%d]: Name must be unique and nonempty", i)
		}
		seen[binding.Name] = true
		endpoint, err := url.Parse(binding.Endpoint)
		if err != nil || endpoint == nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return fmt.Errorf("Komari[%d]: Endpoint must be an absolute http(s) URL", i)
		}
		binding.Endpoint = strings.TrimRight(binding.Endpoint, "/")
		if binding.Token == "" && binding.TokenEnv != "" {
			binding.Token = os.Getenv(binding.TokenEnv)
		}
		if binding.Token == "" {
			return fmt.Errorf("Komari[%d]: Token or TokenEnv is required", i)
		}
		if binding.Interval == 0 {
			binding.Interval = 6
		}
		if binding.Interval < 5 || binding.Interval > 300 {
			return fmt.Errorf("Komari[%d]: Interval must be 5-300 seconds", i)
		}
	}
	return nil
}

func (p *Conf) resolvePanels() error {
	panels := make(map[string]PanelBinding, len(p.Panels))
	for i, binding := range p.Panels {
		if binding.Name == "" {
			return fmt.Errorf("Panels[%d]: Name is required", i)
		}
		if _, exists := panels[binding.Name]; exists {
			return fmt.Errorf("Panels[%d]: duplicate Name %q", i, binding.Name)
		}
		panelURL, err := url.Parse(binding.APIHost)
		if err != nil || panelURL == nil || (panelURL.Scheme != "https" && panelURL.Scheme != "http") || panelURL.Host == "" || panelURL.User != nil || panelURL.RawQuery != "" || panelURL.Fragment != "" {
			return fmt.Errorf("Panels[%d]: ApiHost must be an absolute http(s) URL", i)
		}
		binding.APIHost = strings.TrimRight(binding.APIHost, "/")
		if binding.APIKey == "" && binding.APIKeyEnv != "" {
			binding.APIKey = os.Getenv(binding.APIKeyEnv)
		}
		if binding.APIKey == "" {
			return fmt.Errorf("Panels[%d]: ApiKey or ApiKeyEnv is required", i)
		}
		if binding.MachineID < 0 || binding.Timeout < 0 {
			return fmt.Errorf("Panels[%d]: MachineID and Timeout cannot be negative", i)
		}
		panels[binding.Name] = binding
	}
	for i := range p.NodeConfig {
		node := &p.NodeConfig[i]
		if node.Panel == "" {
			continue
		}
		binding, ok := panels[node.Panel]
		if !ok {
			return fmt.Errorf("Nodes[%d]: unknown Panel %q", i, node.Panel)
		}
		node.ApiConfig.APIHost = binding.APIHost
		node.ApiConfig.Key = binding.APIKey
		if node.ApiConfig.MachineID == 0 {
			node.ApiConfig.MachineID = binding.MachineID
		}
		if binding.Timeout > 0 {
			node.ApiConfig.Timeout = binding.Timeout
		}
	}
	return nil
}

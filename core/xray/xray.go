package xray

import (
	"fmt"
	"os"
	"sync"

	"encoding/json/v2"

	"github.com/Foxtea267/AriNode/conf"
	vCore "github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/core/xray/app/dispatcher"
	_ "github.com/Foxtea267/AriNode/core/xray/distro/all"
	log "github.com/sirupsen/logrus"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
	coreConf "github.com/xtls/xray-core/infra/conf"
)

var _ vCore.Core = (*Xray)(nil)

func init() {
	vCore.RegisterCore("xray", New)
}

// Xray Structure
type Xray struct {
	access                    sync.Mutex
	Server                    *core.Instance
	ihm                       inbound.Manager
	ohm                       outbound.Manager
	dispatcher                *dispatcher.DefaultDispatcher
	users                     *UserMap
	nodeReportMinTrafficBytes map[string]int64
}

type UserMap struct {
	uidMap  map[string]int
	mapLock sync.RWMutex
}

func New(c *conf.CoreConfig) (vCore.Core, error) {
	server, err := getCore(c.XrayConfig)
	if err != nil {
		return nil, err
	}
	return &Xray{
		Server: server,
		users: &UserMap{
			uidMap: make(map[string]int),
		},
		nodeReportMinTrafficBytes: make(map[string]int64),
	}, nil
}

func parseConnectionConfig(c *conf.XrayConnectionConfig) (policy *coreConf.Policy) {
	policy = &coreConf.Policy{
		StatsUserUplink:   true,
		StatsUserDownlink: true,
		Handshake:         &c.Handshake,
		ConnectionIdle:    &c.ConnIdle,
		UplinkOnly:        &c.UplinkOnly,
		DownlinkOnly:      &c.DownlinkOnly,
		BufferSize:        &c.BufferSize,
	}
	return
}

func getCore(c *conf.XrayConfig) (*core.Instance, error) {
	os.Setenv("XRAY_LOCATION_ASSET", c.AssetPath)
	// Log Config
	coreLogConfig := &coreConf.LogConfig{
		LogLevel:  c.LogConfig.Level,
		AccessLog: c.LogConfig.AccessPath,
		ErrorLog:  c.LogConfig.ErrorPath,
	}
	// DNS config
	coreDnsConfig := &coreConf.DNSConfig{}
	os.Setenv("XRAY_DNS_PATH", "")
	if c.DnsConfigPath != "" {
		data, err := os.ReadFile(c.DnsConfigPath)
		if err != nil {
			log.Error(fmt.Sprintf("Failed to read xray dns config file: %v", err))
			coreDnsConfig = &coreConf.DNSConfig{}
		} else {
			if err := json.Unmarshal(data, coreDnsConfig); err != nil {
				log.Error(fmt.Sprintf("Failed to unmarshal xray dns config: %v. Using default DNS options.", err))
				coreDnsConfig = &coreConf.DNSConfig{}
			}
		}
		os.Setenv("XRAY_DNS_PATH", c.DnsConfigPath)
	}
	dnsConfig, err := coreDnsConfig.Build()
	if err != nil {
		return nil, fmt.Errorf("build Xray DNS config: %w", err)
	}
	// Routing config
	coreRouterConfig := &coreConf.RouterConfig{}
	if c.RouteConfigPath != "" {
		data, err := os.ReadFile(c.RouteConfigPath)
		if err != nil {
			return nil, fmt.Errorf("read Xray routing config: %w", err)
		} else {
			if err = json.Unmarshal(data, coreRouterConfig); err != nil {
				return nil, fmt.Errorf("parse Xray routing config: %w", err)
			}
		}
	}
	routeConfig, err := coreRouterConfig.Build()
	if err != nil {
		return nil, fmt.Errorf("build Xray routing config: %w", err)
	}
	// Custom Inbound config
	var coreCustomInboundConfig []coreConf.InboundDetourConfig
	if c.InboundConfigPath != "" {
		data, err := os.ReadFile(c.InboundConfigPath)
		if err != nil {
			return nil, fmt.Errorf("read Xray inbound config: %w", err)
		} else {
			if err = json.Unmarshal(data, &coreCustomInboundConfig); err != nil {
				return nil, fmt.Errorf("parse Xray inbound config: %w", err)
			}
		}
	}
	var inBoundConfig []*core.InboundHandlerConfig
	for _, config := range coreCustomInboundConfig {
		oc, err := config.Build()
		if err != nil {
			return nil, fmt.Errorf("build Xray inbound config: %w", err)
		}
		inBoundConfig = append(inBoundConfig, oc)
	}
	// Custom Outbound config
	var coreCustomOutboundConfig []coreConf.OutboundDetourConfig
	if c.OutboundConfigPath != "" {
		data, err := os.ReadFile(c.OutboundConfigPath)
		if err != nil {
			return nil, fmt.Errorf("read Xray outbound config: %w", err)
		} else {
			if err = json.Unmarshal(data, &coreCustomOutboundConfig); err != nil {
				return nil, fmt.Errorf("parse Xray outbound config: %w", err)
			}
		}
	}
	var outBoundConfig []*core.OutboundHandlerConfig
	for _, config := range coreCustomOutboundConfig {
		oc, err := config.Build()
		if err != nil {
			return nil, fmt.Errorf("build Xray outbound config: %w", err)
		}
		outBoundConfig = append(outBoundConfig, oc)
	}
	// Policy config
	levelPolicyConfig := parseConnectionConfig(c.ConnectionConfig)
	corePolicyConfig := &coreConf.PolicyConfig{}
	corePolicyConfig.Levels = map[uint32]*coreConf.Policy{0: levelPolicyConfig}
	policyConfig, _ := corePolicyConfig.Build()
	// Build Xray conf
	config := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(coreLogConfig.Build()),
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&stats.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(policyConfig),
			serial.ToTypedMessage(dnsConfig),
			serial.ToTypedMessage(routeConfig),
		},
		Inbound:  inBoundConfig,
		Outbound: outBoundConfig,
	}
	server, err := core.New(config)
	if err != nil {
		return nil, fmt.Errorf("create Xray instance: %w", err)
	}
	log.Info("Xray Core Version: ", core.Version())
	return server, nil
}

// Start the Xray
func (c *Xray) Start() error {
	c.access.Lock()
	defer c.access.Unlock()
	if err := c.Server.Start(); err != nil {
		return err
	}
	c.ihm = c.Server.GetFeature(inbound.ManagerType()).(inbound.Manager)
	c.ohm = c.Server.GetFeature(outbound.ManagerType()).(outbound.Manager)
	c.dispatcher = c.Server.GetFeature(routing.DispatcherType()).(*dispatcher.DefaultDispatcher)
	return nil
}

// Close  the core
func (c *Xray) Close() error {
	c.access.Lock()
	defer c.access.Unlock()
	c.ihm = nil
	c.ohm = nil
	c.dispatcher = nil
	err := c.Server.Close()
	if err != nil {
		return err
	}
	return nil
}

func (c *Xray) Protocols() []string {
	return []string{
		"vmess",
		"vless",
		"shadowsocks",
		"trojan",
	}
}

func (c *Xray) Type() string {
	return "xray"
}

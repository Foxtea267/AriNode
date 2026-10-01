package panel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"encoding/json"
)

// Security type
const (
	None    = 0
	Tls     = 1
	Reality = 2
)

type NodeInfo struct {
	Id           int
	Type         string
	Security     int
	PushInterval time.Duration
	PullInterval time.Duration
	RawDNS       RawDNS
	Rules        Rules
	TLSSettings  NativeTLSSettings

	// origin
	VAllss      *VAllssNode
	Shadowsocks *ShadowsocksNode
	Trojan      *TrojanNode
	Tuic        *TuicNode
	AnyTls      *AnyTlsNode
	Mieru       *MieruNode
	Hysteria    *HysteriaNode
	Hysteria2   *Hysteria2Node
	Common      *CommonNode
}

type NativeTLSSettings struct {
	ServerName    string   `json:"server_name"`
	AllowInsecure bool     `json:"allow_insecure"`
	ALPN          []string `json:"alpn"`
}

type CommonNode struct {
	Host       string      `json:"host"`
	ServerPort int         `json:"server_port"`
	ServerName string      `json:"server_name"`
	Routes     []Route     `json:"routes"`
	BaseConfig *BaseConfig `json:"base_config"`
}

type Route struct {
	Id          int         `json:"id"`
	Match       interface{} `json:"match"`
	Action      string      `json:"action"`
	ActionValue string      `json:"action_value"`
}
type BaseConfig struct {
	PushInterval any `json:"push_interval"`
	PullInterval any `json:"pull_interval"`
}

// VAllssNode is vmess and vless node info
type VAllssNode struct {
	CommonNode
	Tls                 int             `json:"tls"`
	TlsSettings         TlsSettings     `json:"tls_settings"`
	TlsSettingsBack     *TlsSettings    `json:"tlsSettings"`
	Network             string          `json:"network"`
	NetworkSettings     json.RawMessage `json:"network_settings"`
	NetworkSettingsBack json.RawMessage `json:"networkSettings"`
	Encryption          string          `json:"encryption"`
	EncryptionSettings  EncSettings     `json:"encryption_settings"`
	ServerName          string          `json:"server_name"`

	// vless only
	Flow          string        `json:"flow"`
	RealityConfig RealityConfig `json:"-"`
}

type TlsSettings struct {
	ServerName  string `json:"server_name"`
	Dest        string `json:"dest"`
	ServerPort  string `json:"server_port"`
	ShortId     string `json:"short_id"`
	PrivateKey  string `json:"private_key"`
	Mldsa65Seed string `json:"mldsa65Seed"`
	Xver        uint64 `json:"xver,string"`
}

type EncSettings struct {
	Mode          string `json:"mode"`
	Ticket        string `json:"ticket"`
	ServerPadding string `json:"server_padding"`
	PrivateKey    string `json:"private_key"`
}

// Xboard versions emit numeric Reality fields as either numbers or strings.
func (s *TlsSettings) UnmarshalJSON(data []byte) error {
	type plain TlsSettings
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"xver", "server_port"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 && raw[0] != '"' && !bytes.Equal(raw, []byte("null")) {
			var number json.Number
			if err := json.Unmarshal(raw, &number); err != nil {
				return fmt.Errorf("invalid TLS %s", key)
			}
			fields[key], _ = json.Marshal(number.String())
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, (*plain)(s))
}

// Empty PHP object arrays occur in all V2Ray transports, not just xhttp.
func normalizeNetworkSettings(data json.RawMessage) json.RawMessage {
	if bytes.Equal(bytes.TrimSpace(data), []byte("[]")) {
		return json.RawMessage(`{}`)
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return data
	}
	var normalize func(any)
	normalize = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, x := range v {
				if key == "headers" {
					if a, ok := x.([]any); ok && len(a) == 0 {
						v[key] = map[string]any{}
						continue
					}
				}
				normalize(x)
			}
		case []any:
			for _, x := range v {
				normalize(x)
			}
		}
	}
	normalize(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return data
	}
	return encoded
}

type RealityConfig struct {
	Xver         uint64 `json:"Xver"`
	MinClientVer string `json:"MinClientVer"`
	MaxClientVer string `json:"MaxClientVer"`
	MaxTimeDiff  string `json:"MaxTimeDiff"`
}

type ShadowsocksNode struct {
	CommonNode
	Cipher    string `json:"cipher"`
	ServerKey string `json:"server_key"`
}

type TrojanNode struct {
	CommonNode
	Network             string          `json:"network"`
	NetworkSettings     json.RawMessage `json:"networkSettings"`
	NetworkSettingsBack json.RawMessage `json:"network_settings"`
}

type TuicNode struct {
	CommonNode
	CongestionControl string `json:"congestion_control"`
	ZeroRTTHandshake  bool   `json:"zero_rtt_handshake"`
}

type AnyTlsNode struct {
	CommonNode
	PaddingScheme []string `json:"padding_scheme,omitempty"`
}

type MieruNode struct {
	CommonNode
	Transport      string `json:"transport"`
	TrafficPattern string `json:"traffic_pattern"`
}

type HysteriaNode struct {
	CommonNode
	UpMbps   int    `json:"up_mbps"`
	DownMbps int    `json:"down_mbps"`
	Obfs     string `json:"obfs"`
}

type Hysteria2Node struct {
	CommonNode
	Ignore_Client_Bandwidth bool   `json:"ignore_client_bandwidth"`
	UpMbps                  int    `json:"up_mbps"`
	DownMbps                int    `json:"down_mbps"`
	ObfsType                string `json:"obfs"`
	ObfsPassword            string `json:"obfs-password"`
}

type RawDNS struct {
	DNSMap  map[string]map[string]interface{}
	DNSJson []byte
}

type Rules struct {
	Regexp   []string
	Protocol []string
	// Match contains native Xboard domain/IP rules; only regexp: is a regexp.
	Match []string
}

func (c *Client) GetNodeInfo() (node *NodeInfo, err error) {
	path := c.endpoint("config")
	r, err := c.client.
		R().
		SetHeader("If-None-Match", c.nodeEtag).
		ForceContentType("application/json").
		Get(path)
	if err = c.checkResponse(r, path, err); err != nil {
		return nil, err
	}

	if r.StatusCode() == 304 {
		return nil, nil
	}
	hash := sha256.Sum256(r.Body())
	newBodyHash := hex.EncodeToString(hash[:])
	if c.responseBodyHash == newBodyHash {
		return nil, nil
	}

	if r != nil {
		defer func() {
			if r.RawBody() != nil {
				r.RawBody().Close()
			}
		}()
	} else {
		return nil, fmt.Errorf("received nil response")
	}
	node = &NodeInfo{
		Id:   c.NodeId,
		Type: c.NodeType,
		RawDNS: RawDNS{
			DNSMap:  make(map[string]map[string]interface{}),
			DNSJson: []byte(""),
		},
	}
	// parse protocol params
	var native struct {
		Version         int                `json:"version"`
		TLS             *int               `json:"tls"`
		TLSSettings     NativeTLSSettings  `json:"tls_settings"`
		TLSSettingsBack *NativeTLSSettings `json:"tlsSettings"`
	}
	if err := json.Unmarshal(r.Body(), &native); err != nil {
		return nil, fmt.Errorf("decode native node settings: %w", err)
	}
	if native.TLSSettingsBack != nil {
		native.TLSSettings = *native.TLSSettingsBack
	}
	node.TLSSettings = native.TLSSettings
	if c.NodeType == "hysteria" && native.Version == 2 {
		node.Type = "hysteria2"
	}
	if c.NodeType == "hysteria" && native.Version != 0 && native.Version != 1 && native.Version != 2 {
		return nil, fmt.Errorf("unsupported hysteria version %d", native.Version)
	}
	if c.NodeType == "tuic" && native.Version != 0 && native.Version != 5 {
		return nil, fmt.Errorf("unsupported TUIC version %d (requires version 5)", native.Version)
	}
	var cm *CommonNode
	switch node.Type {
	case "vmess", "vless":
		rsp := &VAllssNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode v2ray params error: %s", err)
		}
		if len(rsp.NetworkSettingsBack) > 0 {
			rsp.NetworkSettings = rsp.NetworkSettingsBack
			rsp.NetworkSettingsBack = nil
		}
		if rsp.TlsSettingsBack != nil {
			rsp.TlsSettings = *rsp.TlsSettingsBack
			rsp.TlsSettingsBack = nil
		}
		cm = &rsp.CommonNode
		node.VAllss = rsp
		rsp.NetworkSettings = normalizeNetworkSettings(rsp.NetworkSettings)
		rsp.Network = strings.ToLower(rsp.Network)
		if rsp.Network == "" {
			rsp.Network = "tcp"
		}
		node.Security = node.VAllss.Tls
	case "shadowsocks":
		rsp := &ShadowsocksNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode shadowsocks params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Shadowsocks = rsp
		node.Security = None
	case "trojan":
		rsp := &TrojanNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode trojan params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Trojan = rsp
		if len(rsp.NetworkSettingsBack) > 0 {
			rsp.NetworkSettings = rsp.NetworkSettingsBack
			rsp.NetworkSettingsBack = nil
		}
		rsp.NetworkSettings = normalizeNetworkSettings(rsp.NetworkSettings)
		rsp.Network = strings.ToLower(rsp.Network)
		if rsp.Network == "" {
			rsp.Network = "tcp"
		}
		node.Security = Tls
		if native.TLS != nil {
			node.Security = *native.TLS
		}
		if node.Security == Reality {
			// Reality uses the same handshake/key fields as VLESS.
			var settings VAllssNode
			if err := json.Unmarshal(r.Body(), &settings); err != nil {
				return nil, err
			}
			if settings.TlsSettingsBack != nil {
				settings.TlsSettings = *settings.TlsSettingsBack
			}
			node.VAllss = &settings
		}
	case "tuic":
		rsp := &TuicNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode tuic params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Tuic = rsp
		node.Security = Tls
	case "anytls":
		rsp := &AnyTlsNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode anytls params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.AnyTls = rsp
		node.Security = Tls
	case "mieru":
		rsp := &MieruNode{}
		if err := json.Unmarshal(r.Body(), rsp); err != nil {
			return nil, fmt.Errorf("decode mieru params: %w", err)
		}
		rsp.Transport = strings.ToLower(rsp.Transport)
		if rsp.Transport == "" {
			rsp.Transport = "tcp"
		}
		if rsp.Transport != "tcp" && rsp.Transport != "udp" {
			return nil, fmt.Errorf("unsupported mieru transport %q", rsp.Transport)
		}
		cm = &rsp.CommonNode
		node.Mieru = rsp
		node.Security = None
	case "hysteria":
		rsp := &HysteriaNode{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode hysteria params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Hysteria = rsp
		node.Security = Tls
	case "hysteria2":
		rsp := &Hysteria2Node{}
		err = json.Unmarshal(r.Body(), rsp)
		if err != nil {
			return nil, fmt.Errorf("decode hysteria2 params error: %s", err)
		}
		cm = &rsp.CommonNode
		node.Hysteria2 = rsp
		node.Security = Tls
	}
	if cm == nil {
		return nil, fmt.Errorf("unsupported node type %q", c.NodeType)
	}
	if cm.BaseConfig == nil {
		cm.BaseConfig = &BaseConfig{}
	}

	// parse rules and dns
	for i := range cm.Routes {
		matchs, err := routeMatches(cm.Routes[i].Match)
		if err != nil {
			return nil, fmt.Errorf("route %d: %w", cm.Routes[i].Id, err)
		}
		switch cm.Routes[i].Action {
		case "block":
			for _, v := range matchs {
				v = strings.TrimSpace(v)
				if v == "" {
					continue
				}
				if strings.HasPrefix(v, "protocol:") {
					// protocol
					node.Rules.Protocol = append(node.Rules.Protocol, strings.TrimPrefix(v, "protocol:"))
				} else if strings.HasPrefix(v, "regexp:") {
					node.Rules.Regexp = append(node.Rules.Regexp, strings.TrimPrefix(v, "regexp:"))
				} else {
					node.Rules.Match = append(node.Rules.Match, v)
				}
			}
		case "dns":
			if len(matchs) == 0 {
				continue
			}
			var domains []string
			domains = append(domains, matchs...)
			if matchs[0] != "main" {
				node.RawDNS.DNSMap[strconv.Itoa(i)] = map[string]interface{}{
					"address": cm.Routes[i].ActionValue,
					"domains": domains,
				}
			} else {
				dns := []byte(strings.Join(matchs[1:], ""))
				node.RawDNS.DNSJson = dns
			}
		}
	}

	// set interval
	node.PushInterval = intervalToTime(cm.BaseConfig.PushInterval)
	node.PullInterval = intervalToTime(cm.BaseConfig.PullInterval)

	node.Common = cm
	// clear
	cm.Routes = nil
	cm.BaseConfig = nil
	c.responseBodyHash = newBodyHash
	c.nodeEtag = r.Header().Get("ETag")

	return node, nil
}

func routeMatches(value interface{}) ([]string, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case string:
		return strings.Split(v, ","), nil
	case []string:
		return v, nil
	case []interface{}:
		matches := make([]string, len(v))
		for i, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("match %d must be a string", i)
			}
			matches[i] = text
		}
		return matches, nil
	default:
		return nil, fmt.Errorf("match must be a string or string array")
	}
}

func intervalToTime(i interface{}) time.Duration {
	var seconds int
	switch v := i.(type) {
	case int:
		seconds = v
	case float64:
		seconds = int(v)
	case string:
		seconds, _ = strconv.Atoi(v)
	}
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}

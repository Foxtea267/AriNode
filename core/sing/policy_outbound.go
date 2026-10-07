package sing

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/Foxtea267/AriNode/common/nodepolicy"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
)

type policyOutbound struct {
	option.Outbound
	Endpoint bool
}

// The panel's settings may be Xray's servers/vnext layout, or sing-box's flat
// layout. Decode protocol fields explicitly; never treat nested settings as a
// valid sing-box outbound. Multiple servers are rejected rather than discarded.
func singOutbound(ctx context.Context, o nodepolicy.OutboundConfig, tags map[string]string) (policyOutbound, error) {
	switch o.Protocol {
	case "vmess", "vless", "trojan", "shadowsocks", "socks", "http", "wireguard", "tuic", "hysteria2", "anytls", "mieru":
	case "naive":
		return policyOutbound{}, fmt.Errorf("outbound %q: this sing-box build has no Naive outbound implementation", o.Tag)
	default:
		return policyOutbound{}, fmt.Errorf("outbound %q: unsupported sing-box protocol %q", o.Tag, o.Protocol)
	}
	fields, err := nodepolicy.CloneMap(o.Settings)
	if err != nil {
		return policyOutbound{}, err
	}
	fail := func() (policyOutbound, error) {
		return policyOutbound{}, fmt.Errorf("outbound %q: invalid %s settings (credentials omitted)", o.Tag, o.Protocol)
	}
	if _, ok := fields["detour"]; ok {
		return policyOutbound{}, fmt.Errorf("outbound %q: use proxy_tag instead of settings.detour", o.Tag)
	}
	if _, ok := fields["proxy_tag"]; ok {
		return policyOutbound{}, fmt.Errorf("outbound %q: proxy_tag belongs outside settings", o.Tag)
	}
	if o.Protocol == "wireguard" {
		if err := normalizeWireGuard(fields); err != nil {
			return fail()
		}
	} else if _, ok := fields["servers"]; ok {
		switch o.Protocol {
		case "socks", "http", "trojan", "shadowsocks":
		default:
			return fail()
		}
		var wrapped struct {
			Servers []struct {
				Address  string `json:"address"`
				Port     uint16 `json:"port"`
				Password string `json:"password"`
				Method   string `json:"method"`
				Users    []struct {
					User string `json:"user"`
					Pass string `json:"pass"`
				} `json:"users"`
			} `json:"servers"`
		}
		data, _ := json.Marshal(fields)
		if err := json.Unmarshal(data, &wrapped); err != nil || len(wrapped.Servers) != 1 {
			return fail()
		}
		server := wrapped.Servers[0]
		if server.Address == "" || server.Port == 0 {
			return fail()
		}
		fields["server"] = server.Address
		fields["server_port"] = server.Port
		delete(fields, "servers")
		switch o.Protocol {
		case "socks", "http":
			if len(server.Users) > 1 {
				return fail()
			}
			if len(server.Users) == 1 {
				fields["username"] = server.Users[0].User
				fields["password"] = server.Users[0].Pass
			}
		case "trojan":
			fields["password"] = server.Password
		case "shadowsocks":
			fields["method"] = server.Method
			fields["password"] = server.Password
		}
	} else if _, ok := fields["vnext"]; ok {
		if o.Protocol != "vless" && o.Protocol != "vmess" {
			return fail()
		}
		var wrapped struct {
			VNext []struct {
				Address string `json:"address"`
				Port    uint16 `json:"port"`
				Users   []struct {
					ID         string `json:"id"`
					Flow       string `json:"flow"`
					Security   string `json:"security"`
					AlterID    int    `json:"alterId"`
					Encryption string `json:"encryption"`
				} `json:"users"`
			} `json:"vnext"`
		}
		data, _ := json.Marshal(fields)
		if err := json.Unmarshal(data, &wrapped); err != nil || len(wrapped.VNext) != 1 || len(wrapped.VNext[0].Users) != 1 {
			return fail()
		}
		server := wrapped.VNext[0]
		user := server.Users[0]
		if server.Address == "" || server.Port == 0 || user.ID == "" {
			return fail()
		}
		fields["server"] = server.Address
		fields["server_port"] = server.Port
		fields["uuid"] = user.ID
		delete(fields, "vnext")
		if o.Protocol == "vmess" {
			fields["security"] = user.Security
			if user.Security == "" {
				fields["security"] = "auto"
			}
			fields["alter_id"] = user.AlterID
		} else {
			if user.Encryption != "" && user.Encryption != "none" {
				return fail()
			}
			fields["flow"] = user.Flow
		}
	}
	if value, ok := fields["streamSettings"]; ok {
		if err := normalizeStream(fields, value); err != nil {
			return fail()
		}
		delete(fields, "streamSettings")
	}
	fields["type"] = o.Protocol
	fields["tag"] = tags[o.Tag]
	if o.ProxyTag != "" {
		fields["detour"] = tags[o.ProxyTag]
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return fail()
	}
	if o.Protocol == "wireguard" {
		endpoint, err := sjson.UnmarshalExtendedContext[option.Endpoint](ctx, data)
		if err != nil {
			return fail()
		}
		return policyOutbound{Outbound: option.Outbound{Type: endpoint.Type, Tag: endpoint.Tag, Options: endpoint.Options}, Endpoint: true}, nil
	}
	outbound, err := sjson.UnmarshalExtendedContext[option.Outbound](ctx, data)
	if err != nil {
		return fail()
	}
	// Ports decoded as uint16 by native options, so invalid numeric ranges fail.
	if o.Protocol != "mieru" {
		var endpoint struct {
			Server string `json:"server"`
			Port   uint16 `json:"server_port"`
		}
		if err = json.Unmarshal(data, &endpoint); err != nil || endpoint.Server == "" || endpoint.Port == 0 {
			return fail()
		}
	}
	return policyOutbound{Outbound: outbound}, nil
}

func normalizeWireGuard(f map[string]any) error {
	rename := func(old, key string) {
		if v, ok := f[old]; ok {
			if _, exists := f[key]; exists {
				delete(f, old)
				return
			}
			f[key] = v
			delete(f, old)
		}
	}
	rename("secret_key", "private_key")
	rename("secretKey", "private_key")
	rename("local_address", "address")
	rename("system_interface", "system")
	rename("interface_name", "name")
	if peers, ok := f["peers"].([]any); ok {
		for _, value := range peers {
			peer, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid peer")
			}
			for old, key := range map[string]string{"publicKey": "public_key", "preSharedKey": "pre_shared_key", "allowedIPs": "allowed_ips", "keepAlive": "persistent_keepalive_interval", "server": "address", "server_port": "port"} {
				if v, ok := peer[old]; ok {
					peer[key] = v
					delete(peer, old)
				}
			}
			if v, ok := peer["endpoint"]; ok {
				endpoint, ok := v.(string)
				if !ok {
					return fmt.Errorf("invalid endpoint")
				}
				host, port, err := net.SplitHostPort(endpoint)
				if err != nil {
					return err
				}
				number, err := strconv.Atoi(port)
				if err != nil || number < 1 || number > 65535 {
					return fmt.Errorf("invalid endpoint port")
				}
				peer["address"] = host
				peer["port"] = number
				delete(peer, "endpoint")
			}
			if _, ok := peer["allowed_ips"]; !ok {
				peer["allowed_ips"] = []string{"0.0.0.0/0", "::/0"}
			}
		}
	}
	if _, ok := f["server"]; ok {
		peer := map[string]any{"address": f["server"], "port": f["server_port"], "public_key": f["peer_public_key"], "allowed_ips": []string{"0.0.0.0/0", "::/0"}}
		for _, key := range []string{"pre_shared_key", "reserved"} {
			if v, ok := f[key]; ok {
				peer[key] = v
				delete(f, key)
			}
		}
		f["peers"] = []any{peer}
		delete(f, "server")
		delete(f, "server_port")
		delete(f, "peer_public_key")
	}
	return nil
}

func normalizeStream(fields map[string]any, value any) error {
	stream, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("invalid transport")
	}
	for _, key := range []string{"network", "security"} {
		if value, exists := stream[key]; exists {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("invalid transport field")
			}
		}
	}
	allowed := map[string]map[string]bool{"tlsSettings": {"serverName": true, "allowInsecure": true, "alpn": true, "fingerprint": true}, "wsSettings": {"path": true, "headers": true}, "grpcSettings": {"serviceName": true}, "httpSettings": {"path": true, "host": true}, "httpupgradeSettings": {"path": true, "host": true}}
	for key, keys := range allowed {
		if value, exists := stream[key]; exists {
			object, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid transport settings")
			}
			for field := range object {
				if !keys[field] {
					return fmt.Errorf("unsupported transport setting")
				}
			}
		}
	}
	for key := range stream {
		switch key {
		case "network", "security", "tlsSettings", "wsSettings", "grpcSettings", "httpSettings", "httpupgradeSettings":
		default:
			return fmt.Errorf("unsupported Xray transport field")
		}
	}
	if security, _ := stream["security"].(string); security != "" && security != "none" {
		if security != "tls" {
			return fmt.Errorf("unsupported transport security")
		}
		tls := map[string]any{"enabled": true}
		if settings, ok := stream["tlsSettings"].(map[string]any); ok {
			if fingerprint, ok := settings["fingerprint"]; ok {
				tls["utls"] = map[string]any{"enabled": true, "fingerprint": fingerprint}
			}
			for old, key := range map[string]string{"serverName": "server_name", "allowInsecure": "insecure", "alpn": "alpn"} {
				if v, ok := settings[old]; ok {
					tls[key] = v
				}
			}
		}
		fields["tls"] = tls
	}
	network, _ := stream["network"].(string)
	if network == "" || network == "tcp" {
		return nil
	}
	transport := map[string]any{"type": network}
	switch network {
	case "ws":
		if settings, ok := stream["wsSettings"].(map[string]any); ok {
			for _, key := range []string{"path", "headers"} {
				if v, ok := settings[key]; ok {
					transport[key] = v
				}
			}
		}
	case "grpc":
		if settings, ok := stream["grpcSettings"].(map[string]any); ok {
			transport["service_name"] = settings["serviceName"]
		}
	case "http", "h2":
		transport["type"] = "http"
		if settings, ok := stream["httpSettings"].(map[string]any); ok {
			for _, key := range []string{"path", "host"} {
				if v, ok := settings[key]; ok {
					transport[key] = v
				}
			}
		}
	case "httpupgrade":
		if settings, ok := stream["httpupgradeSettings"].(map[string]any); ok {
			for _, key := range []string{"path", "host"} {
				if v, ok := settings[key]; ok {
					transport[key] = v
				}
			}
		}
	default:
		return fmt.Errorf("unsupported transport")
	}
	fields["transport"] = transport
	return nil
}

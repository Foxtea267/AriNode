package nodepolicy

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

// Raw rules remain kernel-native. Only outbound references are normalized here;
// the selected kernel performs strict native parsing afterwards.
func ValidateRaw(rule map[string]any, tags map[string]bool, depth int) error {
	if rule == nil {
		return fmt.Errorf("route must be an object")
	}
	if depth > 32 {
		return fmt.Errorf("logical rule nesting exceeds 32")
	}
	for _, key := range []string{"port", "port_range", "source_port", "source_port_range", "sourcePort", "localPort"} {
		if value, ok := rule[key]; ok {
			values := []any{value}
			if list, ok := value.([]any); ok {
				values = list
			}
			for _, v := range values {
				switch n := v.(type) {
				case float64:
					if n < 1 || n > 65535 || n != float64(uint16(n)) {
						return fmt.Errorf("invalid %s", key)
					}
				case string:
					for _, p := range strings.Split(n, ",") {
						if _, _, err := PortRange(strings.ReplaceAll(p, ":", "-")); err != nil {
							return fmt.Errorf("invalid %s", key)
						}
					}
				default:
					return fmt.Errorf("invalid %s", key)
				}
			}
		}
	}
	if value, ok := rule["network"]; ok {
		var networks []string
		switch v := value.(type) {
		case string:
			networks = strings.Split(v, ",")
		case []any:
			for _, n := range v {
				s, ok := n.(string)
				if !ok {
					return fmt.Errorf("invalid network")
				}
				networks = append(networks, s)
			}
		case []string:
			networks = v
		default:
			return fmt.Errorf("invalid network")
		}
		for _, network := range networks {
			if network != "tcp" && network != "udp" {
				return fmt.Errorf("invalid network %q", network)
			}
		}
	}
	for _, key := range []string{"ip", "sourceIP", "source", "localIP", "ip_cidr", "source_ip_cidr"} {
		if value, ok := rule[key]; ok {
			var cidrs []string
			switch v := value.(type) {
			case string:
				cidrs = []string{v}
			case []string:
				cidrs = v
			case []any:
				for _, item := range v {
					s, ok := item.(string)
					if !ok {
						return fmt.Errorf("invalid %s", key)
					}
					cidrs = append(cidrs, s)
				}
			default:
				return fmt.Errorf("invalid %s", key)
			}
			for _, cidr := range cidrs {
				if strings.HasPrefix(cidr, "geoip:") && key != "ip_cidr" && key != "source_ip_cidr" {
					continue
				}
				if _, err := netip.ParsePrefix(cidr); err != nil {
					if _, err := netip.ParseAddr(cidr); err != nil {
						return fmt.Errorf("invalid %s CIDR", key)
					}
				}
			}
		}
	}
	for _, key := range []string{"outbound", "outboundTag"} {
		if value, ok := rule[key]; ok {
			tag, ok := value.(string)
			if !ok {
				return fmt.Errorf("%s must be an outbound tag string", key)
			}
			if !tags[tag] {
				return fmt.Errorf("route references unknown outbound tag %q", tag)
			}
		}
	}
	if _, ok := rule["balancerTag"]; ok {
		return fmt.Errorf("panel routes cannot reference a shared-core balancer")
	}
	if children, ok := rule["rules"]; ok {
		list, ok := children.([]any)
		if !ok {
			return fmt.Errorf("rules must be an array")
		}
		for _, child := range list {
			m, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("rule must be an object")
			}
			if err := ValidateRaw(m, tags, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func CloneMap(input map[string]any) (map[string]any, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("invalid configuration object")
	}
	result := map[string]any{}
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("invalid configuration object")
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}
func RewriteRaw(input map[string]any, tags map[string]string) (map[string]any, error) {
	result, err := CloneMap(input)
	if err != nil {
		return nil, err
	}
	var rewrite func(map[string]any) error
	rewrite = func(m map[string]any) error {
		for _, key := range []string{"outbound", "outboundTag"} {
			if v, ok := m[key]; ok {
				tag, ok := v.(string)
				if !ok {
					return fmt.Errorf("%s must be a tag", key)
				}
				target, err := Resolve(tags, tag)
				if err != nil {
					return err
				}
				m[key] = target
			}
		}
		if children, ok := m["rules"].([]any); ok {
			for _, child := range children {
				childMap, ok := child.(map[string]any)
				if !ok {
					return fmt.Errorf("rule must be an object")
				}
				if err := rewrite(childMap); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return result, rewrite(result)
}

// Metadata deliberately excludes outbound settings and user credentials.
type RouteLog struct {
	Name     string
	Outbound string
}

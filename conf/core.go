package conf

import (
	"encoding/json"
	"strings"
)

func NormalizeCoreType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "sing", "singbox", "sing-box":
		return "sing"
	case "xray":
		return "xray"
	case "hysteria2":
		return "hysteria2"
	default:
		return value
	}
}

func DefaultCoreConfig() CoreConfig {
	return CoreConfig{Type: "sing", SingConfig: NewSingConfig()}
}

type CoreConfig struct {
	Type            string           `json:"Type"`
	Name            string           `json:"Name"`
	XrayConfig      *XrayConfig      `json:"-"`
	SingConfig      *SingConfig      `json:"-"`
	Hysteria2Config *Hysteria2Config `json:"-"`
}

type _CoreConfig CoreConfig

func (c *CoreConfig) UnmarshalJSON(b []byte) error {
	err := json.Unmarshal(b, (*_CoreConfig)(c))
	if err != nil {
		return err
	}
	c.Type = NormalizeCoreType(c.Type)
	switch c.Type {
	case "xray":
		c.XrayConfig = NewXrayConfig()
		return json.Unmarshal(b, c.XrayConfig)
	case "sing":
		c.SingConfig = NewSingConfig()
		return json.Unmarshal(b, c.SingConfig)
	case "hysteria2":
		c.Hysteria2Config = NewHysteria2Config()
		return json.Unmarshal(b, c.Hysteria2Config)
	}
	return nil
}

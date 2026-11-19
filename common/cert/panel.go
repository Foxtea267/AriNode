package cert

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
)

// PanelConfig is XBoard's certificate payload, shared by every TLS protocol.
type PanelConfig struct {
	CertMode    string            `json:"cert_mode,omitempty"`
	Domain      string            `json:"domain,omitempty"`
	Email       string            `json:"email,omitempty"`
	DNSProvider string            `json:"dns_provider,omitempty"`
	DNSEnv      map[string]string `json:"dns_env,omitempty"`
	HTTPPort    int               `json:"http_port,omitempty"`
	CertFile    string            `json:"cert_file,omitempty"`
	KeyFile     string            `json:"key_file,omitempty"`
	CertContent string            `json:"cert_content,omitempty"`
	KeyContent  string            `json:"key_content,omitempty"`
}

func (c *PanelConfig) UnmarshalJSON(data []byte) error {
	type plain PanelConfig
	var raw struct {
		plain
		Mode     string          `json:"mode"`
		HTTPPort json.RawMessage `json:"http_port"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("invalid panel certificate configuration")
	}
	port, err := panelHTTPPort(raw.HTTPPort)
	if err != nil {
		return err
	}
	next := PanelConfig(raw.plain)
	next.HTTPPort = port
	if next.CertMode == "" {
		next.CertMode = raw.Mode
	}
	next.CertMode = strings.ToLower(strings.TrimSpace(next.CertMode))
	next.Domain = strings.TrimSpace(next.Domain)
	next.CertContent = NormalizePEM(next.CertContent)
	next.KeyContent = NormalizePEM(next.KeyContent)
	if next.CertMode == "content" && next.CertContent != "" && next.KeyContent != "" {
		// Different PEM wrapping or PKCS#1/SEC1 vs PKCS#8 is not key rotation.
		// Preserve invalid material for the controller's safe error/retry path.
		if certificate, key, err := CanonicalPair(next.CertContent, next.KeyContent); err == nil {
			next.CertContent, next.KeyContent = certificate, key
		}
	}
	*c = next
	return nil
}

func panelHTTPPort(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	// PHP form values can reach the node as strings or JSON numbers.
	// Normalize both into the existing integer runtime field. Zero/blank
	// retains the ACME provider's default port; never echo invalid material.
	value := string(raw)
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, fmt.Errorf("invalid panel certificate http_port")
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return 0, nil
		}
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return 0, fmt.Errorf("panel certificate http_port must be an integer from 0 to 65535")
		}
	}
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("panel certificate http_port must be an integer from 0 to 65535")
	}
	return int(port), nil
}

func CanonicalPair(certificate, key string) (string, string, error) {
	if _, err := Parse([]byte(certificate), []byte(key)); err != nil {
		return "", "", err
	}
	pair, err := tls.X509KeyPair([]byte(certificate), []byte(key))
	if err != nil {
		return "", "", err
	}
	var chain strings.Builder
	for _, der := range pair.Certificate {
		chain.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		return "", "", fmt.Errorf("cannot normalize certificate private key")
	}
	return chain.String(), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})), nil
}

func NormalizePEM(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	if value == "" {
		return ""
	}
	return value + "\n"
}

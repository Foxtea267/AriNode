package cert

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
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
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("invalid panel certificate configuration")
	}
	*c = PanelConfig(raw.plain)
	if c.CertMode == "" {
		c.CertMode = raw.Mode
	}
	c.CertMode = strings.ToLower(strings.TrimSpace(c.CertMode))
	c.Domain = strings.TrimSpace(c.Domain)
	c.CertContent = NormalizePEM(c.CertContent)
	c.KeyContent = NormalizePEM(c.KeyContent)
	if c.CertMode == "content" && c.CertContent != "" && c.KeyContent != "" {
		// Different PEM wrapping or PKCS#1/SEC1 vs PKCS#8 is not key rotation.
		// Preserve invalid material for the controller's safe error/retry path.
		if certificate, key, err := CanonicalPair(c.CertContent, c.KeyContent); err == nil {
			c.CertContent, c.KeyContent = certificate, key
		}
	}
	return nil
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

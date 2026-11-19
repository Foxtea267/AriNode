package cert

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPanelCertificateHTTPPortCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want int
	}{
		{"number", "3456", 3456},
		{"string", `"3456"`, 3456},
		{"trimmed string", `" 3456 "`, 3456},
		{"zero", "0", 0},
		{"string zero", `"0"`, 0},
		{"empty string", `""`, 0},
		{"blank string", `" "`, 0},
		{"null", "null", 0},
		{"minimum", "1", 1},
		{"maximum", `"65535"`, 65535},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"mode":" DNS ","domain":" node.example.com ","dns_provider":"cloudflare","dns_env":{"TEST_TOKEN":"test-secret"},"http_port":%s}`, tc.raw)
			var c PanelConfig
			if err := json.Unmarshal([]byte(body), &c); err != nil {
				t.Fatal(err)
			}
			if c.HTTPPort != tc.want || c.CertMode != "dns" || c.Domain != "node.example.com" ||
				c.DNSProvider != "cloudflare" || c.DNSEnv["TEST_TOKEN"] != "test-secret" {
				t.Fatal("certificate fields were changed or dropped")
			}
			encoded, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if tc.want != 0 && fields["http_port"] != float64(tc.want) {
				t.Fatalf("port must serialize as a number, got %T", fields["http_port"])
			}
		})
	}
	c := PanelConfig{HTTPPort: 3456}
	if err := json.Unmarshal([]byte(`{"cert_mode":"self"}`), &c); err != nil || c.HTTPPort != 0 || c.CertMode != "self" {
		t.Fatal("omitted port retained a value from a previous decode")
	}
}

func TestPanelCertificateRejectsInvalidHTTPPortWithoutLeakingSecrets(t *testing.T) {
	for _, raw := range []string{`"not-a-port-secret"`, `true`, `{}`, `[]`, `-1`, `"-1"`, `65536`,
		`"65536"`, `3456.5`, `"3456.5"`, `3e3`, `"3e3"`, `"9999999999999999999999999"`} {
		t.Run(raw, func(t *testing.T) {
			c := PanelConfig{CertMode: "self", HTTPPort: 1234}
			before := c
			err := json.Unmarshal([]byte(fmt.Sprintf(`{"cert_mode":"dns","key_content":"test-private-key-secret","http_port":%s}`, raw)), &c)
			if err == nil {
				t.Fatal("invalid port was silently accepted")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), raw) {
				t.Fatal("decode error leaked certificate or invalid field material")
			}
			if !reflect.DeepEqual(c, before) {
				t.Fatal("failed certificate decode partially modified the existing config")
			}
		})
	}
}

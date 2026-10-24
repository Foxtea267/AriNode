package panel

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Foxtea267/AriNode/common/cert"
	"github.com/Foxtea267/AriNode/conf"
)

func TestCertificateDecodeAndCanonicalConfigHash(t *testing.T) {
	for _, protocol := range []string{"anytls", "hysteria2", "tuic", "trojan", "vless"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int64
			var body atomic.Value
			certificate, key, _ := cert.SelfSigned("node.example.com")
			payload := map[string]any{"server_port": 25331, "tls": 1, "network": "tcp", "tls_settings": map[string]any{"server_name": "node.example.com", "allow_insecure": false},
				"cert_config": map[string]any{"mode": "content", "cert_content": strings.Join(certificate, ""), "key_content": strings.Join(key, "")}}
			set := func() { data, _ := json.Marshal(payload); body.Store(string(data)) }
			set()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Different bytes/ETags and unknown volatile metadata on every pull.
				fmt.Fprintf(w, "{\"nonce\":%d,\"timestamp\":%d,%s}", calls.Add(1), calls.Load(), strings.TrimPrefix(strings.TrimSuffix(body.Load().(string), "}"), "{"))
			}))
			defer server.Close()
			client, _ := New(&conf.ApiConfig{APIHost: server.URL, Key: "test", NodeID: 244, NodeType: protocol, MachineID: 39})
			first, err := client.GetNodeInfo()
			if err != nil {
				t.Fatal(err)
			}
			if first.Common.CertConfig.CertMode != "content" || first.Common.CertConfig.CertContent != strings.Join(certificate, "") || first.ConfigHash == "" {
				t.Fatal("panel certificate lost")
			}
			for i := 0; i < 20; i++ {
				if next, err := client.GetNodeInfo(); err != nil || next != nil {
					t.Fatal("volatile response metadata triggered reload", err)
				}
			}
			certPayload := payload["cert_config"].(map[string]any)
			delete(certPayload, "mode")
			certPayload["cert_mode"] = "content"
			certPayload["cert_content"] = strings.ReplaceAll(strings.Join(certificate, ""), "\n", "\r\n")
			set()
			if next, err := client.GetNodeInfo(); err != nil || next != nil {
				t.Fatal("mode alias or PEM line endings triggered reload", err)
			}
			block, _ := pem.Decode([]byte(strings.Join(key, "")))
			private, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
			sec1, _ := x509.MarshalECPrivateKey(private.(*ecdsa.PrivateKey))
			certPayload["key_content"] = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1}))
			set()
			if next, err := client.GetNodeInfo(); err != nil || next != nil {
				t.Fatal("key encoding change triggered reload", err)
			}
			cert2, key2, _ := cert.SelfSigned("node.example.com")
			certPayload["cert_content"], certPayload["key_content"] = strings.Join(cert2, ""), strings.Join(key2, "")
			set()
			rotated, err := client.GetNodeInfo()
			if err != nil || rotated == nil || rotated.ConfigHash == first.ConfigHash || InboundConfigEqual(first, rotated) {
				t.Fatal("certificate rotation did not change inbound", err)
			}
			if next, err := client.GetNodeInfo(); err != nil || next != nil {
				t.Fatal("rotation was returned more than once", err)
			}
		})
	}
}

package node

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateSelfSignedCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := generateSelfSslCertificate("example.com", certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	for path, wantType := range map[string]string{certPath: "CERTIFICATE", keyPath: "RSA PRIVATE KEY"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(data)
		if block == nil || block.Type != wantType {
			t.Fatalf("invalid PEM in %s", path)
		}
	}
}

package format

import (
	"encoding/base64"
	"fmt"
)

// ShadowsocksUserPassword preserves the panel's SS2022 key derivation and
// rejects short credentials before slicing. Errors never include the key.
func ShadowsocksUserPassword(method, credential string) (string, error) {
	if credential == "" {
		return "", fmt.Errorf("empty Shadowsocks user credential")
	}
	var size int
	switch method {
	case "2022-blake3-aes-128-gcm":
		size = 16
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		size = 32
	default:
		return credential, nil
	}
	if len(credential) < size {
		return "", fmt.Errorf("Shadowsocks 2022 user credential must contain at least %d bytes", size)
	}
	return base64.StdEncoding.EncodeToString([]byte(credential[:size])), nil
}

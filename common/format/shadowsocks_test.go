package format

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestShadowsocks2022PanelCredentialsNeedFullKeyLength(t *testing.T) {
	for method, size := range map[string]int{
		"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32, "2022-blake3-chacha20-poly1305": 32,
	} {
		t.Run(method, func(t *testing.T) {
			key := strings.Repeat("k", size-1)
			if _, err := ShadowsocksUserPassword(method, key); err == nil || strings.Contains(err.Error(), key) {
				t.Fatalf("short key accepted or exposed: %v", err)
			}
			key += "k"
			password, err := ShadowsocksUserPassword(method, key+"suffix")
			if err != nil || password != base64.StdEncoding.EncodeToString([]byte(key)) {
				t.Fatal("existing panel key derivation changed")
			}
		})
	}
}

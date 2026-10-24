package cert

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestContentStoreIdempotenceAndRotation(t *testing.T) {
	store := Store{Root: t.TempDir()}
	certificate, key, err := SelfSigned("node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Save("panel-a/node-244", strings.Join(certificate, "\n"), strings.Join(key, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1600000000, 0)
	for _, path := range []string{first.CertPath, first.KeyPath} {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe certificate permission: %v", info.Mode())
		}
	}
	second, err := store.Save("panel-a/node-244", strings.Join(certificate, "\n"), strings.Join(key, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if first.CertPath != second.CertPath || first.Fingerprint != second.Fingerprint {
		t.Fatal("unchanged material was republished")
	}
	for _, path := range []string{first.CertPath, first.KeyPath} {
		info, _ := os.Stat(path)
		if !info.ModTime().Equal(stamp) {
			t.Fatal("unchanged certificate was rewritten")
		}
	}
	cert2, key2, _ := SelfSigned("node.example.com")
	if _, err := store.Save("panel-a/node-244", strings.Join(certificate, ""), strings.Join(key2, "")); err == nil {
		t.Fatal("mismatched key accepted")
	}
	rotated, err := store.Save("panel-a/node-244", strings.Join(cert2, ""), strings.Join(key2, ""))
	if err != nil {
		t.Fatal(err)
	}
	if rotated.CertPath == first.CertPath || rotated.Fingerprint == first.Fingerprint {
		t.Fatal("changed certificate did not rotate")
	}
	if _, err := Load(first.CertPath, first.KeyPath); err != nil {
		t.Fatal("old generation needed by rollback was destroyed", err)
	}
	other, err := store.Save("panel-b/node-244", strings.Join(certificate, ""), strings.Join(key, ""))
	if err != nil || other.CertPath == first.CertPath {
		t.Fatal("panel namespaces share certificate files", err)
	}
}

func TestAutomaticSelfSignedPersistsAcrossStoreInstances(t *testing.T) {
	root := t.TempDir()
	first, err := (Store{Root: root}).SelfSigned("panel/node", "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := (Store{Root: root}).SelfSigned("panel/node", "node.example.com")
		if err != nil || next.Fingerprint != first.Fingerprint || next.CertPath != first.CertPath {
			t.Fatal("self-signed certificate regenerated", err)
		}
	}
	next, err := (Store{Root: root}).SelfSigned("panel/node", "other.example.com")
	if err != nil || next.Fingerprint == first.Fingerprint {
		t.Fatal("server_name change did not issue a matching certificate", err)
	}
}

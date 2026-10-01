package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStableReleaseValidation(t *testing.T) {
	const tag = "20261001-abcdef12"
	const name = "arinode-linux-amd64.tar.gz"
	valid := Release{Tag: tag, Assets: []Asset{{Name: name, URL: "https://github.com/Foxtea267/AriNode/releases/download/" + tag + "/" + name, Digest: "sha256:" + fmt.Sprintf("%064x", 1), Size: 12}}}
	for _, tc := range []struct {
		name    string
		mutate  func(*Release)
		wantErr bool
	}{
		{"stable", func(*Release) {}, false},
		{"draft", func(r *Release) { r.Draft = true }, true},
		{"prerelease", func(r *Release) { r.Prerelease = true }, true},
		{"bad version", func(r *Release) { r.Tag = "v1.0" }, true},
		{"missing digest", func(r *Release) { r.Assets[0].Digest = "" }, true},
		{"foreign asset", func(r *Release) { r.Assets[0].URL = "https://example.com/arinode.tar.gz" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := valid
			release.Assets = append([]Asset(nil), valid.Assets...)
			tc.mutate(&release)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(release) }))
			defer server.Close()
			client := NewClient()
			client.URL = server.URL
			_, _, err := client.Latest(context.Background(), "amd64")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Latest error=%v, want error=%t", err, tc.wantErr)
			}
		})
	}
	if Newer(tag, tag) || Newer("20261002-12345678", tag) || !Newer("dev", tag) {
		t.Fatal("incorrect version comparison")
	}
}

func testArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, data := range entries {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestVerifiedUpgradeRollback(t *testing.T) {
	archiveBytes := testArchive(t, map[string]string{"arinode": "new-service", "anctl": "new-control"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archiveBytes) }))
	defer server.Close()
	digest := sha256.Sum256(archiveBytes)
	asset := Asset{URL: server.URL, Size: int64(len(archiveBytes)), Digest: "sha256:" + hex.EncodeToString(digest[:])}
	directory := t.TempDir()
	for name, data := range map[string]string{"arinode": "old-service", "anctl": "old-control"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(data), 0755); err != nil {
			t.Fatal(err)
		}
	}
	client := NewClient()
	path, err := client.Download(context.Background(), asset, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	staging, err := ExtractBinaries(path, directory)
	if err != nil {
		t.Fatal(err)
	}
	restarts := 0
	err = ReplaceBinaries(staging, directory, func() error {
		restarts++
		if restarts == 1 {
			return fmt.Errorf("simulated restart failure")
		}
		return nil
	})
	if err == nil || restarts != 2 {
		t.Fatalf("rollback was not triggered: err=%v restarts=%d", err, restarts)
	}
	for name, expected := range map[string]string{"arinode": "old-service", "anctl": "old-control"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != expected {
			t.Fatalf("%s was not restored: %q %v", name, data, err)
		}
	}
	staging, err = ExtractBinaries(path, directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplaceBinaries(staging, directory, nil); err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]string{"arinode": "new-service", "anctl": "new-control"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != expected {
			t.Fatalf("%s was not installed: %q %v", name, data, err)
		}
	}
	asset.Digest = "sha256:" + fmt.Sprintf("%064x", 2)
	if _, err := client.Download(context.Background(), asset, directory); err == nil {
		t.Fatal("download accepted a wrong digest")
	}
}

func TestExtractRejectsUnexpectedEntry(t *testing.T) {
	directory := t.TempDir()
	archive := filepath.Join(directory, "bad.tar.gz")
	if err := os.WriteFile(archive, testArchive(t, map[string]string{"../outside": "bad", "arinode": "good", "anctl": "good"}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractBinaries(archive, directory); err == nil {
		t.Fatal("unsafe archive entry was accepted")
	}
}

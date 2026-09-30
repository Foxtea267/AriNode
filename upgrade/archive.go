package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxArchiveBytes int64 = 512 << 20
const maxBinaryBytes int64 = 256 << 20

func validDigest(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return err == nil
}

// Download verifies GitHub's release-asset digest before the archive is read.
// The temporary file is placed beside the installed binaries, so a later
// rename never crosses filesystems.
func (c Client) Download(ctx context.Context, asset Asset, directory string) (string, error) {
	if !validDigest(asset.Digest) || asset.Size <= 0 || asset.Size > maxArchiveBytes {
		return "", fmt.Errorf("invalid release asset metadata")
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AriNode-updater")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("download release asset: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release download returned HTTP %d", response.StatusCode)
	}
	file, err := os.CreateTemp(directory, ".arinode-download-*")
	if err != nil {
		return "", err
	}
	defer file.Close()
	path := file.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(path)
		}
	}()
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil {
		return "", err
	}
	if written != asset.Size || written > maxArchiveBytes {
		return "", fmt.Errorf("release asset size mismatch")
	}
	if "sha256:"+hex.EncodeToString(hasher.Sum(nil)) != strings.ToLower(asset.Digest) {
		return "", fmt.Errorf("release asset SHA256 mismatch")
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

// ExtractBinaries accepts only the two expected regular files. Archive paths,
// links, duplicates and oversized entries are rejected before installation.
func ExtractBinaries(archive, directory string) (string, error) {
	staging, err := os.MkdirTemp(directory, ".arinode-upgrade-*")
	if err != nil {
		return "", err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(staging)
		}
	}()
	input, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer input.Close()
	compressed, err := gzip.NewReader(input)
	if err != nil {
		return "", err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	seen := make(map[string]bool, 2)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if (header.Name != "arinode" && header.Name != "anctl") || seen[header.Name] || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size <= 0 || header.Size > maxBinaryBytes {
			return "", fmt.Errorf("unexpected release archive entry %q", header.Name)
		}
		seen[header.Name] = true
		output, err := os.OpenFile(filepath.Join(staging, header.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
		if err != nil {
			return "", err
		}
		written, copyErr := io.CopyN(output, reader, header.Size)
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil || written != header.Size {
			return "", fmt.Errorf("extract %s: %v %v", header.Name, copyErr, closeErr)
		}
	}
	if !seen["arinode"] || !seen["anctl"] {
		return "", fmt.Errorf("release archive must contain arinode and anctl")
	}
	remove = false
	return staging, nil
}

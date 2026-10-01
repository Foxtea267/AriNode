package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const LatestReleaseURL = "https://api.github.com/repos/Foxtea267/AriNode/releases/latest"

var versionPattern = regexp.MustCompile(`^[0-9]{8}-[0-9a-f]{8,12}$`)

type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type Release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

type Client struct {
	HTTP *http.Client
	URL  string
}

func NewClient() Client {
	return Client{HTTP: &http.Client{Timeout: 30 * time.Second}, URL: LatestReleaseURL}
}

func ValidVersion(version string) bool {
	if !versionPattern.MatchString(version) {
		return false
	}
	_, err := time.Parse("20060102", version[:8])
	return err == nil
}

// Newer treats a different release from the same day as a candidate because
// commit hashes have no chronological ordering. The latest endpoint determines
// which of those releases is current.
func Newer(current, candidate string) bool {
	if !ValidVersion(candidate) || current == candidate {
		return false
	}
	if !ValidVersion(current) {
		return true
	}
	return candidate[:8] >= current[:8]
}

func AssetName(arch string) (string, error) {
	switch arch {
	case "amd64", "arm64":
		return "arinode-linux-" + arch + ".tar.gz", nil
	default:
		return "", fmt.Errorf("no official upgrade asset for linux/%s", arch)
	}
}

func (c Client) Latest(ctx context.Context, arch string) (Release, Asset, error) {
	name, err := AssetName(arch)
	if err != nil {
		return Release{}, Asset{}, err
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if c.URL == "" {
		c.URL = LatestReleaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return Release{}, Asset{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AriNode-updater/"+runtime.GOARCH)
	response, err := c.HTTP.Do(req)
	if err != nil {
		return Release{}, Asset{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Release{}, Asset{}, fmt.Errorf("latest release returned HTTP %d", response.StatusCode)
	}
	var release Release
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return Release{}, Asset{}, fmt.Errorf("decode latest release: %w", err)
	}
	if release.Draft || release.Prerelease || !ValidVersion(release.Tag) {
		return Release{}, Asset{}, fmt.Errorf("latest release is not a valid stable AriNode version")
	}
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		if asset.Size <= 0 || asset.Size > maxArchiveBytes {
			return Release{}, Asset{}, fmt.Errorf("release asset size is invalid")
		}
		if !strings.HasPrefix(asset.URL, "https://github.com/Foxtea267/AriNode/releases/download/"+release.Tag+"/") {
			return Release{}, Asset{}, fmt.Errorf("release asset URL is unexpected")
		}
		if !validDigest(asset.Digest) {
			return Release{}, Asset{}, fmt.Errorf("release asset has no valid SHA256 digest")
		}
		return release, asset, nil
	}
	return Release{}, Asset{}, fmt.Errorf("release %s has no %s asset", release.Tag, name)
}

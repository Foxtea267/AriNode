// Package dsom manages explicitly configured SOCKS5 upstreams. An observed exit
// address is data, never a candidate proxy address or a port-scanning target.
package dsom

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
)

type Endpoint struct {
	Type            string `json:"type,omitempty"`
	Version         string `json:"version,omitempty"`
	Server          string `json:"server"`
	Port            int    `json:"server_port"`
	UsernameEnv     string `json:"username_env,omitempty"`
	PasswordEnv     string `json:"password_env,omitempty"`
	CredentialsFile string `json:"credentials_file,omitempty"`
}

type Direct struct {
	Enabled bool `json:"enabled"`
	Endpoint
	EndpointFile string `json:"endpoint_file,omitempty"`
}

type Source struct {
	URL       string `json:"url"`
	JSONField string `json:"json_field,omitempty"`
}

type HealthCheck struct {
	Interval         int      `json:"interval,omitempty"`
	Timeout          int      `json:"timeout,omitempty"`
	Retries          int      `json:"retries,omitempty"`
	FailureThreshold int      `json:"failure_threshold,omitempty"`
	SuccessThreshold int      `json:"success_threshold,omitempty"`
	Cooldown         int      `json:"cooldown,omitempty"`
	MinConsensus     int      `json:"min_consensus,omitempty"`
	DNSTTL           int      `json:"dns_ttl,omitempty"`
	Sources          []Source `json:"sources,omitempty"`
}

type Metadata struct {
	// Optional ipwho.is-compatible endpoint. Empty disables metadata requests.
	URL string `json:"url,omitempty"`
}

type Config struct {
	Tag              string      `json:"tag"`
	Enabled          bool        `json:"enabled"`
	Mode             string      `json:"mode"`
	Gateway          *Endpoint   `json:"gateway,omitempty"`
	FallbackGateways []Endpoint  `json:"fallback_gateways,omitempty"`
	Direct           Direct      `json:"direct,omitempty"`
	HealthCheck      HealthCheck `json:"health_check,omitempty"`
	Metadata         Metadata    `json:"metadata,omitempty"`
}

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func normalize(input Config) (Config, error) {
	c := input
	if input.Gateway != nil {
		copy := *input.Gateway
		c.Gateway = &copy
	}
	c.FallbackGateways = append([]Endpoint(nil), input.FallbackGateways...)
	c.HealthCheck.Sources = append([]Source(nil), input.HealthCheck.Sources...)
	if !tagPattern.MatchString(c.Tag) || strings.EqualFold(c.Tag, "direct") || strings.EqualFold(c.Tag, "block") {
		return c, errors.New("DSOM: invalid or reserved tag")
	}
	if c.Mode == "" {
		c.Mode = "auto"
	}
	if c.Mode != "gateway" && c.Mode != "direct" && c.Mode != "auto" {
		return c, errors.New("DSOM: mode must be gateway, direct or auto")
	}
	if c.Enabled && c.Mode != "direct" && c.Gateway == nil {
		return c, errors.New("DSOM: gateway is required in gateway/auto mode")
	}
	if len(c.FallbackGateways) > 4 {
		return c, errors.New("DSOM: at most four fallback gateways")
	}
	if c.Gateway != nil {
		if err := c.Gateway.validate(); err != nil {
			return c, err
		}
	}
	for _, e := range c.FallbackGateways {
		if err := e.validate(); err != nil {
			return c, err
		}
	}
	if c.Direct.EndpointFile != "" && (c.Direct.Server != "" || c.Direct.Port != 0) {
		return c, errors.New("DSOM: direct endpoint_file cannot be combined with server/server_port")
	}
	if c.Direct.Server != "" || c.Direct.Port != 0 {
		if err := c.Direct.Endpoint.validate(); err != nil {
			return c, err
		}
		if c.Gateway != nil && strings.EqualFold(strings.TrimSuffix(c.Direct.Server, "."), strings.TrimSuffix(c.Gateway.Server, ".")) && c.Direct.Port == c.Gateway.Port {
			return c, errors.New("DSOM: direct endpoint is the gateway, not a bypass")
		}
	}
	h := &c.HealthCheck
	if h.Interval == 0 {
		h.Interval = 60
	}
	if h.Timeout == 0 {
		h.Timeout = 5
	}
	if h.FailureThreshold == 0 {
		h.FailureThreshold = 3
	}
	if h.SuccessThreshold == 0 {
		h.SuccessThreshold = 2
	}
	if h.Cooldown == 0 {
		h.Cooldown = 300
	}
	if h.DNSTTL == 0 {
		h.DNSTTL = 60
	}
	if h.MinConsensus == 0 {
		h.MinConsensus = 2
	}
	if len(h.Sources) == 0 {
		h.Sources = []Source{{URL: "https://api.ipify.org"}, {URL: "https://ipv4.icanhazip.com"}}
	}
	if h.Interval < 5 || h.Interval > 86400 || h.Timeout < 1 || h.Timeout > 30 || h.Retries < 0 || h.Retries > 3 || h.Cooldown < 0 || h.Cooldown > 86400 || h.DNSTTL < 5 || h.DNSTTL > 3600 || h.FailureThreshold < 1 || h.FailureThreshold > 100 || h.SuccessThreshold < 1 || h.SuccessThreshold > 100 || len(h.Sources) > 8 || h.MinConsensus < 2 || h.MinConsensus > len(h.Sources) {
		return c, errors.New("DSOM: invalid health-check limits (requires at least two sources and consensus >= 2)")
	}
	origins := map[string]bool{}
	for _, source := range h.Sources {
		u, err := safeURL(source.URL)
		if err != nil {
			return c, err
		}
		port := u.Port()
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		origin := net.JoinHostPort(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), port)
		if origins[origin] {
			return c, errors.New("DSOM: probe sources must have distinct origins")
		}
		origins[origin] = true
	}
	if c.Metadata.URL != "" {
		if strings.Count(c.Metadata.URL, "{ip}") != 1 {
			return c, errors.New("DSOM: metadata URL needs one {ip} placeholder")
		}
		if _, err := safeURL(strings.ReplaceAll(c.Metadata.URL, "{ip}", "8.8.8.8")); err != nil {
			return c, err
		}
	}
	return c, nil
}

func Validate(configs []Config) error {
	if len(configs) > 32 {
		return errors.New("DSOM: at most 32 dynamic outbounds")
	}
	seen := map[string]bool{}
	for _, c := range configs {
		if _, err := normalize(c); err != nil {
			return err
		}
		if seen[c.Tag] {
			return errors.New("DSOM: duplicate dynamic outbound tag")
		}
		seen[c.Tag] = true
	}
	return nil
}

func (e Endpoint) validate() error {
	if (e.Type != "" && e.Type != "socks") || (e.Version != "" && e.Version != "5") {
		return errors.New("DSOM: only SOCKS5 endpoints are supported")
	}
	if e.Server == "" || len(e.Server) > 253 || strings.ContainsAny(e.Server, " \t\r\n/@?#\\") || e.Port < 1 || e.Port > 65535 {
		return errors.New("DSOM: proxy needs an explicit host and port (1-65535)")
	}
	if strings.Contains(e.Server, ":") {
		if _, err := netip.ParseAddr(e.Server); err != nil {
			return errors.New("DSOM: invalid proxy host")
		}
	} else if !hostPattern.MatchString(e.Server) {
		return errors.New("DSOM: invalid proxy hostname (use ASCII/punycode)")
	}
	if e.CredentialsFile != "" && (e.UsernameEnv != "" || e.PasswordEnv != "") {
		return errors.New("DSOM: choose credentials_file or credential environment variables")
	}
	if (e.UsernameEnv == "") != (e.PasswordEnv == "") {
		return errors.New("DSOM: both username_env and password_env are required")
	}
	return nil
}

func safeURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("DSOM: invalid probe/metadata URL (credentials in URLs are forbidden)")
	}
	// HTTP is useful for local mock services only; Internet probes require TLS.
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("DSOM: Internet probe/metadata sources require HTTPS")
	}
	return u, nil
}

func readPrivateJSON(path string, target any) error {
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() {
		return errors.New("DSOM: private JSON path must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("DSOM: cannot read private JSON file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		return errors.New("DSOM: private JSON file is missing, not regular or too large")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("DSOM: private JSON file must have permissions 600 or stricter")
	}
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(data) > 8192 {
		return errors.New("DSOM: cannot read private JSON file")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("DSOM: invalid private JSON file (content omitted)")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("DSOM: private JSON contains trailing data")
	}
	return nil
}

type endpoint struct {
	Endpoint
	username, password string
}

func resolve(e Endpoint) (endpoint, error) {
	result := endpoint{Endpoint: e}
	if err := e.validate(); err != nil {
		return result, err
	}
	if e.CredentialsFile != "" {
		var auth struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := readPrivateJSON(e.CredentialsFile, &auth); err != nil {
			return result, err
		}
		result.username, result.password = auth.Username, auth.Password
	} else if e.UsernameEnv != "" {
		var a, b bool
		result.username, a = os.LookupEnv(e.UsernameEnv)
		result.password, b = os.LookupEnv(e.PasswordEnv)
		if !a || !b {
			return result, errors.New("DSOM: credential environment variable is unset")
		}
	}
	if len(result.username) > 255 || len(result.password) > 255 || (result.username == "") != (result.password == "") {
		return result, errors.New("DSOM: invalid SOCKS5 credential lengths")
	}
	if (e.CredentialsFile != "" || e.UsernameEnv != "") && result.username == "" {
		return result, errors.New("DSOM: explicit authentication cannot be empty")
	}
	return result, nil
}

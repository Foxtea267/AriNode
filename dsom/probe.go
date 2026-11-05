package dsom

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type probeResult struct {
	IPs       []string
	Outcome   string // healthy (consensus), unknown (sources), failed (proxy)
	Reason    string
	LatencyMS int64
}

var excluded = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func publicIP(value string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return netip.Addr{}, false
	}
	for _, prefix := range excluded {
		if prefix.Contains(ip) {
			return netip.Addr{}, false
		}
	}
	return ip, true
}

func proxyHTTP(e endpoint, dns *resolver, h HealthCheck, connected *atomic.Bool, failure *atomic.Value) *http.Client {
	transport := &http.Transport{
		MaxResponseHeaderBytes: 32768,
		Proxy:                  nil, DisableKeepAlives: true, TLSHandshakeTimeout: time.Duration(h.Timeout) * time.Second,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			connection, err := dialSOCKS(ctx, e, target, dns, time.Duration(h.DNSTTL)*time.Second)
			if err == nil && connected != nil {
				connected.Store(true)
			}
			if err != nil && failure != nil {
				failure.Store(err.Error())
			}
			return connection, err
		},
	}
	return &http.Client{Transport: transport, Timeout: time.Duration(h.Timeout) * time.Second,
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			if len(previous) >= 3 {
				return http.ErrUseLastResponse
			}
			_, err := safeURL(request.URL.String())
			return err
		}}
}

func fetchIP(ctx context.Context, client *http.Client, source Source) (netip.Addr, bool) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return netip.Addr{}, false
	}
	request.Header.Set("User-Agent", "AriNode-DSOM/1")
	request.Header.Set("Accept", "application/json, text/plain")
	response, err := client.Do(request)
	if err != nil {
		return netip.Addr{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return netip.Addr{}, false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(body) > 8192 {
		return netip.Addr{}, false
	}
	value := strings.TrimSpace(string(body))
	if source.JSONField != "" {
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) != nil || json.Unmarshal(fields[source.JSONField], &value) != nil {
			return netip.Addr{}, false
		}
	}
	return publicIP(value)
}

func probe(ctx context.Context, e endpoint, h HealthCheck, dns *resolver) probeResult {
	started := time.Now()
	var connected atomic.Bool
	var failure atomic.Value
	client := proxyHTTP(e, dns, h, &connected, &failure)
	defer client.CloseIdleConnections()
	var mu sync.Mutex
	counts := map[netip.Addr]int{}
	var wg sync.WaitGroup
	for _, source := range h.Sources {
		wg.Add(1)
		go func(source Source) {
			defer wg.Done()
			for attempt := 0; attempt <= h.Retries; attempt++ {
				if ctx.Err() != nil {
					return
				}
				if ip, ok := fetchIP(ctx, client, source); ok {
					mu.Lock()
					counts[ip]++
					mu.Unlock()
					return
				}
			}
		}(source)
	}
	wg.Wait()
	result := probeResult{Outcome: "unknown", Reason: "probe_sources_unavailable", LatencyMS: time.Since(started).Milliseconds()}
	totals := map[bool]int{}
	for ip, count := range counts {
		totals[ip.Is4()] += count
	}
	for ip, count := range counts {
		if count >= h.MinConsensus && count*2 > totals[ip.Is4()] {
			result.IPs = append(result.IPs, ip.String())
		}
	}
	sort.Strings(result.IPs)
	if len(result.IPs) > 0 {
		result.Outcome = "healthy"
		result.Reason = ""
		return result
	}
	if len(counts) > 0 {
		result.Reason = "exit_ip_unconfirmed_or_inconsistent"
	}
	if !connected.Load() && len(counts) == 0 {
		result.Outcome = "failed"
		result.Reason = "proxy_protocol_or_forwarding_unavailable"
		if code := failure.Load(); code != nil {
			result.Reason = code.(string)
		}
	}
	return result
}

type Geo struct {
	Country      string `json:"country,omitempty"`
	ASN          int    `json:"asn,omitempty"`
	Organization string `json:"organization,omitempty"`
	NetworkType  string `json:"network_type"`
	Source       string `json:"source,omitempty"`
}

// Metadata is advisory. A provider error, inconsistent IP or absent network
// classification never influences proxy selection or proves residential access.
func geolocate(ctx context.Context, e endpoint, ip string, c Config, dns *resolver) Geo {
	result := Geo{NetworkType: "unknown"}
	if c.Metadata.URL == "" {
		return result
	}
	client := proxyHTTP(e, dns, c.HealthCheck, nil, nil)
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.ReplaceAll(c.Metadata.URL, "{ip}", ip), nil)
	if err != nil {
		return result
	}
	response, err := client.Do(request)
	if err != nil {
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return result
	}
	var value struct {
		IP         string `json:"ip"`
		Success    bool   `json:"success"`
		Country    string `json:"country_code"`
		Connection struct {
			ASN          int    `json:"asn"`
			Organization string `json:"org"`
			Type         string `json:"type"`
		} `json:"connection"`
		Security struct {
			Hosting bool `json:"hosting"`
			Mobile  bool `json:"mobile"`
		} `json:"security"`
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32769))
	if err != nil || len(data) > 32768 || json.Unmarshal(data, &value) != nil || !value.Success {
		return result
	}
	parsed, ok := publicIP(value.IP)
	if !ok || parsed.String() != ip || value.Connection.ASN < 0 || len(value.Country) != 2 {
		return result
	}
	for _, letter := range strings.ToUpper(value.Country) {
		if letter < 'A' || letter > 'Z' {
			return result
		}
	}
	result.Country = strings.ToUpper(value.Country)
	result.ASN = value.Connection.ASN
	if len(value.Connection.Organization) <= 256 {
		result.Organization = value.Connection.Organization
	}
	switch value.Connection.Type {
	case "hosting", "mobile", "residential", "business":
		result.NetworkType = value.Connection.Type
	default:
		if value.Security.Hosting {
			result.NetworkType = "hosting"
		} else if value.Security.Mobile {
			result.NetworkType = "mobile"
		}
	}
	result.Source = "provider_reported"
	return result
}

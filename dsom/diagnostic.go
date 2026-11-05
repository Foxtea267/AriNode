package dsom

import (
	"context"
	"fmt"
	"net"
	"time"
)

type Diagnostic struct {
	Host              string      `json:"host"`
	Port              int         `json:"port"`
	Attempts          int         `json:"attempts"`
	Successful        int         `json:"successful"`
	TCPFailurePercent float64     `json:"tcp_connect_failure_percent"`
	MinMS             float64     `json:"min_tcp_connect_ms"`
	MeanMS            float64     `json:"mean_tcp_connect_ms"`
	MaxMS             float64     `json:"max_tcp_connect_ms"`
	Note              string      `json:"note"`
	ICMP              *ICMPResult `json:"icmp,omitempty"`
	ResolvedIPs       []string    `json:"resolved_ips,omitempty"`
	DNSLookupMS       float64     `json:"dns_lookup_ms"`
}

// Diagnose measures TCP handshakes to exactly one explicit host:port. It is not
// an ICMP measurement, a port scan or a SOCKS5 usability verdict.
func Diagnose(ctx context.Context, host string, port, attempts int, timeout, interval time.Duration) (Diagnostic, error) {
	result := Diagnostic{Host: host, Port: port, Attempts: attempts, Note: "TCP connect measurements only; latency fields require successful > 0; no ICMP loss or proxy-forwarding claim"}
	if (Endpoint{Server: host, Port: port}).validate() != nil || attempts < 1 || attempts > 50 || timeout <= 0 || timeout > 30*time.Second || interval < 0 || interval > 30*time.Second {
		return result, fmt.Errorf("DSOM: invalid diagnostic limits")
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	startedDNS := time.Now()
	lookup, stop := context.WithTimeout(ctx, timeout)
	ips, err := (&resolver{}).addresses(lookup, host, time.Minute)
	stop()
	result.DNSLookupMS = float64(time.Since(startedDNS)) / float64(time.Millisecond)
	if err != nil {
		return result, fmt.Errorf("DSOM: gateway DNS lookup failed; no TCP or packet-loss measurement")
	}
	for _, ip := range ips {
		result.ResolvedIPs = append(result.ResolvedIPs, ip.String())
	}
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		started := time.Now()
		connection, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", net.JoinHostPort(ips[i%len(ips)].String(), fmt.Sprint(port)))
		elapsed := float64(time.Since(started)) / float64(time.Millisecond)
		if err == nil {
			connection.Close()
			result.Successful++
			result.MeanMS += elapsed
			if result.Successful == 1 || elapsed < result.MinMS {
				result.MinMS = elapsed
			}
			if elapsed > result.MaxMS {
				result.MaxMS = elapsed
			}
		}
		if i+1 < attempts {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		}
	}
	result.TCPFailurePercent = 100 * float64(attempts-result.Successful) / float64(attempts)
	if result.Successful > 0 {
		result.MeanMS /= float64(result.Successful)
	}
	return result, nil
}

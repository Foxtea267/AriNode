package dsom

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type ICMPResult struct {
	Available         bool     `json:"available"`
	PacketLossPercent *float64 `json:"packet_loss_percent,omitempty"`
	MeanRTTMS         *float64 `json:"mean_rtt_ms,omitempty"`
	Reason            string   `json:"reason,omitempty"`
}

var lossPattern = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)% packet loss`)
var rttPattern = regexp.MustCompile(`(?:rtt|round-trip)[^=]*=\s*([0-9.]+)/([0-9.]+)/([0-9.]+)`)

// Optional Linux iputils integration: no shell, no elevation, no raw socket in
// AriNode itself. Missing binary/permissions/unsupported output stays unknown.
func ICMP(ctx context.Context, host string, count, timeout int) ICMPResult {
	if runtime.GOOS != "linux" {
		return ICMPResult{Reason: "icmp_requires_linux_iputils_ping"}
	}
	if (Endpoint{Server: host, Port: 1}).validate() != nil || count < 1 || count > 50 || timeout < 1 || timeout > 30 {
		return ICMPResult{Reason: "invalid_icmp_limits"}
	}
	path, err := exec.LookPath("ping")
	if err != nil {
		return ICMPResult{Reason: "ping_not_installed"}
	}
	limited, cancel := context.WithTimeout(ctx, time.Duration(count*(timeout+1)+5)*time.Second)
	defer cancel()
	command := exec.CommandContext(limited, path, "-n", "-c", strconv.Itoa(count), "-W", strconv.Itoa(timeout), host)
	command.Env = []string{"LC_ALL=C"}
	output, _ := command.CombinedOutput()
	if limited.Err() != nil {
		return ICMPResult{Reason: "icmp_cancelled_or_timed_out"}
	}
	return parseICMP(string(output))
}

func parseICMP(output string) ICMPResult {
	result := ICMPResult{Reason: "icmp_unavailable_or_permission_denied"}
	match := lossPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return result
	}
	loss, err := strconv.ParseFloat(match[1], 64)
	if err != nil || loss < 0 || loss > 100 {
		return result
	}
	result.Available = true
	result.PacketLossPercent = &loss
	result.Reason = "ICMP replies can be filtered or deprioritized independently of TCP"
	if values := rttPattern.FindStringSubmatch(output); len(values) == 4 {
		if mean, err := strconv.ParseFloat(strings.TrimSpace(values[2]), 64); err == nil && mean >= 0 {
			result.MeanRTTMS = &mean
		}
	}
	return result
}

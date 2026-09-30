//go:build linux

package komari

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type networkCounters struct{ up, down int64 }

func kernelVersion() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func hostExtras() (load [3]float64, uptime int64, processes int, network networkCounters) {
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		for i := 0; i < 3 && i < len(fields); i++ {
			load[i], _ = strconv.ParseFloat(fields[i], 64)
		}
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			seconds, _ := strconv.ParseFloat(fields[0], 64)
			uptime = int64(seconds)
		}
	}
	if entries, err := os.ReadDir("/proc"); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				if _, err := strconv.Atoi(entry.Name()); err == nil {
					processes++
				}
			}
		}
	}
	if file, err := os.Open("/proc/net/dev"); err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			name, values, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(name) == "lo" {
				continue
			}
			fields := strings.Fields(values)
			if len(fields) < 16 {
				continue
			}
			received, _ := strconv.ParseInt(fields[0], 10, 64)
			sent, _ := strconv.ParseInt(fields[8], 10, 64)
			network.down += received
			network.up += sent
		}
	}
	return
}

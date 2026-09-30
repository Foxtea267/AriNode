//go:build linux

package sysstatus

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Usage struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type Status struct {
	CPU  float64 `json:"cpu"`
	Mem  Usage   `json:"mem"`
	Swap Usage   `json:"swap"`
	Disk Usage   `json:"disk"`
}

func Read() (Status, error) {
	firstTotal, firstIdle, err := cpuTicks()
	if err != nil {
		return Status{}, err
	}
	time.Sleep(100 * time.Millisecond)
	secondTotal, secondIdle, err := cpuTicks()
	if err != nil {
		return Status{}, err
	}
	var cpu float64
	if secondTotal > firstTotal {
		cpu = 100 * float64((secondTotal-firstTotal)-(secondIdle-firstIdle)) / float64(secondTotal-firstTotal)
	}
	values := map[string]uint64{}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return Status{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			value, _ := strconv.ParseUint(fields[1], 10, 64)
			values[strings.TrimSuffix(fields[0], ":")] = value * 1024
		}
	}
	if err := scanner.Err(); err != nil {
		return Status{}, err
	}
	var disk syscall.Statfs_t
	if err := syscall.Statfs("/", &disk); err != nil {
		return Status{}, err
	}
	memTotal := values["MemTotal"]
	swapTotal := values["SwapTotal"]
	return Status{
		CPU:  cpu,
		Mem:  Usage{Total: memTotal, Used: memTotal - values["MemAvailable"]},
		Swap: Usage{Total: swapTotal, Used: swapTotal - values["SwapFree"]},
		Disk: Usage{Total: disk.Blocks * uint64(disk.Bsize), Used: (disk.Blocks - disk.Bfree) * uint64(disk.Bsize)},
	}, nil
}

func cpuTicks() (uint64, uint64, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0, 0, fmt.Errorf("empty /proc/stat")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 6 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("invalid /proc/stat CPU line")
	}
	var total, idle uint64
	for i, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, 0, err
		}
		total += value
		if i == 3 || i == 4 {
			idle += value
		}
	}
	return total, idle, scanner.Err()
}

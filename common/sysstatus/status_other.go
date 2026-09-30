//go:build !linux

package sysstatus

import "fmt"

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
	return Status{}, fmt.Errorf("host status collection is supported on Linux only")
}

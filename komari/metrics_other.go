//go:build !linux

package komari

type networkCounters struct{ up, down int64 }

func kernelVersion() string                                                               { return "" }
func hostExtras() (load [3]float64, uptime int64, processes int, network networkCounters) { return }

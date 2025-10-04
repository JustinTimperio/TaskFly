package main

import (
	"runtime"
)

// getCPUCount returns the number of CPU cores
func (a *Agent) getCPUCount() int {
	return runtime.NumCPU()
}

// getLoadAverages returns the system load averages
// Platform-specific implementations in metrics_*.go files

// getMemoryUsage returns total and used memory in bytes
// Platform-specific implementations in metrics_*.go files

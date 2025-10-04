//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

// getLoadAverages returns the system load averages
// Windows doesn't have a direct equivalent, so we return 0s
func (a *Agent) getLoadAverages() (float64, float64, float64) {
	// Windows doesn't have load averages in the Unix sense
	// Could potentially use performance counters, but for now return 0s
	return 0, 0, 0
}

// getMemoryUsage returns total and used memory in bytes using wmic
func (a *Agent) getMemoryUsage() (uint64, uint64) {
	// Get total physical memory
	totalCmd := exec.Command("wmic", "ComputerSystem", "get", "TotalPhysicalMemory")
	totalOutput, err := totalCmd.Output()
	if err != nil {
		return 0, 0
	}

	var memTotal uint64
	lines := strings.Split(string(totalOutput), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && line != "TotalPhysicalMemory" {
			memTotal, _ = strconv.ParseUint(line, 10, 64)
			break
		}
	}

	// Get available memory
	availCmd := exec.Command("wmic", "OS", "get", "FreePhysicalMemory")
	availOutput, err := availCmd.Output()
	if err != nil {
		return memTotal, 0
	}

	var memFree uint64
	lines = strings.Split(string(availOutput), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && line != "FreePhysicalMemory" {
			// FreePhysicalMemory is in KB, convert to bytes
			memFreeKB, _ := strconv.ParseUint(line, 10, 64)
			memFree = memFreeKB * 1024
			break
		}
	}

	memUsed := memTotal - memFree
	return memTotal, memUsed
}

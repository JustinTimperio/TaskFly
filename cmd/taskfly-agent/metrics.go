package main

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// getCPUCount returns the number of CPU cores
func (a *Agent) getCPUCount() int {
	return runtime.NumCPU()
}

// getLoadAverages returns the system load averages
func (a *Agent) getLoadAverages() (float64, float64, float64) {
	// Try to read /proc/loadavg (Linux)
	data, err := os.ReadFile("/proc/loadavg")
	if err == nil {
		parts := strings.Fields(string(data))
		if len(parts) >= 3 {
			load1, _ := strconv.ParseFloat(parts[0], 64)
			load5, _ := strconv.ParseFloat(parts[1], 64)
			load15, _ := strconv.ParseFloat(parts[2], 64)
			return load1, load5, load15
		}
	}

	// Fall back to zero if we can't read it (e.g., on Darwin without sysctl)
	return 0, 0, 0
}

// getMemoryUsage returns total and used memory in bytes
func (a *Agent) getMemoryUsage() (uint64, uint64) {
	// Try Linux /proc/meminfo
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer file.Close()

	var memTotal, memFree, memAvailable, buffers, cached uint64

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		key := strings.TrimSuffix(parts[0], ":")
		value, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}

		// Convert from KB to bytes
		value *= 1024

		switch key {
		case "MemTotal":
			memTotal = value
		case "MemFree":
			memFree = value
		case "MemAvailable":
			memAvailable = value
		case "Buffers":
			buffers = value
		case "Cached":
			cached = value
		}
	}

	// Calculate used memory
	var memUsed uint64
	if memAvailable > 0 {
		// Modern Linux with MemAvailable
		memUsed = memTotal - memAvailable
	} else {
		// Older systems: approximate as Total - Free - Buffers - Cached
		memUsed = memTotal - memFree - buffers - cached
	}

	return memTotal, memUsed
}

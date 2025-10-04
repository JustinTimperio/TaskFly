//go:build linux

package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// getLoadAverages returns the system load averages
func (a *Agent) getLoadAverages() (float64, float64, float64) {
	// Read /proc/loadavg (Linux only)
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}

	parts := strings.Fields(string(data))
	if len(parts) >= 3 {
		load1, _ := strconv.ParseFloat(parts[0], 64)
		load5, _ := strconv.ParseFloat(parts[1], 64)
		load15, _ := strconv.ParseFloat(parts[2], 64)
		return load1, load5, load15
	}

	return 0, 0, 0
}

// getMemoryUsage returns total and used memory in bytes
func (a *Agent) getMemoryUsage() (uint64, uint64) {
	// Read Linux /proc/meminfo
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

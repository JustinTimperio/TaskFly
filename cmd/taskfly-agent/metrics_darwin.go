//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"os/exec"
	"strconv"
	"strings"
)

// getLoadAverages returns the system load averages using sysctl
func (a *Agent) getLoadAverages() (float64, float64, float64) {
	cmd := exec.Command("sysctl", "-n", "vm.loadavg")
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, 0
	}

	// Output format: "{ 1.23 2.34 3.45 }"
	str := strings.TrimSpace(string(output))
	str = strings.Trim(str, "{}")
	parts := strings.Fields(str)

	if len(parts) >= 3 {
		load1, _ := strconv.ParseFloat(parts[0], 64)
		load5, _ := strconv.ParseFloat(parts[1], 64)
		load15, _ := strconv.ParseFloat(parts[2], 64)
		return load1, load5, load15
	}

	return 0, 0, 0
}

// getMemoryUsage returns total and used memory in bytes using vm_stat
func (a *Agent) getMemoryUsage() (uint64, uint64) {
	// Get page size
	pageSizeCmd := exec.Command("sysctl", "-n", "hw.pagesize")
	pageSizeOutput, err := pageSizeCmd.Output()
	if err != nil {
		return 0, 0
	}
	pageSize, err := strconv.ParseUint(strings.TrimSpace(string(pageSizeOutput)), 10, 64)
	if err != nil {
		return 0, 0
	}

	// Get total memory
	memTotalCmd := exec.Command("sysctl", "-n", "hw.memsize")
	memTotalOutput, err := memTotalCmd.Output()
	if err != nil {
		return 0, 0
	}
	memTotal, err := strconv.ParseUint(strings.TrimSpace(string(memTotalOutput)), 10, 64)
	if err != nil {
		return 0, 0
	}

	// Get memory statistics
	vmStatCmd := exec.Command("vm_stat")
	vmStatOutput, err := vmStatCmd.Output()
	if err != nil {
		return memTotal, 0
	}

	// Parse vm_stat output
	scanner := bufio.NewScanner(bytes.NewReader(vmStatOutput))
	var active, inactive, wired uint64

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "Pages active:") {
			active = parseVMStatLine(line)
		} else if strings.Contains(line, "Pages inactive:") {
			inactive = parseVMStatLine(line)
		} else if strings.Contains(line, "Pages wired down:") {
			wired = parseVMStatLine(line)
		}
	}

	// Calculate used memory
	// Used = wired + active + inactive (speculative is available)
	usedPages := wired + active + inactive
	memUsed := usedPages * pageSize

	return memTotal, memUsed
}

// parseVMStatLine extracts the number from a vm_stat output line
func parseVMStatLine(line string) uint64 {
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		// Remove trailing dot if present
		numStr := strings.TrimSuffix(parts[len(parts)-1], ".")
		val, _ := strconv.ParseUint(numStr, 10, 64)
		return val
	}
	return 0
}

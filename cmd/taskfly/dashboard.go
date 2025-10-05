package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/pterm/pterm"
	"github.com/urfave/cli/v2"
)

type MetricsResponse struct {
	Summary struct {
		TotalCores        int     `json:"total_cores"`
		TotalMemoryGB     float64 `json:"total_memory_gb"`
		TotalMemoryUsedGB float64 `json:"total_memory_used_gb"`
		AvgLoad           float64 `json:"avg_load"`
		NodesWithMetrics  int     `json:"nodes_with_metrics"`
	} `json:"summary"`
	Nodes []struct {
		NodeID     string `json:"node_id"`
		IPAddress  string `json:"ip_address"`
		Status     string `json:"status"`
		LastUpdate string `json:"last_update"`
		Metrics    *struct {
			CPUCores    int     `json:"cpu_cores"`
			CPUUsage    float64 `json:"cpu_usage"`
			MemoryTotal uint64  `json:"memory_total"`
			MemoryUsed  uint64  `json:"memory_used"`
			LoadAvg1    float64 `json:"load_avg_1"`
			LoadAvg5    float64 `json:"load_avg_5"`
			LoadAvg15   float64 `json:"load_avg_15"`
		} `json:"metrics"`
	} `json:"nodes"`
}

func dashboardCommand(c *cli.Context) error {
	// Check if TUI mode is requested
	if c.Bool("tui") {
		return runDashboardTUI(c)
	}

	// Default to simple dashboard
	// Auto-refresh every 3 seconds
	for {
		if err := showDashboard(c); err != nil {
			return err
		}
		time.Sleep(1 * time.Second)
	}
}

func showDashboard(c *cli.Context) error {
	// Fetch deployments
	resp, err := http.Get(getDaemonURL(c) + "/api/v1/deployments")
	if err != nil {
		return fmt.Errorf("failed to fetch deployments: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var deployments []map[string]interface{}
	if err := json.Unmarshal(body, &deployments); err != nil {
		return fmt.Errorf("failed to parse deployments: %w", err)
	}

	// Fetch metrics
	metricsResp, err := http.Get(getDaemonURL(c) + "/api/v1/metrics")
	if err != nil {
		return fmt.Errorf("failed to fetch metrics: %w", err)
	}
	defer metricsResp.Body.Close()

	metricsBody, err := io.ReadAll(metricsResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read metrics: %w", err)
	}

	var metrics MetricsResponse
	if err := json.Unmarshal(metricsBody, &metrics); err != nil {
		return fmt.Errorf("failed to parse metrics: %w", err)
	}

	// Fetch stats
	statsResp, err := http.Get(getDaemonURL(c) + "/api/v1/stats")
	if err != nil {
		return fmt.Errorf("failed to fetch stats: %w", err)
	}
	defer statsResp.Body.Close()

	statsBody, err := io.ReadAll(statsResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read stats: %w", err)
	}

	var stats map[string]interface{}
	if err := json.Unmarshal(statsBody, &stats); err != nil {
		return fmt.Errorf("failed to parse stats: %w", err)
	}

	// Clear screen and move cursor to top
	fmt.Print("\033[H\033[2J\033[3J")
	fmt.Print("\033[H")

	// Compact header
	pterm.DefaultHeader.WithFullWidth().Println("TaskFly Dashboard")

	// Render all sections compactly
	renderSystemMetrics(metrics)
	renderDeploymentSummary(deployments, stats)
	renderRecentDeployments(deployments)

	if metrics.Summary.NodesWithMetrics > 0 {
		renderNodeMetrics(metrics)
	}

	// Flush output to ensure display updates
	os.Stdout.Sync()

	return nil
}

func renderSystemMetrics(metrics MetricsResponse) {
	// Calculate colors
	loadColor := pterm.FgGreen
	if metrics.Summary.AvgLoad > 0.7*float64(metrics.Summary.TotalCores) {
		loadColor = pterm.FgYellow
	}
	if metrics.Summary.AvgLoad > 0.9*float64(metrics.Summary.TotalCores) {
		loadColor = pterm.FgRed
	}

	memUsedGB := metrics.Summary.TotalMemoryUsedGB
	memTotalGB := metrics.Summary.TotalMemoryGB
	memPercent := 0.0
	if memTotalGB > 0 {
		memPercent = (memUsedGB / memTotalGB) * 100
	}

	memColor := pterm.FgGreen
	if memPercent > 70 {
		memColor = pterm.FgYellow
	}
	if memPercent > 90 {
		memColor = pterm.FgRed
	}

	// Compact single-line display
	fmt.Printf("%s  CPU: %s  Load: %s  Memory: %s  Nodes: %s\n",
		pterm.FgCyan.Sprint("System:"),
		pterm.Bold.Sprintf("%d cores", metrics.Summary.TotalCores),
		loadColor.Sprintf("%.2f", metrics.Summary.AvgLoad),
		memColor.Sprintf("%.1f/%.1fGB (%.0f%%)", memUsedGB, memTotalGB, memPercent),
		pterm.Bold.Sprintf("%d", metrics.Summary.NodesWithMetrics),
	)
}

func renderDeploymentSummary(deployments []map[string]interface{}, stats map[string]interface{}) {
	// Count by status
	var active, completed, failed, provisioning int
	for _, dep := range deployments {
		status := fmt.Sprintf("%v", dep["status"])
		switch status {
		case "running":
			active++
		case "completed":
			completed++
		case "failed":
			failed++
		case "provisioning", "pending":
			provisioning++
		}
	}

	// Compact single-line display
	fmt.Printf("%s  Total: %s  Running: %s  Provisioning: %s  Completed: %s  Failed: %s\n",
		pterm.FgCyan.Sprint("Deployments:"),
		pterm.Bold.Sprintf("%d", len(deployments)),
		pterm.FgGreen.Sprintf("%d", active),
		pterm.FgYellow.Sprintf("%d", provisioning),
		pterm.FgCyan.Sprintf("%d", completed),
		pterm.FgRed.Sprintf("%d", failed),
	)
}

func renderRecentDeployments(deployments []map[string]interface{}) {
	fmt.Println()
	pterm.FgCyan.Println("Recent Deployments:")

	if len(deployments) == 0 {
		fmt.Println("  No deployments found")
		return
	}

	// Sort by created_at (most recent first)
	sort.Slice(deployments, func(i, j int) bool {
		iTime, _ := time.Parse(time.RFC3339, fmt.Sprintf("%v", deployments[i]["created_at"]))
		jTime, _ := time.Parse(time.RFC3339, fmt.Sprintf("%v", deployments[j]["created_at"]))
		return iTime.After(jTime)
	})

	// Take last 5
	if len(deployments) > 5 {
		deployments = deployments[:5]
	}

	tableData := pterm.TableData{
		{"ID", "Status", "Progress", "Created"},
	}

	for _, dep := range deployments {
		id := fmt.Sprintf("%v", dep["deployment_id"])
		status := fmt.Sprintf("%v", dep["status"])
		totalNodes := fmt.Sprintf("%v", dep["total_nodes"])
		completed := fmt.Sprintf("%v", dep["nodes_completed"])
		failed := fmt.Sprintf("%v", dep["nodes_failed"])

		// Compact progress bar
		total, _ := dep["total_nodes"].(float64)
		comp, _ := dep["nodes_completed"].(float64)
		progress := ""
		if total > 0 {
			percent := int((comp / total) * 10)
			for i := 0; i < 10; i++ {
				if i < percent {
					progress += "█"
				} else {
					progress += "░"
				}
			}
			progress = fmt.Sprintf("[%s] %s/%s", progress, completed, totalNodes)
		} else {
			progress = fmt.Sprintf("%s/%s", completed, totalNodes)
		}

		// Add failed count if > 0
		if failed != "0" {
			progress += pterm.FgRed.Sprintf(" (%s✗)", failed)
		}

		created := ""
		if createdAt, ok := dep["created_at"].(string); ok {
			if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
				created = t.Format("01-02 15:04")
			}
		}

		tableData = append(tableData, []string{
			id,
			formatStatus(status),
			progress,
			created,
		})
	}

	pterm.DefaultTable.WithHasHeader().WithBoxed(false).WithData(tableData).Render()
}

func renderNodeMetrics(metrics MetricsResponse) {
	fmt.Println()
	pterm.FgCyan.Println("Node Metrics:")

	if len(metrics.Nodes) == 0 {
		fmt.Println("  No nodes with metrics")
		return
	}

	tableData := pterm.TableData{
		{"Node", "IP Address", "CPUs", "Load", "Memory", "Updated"},
	}

	for _, node := range metrics.Nodes {
		if node.Metrics == nil {
			continue
		}

		m := node.Metrics

		// Format memory
		memUsedGB := float64(m.MemoryUsed) / 1024 / 1024 / 1024
		memTotalGB := float64(m.MemoryTotal) / 1024 / 1024 / 1024
		memPercent := (memUsedGB / memTotalGB) * 100

		memStr := fmt.Sprintf("%.1fGB/%.1fGB (%.0f%%)", memUsedGB, memTotalGB, memPercent)
		if memPercent > 90 {
			memStr = pterm.FgRed.Sprint(memStr)
		} else if memPercent > 70 {
			memStr = pterm.FgYellow.Sprint(memStr)
		}

		// Format load
		loadPercent := (m.LoadAvg1 / float64(m.CPUCores)) * 100
		loadStr := fmt.Sprintf("%.2f", m.LoadAvg1)
		if loadPercent > 90 {
			loadStr = pterm.FgRed.Sprint(loadStr)
		} else if loadPercent > 70 {
			loadStr = pterm.FgYellow.Sprint(loadStr)
		}

		// Format last update
		lastUpdate := "unknown"
		if t, err := time.Parse(time.RFC3339, node.LastUpdate); err == nil {
			duration := time.Since(t)
			if duration < time.Minute {
				lastUpdate = fmt.Sprintf("%ds ago", int(duration.Seconds()))
			} else if duration < time.Hour {
				lastUpdate = fmt.Sprintf("%dm ago", int(duration.Minutes()))
			} else {
				lastUpdate = t.Format("15:04:05")
			}
		}

		// Format IP address
		ipAddr := node.IPAddress
		if ipAddr == "" {
			ipAddr = "pending"
		}

		tableData = append(tableData, []string{
			node.NodeID,
			ipAddr,
			fmt.Sprintf("%d", m.CPUCores),
			loadStr,
			memStr,
			lastUpdate,
		})
	}

	pterm.DefaultTable.WithHasHeader().WithBoxed(false).WithData(tableData).Render()
}

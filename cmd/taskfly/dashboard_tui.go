package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/mum4k/termdash"
	"github.com/mum4k/termdash/cell"
	"github.com/mum4k/termdash/container"
	"github.com/mum4k/termdash/container/grid"
	"github.com/mum4k/termdash/keyboard"
	"github.com/mum4k/termdash/linestyle"
	"github.com/mum4k/termdash/terminal/tcell"
	"github.com/mum4k/termdash/terminal/terminalapi"
	"github.com/mum4k/termdash/widgets/linechart"
	"github.com/mum4k/termdash/widgets/text"
	"github.com/urfave/cli/v2"
)

// DashboardTUI represents the main TUI dashboard
type DashboardTUI struct {
	ctx    context.Context
	cancel context.CancelFunc

	// Terminal and container
	terminal  terminalapi.Terminal
	container *container.Container

	// Cluster widgets
	cpuChart  *linechart.LineChart
	memChart  *linechart.LineChart
	loadChart *linechart.LineChart
	nodeChart *linechart.LineChart
	statsText *text.Text

	// Deployment section
	activeTab       int
	tabText         *text.Text
	deploymentsText *text.Text

	// Log stream
	logViewer          *text.Text
	logBuffer          []LogEntry
	logMutex           sync.RWMutex
	seenLogs           map[string]bool // Track all logs we've seen to avoid duplicates
	logCount           int             // Track total logs to detect changes
	lastDisplayedIndex int             // Track the last log index that was displayed

	// Data buffers
	cpuHistory  *RingBuffer
	memHistory  *RingBuffer
	loadHistory *RingBuffer
	nodeHistory *RingBuffer

	// Resource tracking for fixed Y-axis
	totalCores    int
	totalMemoryGB float64

	// Configuration
	daemonURL string
}

// LogEntry represents a single log entry
type LogEntry struct {
	Timestamp    time.Time
	DeploymentID string
	NodeID       string
	Message      string
	Stream       string // stdout or stderr
}

// RingBuffer implements a circular buffer for time series data
type RingBuffer struct {
	data []float64
	size int
	pos  int
	mu   sync.RWMutex
}

func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		data: make([]float64, size),
		size: size,
		pos:  0,
	}
}

func (rb *RingBuffer) Add(value float64) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.data[rb.pos] = value
	rb.pos = (rb.pos + 1) % rb.size
}

func (rb *RingBuffer) GetData() []float64 {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	result := make([]float64, rb.size)
	// Copy data in chronological order: from oldest (pos) to newest (pos-1)
	// This ensures the graph shows a continuous timeline
	for i := 0; i < rb.size; i++ {
		result[i] = rb.data[(rb.pos+i)%rb.size]
	}
	return result
}

// runDashboardTUI runs the TUI dashboard
func runDashboardTUI(c *cli.Context) error {
	dash := &DashboardTUI{
		daemonURL:   getDaemonURL(c),
		cpuHistory:  NewRingBuffer(100),
		memHistory:  NewRingBuffer(100),
		loadHistory: NewRingBuffer(100),
		nodeHistory: NewRingBuffer(100),
		logBuffer:   make([]LogEntry, 0, 1000),
		seenLogs:    make(map[string]bool),
	}

	ctx, cancel := context.WithCancel(context.Background())
	dash.ctx = ctx
	dash.cancel = cancel

	// Initialize terminal
	terminal, err := tcell.New()
	if err != nil {
		return fmt.Errorf("failed to create terminal: %w", err)
	}
	dash.terminal = terminal
	defer terminal.Close()

	// Create widgets
	if err := dash.createWidgets(); err != nil {
		return fmt.Errorf("failed to create widgets: %w", err)
	}

	// Fetch initial metrics to set proper scales
	dash.fetchInitialMetrics()

	// Build layout
	builder := grid.New()
	builder.Add(
		// Top section - Cluster Statistics (30%)
		grid.RowHeightPerc(30,
			grid.ColWidthPerc(25, grid.Widget(dash.cpuChart, container.Border(linestyle.Light), container.BorderTitle("CPU Load %"))),
			grid.ColWidthPerc(25, grid.Widget(dash.memChart, container.Border(linestyle.Light), container.BorderTitle("Memory Usage %"))),
			grid.ColWidthPerc(25, grid.Widget(dash.loadChart, container.Border(linestyle.Light), container.BorderTitle("Load Avg (% of Cores)"))),
			grid.ColWidthPerc(25,
				grid.RowHeightPerc(60, grid.Widget(dash.nodeChart, container.Border(linestyle.Light), container.BorderTitle("Active Nodes"))),
				grid.RowHeightPerc(40, grid.Widget(dash.statsText, container.Border(linestyle.Light), container.BorderTitle("Cluster Stats"))),
			),
		),
		// Middle section - Deployments (40%)
		grid.RowHeightPerc(40,
			grid.RowHeightFixed(3, grid.Widget(dash.tabText, container.Border(linestyle.Light))),
			grid.RowHeightPerc(85, grid.Widget(dash.deploymentsText, container.Border(linestyle.Light), container.BorderTitle("Deployments"))),
		),
		// Bottom section - Logs (30%)
		grid.RowHeightPerc(30, grid.Widget(dash.logViewer, container.Border(linestyle.Light), container.BorderTitle("Live Logs"))),
	)

	gridOpts, err := builder.Build()
	if err != nil {
		return fmt.Errorf("failed to build grid: %w", err)
	}

	cont, err := container.New(terminal, gridOpts...)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}
	dash.container = cont

	// Start data collection goroutines
	go dash.collectClusterMetrics()
	go dash.collectDeployments()
	go dash.collectLogs()

	// Handle keyboard events
	quitter := func(k *terminalapi.Keyboard) {
		switch k.Key {
		case keyboard.KeyEsc, 'q':
			cancel()
		case '1':
			dash.activeTab = 0 // Running
			dash.updateTabDisplay()
		case '2':
			dash.activeTab = 1 // Provisioning
			dash.updateTabDisplay()
		case '3':
			dash.activeTab = 2 // Completed
			dash.updateTabDisplay()
		case '4':
			dash.activeTab = 3 // Failed
			dash.updateTabDisplay()
		case '[':
			dash.activeTab = (dash.activeTab - 1 + 4) % 4
			dash.updateTabDisplay()
		case ']':
			dash.activeTab = (dash.activeTab + 1) % 4
			dash.updateTabDisplay()
		}
	}

	// Run the dashboard
	err = termdash.Run(ctx, terminal, cont, termdash.KeyboardSubscriber(quitter), termdash.RedrawInterval(250*time.Millisecond))
	if err != nil {
		return fmt.Errorf("termdash run failed: %w", err)
	}

	return nil
}

// createWidgets initializes all dashboard widgets
func (d *DashboardTUI) createWidgets() error {
	// CPU Chart - 0-100% utilization
	cpuChart, err := linechart.New(
		linechart.AxesCellOpts(cell.FgColor(cell.ColorGray)),
		linechart.YLabelCellOpts(cell.FgColor(cell.ColorRed)),
		linechart.XLabelCellOpts(cell.FgColor(cell.ColorRed)),
		linechart.YAxisCustomScale(0, 100),
	)
	if err != nil {
		return err
	}
	d.cpuChart = cpuChart

	// Memory Chart - 0-100% usage
	memChart, err := linechart.New(
		linechart.AxesCellOpts(cell.FgColor(cell.ColorGray)),
		linechart.YLabelCellOpts(cell.FgColor(cell.ColorGreen)),
		linechart.XLabelCellOpts(cell.FgColor(cell.ColorGreen)),
		linechart.YAxisCustomScale(0, 100),
	)
	if err != nil {
		return err
	}
	d.memChart = memChart

	// Load Chart - will adjust dynamically based on total cores
	loadChart, err := linechart.New(
		linechart.AxesCellOpts(cell.FgColor(cell.ColorGray)),
		linechart.YLabelCellOpts(cell.FgColor(cell.ColorYellow)),
		linechart.XLabelCellOpts(cell.FgColor(cell.ColorYellow)),
	)
	if err != nil {
		return err
	}
	d.loadChart = loadChart

	// Node Count Chart - adaptive Y-axis
	nodeChart, err := linechart.New(
		linechart.AxesCellOpts(cell.FgColor(cell.ColorGray)),
		linechart.YLabelCellOpts(cell.FgColor(cell.ColorCyan)),
		linechart.XLabelCellOpts(cell.FgColor(cell.ColorCyan)),
	)
	if err != nil {
		return err
	}
	d.nodeChart = nodeChart

	// Stats Text
	statsText, err := text.New()
	if err != nil {
		return err
	}
	d.statsText = statsText

	// Tab Text
	tabText, err := text.New()
	if err != nil {
		return err
	}
	d.tabText = tabText
	d.activeTab = 0
	d.updateTabDisplay()

	// Deployments Text
	deploymentsText, err := text.New(text.WrapAtWords())
	if err != nil {
		return err
	}
	d.deploymentsText = deploymentsText

	// Log Viewer
	logViewer, err := text.New(text.RollContent())
	if err != nil {
		return err
	}
	d.logViewer = logViewer

	return nil
}

// fetchInitialMetrics fetches initial metrics to properly initialize the charts
func (d *DashboardTUI) fetchInitialMetrics() {
	// Try to fetch initial metrics
	resp, err := http.Get(d.daemonURL + "/api/v1/metrics")
	if err != nil {
		// Set defaults if we can't fetch
		d.totalCores = 4
		d.totalMemoryGB = 8
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		d.totalCores = 4
		d.totalMemoryGB = 8
		return
	}

	var metrics MetricsResponse
	if err := json.Unmarshal(body, &metrics); err != nil {
		d.totalCores = 4
		d.totalMemoryGB = 8
		return
	}

	// Set initial values
	d.totalCores = metrics.Summary.TotalCores
	d.totalMemoryGB = metrics.Summary.TotalMemoryGB

	// Initialize buffers with some initial data
	for i := 0; i < 100; i++ {
		d.cpuHistory.Add(0)
		d.memHistory.Add(0)
		d.loadHistory.Add(0)
		d.nodeHistory.Add(0)
	}
}

// updateTabDisplay updates the tab navigation display
func (d *DashboardTUI) updateTabDisplay() {
	tabs := []string{"Running", "Provisioning", "Completed", "Failed"}
	colors := []cell.Color{cell.ColorGreen, cell.ColorYellow, cell.ColorBlue, cell.ColorRed}

	d.tabText.Reset()
	d.tabText.Write(" ")
	for i, tab := range tabs {
		if i == d.activeTab {
			d.tabText.Write(fmt.Sprintf("[%s]", tab), text.WriteCellOpts(cell.FgColor(colors[i]), cell.Bold()))
		} else {
			d.tabText.Write(fmt.Sprintf(" %s ", tab), text.WriteCellOpts(cell.FgColor(cell.ColorGray)))
		}
		d.tabText.Write(" ")
	}
	d.tabText.Write("  (Use 1-4 or [/] to switch)")
}

// collectClusterMetrics periodically fetches and updates cluster-wide metrics
func (d *DashboardTUI) collectClusterMetrics() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			// Fetch metrics from API
			resp, err := http.Get(d.daemonURL + "/api/v1/metrics")
			if err != nil {
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				continue
			}

			var metrics MetricsResponse
			if err := json.Unmarshal(body, &metrics); err != nil {
				continue
			}

			// Update ring buffers
			summary := metrics.Summary

			// Store total resources for Y-axis scaling
			d.totalCores = summary.TotalCores
			d.totalMemoryGB = summary.TotalMemoryGB

			// Track actual load average (not percentage)
			d.loadHistory.Add(summary.AvgLoad)

			// Track actual memory used in GB
			d.memHistory.Add(summary.TotalMemoryUsedGB)

			// Track node count
			d.nodeHistory.Add(float64(summary.NodesWithMetrics))

			// Update charts with normalized data

			// For CPU chart: show load as percentage of total cores (0-100% scale)
			cpuData := make([]float64, 0, 100)
			loadData := d.loadHistory.GetData()
			for _, load := range loadData {
				if d.totalCores > 0 {
					// Convert load to percentage of total cores
					cpuPercent := (load / float64(d.totalCores)) * 100
					cpuData = append(cpuData, cpuPercent)
				} else {
					cpuData = append(cpuData, 0)
				}
			}

			// CPU shows 0-100% scale
			d.cpuChart.Series("cpu", cpuData,
				linechart.SeriesCellOpts(cell.FgColor(cell.ColorRed)))

			// Memory chart: normalize to 0-100% of total memory
			memData := make([]float64, 0, 100)
			memRawData := d.memHistory.GetData()
			for _, memUsed := range memRawData {
				if d.totalMemoryGB > 0 {
					// Convert to percentage
					memPercent := (memUsed / d.totalMemoryGB) * 100
					memData = append(memData, memPercent)
				} else {
					memData = append(memData, 0)
				}
			}

			// Memory shows 0-100% scale
			d.memChart.Series("memory", memData,
				linechart.SeriesCellOpts(cell.FgColor(cell.ColorGreen)))

			// Load chart: show actual load values with scale normalized to total cores
			// We'll scale the data to make total cores = 100 on the chart
			loadNormData := make([]float64, 0, 100)
			for _, load := range loadData {
				if d.totalCores > 0 {
					// Normalize so that totalCores = 100 on display
					normalized := (load / float64(d.totalCores)) * 100
					loadNormData = append(loadNormData, normalized)
				} else {
					loadNormData = append(loadNormData, load*10) // Default scaling
				}
			}

			d.loadChart.Series("load", loadNormData,
				linechart.SeriesCellOpts(cell.FgColor(cell.ColorYellow)))

			// Node chart: show actual count
			d.nodeChart.Series("nodes", d.nodeHistory.GetData(),
				linechart.SeriesCellOpts(cell.FgColor(cell.ColorCyan)))

			// Update stats text
			d.statsText.Reset()
			d.statsText.Write(fmt.Sprintf("Total Cores: %d\n", summary.TotalCores))
			d.statsText.Write(fmt.Sprintf("Memory: %.1f/%.1fGB\n", summary.TotalMemoryUsedGB, summary.TotalMemoryGB))
			d.statsText.Write(fmt.Sprintf("Avg Load: %.2f\n", summary.AvgLoad))
			d.statsText.Write(fmt.Sprintf("Active Nodes: %d", summary.NodesWithMetrics))
		}
	}
}

// collectDeployments periodically fetches and updates deployment information
func (d *DashboardTUI) collectDeployments() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			// Fetch deployments from API
			resp, err := http.Get(d.daemonURL + "/api/v1/deployments")
			if err != nil {
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				continue
			}

			var deployments []map[string]interface{}
			if err := json.Unmarshal(body, &deployments); err != nil {
				continue
			}

			// Filter deployments by current tab
			statusFilter := []string{"running", "provisioning", "completed", "failed"}[d.activeTab]
			filtered := []map[string]interface{}{}

			for _, dep := range deployments {
				status := fmt.Sprintf("%v", dep["status"])
				if status == statusFilter || (statusFilter == "provisioning" && status == "pending") {
					// Fetch full deployment details including nodes
					depID := fmt.Sprintf("%v", dep["deployment_id"])
					detailResp, err := http.Get(d.daemonURL + "/api/v1/deployments/" + depID)
					if err != nil {
						filtered = append(filtered, dep)
						continue
					}

					detailBody, err := io.ReadAll(detailResp.Body)
					detailResp.Body.Close()
					if err != nil {
						filtered = append(filtered, dep)
						continue
					}

					var fullDep map[string]interface{}
					if err := json.Unmarshal(detailBody, &fullDep); err != nil {
						filtered = append(filtered, dep)
						continue
					}

					filtered = append(filtered, fullDep)
				}
			}

			// Sort by creation time (newest first)
			sort.Slice(filtered, func(i, j int) bool {
				iTime, _ := time.Parse(time.RFC3339, fmt.Sprintf("%v", filtered[i]["created_at"]))
				jTime, _ := time.Parse(time.RFC3339, fmt.Sprintf("%v", filtered[j]["created_at"]))
				return iTime.After(jTime)
			})

			// Update deployments display
			d.updateDeploymentsDisplay(filtered)
		}
	}
}

// updateDeploymentsDisplay renders deployment cards in the middle section
func (d *DashboardTUI) updateDeploymentsDisplay(deployments []map[string]interface{}) {
	d.deploymentsText.Reset()

	if len(deployments) == 0 {
		statusNames := []string{"running", "provisioning", "completed", "failed"}
		d.deploymentsText.Write(fmt.Sprintf("\nNo %s deployments found.\n", statusNames[d.activeTab]))
		return
	}

	// Limit to 3 deployments for display (to leave room for node details)
	if len(deployments) > 3 {
		deployments = deployments[:3]
	}

	for idx, dep := range deployments {
		if idx > 0 {
			d.deploymentsText.Write("\n")
		}

		id := fmt.Sprintf("%v", dep["deployment_id"])
		status := fmt.Sprintf("%v", dep["status"])
		totalNodes, _ := dep["total_nodes"].(float64)
		nodesCompleted, _ := dep["nodes_completed"].(float64)
		nodesFailed, _ := dep["nodes_failed"].(float64)

		// Calculate progress
		progress := 0
		if totalNodes > 0 {
			progress = int((nodesCompleted / totalNodes) * 100)
		}

		// Format creation time
		createdAt := ""
		if created, ok := dep["created_at"].(string); ok {
			if t, err := time.Parse(time.RFC3339, created); err == nil {
				duration := time.Since(t)
				if duration < time.Minute {
					createdAt = fmt.Sprintf("%ds ago", int(duration.Seconds()))
				} else if duration < time.Hour {
					createdAt = fmt.Sprintf("%dm ago", int(duration.Minutes()))
				} else {
					createdAt = fmt.Sprintf("%dh ago", int(duration.Hours()))
				}
			}
		}

		// Write deployment header
		d.deploymentsText.Write(fmt.Sprintf("\n%s", id), text.WriteCellOpts(cell.FgColor(cell.ColorCyan), cell.Bold()))
		d.deploymentsText.Write(fmt.Sprintf(" (%s)\n", createdAt), text.WriteCellOpts(cell.FgColor(cell.ColorGray)))

		// Progress bar
		progressBar := ""
		progressFilled := progress / 10
		for i := 0; i < 10; i++ {
			if i < progressFilled {
				progressBar += "█"
			} else {
				progressBar += "░"
			}
		}

		progressColor := cell.ColorGreen
		if nodesFailed > 0 {
			progressColor = cell.ColorRed
		} else if status == "provisioning" || status == "pending" {
			progressColor = cell.ColorYellow
		}

		d.deploymentsText.Write("Progress: [")
		d.deploymentsText.Write(progressBar, text.WriteCellOpts(cell.FgColor(progressColor)))
		d.deploymentsText.Write(fmt.Sprintf("] %d%% - %.0f/%.0f nodes", progress, nodesCompleted, totalNodes))

		if nodesFailed > 0 {
			d.deploymentsText.Write(fmt.Sprintf(" (%.0f failed)", nodesFailed), text.WriteCellOpts(cell.FgColor(cell.ColorRed)))
		}
		d.deploymentsText.Write("\n")

		// Display node information
		d.displayNodeInfo(dep)
	}
}

// displayNodeInfo renders per-node information for a deployment
func (d *DashboardTUI) displayNodeInfo(deployment map[string]interface{}) {
	nodes, ok := deployment["nodes"].([]interface{})
	if !ok || len(nodes) == 0 {
		return
	}

	// Display up to 4 nodes per deployment to keep it compact
	displayLimit := 4
	if len(nodes) > displayLimit {
		d.deploymentsText.Write(fmt.Sprintf("  Nodes (showing %d of %d):\n", displayLimit, len(nodes)),
			text.WriteCellOpts(cell.FgColor(cell.ColorGray)))
	} else {
		d.deploymentsText.Write("  Nodes:\n", text.WriteCellOpts(cell.FgColor(cell.ColorGray)))
	}

	for i, node := range nodes {
		if i >= displayLimit {
			break
		}

		n, ok := node.(map[string]interface{})
		if !ok {
			continue
		}

		nodeID := fmt.Sprintf("%v", n["node_id"])
		nodeStatus := fmt.Sprintf("%v", n["status"])

		// Shorten node ID for display
		shortNodeID := nodeID

		// Get IP address
		ipAddress := "pending"
		if n["ip_address"] != nil {
			ipStr := fmt.Sprintf("%v", n["ip_address"])
			if ipStr != "" && ipStr != "<nil>" {
				ipAddress = ipStr
			}
		}

		// Get instance ID
		instanceID := "-"
		if n["instance_id"] != nil {
			instStr := fmt.Sprintf("%v", n["instance_id"])
			if instStr != "" && instStr != "<nil>" {
				instanceID = instStr
				// Shorten instance ID for display
				if len(instanceID) > 25 {
					instanceID = instanceID[:25] + "..."
				}
			}
		}

		// Color-code status
		statusColor := cell.ColorWhite
		switch nodeStatus {
		case "running":
			statusColor = cell.ColorGreen
		case "provisioning", "pending":
			statusColor = cell.ColorYellow
		case "completed":
			statusColor = cell.ColorBlue
		case "failed":
			statusColor = cell.ColorRed
		}

		// Write node line: [node-id] status | IP: ip | Instance: instance-id
		d.deploymentsText.Write("    [")
		d.deploymentsText.Write(shortNodeID, text.WriteCellOpts(cell.FgColor(cell.ColorCyan)))
		d.deploymentsText.Write("] ")
		d.deploymentsText.Write(fmt.Sprintf("%-12s", nodeStatus), text.WriteCellOpts(cell.FgColor(statusColor)))
		d.deploymentsText.Write(" | IP: ")
		d.deploymentsText.Write(fmt.Sprintf("%-15s", ipAddress), text.WriteCellOpts(cell.FgColor(cell.ColorWhite)))
		d.deploymentsText.Write(" | ")
		d.deploymentsText.Write(instanceID, text.WriteCellOpts(cell.FgColor(cell.ColorGray)))
		d.deploymentsText.Write("\n")
	}
}

// collectLogs periodically fetches and displays logs from all deployments
func (d *DashboardTUI) collectLogs() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			// Fetch deployments to get IDs
			resp, err := http.Get(d.daemonURL + "/api/v1/deployments")
			if err != nil {
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				continue
			}

			var deployments []map[string]interface{}
			if err := json.Unmarshal(body, &deployments); err != nil {
				continue
			}

			// Track if any new logs were added
			previousLogCount := d.logCount

			// Fetch logs from all deployments
			for _, dep := range deployments {
				id := fmt.Sprintf("%v", dep["deployment_id"])
				d.fetchDeploymentLogs(id)
			}

			// Only update display if we got new logs
			if d.logCount > previousLogCount {
				d.updateLogDisplay()
			}
		}
	}
}

// fetchDeploymentLogs fetches logs for a specific deployment
func (d *DashboardTUI) fetchDeploymentLogs(deploymentID string) {
	// Build URL - fetch last 100 logs
	url := fmt.Sprintf("%s/api/v1/deployments/%s/logs?limit=100", d.daemonURL, deploymentID)

	resp, err := http.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Check if we got any response
	if resp.StatusCode != http.StatusOK {
		return
	}

	// Parse response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	// Parse the JSON response (matching the structure from main.go)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return
	}

	// Extract logs array from the result
	logsInterface, ok := result["logs"].([]interface{})
	if !ok || len(logsInterface) == 0 {
		return
	}

	// Convert to map slice
	var logs []map[string]interface{}
	for _, logItem := range logsInterface {
		if logMap, ok := logItem.(map[string]interface{}); ok {
			logs = append(logs, logMap)
		}
	}

	d.logMutex.Lock()
	defer d.logMutex.Unlock()

	for _, log := range logs {
		// Handle different possible field names
		nodeID := ""
		if val, ok := log["node_id"]; ok {
			nodeID = fmt.Sprintf("%v", val)
		} else if val, ok := log["nodeId"]; ok {
			nodeID = fmt.Sprintf("%v", val)
		}

		message := ""
		if val, ok := log["message"]; ok {
			message = fmt.Sprintf("%v", val)
		} else if val, ok := log["log"]; ok {
			message = fmt.Sprintf("%v", val)
		}

		stream := "stdout"
		if val, ok := log["stream"]; ok {
			stream = fmt.Sprintf("%v", val)
		}

		// Skip empty messages
		if message == "" {
			continue
		}

		// Get timestamp
		timestamp := ""
		if ts, ok := log["timestamp"].(string); ok {
			timestamp = ts
		}

		// Create a unique key for this exact log entry
		// Include all fields to ensure uniqueness
		logKey := fmt.Sprintf("%s|%s|%s|%s|%s", deploymentID, nodeID, timestamp, stream, message)

		// Skip if we've already seen this exact log
		if d.seenLogs[logKey] {
			continue
		}

		// Mark this log as seen
		d.seenLogs[logKey] = true

		entry := LogEntry{
			DeploymentID: deploymentID,
			NodeID:       nodeID,
			Message:      message,
			Stream:       stream,
			Timestamp:    time.Now(),
		}

		// Try to parse timestamp if available
		if timestamp != "" {
			if t, err := time.Parse(time.RFC3339, timestamp); err == nil {
				entry.Timestamp = t
			}
		}

		// Add to buffer and increment count
		d.logBuffer = append(d.logBuffer, entry)
		d.logCount++
	}

	// Keep buffer size limited to 1000 entries
	if len(d.logBuffer) > 1000 {
		// Remove oldest entries
		removed := len(d.logBuffer) - 1000
		d.logBuffer = d.logBuffer[removed:]

		// Adjust the last displayed index
		if d.lastDisplayedIndex > removed {
			d.lastDisplayedIndex -= removed
		} else {
			d.lastDisplayedIndex = 0
		}
	}

	// Clean up seenLogs map if it gets too large
	if len(d.seenLogs) > 5000 {
		// Rebuild from current buffer to keep memory usage reasonable
		d.seenLogs = make(map[string]bool)
		for _, entry := range d.logBuffer {
			logKey := fmt.Sprintf("%s|%s|%s|%s|%s",
				entry.DeploymentID, entry.NodeID,
				entry.Timestamp.Format(time.RFC3339),
				entry.Stream, entry.Message)
			d.seenLogs[logKey] = true
		}
	}
}

// updateLogDisplay updates the log viewer widget
func (d *DashboardTUI) updateLogDisplay() {
	d.logMutex.RLock()
	defer d.logMutex.RUnlock()

	// Check if we need to do a full reset (buffer was trimmed)
	if d.lastDisplayedIndex > len(d.logBuffer) {
		d.lastDisplayedIndex = 0
		d.logViewer.Reset()
	}

	// Use a map to assign colors to deployment IDs
	colors := []cell.Color{cell.ColorCyan, cell.ColorMagenta, cell.ColorYellow, cell.ColorGreen, cell.ColorBlue}
	deploymentColors := make(map[string]cell.Color)

	// Build color map from all logs (for consistency)
	colorIndex := 0
	for _, log := range d.logBuffer {
		if _, ok := deploymentColors[log.DeploymentID]; !ok {
			deploymentColors[log.DeploymentID] = colors[colorIndex%len(colors)]
			colorIndex++
		}
	}

	// Only append new logs since last display
	for i := d.lastDisplayedIndex; i < len(d.logBuffer); i++ {
		log := d.logBuffer[i]

		// Format: [deployment-id][node-id] message
		d.logViewer.Write("[", text.WriteCellOpts(cell.FgColor(cell.ColorGray)))

		// Handle short deployment IDs
		depID := log.DeploymentID
		d.logViewer.Write(depID, text.WriteCellOpts(cell.FgColor(deploymentColors[log.DeploymentID])))

		d.logViewer.Write("][", text.WriteCellOpts(cell.FgColor(cell.ColorGray)))

		// Handle short node IDs
		nodeID := log.NodeID
		d.logViewer.Write(nodeID, text.WriteCellOpts(cell.FgColor(cell.ColorWhite)))

		d.logViewer.Write("] ", text.WriteCellOpts(cell.FgColor(cell.ColorGray)))

		// Color stderr differently
		if log.Stream == "stderr" {
			d.logViewer.Write(log.Message, text.WriteCellOpts(cell.FgColor(cell.ColorRed)))
		} else {
			d.logViewer.Write(log.Message)
		}
		d.logViewer.Write("\n")
	}

	// Update the last displayed index
	d.lastDisplayedIndex = len(d.logBuffer)
}

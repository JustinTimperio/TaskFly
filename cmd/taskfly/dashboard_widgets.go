package main

import (
	"fmt"
	"time"

	"github.com/mum4k/termdash/cell"
	"github.com/mum4k/termdash/container"
	"github.com/mum4k/termdash/container/grid"
	"github.com/mum4k/termdash/widgets/gauge"
	"github.com/mum4k/termdash/widgets/sparkline"
	"github.com/mum4k/termdash/widgets/text"
)

// DeploymentCard represents a single deployment's display card
type DeploymentCard struct {
	ID           string
	Status       string
	CreatedAt    time.Time
	TotalNodes   int
	NodesComplete int
	NodesFailed  int
	CPUUsage     float64
	MemUsage     float64

	// Widgets
	infoText     *text.Text
	cpuGauge     *gauge.Gauge
	memGauge     *gauge.Gauge
	progressGauge *gauge.Gauge
}

// NewDeploymentCard creates a new deployment card with widgets
func NewDeploymentCard(id string) (*DeploymentCard, error) {
	card := &DeploymentCard{
		ID: id,
	}

	// Create info text widget
	infoText, err := text.New(text.WrapAtWords())
	if err != nil {
		return nil, err
	}
	card.infoText = infoText

	// Create CPU gauge
	cpuGauge, err := gauge.New(
		gauge.Height(1),
		gauge.Color(cell.ColorGreen),
		gauge.BorderTitle("CPU"),
	)
	if err != nil {
		return nil, err
	}
	card.cpuGauge = cpuGauge

	// Create memory gauge
	memGauge, err := gauge.New(
		gauge.Height(1),
		gauge.Color(cell.ColorBlue),
		gauge.BorderTitle("Memory"),
	)
	if err != nil {
		return nil, err
	}
	card.memGauge = memGauge

	// Create progress gauge for node completion
	progressGauge, err := gauge.New(
		gauge.Height(1),
		gauge.Color(cell.ColorCyan),
		gauge.BorderTitle("Node Progress"),
	)
	if err != nil {
		return nil, err
	}
	card.progressGauge = progressGauge

	return card, nil
}

// Update updates the deployment card with new data
func (dc *DeploymentCard) Update(data map[string]interface{}) {
	// Parse data
	if status, ok := data["status"].(string); ok {
		dc.Status = status
	}

	if created, ok := data["created_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, created); err == nil {
			dc.CreatedAt = t
		}
	}

	if total, ok := data["total_nodes"].(float64); ok {
		dc.TotalNodes = int(total)
	}

	if completed, ok := data["nodes_completed"].(float64); ok {
		dc.NodesComplete = int(completed)
	}

	if failed, ok := data["nodes_failed"].(float64); ok {
		dc.NodesFailed = int(failed)
	}

	// Update widgets
	dc.updateDisplay()
}

// updateDisplay refreshes the widget displays
func (dc *DeploymentCard) updateDisplay() {
	// Update info text
	dc.infoText.Reset()

	// Format time since creation
	age := time.Since(dc.CreatedAt)
	ageStr := ""
	if age < time.Minute {
		ageStr = fmt.Sprintf("%ds ago", int(age.Seconds()))
	} else if age < time.Hour {
		ageStr = fmt.Sprintf("%dm ago", int(age.Minutes()))
	} else {
		ageStr = fmt.Sprintf("%dh ago", int(age.Hours()))
	}

	dc.infoText.Write(fmt.Sprintf("%s ", dc.ID[:12]), text.WriteCellOpts(cell.Bold()))
	dc.infoText.Write(fmt.Sprintf("(%s)\n", ageStr), text.WriteCellOpts(cell.FgColor(cell.ColorGray)))

	// Status color
	statusColor := cell.ColorWhite
	switch dc.Status {
	case "running":
		statusColor = cell.ColorGreen
	case "provisioning", "pending":
		statusColor = cell.ColorYellow
	case "completed":
		statusColor = cell.ColorBlue
	case "failed":
		statusColor = cell.ColorRed
	}

	dc.infoText.Write("Status: ", text.WriteCellOpts(cell.FgColor(cell.ColorGray)))
	dc.infoText.Write(dc.Status, text.WriteCellOpts(cell.FgColor(statusColor)))
	dc.infoText.Write("\n")

	// Node summary
	dc.infoText.Write(fmt.Sprintf("Nodes: %d/%d", dc.NodesComplete, dc.TotalNodes))
	if dc.NodesFailed > 0 {
		dc.infoText.Write(fmt.Sprintf(" (%d failed)", dc.NodesFailed), text.WriteCellOpts(cell.FgColor(cell.ColorRed)))
	}

	// Update CPU gauge
	cpuColor := cell.ColorGreen
	if dc.CPUUsage > 70 {
		cpuColor = cell.ColorYellow
	}
	if dc.CPUUsage > 90 {
		cpuColor = cell.ColorRed
	}
	dc.cpuGauge.Percent(int(dc.CPUUsage), gauge.Color(cpuColor))

	// Update memory gauge
	memColor := cell.ColorBlue
	if dc.MemUsage > 70 {
		memColor = cell.ColorYellow
	}
	if dc.MemUsage > 90 {
		memColor = cell.ColorRed
	}
	dc.memGauge.Percent(int(dc.MemUsage), gauge.Color(memColor))

	// Update progress gauge
	progress := 0
	if dc.TotalNodes > 0 {
		progress = (dc.NodesComplete * 100) / dc.TotalNodes
	}

	progressColor := cell.ColorCyan
	if dc.NodesFailed > 0 {
		progressColor = cell.ColorRed
	} else if dc.Status == "completed" {
		progressColor = cell.ColorGreen
	} else if dc.Status == "provisioning" || dc.Status == "pending" {
		progressColor = cell.ColorYellow
	}

	dc.progressGauge.Percent(progress, gauge.Color(progressColor))
}

// BuildCardGrid creates a grid layout for the deployment card
func (dc *DeploymentCard) BuildCardGrid() []container.Option {
	builder := grid.New()
	builder.Add(
		// Info section (left side)
		grid.ColWidthPerc(40, grid.Widget(dc.infoText)),
		// Gauges section (right side)
		grid.ColWidthPerc(60,
			grid.RowHeightFixed(1, grid.Widget(dc.cpuGauge)),
			grid.RowHeightFixed(1, grid.Widget(dc.memGauge)),
			grid.RowHeightFixed(1, grid.Widget(dc.progressGauge)),
		),
	)

	opts, _ := builder.Build()
	return opts
}

// MetricsCard represents cluster-wide metrics display
type MetricsCard struct {
	sparklines map[string]*sparkline.SparkLine
}

// NewMetricsCard creates a new metrics display card
func NewMetricsCard() (*MetricsCard, error) {
	card := &MetricsCard{
		sparklines: make(map[string]*sparkline.SparkLine),
	}

	// Create sparklines for quick metrics
	cpuSpark, err := sparkline.New(
		sparkline.Label("CPU", cell.FgColor(cell.ColorRed)),
		sparkline.Color(cell.ColorRed),
	)
	if err != nil {
		return nil, err
	}
	card.sparklines["cpu"] = cpuSpark

	memSpark, err := sparkline.New(
		sparkline.Label("MEM", cell.FgColor(cell.ColorGreen)),
		sparkline.Color(cell.ColorGreen),
	)
	if err != nil {
		return nil, err
	}
	card.sparklines["memory"] = memSpark

	loadSpark, err := sparkline.New(
		sparkline.Label("LOAD", cell.FgColor(cell.ColorYellow)),
		sparkline.Color(cell.ColorYellow),
	)
	if err != nil {
		return nil, err
	}
	card.sparklines["load"] = loadSpark

	return card, nil
}

// UpdateSparkline updates a specific sparkline with new data
func (mc *MetricsCard) UpdateSparkline(name string, data []int) error {
	if spark, ok := mc.sparklines[name]; ok {
		return spark.Add(data)
	}
	return fmt.Errorf("sparkline %s not found", name)
}
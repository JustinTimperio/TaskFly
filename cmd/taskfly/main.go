package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/JustinTimperio/TaskFly/internal/validation"
	"github.com/chzyer/readline"
	"github.com/pterm/pterm"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v2"
)

// NodesConfig represents the enhanced nodes configuration
type NodesConfig struct {
	Count            int                      `yaml:"count"`
	GlobalMetadata   map[string]interface{}   `yaml:"global_metadata"`
	DistributedLists map[string][]interface{} `yaml:"distributed_lists"`
	ConfigTemplate   map[string]interface{}   `yaml:"config_template"`
}

// TaskFlyConfig represents the taskfly.yml configuration
type TaskFlyConfig struct {
	CloudProvider     string                            `yaml:"cloud_provider"`
	InstanceConfig    map[string]map[string]interface{} `yaml:"instance_config"`
	ApplicationFiles  []string                          `yaml:"application_files"`
	RemoteDestDir     string                            `yaml:"remote_dest_dir"`
	RemoteScriptToRun string                            `yaml:"remote_script_to_run"`
	BundleName        string                            `yaml:"bundle_name"`
	Nodes             NodesConfig                       `yaml:"nodes"`
}

// CLIConfig represents the ~/.taskfly/taskfly.yml configuration
type CLIConfig struct {
	DaemonIP   string `yaml:"daemon_ip"`
	DaemonPort string `yaml:"daemon_port"`
	Verbose    bool   `yaml:"verbose"`
}

// loadCLIConfig loads the CLI configuration from ~/.taskfly/taskfly.yml
func loadCLIConfig() (*CLIConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	configPath := filepath.Join(homeDir, ".taskfly", "taskfly.yml")

	// If config file doesn't exist, return empty config (not an error)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return &CLIConfig{}, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config CLIConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &config, nil
}

func main() {
	// Load CLI config from ~/.taskfly/taskfly.yml
	cliConfig, err := loadCLIConfig()
	if err != nil {
		logrus.Warnf("Failed to load CLI config: %v", err)
		cliConfig = &CLIConfig{} // Use empty config on error
	}

	// Set defaults from config file (flags and env vars will override)
	daemonIP := "localhost"
	daemonPort := "8080"
	verbose := false

	if cliConfig.DaemonIP != "" {
		daemonIP = cliConfig.DaemonIP
	}
	if cliConfig.DaemonPort != "" {
		daemonPort = cliConfig.DaemonPort
	}
	if cliConfig.Verbose {
		verbose = cliConfig.Verbose
	}

	app := &cli.App{
		Name:  "taskfly",
		Usage: "Distributed task orchestration CLI",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "daemon-ip",
				Aliases: []string{"d"},
				Usage:   "IP address of the TaskFly daemon",
				Value:   daemonIP,
				EnvVars: []string{"TASKFLY_DAEMON_IP"},
			},
			&cli.StringFlag{
				Name:    "daemon-port",
				Aliases: []string{"p"},
				Usage:   "Port of the TaskFly daemon",
				Value:   daemonPort,
				EnvVars: []string{"TASKFLY_DAEMON_PORT"},
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Enable verbose logging",
				Value:   verbose,
				EnvVars: []string{"TASKFLY_VERBOSE"},
			},
		},
		Commands: []*cli.Command{
			{
				Name:   "up",
				Usage:  "Deploy and run a new deployment",
				Action: deployCommand,
			},
			{
				Name:   "validate",
				Usage:  "Validate taskfly.yml configuration",
				Action: validateCommand,
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "config",
						Aliases: []string{"c"},
						Usage:   "Path to taskfly.yml config file",
						Value:   "taskfly.yml",
					},
				},
			},
			{
				Name:   "list",
				Usage:  "List all deployments",
				Action: listCommand,
			},
			{
				Name:   "status",
				Usage:  "Show status of a deployment",
				Action: statusCommand,
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "id",
						Usage:    "Deployment ID",
						Required: true,
					},
				},
			},
			{
				Name:   "logs",
				Usage:  "Stream logs from a deployment",
				Action: logsCommand,
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "id",
						Usage:    "Deployment ID",
						Required: true,
					},
					&cli.StringFlag{
						Name:  "node",
						Usage: "Filter logs by node ID (optional)",
					},
					&cli.BoolFlag{
						Name:    "follow",
						Aliases: []string{"f"},
						Usage:   "Follow log output",
					},
				},
			},
			{
				Name:   "down",
				Usage:  "Terminate a deployment",
				Action: downCommand,
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "id",
						Usage:    "Deployment ID",
						Required: true,
					},
				},
			},
			{
				Name:   "shell",
				Usage:  "Start an interactive shell for managing deployments",
				Action: shellCommand,
			},
			{
				Name:    "dashboard",
				Aliases: []string{"dash"},
				Usage:   "Show the deployment dashboard",
				Action:  dashboardCommand,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
}

// getDaemonURL constructs the daemon URL from the IP and port flags
func getDaemonURL(c *cli.Context) string {
	ip := c.String("daemon-ip")
	port := c.String("daemon-port")
	return fmt.Sprintf("http://%s:%s", ip, port)
}

func validateCommand(c *cli.Context) error {
	configPath := c.String("config")

	pterm.DefaultSection.Printfln("Validating configuration: %s", configPath)
	fmt.Println()

	// Check if config file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		pterm.Error.Printfln("Config file not found: %s", configPath)
		return fmt.Errorf("config file not found")
	}

	// Create validator
	validator, err := validation.NewValidator(configPath)
	if err != nil {
		pterm.Error.Printfln("Failed to parse config: %v", err)
		return err
	}

	// Run validation
	result := validator.Validate()

	// Display results
	hasIssues := false

	if len(result.Errors) > 0 {
		hasIssues = true
		pterm.DefaultSection.WithLevel(2).Println("Errors")
		for _, err := range result.Errors {
			pterm.Error.Printf("  %s: %s\n", pterm.FgRed.Sprint(err.Field), err.Message)
		}
		fmt.Println()
	}

	if len(result.Warnings) > 0 {
		hasIssues = true
		pterm.DefaultSection.WithLevel(2).Println("Warnings")
		for _, warn := range result.Warnings {
			pterm.Warning.Printf("  %s: %s\n", pterm.FgYellow.Sprint(warn.Field), warn.Message)
		}
		fmt.Println()
	}

	if len(result.Info) > 0 {
		pterm.DefaultSection.WithLevel(2).Println("Info")
		for _, info := range result.Info {
			pterm.Info.Printf("  %s: %s\n", pterm.FgCyan.Sprint(info.Field), info.Message)
		}
		fmt.Println()
	}

	// Summary
	if result.Valid && !hasIssues {
		pterm.Success.Println("✓ Configuration is valid! No issues found.")
		return nil
	} else if result.Valid {
		pterm.Success.Printfln("✓ Configuration is valid (%d warnings, %d info messages)",
			len(result.Warnings), len(result.Info))
		return nil
	} else {
		pterm.Error.Printfln("✗ Configuration is invalid (%d errors, %d warnings)",
			len(result.Errors), len(result.Warnings))
		return fmt.Errorf("validation failed")
	}
}

func deployCommand(c *cli.Context) error {
	if c.Bool("verbose") {
		logrus.SetLevel(logrus.DebugLevel)
	}

	fmt.Println("🚀 Starting TaskFly deployment...")
	if c.Bool("verbose") {
		fmt.Printf("🔧 Using daemon URL: %s\n", getDaemonURL(c))
	}

	// Load configuration
	config, err := loadConfig("taskfly.yml")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Create bundle
	fmt.Println("📦 Creating application bundle...")
	bundlePath, err := createBundle(config)
	if err != nil {
		return fmt.Errorf("failed to create bundle: %w", err)
	}
	defer os.Remove(bundlePath) // Clean up

	// Upload to daemon
	fmt.Println("⬆️ Uploading bundle to daemon...")
	resp, err := uploadBundle(c, bundlePath)
	if err != nil {
		return fmt.Errorf("failed to upload bundle: %w", err)
	}

	fmt.Printf("✅ Deployment created: %s\n", resp["deployment_id"])
	fmt.Printf("📊 Status URL: %s\n", resp["status_url"])

	return nil
}

func listCommand(c *cli.Context) error {
	pterm.Info.Println("Fetching deployments...")

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
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if len(deployments) == 0 {
		pterm.Info.Println("No deployments found")
		return nil
	}

	// Create table data
	tableData := pterm.TableData{
		{"ID", "Status", "Nodes", "Completed", "Failed", "Created"},
	}

	for _, dep := range deployments {
		status := fmt.Sprintf("%v", dep["status"])
		statusFormatted := formatStatus(status)

		created := ""
		if createdAt, ok := dep["created_at"].(string); ok {
			if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
				created = t.Format("2006-01-02 15:04:05")
			}
		}

		tableData = append(tableData, []string{
			fmt.Sprintf("%v", dep["deployment_id"]),
			statusFormatted,
			fmt.Sprintf("%v", dep["total_nodes"]),
			fmt.Sprintf("%v", dep["nodes_completed"]),
			fmt.Sprintf("%v", dep["nodes_failed"]),
			created,
		})
	}

	pterm.DefaultTable.WithHasHeader().WithData(tableData).Render()

	return nil
}

func formatStatus(status string) string {
	switch status {
	case "running":
		return pterm.FgGreen.Sprint(status)
	case "completed":
		return pterm.FgCyan.Sprint(status)
	case "failed":
		return pterm.FgRed.Sprint(status)
	case "pending", "provisioning":
		return pterm.FgYellow.Sprint(status)
	case "terminated":
		return pterm.FgGray.Sprint(status)
	default:
		return status
	}
}

func statusCommand(c *cli.Context) error {
	if c.Bool("verbose") {
		logrus.SetLevel(logrus.DebugLevel)
	}

	id := c.String("id")
	pterm.Info.Printfln("Getting status for deployment: %s", id)

	resp, err := http.Get(getDaemonURL(c) + "/api/v1/deployments/" + id)
	if err != nil {
		return fmt.Errorf("failed to get deployment status: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var deployment map[string]interface{}
	if err := json.Unmarshal(body, &deployment); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	// Handle case where deployment doesn't exist
	if deployment["deployment_id"] == nil {
		return fmt.Errorf("deployment %s not found", id)
	}

	// Display deployment info
	status := fmt.Sprintf("%v", deployment["status"])
	pterm.DefaultSection.Printfln("Deployment: %s", deployment["deployment_id"])
	fmt.Printf("Status: %s\n", formatStatus(status))
	fmt.Printf("Cloud Provider: %v\n", deployment["cloud_provider"])
	fmt.Printf("Total Nodes: %v\n", deployment["total_nodes"])
	fmt.Printf("Completed: %v | Failed: %v\n\n", deployment["nodes_completed"], deployment["nodes_failed"])

	// Safely handle nodes array
	if deployment["nodes"] == nil {
		pterm.Info.Println("No nodes found for this deployment")
		return nil
	}

	nodes, ok := deployment["nodes"].([]interface{})
	if !ok {
		pterm.Error.Println("Invalid nodes data format")
		return nil
	}

	if len(nodes) == 0 {
		pterm.Info.Println("No nodes found for this deployment")
		return nil
	}

	// Create nodes table
	tableData := pterm.TableData{
		{"Node ID", "Status", "IP Address", "Instance ID"},
	}

	for _, node := range nodes {
		n := node.(map[string]interface{})
		nodeID := fmt.Sprintf("%v", n["node_id"])
		nodeStatus := fmt.Sprintf("%v", n["status"])
		ip := "pending"
		if n["ip_address"] != nil {
			ipStr := fmt.Sprintf("%v", n["ip_address"])
			if ipStr != "" {
				ip = ipStr
			}
		}
		instanceID := "-"
		if n["instance_id"] != nil {
			instanceID = fmt.Sprintf("%v", n["instance_id"])
		}

		tableData = append(tableData, []string{
			nodeID,
			formatStatus(nodeStatus),
			ip,
			instanceID,
		})
	}

	pterm.DefaultTable.WithHasHeader().WithData(tableData).Render()

	return nil
}

func logsCommand(c *cli.Context) error {
	id := c.String("id")
	nodeFilter := c.String("node")
	follow := c.Bool("follow")

	pterm.Info.Printfln("Fetching logs for deployment: %s", id)
	if nodeFilter != "" {
		pterm.Info.Printfln("Filtering by node: %s", nodeFilter)
	}

	// Define colors for different nodes (cycling through)
	colors := []func(...interface{}) string{
		pterm.FgLightCyan.Sprint,
		pterm.FgLightGreen.Sprint,
		pterm.FgLightYellow.Sprint,
		pterm.FgLightMagenta.Sprint,
		pterm.FgLightBlue.Sprint,
	}

	nodeColors := make(map[string]func(...interface{}) string)
	colorIndex := 0

	var lastTimestamp time.Time

	for {
		// Build URL with query parameters
		url := fmt.Sprintf("%s/api/v1/deployments/%s/logs?limit=1000", getDaemonURL(c), id)
		if nodeFilter != "" {
			url += "&node=" + nodeFilter
		}
		if !lastTimestamp.IsZero() {
			url += "&since=" + lastTimestamp.Format(time.RFC3339)
		}

		resp, err := http.Get(url)
		if err != nil {
			return fmt.Errorf("failed to fetch logs: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}

		logs, ok := result["logs"].([]interface{})
		if !ok || len(logs) == 0 {
			if !follow {
				if lastTimestamp.IsZero() {
					pterm.Info.Println("No logs available yet")
				}
				break
			}
			time.Sleep(3 * time.Second)
			continue
		}

		// Display logs
		for _, logEntry := range logs {
			log := logEntry.(map[string]interface{})

			nodeID := fmt.Sprintf("%v", log["node_id"])
			message := fmt.Sprintf("%v", log["message"])
			stream := fmt.Sprintf("%v", log["stream"])
			timestamp := fmt.Sprintf("%v", log["timestamp"])

			// Parse timestamp
			if ts, err := time.Parse(time.RFC3339, timestamp); err == nil {
				if ts.After(lastTimestamp) {
					lastTimestamp = ts
				}
			}

			// Assign color to node if not already assigned
			if _, exists := nodeColors[nodeID]; !exists {
				nodeColors[nodeID] = colors[colorIndex%len(colors)]
				colorIndex++
			}

			// Format output like docker-compose
			nodeLabel := nodeColors[nodeID](fmt.Sprintf("[%s]", nodeID))

			// Color stderr messages in red
			if stream == "stderr" {
				message = pterm.FgRed.Sprint(message)
			}

			fmt.Printf("%s %s\n", nodeLabel, message)
		}

		if !follow {
			break
		}

		time.Sleep(3 * time.Second)
	}

	return nil
}

func downCommand(c *cli.Context) error {
	id := c.String("id")
	fmt.Printf("🔻 Terminating deployment: %s\n", id)

	client := &http.Client{}
	req, err := http.NewRequest("DELETE", getDaemonURL(c)+"/api/v1/deployments/"+id, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to terminate deployment: %w", err)
	}
	defer resp.Body.Close()

	fmt.Printf("✅ Termination initiated for deployment: %s\n", id)
	return nil
}

func loadConfig(filename string) (*TaskFlyConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config TaskFlyConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

func createBundle(config *TaskFlyConfig) (string, error) {
	bundleName := config.BundleName
	if bundleName == "" {
		bundleName = "taskfly_bundle.tar.gz"
	}

	// Create tar.gz file
	file, err := os.Create(bundleName)
	if err != nil {
		return "", err
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	// Add taskfly.yml first
	if err := addFileToTar(tarWriter, "taskfly.yml"); err != nil {
		return "", fmt.Errorf("failed to add taskfly.yml: %w", err)
	}

	// Add application files
	for _, filePath := range config.ApplicationFiles {
		if err := addFileToTar(tarWriter, filePath); err != nil {
			return "", fmt.Errorf("failed to add %s: %w", filePath, err)
		}
	}

	return bundleName, nil
}

func addFileToTar(tarWriter *tar.Writer, filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return err
	}
	header.Name = filename

	if err := tarWriter.WriteHeader(header); err != nil {
		return err
	}

	_, err = io.Copy(tarWriter, file)
	return err
}

func uploadBundle(c *cli.Context, bundlePath string) (map[string]interface{}, error) {
	// Open the bundle file
	file, err := os.Open(bundlePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Create multipart form
	var b bytes.Buffer
	writer := multipart.NewWriter(&b)
	part, err := writer.CreateFormFile("bundle", filepath.Base(bundlePath))
	if err != nil {
		return nil, err
	}

	_, err = io.Copy(part, file)
	if err != nil {
		return nil, err
	}

	err = writer.Close()
	if err != nil {
		return nil, err
	}

	// Create and send request
	req, err := http.NewRequest("POST", getDaemonURL(c)+"/api/v1/deployments", &b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

func shellCommand(c *cli.Context) error {
	pterm.DefaultHeader.WithFullWidth().Println("TaskFly Interactive Shell")
	pterm.Info.Println("Type 'help' for available commands, 'exit' to quit")
	fmt.Println()

	// Setup readline with auto-completion
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          pterm.FgCyan.Sprint("taskfly> "),
		HistoryFile:     filepath.Join(os.TempDir(), ".taskfly_history"),
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		return fmt.Errorf("failed to initialize shell: %w", err)
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		if err != nil { // io.EOF or readline.ErrInterrupt
			break
		}

		line = filepath.Clean("/" + line)[1:] // Trim spaces
		if line == "" {
			continue
		}

		parts := splitShellCommand(line)
		if len(parts) == 0 {
			continue
		}

		cmd := parts[0]

		switch cmd {
		case "help":
			printShellHelp()

		case "list", "ls":
			if err := listCommand(c); err != nil {
				pterm.Error.Println(err)
			}

		case "status":
			if len(parts) < 2 {
				pterm.Error.Println("Usage: status <deployment-id>")
				continue
			}
			// Create a temporary context with the id flag
			set := flag.NewFlagSet("status", flag.ContinueOnError)
			set.String("id", parts[1], "")
			set.Bool("verbose", c.Bool("verbose"), "")
			tempCtx := cli.NewContext(c.App, set, c)
			set.Parse([]string{})

			if err := statusCommand(tempCtx); err != nil {
				pterm.Error.Println(err)
			}

		case "logs":
			if len(parts) < 2 {
				pterm.Error.Println("Usage: logs <deployment-id> [--node <node-id>] [--follow]")
				continue
			}

			// Parse flags
			deploymentID := parts[1]
			nodeFilter := ""
			follow := false

			for i := 2; i < len(parts); i++ {
				if parts[i] == "--node" && i+1 < len(parts) {
					nodeFilter = parts[i+1]
					i++
				} else if parts[i] == "--follow" || parts[i] == "-f" {
					follow = true
				}
			}

			// Create temporary context
			set := flag.NewFlagSet("logs", flag.ContinueOnError)
			set.String("id", deploymentID, "")
			set.String("node", nodeFilter, "")
			set.Bool("follow", follow, "")
			tempCtx := cli.NewContext(c.App, set, c)
			set.Parse([]string{})

			if err := logsCommand(tempCtx); err != nil {
				pterm.Error.Println(err)
			}

		case "down", "terminate":
			if len(parts) < 2 {
				pterm.Error.Println("Usage: down <deployment-id>")
				continue
			}

			set := flag.NewFlagSet("down", flag.ContinueOnError)
			set.String("id", parts[1], "")
			tempCtx := cli.NewContext(c.App, set, c)
			set.Parse([]string{})

			if err := downCommand(tempCtx); err != nil {
				pterm.Error.Println(err)
			}

		case "up", "deploy":
			if err := deployCommand(c); err != nil {
				pterm.Error.Println(err)
			}

		case "validate":
			configFile := "taskfly.yml"
			if len(parts) > 1 {
				configFile = parts[1]
			}

			set := flag.NewFlagSet("validate", flag.ContinueOnError)
			set.String("config", configFile, "")
			tempCtx := cli.NewContext(c.App, set, c)
			set.Parse([]string{})

			if err := validateCommand(tempCtx); err != nil {
				pterm.Error.Println(err)
			}

		case "dashboard", "dash":
			// Dashboard in shell just shows it once
			// For continuous updates, use the standalone dashboard command
			if err := showDashboard(c); err != nil {
				pterm.Error.Println(err)
			}

		case "clear":
			fmt.Print("\033[H\033[2J") // Clear screen

		case "exit", "quit":
			pterm.Info.Println("Goodbye!")
			return nil

		default:
			pterm.Error.Printfln("Unknown command: %s (type 'help' for available commands)", cmd)
		}

		fmt.Println() // Add spacing between commands
	}

	return nil
}

func printShellHelp() {
	pterm.DefaultSection.Println("Available Commands")

	commands := [][]string{
		{"dashboard, dash", "Show the deployment dashboard"},
		{"list, ls", "List all deployments"},
		{"status <id>", "Show detailed status of a deployment"},
		{"logs <id> [--node <node-id>] [--follow]", "View logs from a deployment"},
		{"up, deploy", "Deploy from taskfly.yml in current directory"},
		{"validate [config]", "Validate taskfly.yml configuration"},
		{"down <id>", "Terminate a deployment"},
		{"clear", "Clear the screen"},
		{"help", "Show this help message"},
		{"exit, quit", "Exit the shell"},
	}

	data := pterm.TableData{{"Command", "Description"}}
	for _, cmd := range commands {
		data = append(data, cmd)
	}

	pterm.DefaultTable.WithHasHeader().WithData(data).Render()
}

func splitShellCommand(line string) []string {
	var parts []string
	var current string
	inQuotes := false

	for _, char := range line {
		switch char {
		case ' ':
			if inQuotes {
				current += string(char)
			} else if current != "" {
				parts = append(parts, current)
				current = ""
			}
		case '"':
			inQuotes = !inQuotes
		default:
			current += string(char)
		}
	}

	if current != "" {
		parts = append(parts, current)
	}

	return parts
}

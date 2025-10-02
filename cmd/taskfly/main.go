package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

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

func main() {
	app := &cli.App{
		Name:  "taskfly",
		Usage: "Distributed task orchestration CLI",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "daemon-ip",
				Aliases: []string{"d"},
				Usage:   "IP address of the TaskFly daemon",
				Value:   "localhost",
				EnvVars: []string{"TASKFLY_DAEMON_IP"},
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Enable verbose logging",
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
		},
	}

	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
}

// getDaemonURL constructs the daemon URL from the IP flag
func getDaemonURL(c *cli.Context) string {
	ip := c.String("daemon-ip")
	return fmt.Sprintf("http://%s", ip)
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
	fmt.Println("📋 Fetching deployments...")

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

	fmt.Printf("%-15s %-10s %-5s %-20s\n", "ID", "STATUS", "NODES", "CREATED")
	fmt.Println("-----------------------------------------------------------")
	for _, dep := range deployments {
		fmt.Printf("%-15s %-10s %-5v %-20s\n",
			dep["deployment_id"], dep["status"], dep["total_nodes"], dep["created_at"])
	}

	return nil
}

func statusCommand(c *cli.Context) error {
	if c.Bool("verbose") {
		logrus.SetLevel(logrus.DebugLevel)
	}

	id := c.String("id")
	fmt.Printf("📊 Getting status for deployment: %s\n", id)
	if c.Bool("verbose") {
		fmt.Printf("🔧 Using daemon URL: %s\n", getDaemonURL(c))
	}

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

	fmt.Printf("Deployment: %s (Status: %s)\n", deployment["deployment_id"], deployment["status"])
	fmt.Println("\nNodes:")

	// Safely handle nodes array
	if deployment["nodes"] == nil {
		fmt.Println("  No nodes found for this deployment")
		return nil
	}

	nodes, ok := deployment["nodes"].([]interface{})
	if !ok {
		fmt.Println("  Invalid nodes data format")
		return nil
	}

	for _, node := range nodes {
		n := node.(map[string]interface{})
		ip := "pending"
		if n["ip_address"] != nil {
			ipStr := n["ip_address"].(string)
			if ipStr != "" {
				ip = ipStr
			}
		}
		fmt.Printf("  - %s: %s (%s)\n", n["node_id"], n["status"], ip)
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

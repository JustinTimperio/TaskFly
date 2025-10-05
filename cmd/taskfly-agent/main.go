package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	Version = "0.1.0"
)

type Config struct {
	Token     string
	DaemonURL string
	WorkDir   string
}

type RegistrationResponse struct {
	NodeID       string                 `json:"node_id"`
	AuthToken    string                 `json:"auth_token"`
	AssetsURL    string                 `json:"assets_url"`
	StatusURL    string                 `json:"status_url"`
	HeartbeatURL string                 `json:"heartbeat_url"`
	LogsURL      string                 `json:"logs_url"`
	Config       map[string]interface{} `json:"config"`
}

type StatusUpdate struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type SystemMetrics struct {
	CPUCores    int     `json:"cpu_cores"`
	CPUUsage    float64 `json:"cpu_usage"`    // percentage
	MemoryTotal uint64  `json:"memory_total"` // bytes
	MemoryUsed  uint64  `json:"memory_used"`  // bytes
	LoadAvg1    float64 `json:"load_avg_1"`   // 1 minute load average
	LoadAvg5    float64 `json:"load_avg_5"`   // 5 minute load average
	LoadAvg15   float64 `json:"load_avg_15"`  // 15 minute load average
}

type Heartbeat struct {
	Metrics *SystemMetrics `json:"metrics,omitempty"`
}

type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	NodeID    string    `json:"node_id"`
	Message   string    `json:"message"`
	Stream    string    `json:"stream"` // "stdout" or "stderr"
}

type Agent struct {
	config       Config
	nodeID       string
	authToken    string
	statusURL    string
	heartbeatURL string
	logsURL      string
	nodeConfig   map[string]interface{}
	client       *http.Client
	workDir      string
	setupCmd     *exec.Cmd
	ctx          context.Context
	cancel       context.CancelFunc
	logBuffer    []LogEntry
	logMutex     sync.Mutex
}

func main() {
	var config Config
	flag.StringVar(&config.Token, "token", "", "Provision token")
	flag.StringVar(&config.DaemonURL, "daemon", "", "Daemon URL")
	flag.StringVar(&config.WorkDir, "workdir", "", "Working directory (default: /tmp/taskfly-<token>)")
	flag.Parse()

	if config.Token == "" || config.DaemonURL == "" {
		log.Fatal("Both --token and --daemon flags are required")
	}

	if config.WorkDir == "" {
		config.WorkDir = fmt.Sprintf("/tmp/taskfly-%s", config.Token)
	}

	log.Printf("TaskFly Agent v%s starting...", Version)
	log.Printf("Daemon URL: %s", config.DaemonURL)
	log.Printf("Provision Token: %s", config.Token)
	log.Printf("Working Directory: %s", config.WorkDir)

	agent := NewAgent(config)
	if err := agent.Run(); err != nil {
		log.Fatalf("Agent failed: %v", err)
	}
}

func NewAgent(config Config) *Agent {
	ctx, cancel := context.WithCancel(context.Background())
	return &Agent{
		config: config,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		ctx:    ctx,
		cancel: cancel,
	}
}

func (a *Agent) Run() error {
	// Setup signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	defer a.cleanup()

	// Create working directory
	if err := os.MkdirAll(a.config.WorkDir, 0755); err != nil {
		return fmt.Errorf("failed to create working directory: %w", err)
	}
	a.workDir = a.config.WorkDir

	// Register with daemon
	log.Println("Registering with daemon...")
	if err := a.register(); err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}
	log.Printf("Successfully registered as node: %s", a.nodeID)

	// Start heartbeat goroutine
	go a.heartbeatLoop()

	// Start log pushing goroutine
	go a.logPushLoop()

	// Download bundle
	if err := a.updateStatus("downloading_assets", "Downloading deployment bundle"); err != nil {
		log.Printf("Failed to update status: %v", err)
	}

	bundlePath := filepath.Join(a.workDir, "bundle.tar.gz")
	if err := a.downloadBundle(bundlePath); err != nil {
		a.updateStatus("failed", fmt.Sprintf("Failed to download bundle: %v", err))
		return fmt.Errorf("failed to download bundle: %w", err)
	}

	// Extract bundle
	if err := a.updateStatus("extracting", "Extracting deployment bundle"); err != nil {
		log.Printf("Failed to update status: %v", err)
	}

	if err := a.extractBundle(bundlePath); err != nil {
		a.updateStatus("failed", fmt.Sprintf("Failed to extract bundle: %v", err))
		return fmt.Errorf("failed to extract bundle: %w", err)
	}

	// Execute setup script if it exists
	setupScript := filepath.Join(a.workDir, "setup.sh")
	if _, err := os.Stat(setupScript); err == nil {
		if err := a.updateStatus("running", "Executing deployment script"); err != nil {
			log.Printf("Failed to update status: %v", err)
		}

		if err := a.executeSetup(setupScript); err != nil {
			a.updateStatus("failed", fmt.Sprintf("Setup script failed: %v", err))
			return fmt.Errorf("setup script failed: %w", err)
		}

		// Monitor setup process
		if err := a.monitorSetup(); err != nil {
			a.updateStatus("failed", fmt.Sprintf("Setup monitoring failed: %v", err))
			return fmt.Errorf("setup monitoring failed: %w", err)
		}
	} else {
		log.Println("No setup.sh found in bundle, marking as completed")
		if err := a.updateStatus("completed", "No deployment script found, node ready"); err != nil {
			log.Printf("Failed to update status: %v", err)
		}
	}

	// Wait for termination signal (either OS signal or context cancellation from daemon)
	log.Println("Agent running, waiting for termination signal...")
	select {
	case <-sigCh:
		log.Println("Received OS termination signal, shutting down...")
	case <-a.ctx.Done():
		log.Println("Received shutdown signal from daemon, shutting down...")
	}

	return nil
}

func (a *Agent) register() error {
	payload := map[string]string{
		"provision_token": a.config.Token,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal registration payload: %w", err)
	}

	req, err := http.NewRequestWithContext(a.ctx, "POST",
		fmt.Sprintf("%s/api/v1/nodes/register", a.config.DaemonURL),
		bytes.NewReader(data),
	)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("registration request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("registration failed with status %d: %s", resp.StatusCode, string(body))
	}

	var regResp RegistrationResponse
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		return fmt.Errorf("failed to decode registration response: %w", err)
	}

	a.nodeID = regResp.NodeID
	a.authToken = regResp.AuthToken
	a.statusURL = regResp.StatusURL
	a.heartbeatURL = regResp.HeartbeatURL
	a.nodeConfig = regResp.Config

	// Set logs URL (construct if not provided for backward compatibility)
	if regResp.LogsURL != "" {
		a.logsURL = regResp.LogsURL
	} else {
		a.logsURL = fmt.Sprintf("%s/api/v1/nodes/logs", a.config.DaemonURL)
	}

	log.Printf("Received node configuration with %d keys", len(a.nodeConfig))

	return nil
}

func (a *Agent) updateStatus(status, message string) error {
	update := StatusUpdate{
		Status:  status,
		Message: message,
	}

	data, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("failed to marshal status update: %w", err)
	}

	req, err := http.NewRequestWithContext(a.ctx, "POST", a.statusURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create status request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", a.authToken))

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("status update request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status update failed with status %d: %s", resp.StatusCode, string(body))
	}

	log.Printf("Status updated: %s - %s", status, message)
	return nil
}

func (a *Agent) heartbeatLoop() {
	if a.heartbeatURL == "" {
		log.Println("No heartbeat URL provided, skipping heartbeat loop")
		return
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if err := a.sendHeartbeat(); err != nil {
				log.Printf("Heartbeat failed: %v", err)
			}
		}
	}
}

func (a *Agent) sendHeartbeat() error {
	// Collect system metrics
	metrics := a.collectMetrics()

	hb := Heartbeat{
		Metrics: metrics,
	}

	data, err := json.Marshal(hb)
	if err != nil {
		return fmt.Errorf("failed to marshal heartbeat: %w", err)
	}

	req, err := http.NewRequestWithContext(a.ctx, "POST", a.heartbeatURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create heartbeat request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", a.authToken))

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("heartbeat request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// 401 means our auth token is invalid - deployment was likely terminated
		log.Printf("Heartbeat rejected (401), deployment likely terminated. Shutting down...")
		a.cancel() // Trigger graceful shutdown
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat failed with status %d", resp.StatusCode)
	}

	// Parse heartbeat response to check for shutdown signal
	var hbResp struct {
		Status   string `json:"status"`
		Shutdown bool   `json:"shutdown"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hbResp); err != nil {
		log.Printf("Warning: failed to decode heartbeat response: %v", err)
		return nil
	}

	// If daemon signals shutdown, initiate graceful shutdown
	if hbResp.Shutdown {
		log.Println("Received shutdown signal from daemon, initiating graceful shutdown...")
		a.cancel() // Trigger context cancellation to shutdown agent
	}

	return nil
}

func (a *Agent) collectMetrics() *SystemMetrics {
	metrics := &SystemMetrics{}

	// Get CPU count
	metrics.CPUCores = a.getCPUCount()

	// Get load averages (Unix-like systems)
	metrics.LoadAvg1, metrics.LoadAvg5, metrics.LoadAvg15 = a.getLoadAverages()

	// Get memory usage
	metrics.MemoryTotal, metrics.MemoryUsed = a.getMemoryUsage()

	// Get CPU usage (simple approximation based on load avg)
	if metrics.CPUCores > 0 {
		metrics.CPUUsage = (metrics.LoadAvg1 / float64(metrics.CPUCores)) * 100
		if metrics.CPUUsage > 100 {
			metrics.CPUUsage = 100
		}
	}

	return metrics
}

func (a *Agent) downloadBundle(path string) error {
	// Try using the provided assets URL or construct default
	assetsURL := fmt.Sprintf("%s/api/v1/nodes/assets", a.config.DaemonURL)

	log.Printf("Downloading bundle from: %s", assetsURL)

	req, err := http.NewRequestWithContext(a.ctx, "GET", assetsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", a.authToken))

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create bundle file: %w", err)
	}
	defer out.Close()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write bundle: %w", err)
	}

	log.Printf("Bundle downloaded successfully (%d bytes)", written)
	return nil
}

func (a *Agent) extractBundle(path string) error {
	log.Printf("Extracting bundle from: %s", path)

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open bundle: %w", err)
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		target := filepath.Join(a.workDir, header.Name)

		// Ensure the target is within workDir (prevent path traversal)
		if !filepath.HasPrefix(target, filepath.Clean(a.workDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", target, err)
			}
		case tar.TypeReg:
			// Ensure parent directory exists
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for %s: %w", target, err)
			}

			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", target, err)
			}

			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to write file %s: %w", target, err)
			}
			outFile.Close()
		default:
			log.Printf("Skipping unsupported file type %c for %s", header.Typeflag, header.Name)
		}
	}

	log.Println("Bundle extracted successfully")
	return nil
}

func (a *Agent) executeSetup(scriptPath string) error {
	log.Printf("Executing setup script: %s", scriptPath)

	// Make script executable
	if err := os.Chmod(scriptPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod setup script: %w", err)
	}

	// Execute setup script
	cmd := exec.CommandContext(a.ctx, scriptPath)
	cmd.Dir = a.workDir

	// Start with the current environment
	env := os.Environ()

	// Add node configuration as environment variables
	// Convert keys to uppercase for consistency
	for key, value := range a.nodeConfig {
		// Convert value to string
		var strValue string
		switch v := value.(type) {
		case string:
			strValue = v
		case int, int64, float64, bool:
			strValue = fmt.Sprintf("%v", v)
		default:
			// For complex types, try JSON encoding
			if jsonBytes, err := json.Marshal(v); err == nil {
				strValue = string(jsonBytes)
			} else {
				strValue = fmt.Sprintf("%v", v)
			}
		}

		// Convert key to uppercase for environment variable
		upperKey := strings.ToUpper(key)

		env = append(env, fmt.Sprintf("%s=%s", upperKey, strValue))
		log.Printf("Setting env var: %s=%s", upperKey, strValue)
	}

	cmd.Env = env

	// Capture stdout and stderr
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start setup script: %w", err)
	}

	a.setupCmd = cmd
	log.Printf("Setup script started with PID: %d", cmd.Process.Pid)

	// Stream stdout
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("[STDOUT] %s", line) // Also log locally
			a.addLog(line, "stdout")
		}
	}()

	// Stream stderr
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("[STDERR] %s", line) // Also log locally
			a.addLog(line, "stderr")
		}
	}()

	return nil
}

func (a *Agent) monitorSetup() error {
	if a.setupCmd == nil {
		return fmt.Errorf("no setup command to monitor")
	}

	// Wait for setup to complete
	err := a.setupCmd.Wait()

	// Give goroutines a moment to finish reading remaining output
	time.Sleep(500 * time.Millisecond)

	// Push any remaining logs immediately
	a.pushLogs()

	if err != nil {
		// Check if context was cancelled
		if a.ctx.Err() != nil {
			log.Println("Setup script terminated due to agent shutdown")
			return nil
		}

		log.Printf("Setup script failed with error: %v", err)
		a.updateStatus("failed", fmt.Sprintf("Setup script failed: %v", err))
		return fmt.Errorf("setup script exited with error: %w", err)
	}

	log.Println("Setup script completed successfully")
	if err := a.updateStatus("completed", "Deployment completed successfully"); err != nil {
		log.Printf("Warning: Failed to update completion status: %v", err)
		// Don't return error here as the script itself succeeded
	}

	return nil
}

func (a *Agent) logPushLoop() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			// Push any remaining logs before exiting
			a.pushLogs()
			return
		case <-ticker.C:
			a.pushLogs()
		}
	}
}

func (a *Agent) pushLogs() {
	a.logMutex.Lock()
	if len(a.logBuffer) == 0 {
		a.logMutex.Unlock()
		return
	}

	// Copy buffer and clear it
	logsToPush := make([]LogEntry, len(a.logBuffer))
	copy(logsToPush, a.logBuffer)
	a.logBuffer = a.logBuffer[:0]
	a.logMutex.Unlock()

	log.Printf("Pushing %d log entries to daemon at %s", len(logsToPush), a.logsURL)

	// Send logs to daemon
	payload := map[string]interface{}{
		"logs": logsToPush,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal logs: %v", err)
		return
	}

	req, err := http.NewRequestWithContext(a.ctx, "POST", a.logsURL, bytes.NewReader(data))
	if err != nil {
		log.Printf("Failed to create log push request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", a.authToken))

	resp, err := a.client.Do(req)
	if err != nil {
		log.Printf("Failed to push logs: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Log push failed with status %d: %s", resp.StatusCode, string(body))
	} else {
		log.Printf("Successfully pushed %d logs", len(logsToPush))
	}
}

func (a *Agent) addLog(message, stream string) {
	a.logMutex.Lock()
	defer a.logMutex.Unlock()

	a.logBuffer = append(a.logBuffer, LogEntry{
		Timestamp: time.Now(),
		NodeID:    a.nodeID,
		Message:   message,
		Stream:    stream,
	})
}

func (a *Agent) cleanup() {
	log.Println("Cleaning up agent resources...")

	// Push any remaining logs
	a.pushLogs()

	a.cancel()

	// Kill setup process if still running
	if a.setupCmd != nil && a.setupCmd.Process != nil {
		log.Printf("Terminating setup process (PID: %d)...", a.setupCmd.Process.Pid)
		a.setupCmd.Process.Signal(syscall.SIGTERM)

		// Give it 5 seconds to terminate gracefully
		time.Sleep(5 * time.Second)

		// Force kill if still running
		if a.setupCmd.ProcessState == nil || !a.setupCmd.ProcessState.Exited() {
			log.Println("Force killing setup process...")
			a.setupCmd.Process.Kill()
		}
	}

	// Optionally clean up working directory
	// Commented out for debugging, but you can enable this
	// log.Printf("Removing working directory: %s", a.workDir)
	// os.RemoveAll(a.workDir)

	log.Println("Cleanup complete")
}

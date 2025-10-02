package main

import (
	"archive/tar"
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
	NodeID       string `json:"node_id"`
	AuthToken    string `json:"auth_token"`
	AssetsURL    string `json:"assets_url"`
	StatusURL    string `json:"status_url"`
	HeartbeatURL string `json:"heartbeat_url"`
}

type StatusUpdate struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type Agent struct {
	config       Config
	nodeID       string
	authToken    string
	statusURL    string
	heartbeatURL string
	client       *http.Client
	workDir      string
	setupCmd     *exec.Cmd
	ctx          context.Context
	cancel       context.CancelFunc
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

	ticker := time.NewTicker(30 * time.Second)
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
	req, err := http.NewRequestWithContext(a.ctx, "POST", a.heartbeatURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create heartbeat request: %w", err)
	}

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

	// Create log file for setup output
	logPath := filepath.Join(a.workDir, "setup.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}
	defer logFile.Close()

	// Execute setup script
	cmd := exec.CommandContext(a.ctx, scriptPath)
	cmd.Dir = a.workDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start setup script: %w", err)
	}

	a.setupCmd = cmd
	log.Printf("Setup script started with PID: %d", cmd.Process.Pid)

	return nil
}

func (a *Agent) monitorSetup() error {
	if a.setupCmd == nil {
		return fmt.Errorf("no setup command to monitor")
	}

	// Wait for setup to complete
	err := a.setupCmd.Wait()

	if err != nil {
		// Check if context was cancelled
		if a.ctx.Err() != nil {
			log.Println("Setup script terminated due to agent shutdown")
			return nil
		}

		// Read log for error details
		logPath := filepath.Join(a.workDir, "setup.log")
		logData, _ := os.ReadFile(logPath)

		log.Printf("Setup script failed. Log contents:\n%s", string(logData))
		return fmt.Errorf("setup script exited with error: %w", err)
	}

	log.Println("Setup script completed successfully")
	a.updateStatus("completed", "Deployment completed successfully")

	return nil
}

func (a *Agent) cleanup() {
	log.Println("Cleaning up agent resources...")

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

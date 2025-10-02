package cloud

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHDeploymentConfig contains configuration for SSH-based agent deployment
type SSHDeploymentConfig struct {
	Host           string
	Port           int
	User           string
	KeyPath        string
	ProvisionToken string
	DaemonURL      string
	AgentBinary    []byte
}

// getSSHClient creates an SSH client with common configuration
func getSSHClient(host, user, keyPath string, port int, timeout time.Duration) (*ssh.Client, error) {
	if port == 0 {
		port = 22
	}

	// Expand home directory in key path
	if len(keyPath) >= 2 && keyPath[:2] == "~/" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		keyPath = filepath.Join(homeDir, keyPath[2:])
	}

	// Read SSH key
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH key: %w", err)
	}

	// Parse private key
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH key: %w", err)
	}

	// Create SSH client config
	sshConfig := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Add proper host key verification
		Timeout:         timeout,
	}

	// Connect to host
	addr := fmt.Sprintf("%s:%d", host, port)
	return ssh.Dial("tcp", addr, sshConfig)
}

// DeployAgentViaSSH deploys the agent binary to a remote host via SSH and executes it
func DeployAgentViaSSH(config SSHDeploymentConfig) error {
	// Default port
	if config.Port == 0 {
		config.Port = 22
	}

	// Connect to host
	client, err := getSSHClient(config.Host, config.User, config.KeyPath, config.Port, 30*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer client.Close()

	// Use provision token to create unique paths for this deployment
	agentPath := fmt.Sprintf("/tmp/taskfly-agent-%s", config.ProvisionToken)
	logPath := fmt.Sprintf("/tmp/taskfly-agent-%s.log", config.ProvisionToken)

	// Step 1: Upload agent binary
	if err := uploadAgentBinary(client, config.AgentBinary, agentPath); err != nil {
		return fmt.Errorf("failed to upload agent binary: %w", err)
	}

	// Step 2: Execute agent
	if err := executeAgent(client, agentPath, logPath, config.ProvisionToken, config.DaemonURL); err != nil {
		return fmt.Errorf("failed to execute agent: %w", err)
	}

	return nil
}

// uploadAgentBinary uploads the agent binary to a unique path via SSH
func uploadAgentBinary(client *ssh.Client, agentBinary []byte, agentPath string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Use cat to write the binary
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	// Start the command to receive the binary at the unique path
	cmd := fmt.Sprintf("cat > %s && chmod +x %s", agentPath, agentPath)
	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("failed to start upload command: %w", err)
	}

	// Write the binary
	if _, err := stdinPipe.Write(agentBinary); err != nil {
		return fmt.Errorf("failed to write binary: %w", err)
	}
	stdinPipe.Close()

	// Wait for command to complete
	if err := session.Wait(); err != nil {
		return fmt.Errorf("upload command failed: %w", err)
	}

	return nil
}

// executeAgent starts the agent in the background via SSH with unique paths
func executeAgent(client *ssh.Client, agentPath, logPath, token, daemonURL string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Execute agent in background with nohup using unique paths
	cmd := fmt.Sprintf(
		"nohup %s --token=%s --daemon=%s > %s 2>&1 &",
		agentPath,
		token,
		daemonURL,
		logPath,
	)

	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("failed to start agent: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// WaitForSSH waits for SSH to become available on the host
func WaitForSSH(host, user, keyPath string, port int, timeout time.Duration) error {
	if port == 0 {
		port = 22
	}

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		client, err := getSSHClient(host, user, keyPath, port, 5*time.Second)
		if err == nil {
			// Successfully connected, test with a simple command
			session, err := client.NewSession()
			if err == nil {
				session.Close()
				client.Close()
				return nil
			}
			client.Close()
		}

		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("SSH did not become available within %v", timeout)
}

// TestSSHConnection tests if SSH connection works
func TestSSHConnection(host, user, keyPath string, port int) error {
	if port == 0 {
		port = 22
	}

	client, err := getSSHClient(host, user, keyPath, port, 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer client.Close()

	// Test with a simple command
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	if err := session.Run("echo 'SSH test successful'"); err != nil {
		return fmt.Errorf("failed to run test command: %w", err)
	}

	return nil
}

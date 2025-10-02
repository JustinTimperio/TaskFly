package cloud

import (
	"fmt"
	"time"
)

// DeploymentConfig contains all information needed to deploy an agent to a host
type DeploymentConfig struct {
	Host           string
	SSHUser        string
	SSHKeyPath     string
	SSHPort        int
	ProvisionToken string
	DaemonURL      string
	TargetOS       string
	TargetArch     string
	WaitForSSH     bool
	SSHTimeout     time.Duration
}

// DeployAgentToHost is a unified function that both AWS and Local providers can use
// It handles: SSH connection, agent binary retrieval, and deployment
func DeployAgentToHost(config DeploymentConfig) error {
	// Set defaults
	if config.SSHPort == 0 {
		config.SSHPort = 22
	}
	if config.SSHTimeout == 0 {
		config.SSHTimeout = 5 * time.Minute
	}
	if config.TargetOS == "" {
		config.TargetOS = "linux"
	}
	if config.TargetArch == "" {
		config.TargetArch = "amd64"
	}

	// Wait for SSH if requested (typically for AWS)
	if config.WaitForSSH {
		fmt.Printf("Waiting for SSH to become available on %s...\n", config.Host)
		if err := WaitForSSH(config.Host, config.SSHUser, config.SSHKeyPath, config.SSHPort, config.SSHTimeout); err != nil {
			return fmt.Errorf("SSH did not become available: %w", err)
		}
	} else {
		// Test SSH connection (typically for Local)
		fmt.Printf("Testing SSH connection to %s@%s...\n", config.SSHUser, config.Host)
		if err := TestSSHConnection(config.Host, config.SSHUser, config.SSHKeyPath, config.SSHPort); err != nil {
			return fmt.Errorf("failed to connect to host: %w", err)
		}
	}

	// Get agent binary for the target platform
	fmt.Printf("Loading agent binary for %s/%s...\n", config.TargetOS, config.TargetArch)
	agentBinary, err := GetAgentBinary(config.TargetOS, config.TargetArch)
	if err != nil {
		return fmt.Errorf("failed to get agent binary for %s/%s: %w", config.TargetOS, config.TargetArch, err)
	}

	// Deploy agent via SSH
	fmt.Printf("Deploying agent to %s@%s...\n", config.SSHUser, config.Host)
	deployConfig := SSHDeploymentConfig{
		Host:           config.Host,
		Port:           config.SSHPort,
		User:           config.SSHUser,
		KeyPath:        config.SSHKeyPath,
		ProvisionToken: config.ProvisionToken,
		DaemonURL:      config.DaemonURL,
		AgentBinary:    agentBinary,
	}

	if err := DeployAgentViaSSH(deployConfig); err != nil {
		return fmt.Errorf("failed to deploy agent: %w", err)
	}

	fmt.Printf("✅ Agent deployed successfully to %s\n", config.Host)
	return nil
}

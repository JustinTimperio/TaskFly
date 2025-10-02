package cloud

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LocalProvider implements the Provider interface for local/SSH deployments
type LocalProvider struct {
	config       map[string]interface{}
	configHelper *ProviderConfigHelper
}

// NewLocalProvider creates a new local provider
func NewLocalProvider(config map[string]interface{}) (*LocalProvider, error) {
	return &LocalProvider{
		config:       config,
		configHelper: NewProviderConfigHelper(config),
	}, nil
}

// GetProviderName returns the provider name
func (p *LocalProvider) GetProviderName() string {
	return "local"
}

// ProvisionInstance for local provider means connecting to an existing host via SSH
func (p *LocalProvider) ProvisionInstance(ctx context.Context, config InstanceConfig) (*InstanceInfo, error) {
	var host string

	// Check for multiple hosts first
	if hostsInterface, ok := p.config["hosts"]; ok {
		if hostSlice, ok := hostsInterface.([]interface{}); ok {
			if len(hostSlice) > config.NodeIndex {
				if hostStr, ok := hostSlice[config.NodeIndex].(string); ok {
					host = hostStr
				}
			}
		}
	}

	// Fall back to single host if hosts array not found or index out of range
	if host == "" {
		if singleHost, ok := p.config["host"].(string); ok {
			host = singleHost
		}
	}

	if host == "" {
		return nil, fmt.Errorf("host not specified in local provider config (checked both 'host' and 'hosts[%d]')", config.NodeIndex)
	}

	sshUser, ok := p.config["ssh_user"].(string)
	if !ok || sshUser == "" {
		return nil, fmt.Errorf("ssh_user not specified in local provider config")
	}

	sshKeyPath, ok := p.config["ssh_key_path"].(string)
	if !ok || sshKeyPath == "" {
		return nil, fmt.Errorf("ssh_key_path not specified in local provider config")
	}

	// Expand home directory in SSH key path
	if len(sshKeyPath) >= 2 && sshKeyPath[:2] == "~/" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		sshKeyPath = filepath.Join(homeDir, sshKeyPath[2:])
	}

	// Get target OS and architecture (configurable for local provider)
	targetOS := p.configHelper.GetString("target_os", "linux")
	targetArch := p.configHelper.GetString("target_arch", "amd64")

	// Deploy agent using unified deployment function
	deployConfig := DeploymentConfig{
		Host:           host,
		SSHUser:        sshUser,
		SSHKeyPath:     sshKeyPath,
		SSHPort:        22,
		ProvisionToken: config.ProvisionToken,
		DaemonURL:      config.DaemonURL,
		TargetOS:       targetOS,
		TargetArch:     targetArch,
		WaitForSSH:     false, // Local hosts should already be accessible
		SSHTimeout:     0,
	}

	if err := DeployAgentToHost(deployConfig); err != nil {
		return nil, fmt.Errorf("failed to deploy agent: %w", err)
	}

	// Generate a pseudo instance ID for local deployments
	instanceID := fmt.Sprintf("local-%s-%d", host, time.Now().Unix())

	return &InstanceInfo{
		InstanceID: instanceID,
		IPAddress:  host,
		Status:     "running",
	}, nil
}

// GetInstanceStatus returns the status of a "local instance"
func (p *LocalProvider) GetInstanceStatus(ctx context.Context, instanceID string) (string, error) {
	// For local provider, we assume the host is always running
	// In a more sophisticated implementation, we could check SSH connectivity
	return "running", nil
}

// TerminateInstance for local provider means cleaning up any processes
func (p *LocalProvider) TerminateInstance(ctx context.Context, instanceID string) error {
	// For local provider, we don't actually terminate anything
	// In a more sophisticated implementation, we could kill the agent process
	return nil
}

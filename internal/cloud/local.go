package cloud

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"golang.org/x/crypto/ssh"
)

//go:embed scripts/local_bootstrap.sh
var localBootstrapScript string

// LocalProvider implements the Provider interface for local/SSH deployments
type LocalProvider struct {
	config map[string]interface{}
}

// NewLocalProvider creates a new local provider
func NewLocalProvider(config map[string]interface{}) (*LocalProvider, error) {
	return &LocalProvider{
		config: config,
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
	if sshKeyPath[:2] == "~/" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		sshKeyPath = filepath.Join(homeDir, sshKeyPath[2:])
	}

	// Test SSH connection
	if err := p.testSSHConnection(host, sshUser, sshKeyPath); err != nil {
		return nil, fmt.Errorf("failed to connect to host %s: %w", host, err)
	}

	// Deploy bootstrap script
	if err := p.deployBootstrapScript(host, sshUser, sshKeyPath, config); err != nil {
		return nil, fmt.Errorf("failed to deploy bootstrap script: %w", err)
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

// testSSHConnection tests if we can connect to the host
func (p *LocalProvider) testSSHConnection(host, user, keyPath string) error {
	// Read the private key
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("failed to read SSH key: %w", err)
	}

	// Create the signer
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to parse SSH key: %w", err)
	}

	// Create SSH client config
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // For simplicity - in production, use proper host key checking
		Timeout:         10 * time.Second,
	}

	// Connect to the host
	client, err := ssh.Dial("tcp", host+":22", config)
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

	// Run a simple test command
	if err := session.Run("echo 'SSH connection test successful'"); err != nil {
		return fmt.Errorf("failed to run test command: %w", err)
	}

	return nil
}

// deployBootstrapScript creates and runs the bootstrap script on the remote host
func (p *LocalProvider) deployBootstrapScript(host, user, keyPath string, config InstanceConfig) error {
	fmt.Printf("🔧 Deploying bootstrap script to %s@%s\n", user, host)
	fmt.Printf("🔧 Using SSH key: %s\n", keyPath)
	fmt.Printf("🔧 Daemon URL: %s\n", config.DaemonURL)
	fmt.Printf("🔧 Provision Token: %s\n", config.ProvisionToken)

	// Read the private key
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("failed to read SSH key: %w", err)
	}

	// Create the signer
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to parse SSH key: %w", err)
	}

	// Create SSH client config
	sshConfig := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	// Connect to the host
	client, err := ssh.Dial("tcp", host+":22", sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer client.Close()

	// Create bootstrap script from template
	bootstrapScript := p.createBootstrapScript(config)

	// Execute the bootstrap script
	fmt.Printf("🔧 Executing bootstrap script on %s\n", host)
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Capture output
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Run the bootstrap script
	if err := session.Run(bootstrapScript); err != nil {
		fmt.Printf("❌ Bootstrap script failed on %s:\n", host)
		fmt.Printf("STDOUT:\n%s\n", stdout.String())
		fmt.Printf("STDERR:\n%s\n", stderr.String())
		return fmt.Errorf("failed to run bootstrap script: %w", err)
	}

	fmt.Printf("✅ Bootstrap script completed successfully on %s\n", host)
	fmt.Printf("STDOUT:\n%s\n", stdout.String())
	if stderr.Len() > 0 {
		fmt.Printf("STDERR:\n%s\n", stderr.String())
	}

	return nil
}

// createBootstrapScript creates the bootstrap script from the embedded template
func (p *LocalProvider) createBootstrapScript(config InstanceConfig) string {
	// Create template data
	templateData := struct {
		ProvisionToken string
		DaemonURL      string
		NodeConfig     map[string]interface{}
	}{
		ProvisionToken: config.ProvisionToken,
		DaemonURL:      config.DaemonURL,
		NodeConfig:     config.NodeConfig,
	}

	// Parse and execute template with custom functions
	funcMap := template.FuncMap{
		"ToUpper": strings.ToUpper,
	}

	tmpl, err := template.New("bootstrap").Funcs(funcMap).Parse(localBootstrapScript)
	if err != nil {
		// Fallback to simple string replacement if template parsing fails
		script := strings.ReplaceAll(localBootstrapScript, "{{.ProvisionToken}}", config.ProvisionToken)
		script = strings.ReplaceAll(script, "{{.DaemonURL}}", config.DaemonURL)
		return script
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, templateData); err != nil {
		// Fallback to simple string replacement if template execution fails
		script := strings.ReplaceAll(localBootstrapScript, "{{.ProvisionToken}}", config.ProvisionToken)
		script = strings.ReplaceAll(script, "{{.DaemonURL}}", config.DaemonURL)
		return script
	}

	return buf.String()
}

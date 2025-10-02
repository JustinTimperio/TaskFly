package cloud

import (
	"context"
	"fmt"
)

// InstanceConfig represents the configuration for provisioning an instance
type InstanceConfig struct {
	// Common fields
	InstanceType string
	SSHUser      string
	SSHKeyPath   string
	NodeIndex    int // Index of the node being provisioned

	// AWS-specific fields
	AMI     string
	KeyName string

	// Local-specific fields
	Host string

	// Bootstrap configuration
	ProvisionToken string
	DaemonURL      string
	NodeConfig     map[string]interface{} // Node-specific configuration/environment variables
}

// InstanceInfo represents information about a provisioned instance
type InstanceInfo struct {
	InstanceID string
	IPAddress  string
	Status     string
}

// Provider defines the interface for cloud providers
type Provider interface {
	// ProvisionInstance creates a new instance and returns its info
	ProvisionInstance(ctx context.Context, config InstanceConfig) (*InstanceInfo, error)

	// GetInstanceStatus returns the current status of an instance
	GetInstanceStatus(ctx context.Context, instanceID string) (string, error)

	// TerminateInstance terminates an instance
	TerminateInstance(ctx context.Context, instanceID string) error

	// GetProviderName returns the name of this provider
	GetProviderName() string
}

// ProviderFactory creates cloud providers
type ProviderFactory struct{}

// NewProvider creates a new provider instance based on the provider name
func (f *ProviderFactory) NewProvider(providerName string, config map[string]interface{}) (Provider, error) {
	switch providerName {
	case "aws":
		return NewAWSProvider(config)
	case "local":
		return NewLocalProvider(config)
	default:
		return nil, fmt.Errorf("unsupported cloud provider: %s", providerName)
	}
}

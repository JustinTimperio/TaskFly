package cloud

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAWSProviderWithLocalStack tests the AWS provider with LocalStack
func TestAWSProviderWithLocalStack(t *testing.T) {
	// Skip if not explicitly testing with LocalStack
	if os.Getenv("TEST_WITH_LOCALSTACK") != "true" {
		t.Skip("Skipping LocalStack test. Set TEST_WITH_LOCALSTACK=true to run")
	}

	ctx := context.Background()

	// Create AWS provider configured for LocalStack
	config := map[string]interface{}{
		"region":             "us-east-1",
		"image_id":           "ami-12345678", // LocalStack accepts any AMI
		"instance_type":      "t2.micro",
		"key_name":           "test-key",
		"use_localstack":     true,
		"localstack_endpoint": "http://localhost:4566",
		"security_groups":    []interface{}{"default"},
	}

	provider, err := NewAWSProvider(config)
	require.NoError(t, err)
	assert.NotNil(t, provider)
	assert.Equal(t, "aws", provider.GetProviderName())

	// Test instance provisioning
	instanceConfig := InstanceConfig{
		InstanceType:   "t2.micro",
		AMI:            "ami-12345678",
		KeyName:        "test-key",
		NodeIndex:      0,
		ProvisionToken: "test-token-123",
		DaemonURL:      "http://localhost:8080",
		NodeConfig: map[string]interface{}{
			"role": "worker",
		},
	}

	// Provision an instance
	instanceInfo, err := provider.ProvisionInstance(ctx, instanceConfig)
	require.NoError(t, err)
	assert.NotNil(t, instanceInfo)
	assert.NotEmpty(t, instanceInfo.InstanceID)
	assert.Equal(t, "running", instanceInfo.Status)

	t.Logf("Created instance: %s", instanceInfo.InstanceID)

	// Test getting instance status
	status, err := provider.GetInstanceStatus(ctx, instanceInfo.InstanceID)
	require.NoError(t, err)
	assert.Equal(t, "running", status)

	// Test terminating the instance
	err = provider.TerminateInstance(ctx, instanceInfo.InstanceID)
	require.NoError(t, err)

	t.Logf("Terminated instance: %s", instanceInfo.InstanceID)
}

// TestAWSProviderConfiguration tests AWS provider configuration
func TestAWSProviderConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]interface{}
		valid  bool
	}{
		{
			name: "valid config",
			config: map[string]interface{}{
				"region":        "us-east-1",
				"image_id":      "ami-12345678",
				"instance_type": "t2.micro",
				"key_name":      "my-key",
			},
			valid: true,
		},
		{
			name: "LocalStack config",
			config: map[string]interface{}{
				"use_localstack":      true,
				"localstack_endpoint": "http://localhost:4566",
				"region":              "us-east-1",
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewAWSProvider(tt.config)
			if tt.valid {
				assert.NoError(t, err)
				assert.NotNil(t, provider)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// TestProviderFactory tests the provider factory with AWS
func TestProviderFactoryAWS(t *testing.T) {
	factory := &ProviderFactory{}

	config := map[string]interface{}{
		"region": "us-east-1",
	}

	provider, err := factory.NewProvider("aws", config)
	require.NoError(t, err)
	assert.NotNil(t, provider)
	assert.Equal(t, "aws", provider.GetProviderName())
}
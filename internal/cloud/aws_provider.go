package cloud

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// AWS provider uses SSH to deploy agent binaries directly

// AWSProvider implements the Provider interface for AWS EC2
type AWSProvider struct {
	client       *ec2.Client
	config       map[string]interface{}
	configHelper *ProviderConfigHelper
}

// NewAWSProvider creates a new AWS provider
func NewAWSProvider(providerConfig map[string]interface{}) (*AWSProvider, error) {
	// Check if we should use LocalStack for testing
	useLocalStack := false
	localStackEndpoint := ""

	if val, ok := providerConfig["use_localstack"].(bool); ok {
		useLocalStack = val
	}

	if val, ok := providerConfig["localstack_endpoint"].(string); ok {
		localStackEndpoint = val
	} else if useLocalStack {
		localStackEndpoint = "http://localhost:4566"
	}

	// Load AWS config
	var cfg aws.Config
	var err error

	if useLocalStack {
		// Configure for LocalStack
		cfg, err = config.LoadDefaultConfig(context.TODO(),
			config.WithRegion("us-east-1"),
			config.WithEndpointResolverWithOptions(
				aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
					return aws.Endpoint{
						URL:           localStackEndpoint,
						SigningRegion: region,
					}, nil
				}),
			),
			config.WithCredentialsProvider(
				aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
					return aws.Credentials{
						AccessKeyID:     "test",
						SecretAccessKey: "test",
					}, nil
				}),
			),
		)
	} else {
		// Normal AWS config
		cfg, err = config.LoadDefaultConfig(context.TODO())
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Override region if specified
	if region, ok := providerConfig["region"].(string); ok && region != "" {
		cfg.Region = region
	}

	return &AWSProvider{
		client:       ec2.NewFromConfig(cfg),
		config:       providerConfig,
		configHelper: NewProviderConfigHelper(providerConfig),
	}, nil
}

// GetProviderName returns the provider name
func (p *AWSProvider) GetProviderName() string {
	return "aws"
}

// ProvisionInstance creates a new EC2 instance
func (p *AWSProvider) ProvisionInstance(ctx context.Context, config InstanceConfig) (*InstanceInfo, error) {
	// Get configuration values with defaults
	imageID := p.configHelper.GetString("image_id", "no-default")
	instanceType := p.configHelper.GetString("instance_type", "no-default")
	keyName := p.configHelper.GetString("key_name", "")
	securityGroups := p.configHelper.GetStringSlice("security_groups", []string{"default"})
	subnetID := p.configHelper.GetString("subnet_id", "")

	if keyName == "" {
		return nil, fmt.Errorf("key_name is required for AWS provider")
	}

	// Get SSH configuration for agent deployment
	sshUser := p.configHelper.GetString("ssh_user", "ec2-user") // Default for Amazon Linux
	sshKeyPath := p.configHelper.GetString("ssh_key_path", "")
	if sshKeyPath == "" {
		return nil, fmt.Errorf("ssh_key_path is required for AWS provider")
	}

	// Prepare run instances input
	runInput := &ec2.RunInstancesInput{
		ImageId:      aws.String(imageID),
		InstanceType: types.InstanceType(instanceType),
		KeyName:      aws.String(keyName),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SecurityGroups: func() []string {
			if subnetID != "" {
				return nil // Use SecurityGroupIds for VPC
			}
			return securityGroups
		}(),
		SecurityGroupIds: func() []string {
			if subnetID != "" {
				return securityGroups
			}
			return nil
		}(),
		SubnetId: func() *string {
			if subnetID != "" {
				return aws.String(subnetID)
			}
			return nil
		}(),
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(fmt.Sprintf("taskfly-node-%d", time.Now().Unix())),
					},
					{
						Key:   aws.String("CreatedBy"),
						Value: aws.String("TaskFly"),
					},
					{
						Key:   aws.String("ProvisionToken"),
						Value: aws.String(config.ProvisionToken),
					},
				},
			},
		},
	}

	// Launch the instance
	result, err := p.client.RunInstances(ctx, runInput)
	if err != nil {
		return nil, fmt.Errorf("failed to launch instance: %w", err)
	}

	if len(result.Instances) == 0 {
		return nil, fmt.Errorf("no instances were created")
	}

	instance := result.Instances[0]
	instanceID := aws.ToString(instance.InstanceId)

	// Wait for the instance to be running
	if err := p.waitForInstanceRunning(ctx, instanceID); err != nil {
		return nil, fmt.Errorf("instance failed to start: %w", err)
	}

	// Get the updated instance information with public IP
	instanceInfo, err := p.getInstanceInfo(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance info: %w", err)
	}

	// Detect architecture from instance type
	arch := DetectArchFromInstanceType(instanceType)
	fmt.Printf("Detected architecture %s for instance type %s\n", arch, instanceType)

	// Deploy agent using unified deployment function
	deployConfig := DeploymentConfig{
		Host:           instanceInfo.IPAddress,
		SSHUser:        sshUser,
		SSHKeyPath:     sshKeyPath,
		SSHPort:        22,
		ProvisionToken: config.ProvisionToken,
		DaemonURL:      config.DaemonURL,
		TargetOS:       "linux",
		TargetArch:     arch,
		WaitForSSH:     true,
		SSHTimeout:     5 * time.Minute,
	}

	if err := DeployAgentToHost(deployConfig); err != nil {
		return nil, fmt.Errorf("failed to deploy agent: %w", err)
	}

	return instanceInfo, nil
}

// GetInstanceStatus returns the status of an EC2 instance
func (p *AWSProvider) GetInstanceStatus(ctx context.Context, instanceID string) (string, error) {
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := p.client.DescribeInstances(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe instance: %w", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return "terminated", nil
	}

	instance := result.Reservations[0].Instances[0]
	return string(instance.State.Name), nil
}

// TerminateInstance terminates an EC2 instance
func (p *AWSProvider) TerminateInstance(ctx context.Context, instanceID string) error {
	input := &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	}

	_, err := p.client.TerminateInstances(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to terminate instance: %w", err)
	}

	return nil
}

// waitForInstanceRunning waits for an instance to be in running state
func (p *AWSProvider) waitForInstanceRunning(ctx context.Context, instanceID string) error {
	waiter := ec2.NewInstanceRunningWaiter(p.client)

	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	return waiter.Wait(ctx, input, 5*time.Minute)
}

// getInstanceInfo retrieves detailed information about an instance
func (p *AWSProvider) getInstanceInfo(ctx context.Context, instanceID string) (*InstanceInfo, error) {
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := p.client.DescribeInstances(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe instance: %w", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return nil, fmt.Errorf("instance not found")
	}

	instance := result.Reservations[0].Instances[0]

	ipAddress := aws.ToString(instance.PublicIpAddress)
	if ipAddress == "" {
		ipAddress = aws.ToString(instance.PrivateIpAddress)
	}

	return &InstanceInfo{
		InstanceID: instanceID,
		IPAddress:  ipAddress,
		Status:     string(instance.State.Name),
	}, nil
}

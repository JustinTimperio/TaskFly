# TaskFly Testing Guide

This guide explains how to test TaskFly with LocalStack for AWS EC2 emulation.

## Prerequisites

- Docker Desktop or Docker Daemon (modern version with Compose V2)
- Go 1.21+ installed
- LocalStack container running (for AWS testing)

**Note**: This testing setup has been verified to work with:
- Docker Desktop on macOS (Apple Silicon and Intel)
- Docker Desktop on Linux
- Docker Daemon with Docker Compose V2

## Quick Start

### 1. Start LocalStack

```bash
# Start LocalStack container
docker-compose -f docker-compose.test.yml up -d

# Verify LocalStack is running
curl http://localhost:4566/_localstack/health
```

### 2. Run Tests

```bash
# Run all unit tests (no LocalStack required)
go test -v ./...

# Run tests with LocalStack
TEST_WITH_LOCALSTACK=true go test -v ./internal/cloud/

# Run specific test
go test -v -run TestAWSProviderWithLocalStack ./internal/cloud/
```

### 3. Stop LocalStack

```bash
docker-compose -f docker-compose.test.yml down
```

## Test Script

A convenience script is provided for testing:

```bash
# Make script executable
chmod +x test.sh

# Show available commands
./test.sh help

# Start LocalStack
./test.sh localstack-up

# Run unit tests
./test.sh unit

# Run integration tests (requires LocalStack)
./test.sh integration

# Run end-to-end deployment test (requires LocalStack)
./test.sh e2e

# Stop LocalStack
./test.sh localstack-down
```

## Configuration

### Using LocalStack in Code

```go
config := map[string]interface{}{
    "use_localstack": true,
    "localstack_endpoint": "http://localhost:4566",
    "region": "us-east-1",
    "image_id": "ami-12345678",
    "instance_type": "t2.micro",
    "key_name": "test-key",
}

provider, err := cloud.NewAWSProvider(config)
```

### YAML Configuration

See `example_app/config_localstack.yaml` for a complete example.

## Architecture

The improved AWS provider supports:

1. **Infrastructure Provisioning** - Manages EC2 instances lifecycle
2. **Application Deployment** - Deploys applications to instances
3. **Resource Pooling** - Reuses instances for efficiency
4. **LocalStack Testing** - Full EC2 emulation for development

### Provider Interfaces

- `Provider` - Original interface for backward compatibility
- `InfrastructureProvider` - New interface for infrastructure management
- `DeploymentProvider` - New interface for application deployment

## Testing Best Practices

1. **Unit Tests**: Run without external dependencies
   ```bash
   go test -v -short ./...
   ```

2. **Integration Tests**: Require LocalStack
   ```bash
   TEST_WITH_LOCALSTACK=true go test -v ./internal/cloud/
   ```

3. **End-to-End Tests**: Full deployment workflow
   ```bash
   ./test.sh e2e
   ```
   This test:
   - Builds TaskFly binaries (daemon and CLI)
   - Starts the daemon in background
   - Deploys example_app to 3 LocalStack EC2 instances
   - Verifies all nodes register and become ready
   - Validates deployment status and instance count
   - Cleans up all resources

4. **Mocking**: Use mock implementations for unit tests
   - `MockEC2Client` for AWS EC2 operations
   - `MockProvider` for provider operations

## Troubleshooting

### LocalStack Not Running

```bash
# Check if LocalStack is running
docker ps | grep localstack

# Check LocalStack logs
./test.sh localstack-logs

# Restart LocalStack
./test.sh localstack-down
./test.sh localstack-up
```

### Test Failures

1. Ensure LocalStack is running before integration tests
2. Check AWS credentials (LocalStack uses dummy credentials)
3. Verify network connectivity to LocalStack (port 4566)

### Docker Compose Version Warnings

If you see warnings about `version` being obsolete, this is expected with Docker Compose V2 and can be safely ignored. The configuration is compatible with both Docker Compose V1 and V2.

### Volume Mount Issues on macOS

The test configuration disables data persistence to avoid volume mount conflicts with modern Docker Desktop on macOS. If you need persistence for debugging:

1. Ensure no other LocalStack containers are using the same volumes
2. Check Docker Desktop volume settings
3. Use `docker volume prune` to clean up stale volumes

## Environment Variables

- `TEST_WITH_LOCALSTACK=true` - Enable LocalStack tests
- `LOCALSTACK_ENDPOINT=http://localhost:4566` - LocalStack endpoint
- `AWS_REGION=us-east-1` - AWS region for testing

## Resources

- [LocalStack Documentation](https://docs.localstack.cloud)
- [AWS SDK Go v2](https://aws.github.io/aws-sdk-go-v2/)
- [Go Testing](https://golang.org/pkg/testing/)
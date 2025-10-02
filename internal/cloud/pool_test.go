package cloud

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockProvider is a mock implementation of Provider for testing
type MockProvider struct {
	mock.Mock
}

func (m *MockProvider) ProvisionInstance(ctx context.Context, config InstanceConfig) (*InstanceInfo, error) {
	args := m.Called(ctx, config)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*InstanceInfo), args.Error(1)
}

func (m *MockProvider) GetInstanceStatus(ctx context.Context, instanceID string) (string, error) {
	args := m.Called(ctx, instanceID)
	return args.String(0), args.Error(1)
}

func (m *MockProvider) TerminateInstance(ctx context.Context, instanceID string) error {
	args := m.Called(ctx, instanceID)
	return args.Error(0)
}

func (m *MockProvider) GetProviderName() string {
	args := m.Called()
	return args.String(0)
}

// TestResourcePool tests resource pool functionality
func TestResourcePool(t *testing.T) {
	ctx := context.Background()
	mockProvider := new(MockProvider)

	poolConfig := PoolConfig{
		MaxInstances:   5,
		MinInstances:   1,
		IdleTimeout:    10 * time.Minute,
		ProvisionAhead: 0, // Disable provision-ahead to avoid race conditions in tests
	}

	pool := NewResourcePool(mockProvider, poolConfig)

	config := InstanceConfig{
		InstanceType: "t2.micro",
		AMI:          "ami-12345",
		KeyName:      "test-key",
	}

	// Mock provider provision call
	instance1 := &InstanceInfo{
		InstanceID: "i-111",
		IPAddress:  "54.1.1.1",
		Status:     "running",
	}

	mockProvider.On("ProvisionInstance", ctx, config).Return(instance1, nil).Once()

	// Test acquiring instance from empty pool
	acquired, err := pool.Acquire(ctx, config)
	require.NoError(t, err)
	assert.Equal(t, instance1.InstanceID, acquired.InstanceID)
	assert.Equal(t, "t2.micro", acquired.Type)
	assert.True(t, acquired.InUse)

	// Verify instance is marked as in use
	status := pool.GetPoolStatus()
	assert.Equal(t, 1, status.TotalInstances)
	assert.Equal(t, 1, status.InUse)
	assert.Equal(t, 0, status.Available)

	// Release instance
	err = pool.Release(ctx, instance1.InstanceID)
	require.NoError(t, err)

	// Verify instance is available
	status = pool.GetPoolStatus()
	assert.Equal(t, 1, status.TotalInstances)
	assert.Equal(t, 0, status.InUse)
	assert.Equal(t, 1, status.Available)

	// Acquire again - should reuse existing instance
	acquired2, err := pool.Acquire(ctx, config)
	require.NoError(t, err)
	assert.Equal(t, instance1.InstanceID, acquired2.InstanceID)

	// Verify reused instance is marked as in use
	status = pool.GetPoolStatus()
	assert.Equal(t, 1, status.TotalInstances)
	assert.Equal(t, 1, status.InUse)
	assert.Equal(t, 0, status.Available)

	mockProvider.AssertExpectations(t)
}

// TestResourcePoolMaxCapacity tests pool capacity limits
func TestResourcePoolMaxCapacity(t *testing.T) {
	ctx := context.Background()
	mockProvider := new(MockProvider)

	poolConfig := PoolConfig{
		MaxInstances:   2,
		MinInstances:   0,
		IdleTimeout:    0,
		ProvisionAhead: 0,
	}

	pool := NewResourcePool(mockProvider, poolConfig)

	config := InstanceConfig{
		InstanceType: "t2.micro",
		AMI:          "ami-12345",
		KeyName:      "test-key",
	}

	// Mock two instance provisions
	mockProvider.On("ProvisionInstance", ctx, config).Return(&InstanceInfo{
		InstanceID: "i-1",
		IPAddress:  "54.1.1.1",
		Status:     "running",
	}, nil).Once()

	mockProvider.On("ProvisionInstance", ctx, config).Return(&InstanceInfo{
		InstanceID: "i-2",
		IPAddress:  "54.1.1.2",
		Status:     "running",
	}, nil).Once()

	// Acquire two instances (max capacity)
	inst1, err := pool.Acquire(ctx, config)
	require.NoError(t, err)
	assert.Equal(t, "i-1", inst1.InstanceID)

	inst2, err := pool.Acquire(ctx, config)
	require.NoError(t, err)
	assert.Equal(t, "i-2", inst2.InstanceID)

	// Try to acquire third instance - should fail
	_, err = pool.Acquire(ctx, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum capacity")

	mockProvider.AssertExpectations(t)
}

// TestResourcePoolInstanceMatching tests instance type matching
func TestResourcePoolInstanceMatching(t *testing.T) {
	ctx := context.Background()
	mockProvider := new(MockProvider)

	poolConfig := PoolConfig{
		MaxInstances:   5,
		MinInstances:   0,
		IdleTimeout:    0,
		ProvisionAhead: 0,
	}

	pool := NewResourcePool(mockProvider, poolConfig)

	// Provision a t2.micro instance
	microConfig := InstanceConfig{
		InstanceType: "t2.micro",
		AMI:          "ami-12345",
		KeyName:      "test-key",
	}

	mockProvider.On("ProvisionInstance", ctx, microConfig).Return(&InstanceInfo{
		InstanceID: "i-micro",
		IPAddress:  "54.1.1.1",
		Status:     "running",
	}, nil).Once()

	inst1, err := pool.Acquire(ctx, microConfig)
	require.NoError(t, err)
	assert.Equal(t, "i-micro", inst1.InstanceID)

	// Release it
	err = pool.Release(ctx, inst1.InstanceID)
	require.NoError(t, err)

	// Try to acquire a t2.small - should NOT reuse micro instance
	smallConfig := InstanceConfig{
		InstanceType: "t2.small",
		AMI:          "ami-12345",
		KeyName:      "test-key",
	}

	mockProvider.On("ProvisionInstance", ctx, smallConfig).Return(&InstanceInfo{
		InstanceID: "i-small",
		IPAddress:  "54.1.1.2",
		Status:     "running",
	}, nil).Once()

	inst2, err := pool.Acquire(ctx, smallConfig)
	require.NoError(t, err)
	assert.Equal(t, "i-small", inst2.InstanceID)
	assert.NotEqual(t, inst1.InstanceID, inst2.InstanceID)

	mockProvider.AssertExpectations(t)
}

// TestResourcePoolTerminate tests explicit instance termination
func TestResourcePoolTerminate(t *testing.T) {
	ctx := context.Background()
	mockProvider := new(MockProvider)

	poolConfig := PoolConfig{
		MaxInstances:   5,
		MinInstances:   0,
		IdleTimeout:    0,
		ProvisionAhead: 0,
	}

	pool := NewResourcePool(mockProvider, poolConfig)

	config := InstanceConfig{
		InstanceType: "t2.micro",
		AMI:          "ami-12345",
		KeyName:      "test-key",
	}

	// Mock provision
	mockProvider.On("ProvisionInstance", ctx, config).Return(&InstanceInfo{
		InstanceID: "i-1",
		IPAddress:  "54.1.1.1",
		Status:     "running",
	}, nil).Once()

	inst, err := pool.Acquire(ctx, config)
	require.NoError(t, err)

	// Verify instance in pool
	status := pool.GetPoolStatus()
	assert.Equal(t, 1, status.TotalInstances)

	// Mock termination
	mockProvider.On("TerminateInstance", ctx, "i-1").Return(nil).Once()

	// Terminate instance
	err = pool.Terminate(ctx, inst.InstanceID)
	require.NoError(t, err)

	// Verify instance removed from pool
	status = pool.GetPoolStatus()
	assert.Equal(t, 0, status.TotalInstances)

	mockProvider.AssertExpectations(t)
}

// TestResourcePoolClose tests closing the entire pool
func TestResourcePoolClose(t *testing.T) {
	ctx := context.Background()
	mockProvider := new(MockProvider)

	poolConfig := PoolConfig{
		MaxInstances:   5,
		MinInstances:   0,
		IdleTimeout:    0,
		ProvisionAhead: 0,
	}

	pool := NewResourcePool(mockProvider, poolConfig)

	config := InstanceConfig{
		InstanceType: "t2.micro",
		AMI:          "ami-12345",
		KeyName:      "test-key",
	}

	// Mock two provisions
	mockProvider.On("ProvisionInstance", ctx, config).Return(&InstanceInfo{
		InstanceID: "i-1",
		IPAddress:  "54.1.1.1",
		Status:     "running",
	}, nil).Once()

	mockProvider.On("ProvisionInstance", ctx, config).Return(&InstanceInfo{
		InstanceID: "i-2",
		IPAddress:  "54.1.1.2",
		Status:     "running",
	}, nil).Once()

	// Acquire two instances
	_, err := pool.Acquire(ctx, config)
	require.NoError(t, err)
	_, err = pool.Acquire(ctx, config)
	require.NoError(t, err)

	// Mock terminations
	mockProvider.On("TerminateInstance", ctx, mock.AnythingOfType("string")).Return(nil).Twice()

	// Close pool
	err = pool.Close(ctx)
	require.NoError(t, err)

	// Verify pool is empty
	status := pool.GetPoolStatus()
	assert.Equal(t, 0, status.TotalInstances)

	mockProvider.AssertExpectations(t)
}

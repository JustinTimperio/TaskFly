package cloud

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PooledProvider wraps a Provider to work with ResourcePool
// This adapter allows the simple Provider interface to work with pooling
type PooledProvider struct {
	provider Provider
}

// NewPooledProvider creates a pooled provider wrapper
func NewPooledProvider(provider Provider) *PooledProvider {
	return &PooledProvider{provider: provider}
}

// ProvisionPooled provisions an instance and returns it in poolable format
func (p *PooledProvider) ProvisionPooled(ctx context.Context, config InstanceConfig) (*PooledInstance, error) {
	info, err := p.provider.ProvisionInstance(ctx, config)
	if err != nil {
		return nil, err
	}

	return &PooledInstance{
		InstanceID: info.InstanceID,
		IPAddress:  info.IPAddress,
		Status:     info.Status,
		InUse:      false,
		LastUsed:   time.Now(),
		CreatedAt:  time.Now(),
	}, nil
}

// Terminate terminates an instance
func (p *PooledProvider) Terminate(ctx context.Context, instanceID string) error {
	return p.provider.TerminateInstance(ctx, instanceID)
}

// GetStatus retrieves instance status
func (p *PooledProvider) GetStatus(ctx context.Context, instanceID string) (string, error) {
	return p.provider.GetInstanceStatus(ctx, instanceID)
}

// ResourcePool manages a pool of reusable instances for cost optimization
// Instead of terminating instances immediately, they are kept alive and reused
// for subsequent jobs with matching instance types.
type ResourcePool struct {
	mu             sync.RWMutex
	provider       *PooledProvider
	instances      map[string]*PooledInstance
	maxInstances   int
	minInstances   int
	idleTimeout    time.Duration
	provisionAhead int
}

// PooledInstance represents an instance in the pool
type PooledInstance struct {
	InstanceID string
	IPAddress  string
	Status     string
	Type       string // Instance type (e.g., "t2.micro")
	Region     string
	InUse      bool
	LastUsed   time.Time
	CreatedAt  time.Time
	Reserved   bool // Reserved for provision-ahead
}

// NewResourcePool creates a new resource pool
func NewResourcePool(provider Provider, config PoolConfig) *ResourcePool {
	return &ResourcePool{
		provider:       NewPooledProvider(provider),
		instances:      make(map[string]*PooledInstance),
		maxInstances:   config.MaxInstances,
		minInstances:   config.MinInstances,
		idleTimeout:    config.IdleTimeout,
		provisionAhead: config.ProvisionAhead,
	}
}

// PoolConfig configures a resource pool
type PoolConfig struct {
	MaxInstances   int           // Maximum instances in pool
	MinInstances   int           // Minimum instances to keep alive
	IdleTimeout    time.Duration // Time before idle instance is terminated
	ProvisionAhead int           // Number of instances to provision in advance
}

// Acquire gets an available instance from the pool or provisions a new one
// This reuses existing instances when possible to save on AWS costs.
func (p *ResourcePool) Acquire(ctx context.Context, config InstanceConfig) (*PooledInstance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Look for an available instance with matching type
	for _, pooled := range p.instances {
		if !pooled.InUse && !pooled.Reserved && pooled.Status == "running" {
			// Check if instance matches requirements
			if p.matchesConfig(pooled, config) {
				pooled.InUse = true
				pooled.LastUsed = time.Now()
				return pooled, nil
			}
		}
	}

	// Check if we can provision a new instance
	if len(p.instances) >= p.maxInstances {
		return nil, fmt.Errorf("resource pool at maximum capacity (%d instances)", p.maxInstances)
	}

	// Provision a new instance
	pooled, err := p.provider.ProvisionPooled(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to provision instance: %w", err)
	}

	// Store instance type and region for matching
	pooled.Type = config.InstanceType
	pooled.InUse = true
	pooled.LastUsed = time.Now()

	p.instances[pooled.InstanceID] = pooled

	// Optionally provision ahead
	if p.provisionAhead > 0 {
		go p.provisionAheadInstances(ctx, config)
	}

	return pooled, nil
}

// Release returns an instance to the pool for reuse
func (p *ResourcePool) Release(ctx context.Context, instanceID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	pooled, exists := p.instances[instanceID]
	if !exists {
		return fmt.Errorf("instance %s not found in pool", instanceID)
	}

	pooled.InUse = false
	pooled.LastUsed = time.Now()

	// Schedule cleanup if idle timeout is set
	if p.idleTimeout > 0 {
		go p.scheduleCleanup(ctx, instanceID, p.idleTimeout)
	}

	return nil
}

// matchesConfig checks if an instance matches the required configuration
func (p *ResourcePool) matchesConfig(pooled *PooledInstance, config InstanceConfig) bool {
	// Match instance type
	if pooled.Type != config.InstanceType {
		return false
	}

	// Could add more matching logic here (region, etc)
	return true
}

// provisionAheadInstances provisions instances in advance to reduce wait time
func (p *ResourcePool) provisionAheadInstances(ctx context.Context, config InstanceConfig) {
	for i := 0; i < p.provisionAhead; i++ {
		p.mu.RLock()
		count := len(p.instances)
		p.mu.RUnlock()

		if count >= p.maxInstances {
			break
		}

		pooled, err := p.provider.ProvisionPooled(ctx, config)
		if err != nil {
			// Log error but continue
			continue
		}

		pooled.Type = config.InstanceType
		pooled.Reserved = true // Reserved for future use
		pooled.InUse = false

		p.mu.Lock()
		p.instances[pooled.InstanceID] = pooled
		p.mu.Unlock()
	}
}

// scheduleCleanup removes idle instances after timeout to save costs
func (p *ResourcePool) scheduleCleanup(ctx context.Context, instanceID string, timeout time.Duration) {
	time.Sleep(timeout)

	p.mu.Lock()
	defer p.mu.Unlock()

	pooled, exists := p.instances[instanceID]
	if !exists {
		return
	}

	// Check if still idle and past timeout
	if !pooled.InUse && time.Since(pooled.LastUsed) >= timeout {
		// Only cleanup if above minimum instances
		if len(p.instances) > p.minInstances {
			// Terminate the instance
			if err := p.provider.Terminate(ctx, instanceID); err != nil {
				// Log error but remove from pool anyway
			}
			delete(p.instances, instanceID)
		}
	}
}

// GetPoolStatus returns the current status of the pool
func (p *ResourcePool) GetPoolStatus() PoolStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	status := PoolStatus{
		TotalInstances: len(p.instances),
		MaxInstances:   p.maxInstances,
		MinInstances:   p.minInstances,
	}

	for _, pooled := range p.instances {
		if pooled.InUse {
			status.InUse++
		} else if pooled.Reserved {
			status.Reserved++
		} else {
			status.Available++
		}
	}

	return status
}

// PoolStatus contains information about the pool's current state
type PoolStatus struct {
	TotalInstances int
	Available      int
	InUse          int
	Reserved       int
	MaxInstances   int
	MinInstances   int
}

// Terminate removes an instance from the pool and terminates it
func (p *ResourcePool) Terminate(ctx context.Context, instanceID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	_, exists := p.instances[instanceID]
	if !exists {
		return fmt.Errorf("instance %s not found in pool", instanceID)
	}

	// Terminate the instance
	if err := p.provider.Terminate(ctx, instanceID); err != nil {
		return fmt.Errorf("failed to terminate instance: %w", err)
	}

	delete(p.instances, instanceID)
	return nil
}

// Close terminates all instances in the pool
func (p *ResourcePool) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error
	for instanceID := range p.instances {
		if err := p.provider.Terminate(ctx, instanceID); err != nil {
			errs = append(errs, err)
		}
		delete(p.instances, instanceID)
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to terminate %d instances", len(errs))
	}

	return nil
}

package state

import (
	"fmt"
	"sync"
	"time"
)

// DeploymentStatus represents the current state of a deployment
type DeploymentStatus string

const (
	StatusPending      DeploymentStatus = "pending"
	StatusProvisioning DeploymentStatus = "provisioning"
	StatusRunning      DeploymentStatus = "running"
	StatusCompleted    DeploymentStatus = "completed"
	StatusFailed       DeploymentStatus = "failed"
	StatusTerminating  DeploymentStatus = "terminating"
	StatusTerminated   DeploymentStatus = "terminated"
)

// NodeStatus represents the current state of a node
type NodeStatus string

const (
	NodeStatusPending      NodeStatus = "pending"
	NodeStatusProvisioning NodeStatus = "provisioning"
	NodeStatusBooting      NodeStatus = "booting"
	NodeStatusRegistering  NodeStatus = "registering"
	NodeStatusDownloading  NodeStatus = "downloading_assets"
	NodeStatusRunning      NodeStatus = "running"
	NodeStatusCompleted    NodeStatus = "completed"
	NodeStatusFailed       NodeStatus = "failed"
	NodeStatusTerminating  NodeStatus = "terminating"
	NodeStatusTerminated   NodeStatus = "terminated"
)

// LogEntry represents a single log line from a node
type LogEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	NodeID       string    `json:"node_id"`
	DeploymentID string    `json:"deployment_id"`
	Message      string    `json:"message"`
	Stream       string    `json:"stream"` // "stdout" or "stderr"
}

// SystemMetrics represents system resource metrics from a node
type SystemMetrics struct {
	CPUCores    int       `json:"cpu_cores"`
	CPUUsage    float64   `json:"cpu_usage"`
	MemoryTotal uint64    `json:"memory_total"`
	MemoryUsed  uint64    `json:"memory_used"`
	LoadAvg1    float64   `json:"load_avg_1"`
	LoadAvg5    float64   `json:"load_avg_5"`
	LoadAvg15   float64   `json:"load_avg_15"`
	Timestamp   time.Time `json:"timestamp"`
}

// Node represents a single node in a deployment
type Node struct {
	NodeID         string                 `json:"node_id"`
	NodeIndex      int                    `json:"node_index"`
	DeploymentID   string                 `json:"deployment_id"`
	Status         NodeStatus             `json:"status"`
	IPAddress      string                 `json:"ip_address,omitempty"`
	InstanceID     string                 `json:"instance_id,omitempty"`
	Config         map[string]interface{} `json:"config"`
	ProvisionToken string                 `json:"provision_token,omitempty"`
	AuthToken      string                 `json:"auth_token,omitempty"`
	ShouldShutdown bool                   `json:"should_shutdown"`
	LastUpdate     time.Time              `json:"last_update"`
	ErrorMessage   string                 `json:"error_message,omitempty"`
	Metrics        *SystemMetrics         `json:"metrics,omitempty"`
}

// Deployment represents a complete deployment with all its nodes
type Deployment struct {
	ID             string                 `json:"deployment_id"`
	Status         DeploymentStatus       `json:"status"`
	CloudProvider  string                 `json:"cloud_provider"`
	TotalNodes     int                    `json:"total_nodes"`
	NodesCompleted int                    `json:"nodes_completed"`
	NodesFailed    int                    `json:"nodes_failed"`
	BundlePath     string                 `json:"bundle_path,omitempty"`
	Config         map[string]interface{} `json:"config,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	CompletedAt    *time.Time             `json:"completed_at,omitempty"`
	ErrorMessage   string                 `json:"error_message,omitempty"`
}

// StateStore defines the interface for state storage implementations
type StateStore interface {
	CreateDeployment(deployment *Deployment) error
	FindNodeByAuthToken(authToken string) (*Node, *Deployment, error)
	GetDeployment(deploymentID string) (*Deployment, error)
	GetAllDeployments() []*Deployment
	UpdateDeploymentStatus(deploymentID string, status DeploymentStatus, errorMessage ...string) error
	CreateNode(node *Node) error
	GetNode(nodeID string) (*Node, error)
	GetNodesByDeployment(deploymentID string) ([]*Node, error)
	UpdateNodeStatus(deploymentID, nodeID string, status NodeStatus, errorMessage ...string) error
	UpdateNodeAuthToken(deploymentID, nodeID, authToken string) error
	UpdateNodeLastSeen(deploymentID, nodeID string) error
	UpdateNodeMessage(deploymentID, nodeID, message string) error
	UpdateNodeInstanceInfo(deploymentID, nodeID, instanceID, ipAddress string) error
	MarkNodeForShutdown(deploymentID, nodeID string) error
	DeleteDeployment(deploymentID string) error
	GetStats() map[string]interface{}

	// Log management
	AppendLogs(deploymentID string, logs []LogEntry) error
	GetLogs(deploymentID string, nodeID string, since time.Time, limit int) ([]LogEntry, error)
	ClearLogs(deploymentID string) error

	// Metrics management
	UpdateNodeMetrics(deploymentID, nodeID string, metrics *SystemMetrics) error
}

// Store manages all deployment and node state in memory
type Store struct {
	mu                   sync.RWMutex
	deployments          map[string]*Deployment
	nodes                map[string]*Node      // key is node_id
	nodesByDep           map[string][]*Node    // key is deployment_id
	logs                 map[string][]LogEntry // key is deployment_id, circular buffer
	maxLogsPerDeployment int
}

// NewStore creates a new in-memory state store
func NewStore() *Store {
	return &Store{
		deployments:          make(map[string]*Deployment),
		nodes:                make(map[string]*Node),
		nodesByDep:           make(map[string][]*Node),
		logs:                 make(map[string][]LogEntry),
		maxLogsPerDeployment: 10000, // Keep last 10K log entries per deployment
	}
}

// CreateDeployment creates a new deployment record
func (s *Store) CreateDeployment(deployment *Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.deployments[deployment.ID]; exists {
		return fmt.Errorf("deployment %s already exists", deployment.ID)
	}

	deployment.CreatedAt = time.Now()
	deployment.UpdatedAt = time.Now()
	s.deployments[deployment.ID] = deployment
	s.nodesByDep[deployment.ID] = make([]*Node, 0)

	return nil
}

// FindNodeByAuthToken finds a node and its deployment by auth token
func (s *Store) FindNodeByAuthToken(authToken string) (*Node, *Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, dep := range s.deployments {
		nodesInDep, ok := s.nodesByDep[dep.ID]
		if !ok {
			continue
		}
		for _, node := range nodesInDep {
			if node.AuthToken == authToken {
				// Return copies to be safe
				nodeCopy := *node
				depCopy := *dep
				return &nodeCopy, &depCopy, nil
			}
		}
	}

	return nil, nil, fmt.Errorf("node with auth token not found")
}

// GetDeployment retrieves a deployment by ID
func (s *Store) GetDeployment(deploymentID string) (*Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	deployment, exists := s.deployments[deploymentID]
	if !exists {
		return nil, fmt.Errorf("deployment %s not found", deploymentID)
	}

	// Create a copy to avoid race conditions
	depCopy := *deployment
	return &depCopy, nil
}

// GetAllDeployments returns all deployments
func (s *Store) GetAllDeployments() []*Deployment {
	s.mu.RLock()
	defer s.mu.RUnlock()

	deployments := make([]*Deployment, 0, len(s.deployments))
	for _, dep := range s.deployments {
		// Create copies to avoid race conditions
		depCopy := *dep
		deployments = append(deployments, &depCopy)
	}

	return deployments
}

// UpdateDeploymentStatus updates the status of a deployment
func (s *Store) UpdateDeploymentStatus(deploymentID string, status DeploymentStatus, errorMessage ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	deployment, exists := s.deployments[deploymentID]
	if !exists {
		return fmt.Errorf("deployment %s not found", deploymentID)
	}

	deployment.Status = status
	deployment.UpdatedAt = time.Now()

	if len(errorMessage) > 0 {
		deployment.ErrorMessage = errorMessage[0]
	}

	if status == StatusCompleted || status == StatusFailed || status == StatusTerminated {
		now := time.Now()
		deployment.CompletedAt = &now
	}

	return nil
}

// CreateNode creates a new node record
func (s *Store) CreateNode(node *Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.nodes[node.NodeID]; exists {
		return fmt.Errorf("node %s already exists", node.NodeID)
	}

	node.LastUpdate = time.Now()
	s.nodes[node.NodeID] = node
	s.nodesByDep[node.DeploymentID] = append(s.nodesByDep[node.DeploymentID], node)

	return nil
}

// GetNode retrieves a node by ID
func (s *Store) GetNode(nodeID string) (*Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, exists := s.nodes[nodeID]
	if !exists {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}

	// Create a copy to avoid race conditions
	nodeCopy := *node
	return &nodeCopy, nil
}

// GetNodesByDeployment returns all nodes for a deployment
func (s *Store) GetNodesByDeployment(deploymentID string) ([]*Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodes, exists := s.nodesByDep[deploymentID]
	if !exists {
		return nil, fmt.Errorf("deployment %s not found", deploymentID)
	}

	// Create copies to avoid race conditions
	nodesCopy := make([]*Node, len(nodes))
	for i, node := range nodes {
		nodeCopy := *node
		nodesCopy[i] = &nodeCopy
	}

	return nodesCopy, nil
}

// UpdateNodeStatus updates the status of a node
func (s *Store) UpdateNodeStatus(deploymentID, nodeID string, status NodeStatus, errorMessage ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, exists := s.nodes[nodeID]
	if !exists {
		return fmt.Errorf("node %s not found", nodeID)
	}

	if node.DeploymentID != deploymentID {
		return fmt.Errorf("node %s does not belong to deployment %s", nodeID, deploymentID)
	}

	node.Status = status
	node.LastUpdate = time.Now()
	if len(errorMessage) > 0 {
		node.ErrorMessage = errorMessage[0]
	}

	// Update deployment completion counts and status
	s.checkDeploymentCompletion(deploymentID)

	return nil
}

// UpdateNodeAuthToken updates the auth token of a node
func (s *Store) UpdateNodeAuthToken(deploymentID, nodeID, authToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, exists := s.nodes[nodeID]
	if !exists {
		return fmt.Errorf("node %s not found", nodeID)
	}

	if node.DeploymentID != deploymentID {
		return fmt.Errorf("node %s does not belong to deployment %s", nodeID, deploymentID)
	}

	node.AuthToken = authToken
	node.LastUpdate = time.Now()
	return nil
}

// UpdateNodeLastSeen updates the last seen time of a node
func (s *Store) UpdateNodeLastSeen(deploymentID, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, exists := s.nodes[nodeID]
	if !exists {
		return fmt.Errorf("node %s not found", nodeID)
	}

	if node.DeploymentID != deploymentID {
		return fmt.Errorf("node %s does not belong to deployment %s", nodeID, deploymentID)
	}

	node.LastUpdate = time.Now()
	return nil
}

// UpdateNodeMessage updates the message of a node
func (s *Store) UpdateNodeMessage(deploymentID, nodeID, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, exists := s.nodes[nodeID]
	if !exists {
		return fmt.Errorf("node %s not found", nodeID)
	}

	if node.DeploymentID != deploymentID {
		return fmt.Errorf("node %s does not belong to deployment %s", nodeID, deploymentID)
	}

	node.ErrorMessage = message
	node.LastUpdate = time.Now()
	return nil
}

// UpdateNodeInstanceInfo updates the instance ID and IP address of a node
func (s *Store) UpdateNodeInstanceInfo(deploymentID, nodeID, instanceID, ipAddress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, exists := s.nodes[nodeID]
	if !exists {
		return fmt.Errorf("node %s not found", nodeID)
	}

	if node.DeploymentID != deploymentID {
		return fmt.Errorf("node %s does not belong to deployment %s", nodeID, deploymentID)
	}

	node.InstanceID = instanceID
	node.IPAddress = ipAddress
	node.LastUpdate = time.Now()
	return nil
}

// MarkNodeForShutdown marks a node to be shut down
func (s *Store) MarkNodeForShutdown(deploymentID, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, exists := s.nodes[nodeID]
	if !exists {
		return fmt.Errorf("node %s not found", nodeID)
	}

	if node.DeploymentID != deploymentID {
		return fmt.Errorf("node %s does not belong to deployment %s", nodeID, deploymentID)
	}

	node.ShouldShutdown = true
	node.LastUpdate = time.Now()
	return nil
}

// Helper to check if all nodes in a deployment are done
func (s *Store) checkDeploymentCompletion(deploymentID string) {
	deployment, exists := s.deployments[deploymentID]
	if !exists {
		return
	}

	nodes := s.nodesByDep[deploymentID]
	completed := 0
	failed := 0
	running := 0
	other := 0

	for _, node := range nodes {
		switch node.Status {
		case NodeStatusCompleted:
			completed++
		case NodeStatusFailed:
			failed++
		case NodeStatusRunning:
			running++
		default:
			other++
		}
	}

	// Update deployment counters
	deployment.NodesCompleted = completed
	deployment.NodesFailed = failed
	deployment.UpdatedAt = time.Now()

	// Update deployment status based on node states
	if completed+failed == deployment.TotalNodes {
		// All nodes are done (either completed or failed)
		if failed > 0 {
			deployment.Status = StatusFailed
		} else {
			deployment.Status = StatusCompleted
		}
		now := time.Now()
		deployment.CompletedAt = &now
	} else if running > 0 || other > 0 {
		// Some nodes are still working
		if deployment.Status == StatusProvisioning {
			deployment.Status = StatusRunning
		}
	}
}

// DeleteDeployment removes a deployment and all its nodes from the store
func (s *Store) DeleteDeployment(deploymentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if deployment exists
	_, exists := s.deployments[deploymentID]
	if !exists {
		return fmt.Errorf("deployment %s not found", deploymentID)
	}

	// Remove all nodes for this deployment
	if nodes, exists := s.nodesByDep[deploymentID]; exists {
		for _, node := range nodes {
			delete(s.nodes, node.NodeID)
		}
		delete(s.nodesByDep, deploymentID)
	}

	// Remove the deployment
	delete(s.deployments, deploymentID)

	return nil
}

// GetStats returns basic statistics about the store
func (s *Store) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	statusCounts := make(map[DeploymentStatus]int)
	for _, dep := range s.deployments {
		statusCounts[dep.Status]++
	}

	totalLogs := 0
	for _, logs := range s.logs {
		totalLogs += len(logs)
	}

	return map[string]interface{}{
		"total_deployments": len(s.deployments),
		"total_nodes":       len(s.nodes),
		"total_logs":        totalLogs,
		"deployment_status": statusCounts,
	}
}

// AppendLogs adds log entries for a deployment
func (s *Store) AppendLogs(deploymentID string, logs []LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify deployment exists
	if _, exists := s.deployments[deploymentID]; !exists {
		return fmt.Errorf("deployment %s not found", deploymentID)
	}

	// Get existing logs
	existingLogs := s.logs[deploymentID]

	// Append new logs
	existingLogs = append(existingLogs, logs...)

	// Trim to max size (keep most recent)
	if len(existingLogs) > s.maxLogsPerDeployment {
		existingLogs = existingLogs[len(existingLogs)-s.maxLogsPerDeployment:]
	}

	s.logs[deploymentID] = existingLogs
	return nil
}

// GetLogs retrieves logs for a deployment, optionally filtered by node and time
func (s *Store) GetLogs(deploymentID string, nodeID string, since time.Time, limit int) ([]LogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Verify deployment exists
	if _, exists := s.deployments[deploymentID]; !exists {
		return nil, fmt.Errorf("deployment %s not found", deploymentID)
	}

	allLogs := s.logs[deploymentID]
	if allLogs == nil {
		return []LogEntry{}, nil
	}

	// Filter logs
	var filtered []LogEntry
	for _, log := range allLogs {
		// Filter by node if specified
		if nodeID != "" && log.NodeID != nodeID {
			continue
		}
		// Filter by time if specified
		if !since.IsZero() && log.Timestamp.Before(since) {
			continue
		}
		filtered = append(filtered, log)
	}

	// Apply limit
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}

	return filtered, nil
}

// ClearLogs removes all logs for a deployment
func (s *Store) ClearLogs(deploymentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.logs, deploymentID)
	return nil
}

// UpdateNodeMetrics updates the metrics for a node
func (s *Store) UpdateNodeMetrics(deploymentID, nodeID string, metrics *SystemMetrics) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, exists := s.nodes[nodeID]
	if !exists {
		return fmt.Errorf("node %s not found", nodeID)
	}

	if node.DeploymentID != deploymentID {
		return fmt.Errorf("node %s does not belong to deployment %s", nodeID, deploymentID)
	}

	metrics.Timestamp = time.Now()
	node.Metrics = metrics
	node.LastUpdate = time.Now()

	return nil
}

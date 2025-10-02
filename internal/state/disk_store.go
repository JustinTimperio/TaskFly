package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DiskStore implements persistent state storage using JSON files
type DiskStore struct {
	mu          sync.RWMutex
	deployments map[string]*Deployment
	nodes       map[string]*Node
	nodesByDep  map[string][]*Node
	dataDir     string
}

// persisted state structure for JSON serialization
type persistedState struct {
	Deployments map[string]*Deployment `json:"deployments"`
	Nodes       map[string]*Node       `json:"nodes"`
}

// NewDiskStore creates a new disk-backed state store
func NewDiskStore(dataDir string) (*DiskStore, error) {
	// Create data directory if it doesn't exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	store := &DiskStore{
		deployments: make(map[string]*Deployment),
		nodes:       make(map[string]*Node),
		nodesByDep:  make(map[string][]*Node),
		dataDir:     dataDir,
	}

	// Load existing state from disk
	if err := store.load(); err != nil {
		return nil, fmt.Errorf("failed to load state: %w", err)
	}

	return store, nil
}

// load reads state from disk
func (s *DiskStore) load() error {
	stateFile := filepath.Join(s.dataDir, "state.json")

	// Check if state file exists
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		// No state file yet, start fresh
		return nil
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		return fmt.Errorf("failed to read state file: %w", err)
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("failed to unmarshal state: %w", err)
	}

	// Restore deployments
	s.deployments = state.Deployments
	if s.deployments == nil {
		s.deployments = make(map[string]*Deployment)
	}

	// Restore nodes
	s.nodes = state.Nodes
	if s.nodes == nil {
		s.nodes = make(map[string]*Node)
	}

	// Rebuild nodesByDep index
	s.nodesByDep = make(map[string][]*Node)
	for _, node := range s.nodes {
		s.nodesByDep[node.DeploymentID] = append(s.nodesByDep[node.DeploymentID], node)
	}

	return nil
}

// save writes current state to disk
func (s *DiskStore) save() error {
	state := persistedState{
		Deployments: s.deployments,
		Nodes:       s.nodes,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	stateFile := filepath.Join(s.dataDir, "state.json")
	tempFile := stateFile + ".tmp"

	// Write to temp file first
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp state file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempFile, stateFile); err != nil {
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// CreateDeployment creates a new deployment record and persists to disk
func (s *DiskStore) CreateDeployment(deployment *Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.deployments[deployment.ID]; exists {
		return fmt.Errorf("deployment %s already exists", deployment.ID)
	}

	deployment.CreatedAt = time.Now()
	deployment.UpdatedAt = time.Now()
	s.deployments[deployment.ID] = deployment
	s.nodesByDep[deployment.ID] = make([]*Node, 0)

	return s.save()
}

// FindNodeByAuthToken finds a node and its deployment by auth token
func (s *DiskStore) FindNodeByAuthToken(authToken string) (*Node, *Deployment, error) {
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
func (s *DiskStore) GetDeployment(deploymentID string) (*Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	deployment, exists := s.deployments[deploymentID]
	if !exists {
		return nil, fmt.Errorf("deployment %s not found", deploymentID)
	}

	depCopy := *deployment
	return &depCopy, nil
}

// GetAllDeployments returns all deployments
func (s *DiskStore) GetAllDeployments() []*Deployment {
	s.mu.RLock()
	defer s.mu.RUnlock()

	deployments := make([]*Deployment, 0, len(s.deployments))
	for _, dep := range s.deployments {
		depCopy := *dep
		deployments = append(deployments, &depCopy)
	}

	return deployments
}

// UpdateDeploymentStatus updates the status of a deployment and persists to disk
func (s *DiskStore) UpdateDeploymentStatus(deploymentID string, status DeploymentStatus, errorMessage ...string) error {
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

	return s.save()
}

// CreateNode creates a new node record and persists to disk
func (s *DiskStore) CreateNode(node *Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.nodes[node.NodeID]; exists {
		return fmt.Errorf("node %s already exists", node.NodeID)
	}

	node.LastUpdate = time.Now()
	s.nodes[node.NodeID] = node
	s.nodesByDep[node.DeploymentID] = append(s.nodesByDep[node.DeploymentID], node)

	return s.save()
}

// GetNode retrieves a node by ID
func (s *DiskStore) GetNode(nodeID string) (*Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, exists := s.nodes[nodeID]
	if !exists {
		return nil, fmt.Errorf("node %s not found", nodeID)
	}

	nodeCopy := *node
	return &nodeCopy, nil
}

// GetNodesByDeployment returns all nodes for a deployment
func (s *DiskStore) GetNodesByDeployment(deploymentID string) ([]*Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodes, exists := s.nodesByDep[deploymentID]
	if !exists {
		return nil, fmt.Errorf("deployment %s not found", deploymentID)
	}

	nodesCopy := make([]*Node, len(nodes))
	for i, node := range nodes {
		nodeCopy := *node
		nodesCopy[i] = &nodeCopy
	}

	return nodesCopy, nil
}

// UpdateNodeStatus updates the status of a node and persists to disk
func (s *DiskStore) UpdateNodeStatus(deploymentID, nodeID string, status NodeStatus, errorMessage ...string) error {
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

	return s.save()
}

// UpdateNodeAuthToken updates the auth token of a node and persists to disk
func (s *DiskStore) UpdateNodeAuthToken(deploymentID, nodeID, authToken string) error {
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

	return s.save()
}

// UpdateNodeLastSeen updates the last seen time of a node and persists to disk
func (s *DiskStore) UpdateNodeLastSeen(deploymentID, nodeID string) error {
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

	return s.save()
}

// UpdateNodeMessage updates the message of a node and persists to disk
func (s *DiskStore) UpdateNodeMessage(deploymentID, nodeID, message string) error {
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

	return s.save()
}

// UpdateNodeInstanceInfo updates the instance ID and IP address of a node and persists to disk
func (s *DiskStore) UpdateNodeInstanceInfo(deploymentID, nodeID, instanceID, ipAddress string) error {
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

	return s.save()
}

// MarkNodeForShutdown marks a node to be shut down and persists to disk
func (s *DiskStore) MarkNodeForShutdown(deploymentID, nodeID string) error {
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

	return s.save()
}

// checkDeploymentCompletion updates deployment status based on node states (must be called with lock held)
func (s *DiskStore) checkDeploymentCompletion(deploymentID string) {
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

// DeleteDeployment removes a deployment and all its nodes from the store and persists to disk
func (s *DiskStore) DeleteDeployment(deploymentID string) error {
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

	return s.save()
}

// GetStats returns basic statistics about the store
func (s *DiskStore) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	statusCounts := make(map[DeploymentStatus]int)
	for _, dep := range s.deployments {
		statusCounts[dep.Status]++
	}

	return map[string]interface{}{
		"total_deployments": len(s.deployments),
		"total_nodes":       len(s.nodes),
		"deployment_status": statusCounts,
	}
}

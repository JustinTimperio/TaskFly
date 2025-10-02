package orchestrator

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/JustinTimperio/TaskFly/internal/cloud"
	"github.com/JustinTimperio/TaskFly/internal/metadata"
	"github.com/JustinTimperio/TaskFly/internal/state"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

// TaskFlyConfig represents the taskfly.yml configuration
type TaskFlyConfig struct {
	CloudProvider     string                            `yaml:"cloud_provider"`
	InstanceConfig    map[string]map[string]interface{} `yaml:"instance_config"`
	ApplicationFiles  []string                          `yaml:"application_files"`
	RemoteDestDir     string                            `yaml:"remote_dest_dir"`
	RemoteScriptToRun string                            `yaml:"remote_script_to_run"`
	BundleName        string                            `yaml:"bundle_name"`
	Nodes             metadata.NodesConfig              `yaml:"nodes"`
}

// Orchestrator manages the deployment lifecycle
type Orchestrator struct {
	store      *state.Store
	workingDir string
	logger     *logrus.Logger
	daemonURL  string
}

// NewOrchestrator creates a new orchestrator instance
func NewOrchestrator(store *state.Store, workingDir string, daemonURL string) *Orchestrator {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	return &Orchestrator{
		store:      store,
		workingDir: workingDir,
		logger:     logger,
		daemonURL:  daemonURL,
	}
}

// ProcessDeployment processes an uploaded bundle and creates a deployment
func (o *Orchestrator) ProcessDeployment(bundlePath string) (*state.Deployment, error) {
	o.logger.Infof("Processing deployment bundle: %s", bundlePath)

	// Generate deployment ID
	deploymentID, err := generateID("dep")
	if err != nil {
		return nil, fmt.Errorf("failed to generate deployment ID: %w", err)
	}

	// Create deployment working directory
	deploymentDir := filepath.Join(o.workingDir, deploymentID)
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create deployment directory: %w", err)
	}

	// Extract and parse configuration
	config, workerBundlePath, err := o.extractAndParseConfig(bundlePath, deploymentDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse configuration: %w", err)
	}

	// Validate nodes configuration
	if err := metadata.ValidateNodesConfig(config.Nodes); err != nil {
		return nil, fmt.Errorf("invalid nodes configuration: %w", err)
	}

	// Create deployment record
	deployment := &state.Deployment{
		ID:            deploymentID,
		Status:        state.StatusPending,
		CloudProvider: config.CloudProvider,
		TotalNodes:    config.Nodes.Count,
		BundlePath:    workerBundlePath, // Use worker bundle path (without taskfly.yml)
		Config: map[string]interface{}{
			"cloud_provider":       config.CloudProvider,
			"instance_config":      config.InstanceConfig,
			"remote_dest_dir":      config.RemoteDestDir,
			"remote_script_to_run": config.RemoteScriptToRun,
		},
	}

	// Store the deployment
	if err := o.store.CreateDeployment(deployment); err != nil {
		return nil, fmt.Errorf("failed to create deployment record: %w", err)
	}

	// Generate node configurations
	nodeConfigs, err := metadata.GenerateNodeConfigs(config.Nodes, deploymentID)
	if err != nil {
		o.store.UpdateDeploymentStatus(deploymentID, state.StatusFailed, err.Error())
		return nil, fmt.Errorf("failed to generate node configurations: %w", err)
	}

	// Create node records
	for _, nodeConfig := range nodeConfigs {
		provisionToken, err := generateID("pt")
		if err != nil {
			o.store.UpdateDeploymentStatus(deploymentID, state.StatusFailed, err.Error())
			return nil, fmt.Errorf("failed to generate provision token: %w", err)
		}

		node := &state.Node{
			NodeID:         nodeConfig.NodeID,
			NodeIndex:      nodeConfig.NodeIndex,
			DeploymentID:   deploymentID,
			Status:         state.NodeStatusPending,
			Config:         nodeConfig.Config,
			ProvisionToken: provisionToken,
		}

		if err := o.store.CreateNode(node); err != nil {
			o.store.UpdateDeploymentStatus(deploymentID, state.StatusFailed, err.Error())
			return nil, fmt.Errorf("failed to create node record: %w", err)
		}
	}

	o.logger.Infof("Created deployment %s with %d nodes", deploymentID, len(nodeConfigs))

	// Start the deployment process in a goroutine
	go o.executeDeployment(deploymentID, config)

	return deployment, nil
}

// executeDeployment runs the deployment process in the background
func (o *Orchestrator) executeDeployment(deploymentID string, config *TaskFlyConfig) {
	o.logger.Infof("Starting deployment execution for %s", deploymentID)

	// Update deployment status to provisioning
	if err := o.store.UpdateDeploymentStatus(deploymentID, state.StatusProvisioning); err != nil {
		o.logger.Errorf("Failed to update deployment status: %v", err)
		return
	}

	// Get all nodes for this deployment
	nodes, err := o.store.GetNodesByDeployment(deploymentID)
	if err != nil {
		o.logger.Errorf("Failed to get nodes for deployment %s: %v", deploymentID, err)
		o.store.UpdateDeploymentStatus(deploymentID, state.StatusFailed, err.Error())
		return
	}

	// Provision nodes with real cloud providers
	o.provisionNodes(deploymentID, nodes, config)
}

// provisionNodes provisions nodes using real cloud providers
func (o *Orchestrator) provisionNodes(deploymentID string, nodes []*state.Node, config *TaskFlyConfig) {
	o.logger.Infof("Provisioning %d nodes for deployment %s using %s provider", len(nodes), deploymentID, config.CloudProvider)

	// Create the appropriate cloud provider
	provider, err := o.createProvider(config.CloudProvider, config.InstanceConfig[config.CloudProvider])
	if err != nil {
		o.logger.Errorf("Failed to create cloud provider: %v", err)
		o.store.UpdateDeploymentStatus(deploymentID, state.StatusFailed, err.Error())
		return
	}

	// Provision each node concurrently
	for _, node := range nodes {
		go o.provisionSingleNode(node, provider, config)
	}

	// Update deployment status to running
	// The deployment will automatically transition based on node completion
	o.store.UpdateDeploymentStatus(deploymentID, state.StatusRunning)
	o.logger.Infof("Started provisioning for deployment %s", deploymentID)
}

// provisionSingleNode provisions a single node
func (o *Orchestrator) provisionSingleNode(node *state.Node, provider cloud.Provider, config *TaskFlyConfig) {
	o.logger.Infof("Provisioning node %s", node.NodeID)

	// Update node status to provisioning
	o.store.UpdateNodeStatus(node.DeploymentID, node.NodeID, state.NodeStatusProvisioning)

	// Provision the instance
	ctx := context.Background()
	instanceInfo, err := provider.ProvisionInstance(ctx, cloud.InstanceConfig{
		NodeIndex:      node.NodeIndex,
		ProvisionToken: node.ProvisionToken,
		DaemonURL:      o.daemonURL,
		NodeConfig:     node.Config,
	})

	if err != nil {
		o.logger.Errorf("Failed to provision node %s: %v", node.NodeID, err)
		o.store.UpdateNodeStatus(node.DeploymentID, node.NodeID, state.NodeStatusFailed, err.Error())
		return
	}

	// Update node with instance information
	o.store.UpdateNodeInstanceInfo(node.DeploymentID, node.NodeID, instanceInfo.InstanceID, instanceInfo.IPAddress)
	o.store.UpdateNodeStatus(node.DeploymentID, node.NodeID, state.NodeStatusBooting)

	o.logger.Infof("Node %s provisioned: %s (%s)", node.NodeID, instanceInfo.InstanceID, instanceInfo.IPAddress)

	// For local provider, the node is ready immediately
	// For cloud providers, we wait for the node to register itself
	if config.CloudProvider == "local" {
		o.store.UpdateNodeStatus(node.DeploymentID, node.NodeID, state.NodeStatusRegistering)
	}
}

// createProvider creates the appropriate cloud provider
func (o *Orchestrator) createProvider(providerName string, config map[string]interface{}) (cloud.Provider, error) {
	switch providerName {
	case "local":
		return cloud.NewLocalProvider(config)
	case "aws":
		return cloud.NewAWSProvider(config)
	default:
		return nil, fmt.Errorf("unsupported cloud provider: %s", providerName)
	}
}

// extractAndParseConfig extracts the bundle and parses taskfly.yml
func (o *Orchestrator) extractAndParseConfig(bundlePath, extractDir string) (*TaskFlyConfig, string, error) {
	// Open the bundle file
	file, err := os.Open(bundlePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open bundle: %w", err)
	}
	defer file.Close()

	// Create gzip reader
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzipReader.Close()

	// Create tar reader
	tarReader := tar.NewReader(gzipReader)

	var configData []byte

	// Extract files and look for taskfly.yml
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("failed to read tar entry: %w", err)
		}

		// Create the extracted file path
		extractPath := filepath.Join(extractDir, header.Name)

		switch header.Typeflag {
		case tar.TypeReg:
			// If this is taskfly.yml, read its content but don't extract it to worker bundle directory
			if header.Name == "taskfly.yml" {
				// Read the config data directly from tar
				configData, err = io.ReadAll(tarReader)
				if err != nil {
					return nil, "", fmt.Errorf("failed to read taskfly.yml from bundle: %w", err)
				}
			} else {
				// Create directories if needed
				if err := os.MkdirAll(filepath.Dir(extractPath), 0755); err != nil {
					return nil, "", fmt.Errorf("failed to create directory: %w", err)
				}

				// Extract all other files (application files) to the worker bundle directory
				outFile, err := os.Create(extractPath)
				if err != nil {
					return nil, "", fmt.Errorf("failed to create file %s: %w", extractPath, err)
				}

				if _, err := io.Copy(outFile, tarReader); err != nil {
					outFile.Close()
					return nil, "", fmt.Errorf("failed to extract file %s: %w", extractPath, err)
				}
				outFile.Close()
			}
		}
	}

	if configData == nil {
		return nil, "", fmt.Errorf("taskfly.yml not found in bundle")
	}

	// Parse the configuration
	var config TaskFlyConfig
	if err := yaml.Unmarshal(configData, &config); err != nil {
		return nil, "", fmt.Errorf("failed to parse taskfly.yml: %w", err)
	}

	// Create a worker bundle (tar.gz) from the extracted files (excluding taskfly.yml)
	workerBundlePath := filepath.Join(extractDir, "worker_bundle.tar.gz")
	if err := o.createWorkerBundle(extractDir, workerBundlePath); err != nil {
		return nil, "", fmt.Errorf("failed to create worker bundle: %w", err)
	}

	return &config, workerBundlePath, nil
}

// createWorkerBundle creates a tar.gz bundle from the extracted application files
func (o *Orchestrator) createWorkerBundle(extractDir, workerBundlePath string) error {
	// Create the worker bundle file
	bundleFile, err := os.Create(workerBundlePath)
	if err != nil {
		return fmt.Errorf("failed to create worker bundle file: %w", err)
	}
	defer bundleFile.Close()

	// Create gzip writer
	gzipWriter := gzip.NewWriter(bundleFile)
	defer gzipWriter.Close()

	// Create tar writer
	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	// Walk through the extracted directory and add all files except taskfly.yml
	return filepath.Walk(extractDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and the worker bundle file itself
		if info.IsDir() || filepath.Base(path) == "worker_bundle.tar.gz" {
			return nil
		}

		// Skip taskfly.yml (though it shouldn't be extracted anyway)
		if filepath.Base(path) == "taskfly.yml" {
			return nil
		}

		// Get relative path
		relPath, err := filepath.Rel(extractDir, path)
		if err != nil {
			return err
		}

		// Create tar header
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		header.Name = relPath

		// Write header
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		// Open and copy file content
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(tarWriter, file)
		return err
	})
}

// TerminateDeployment initiates termination of a deployment
func (o *Orchestrator) TerminateDeployment(deploymentID string) error {
	o.logger.Infof("Terminating deployment %s", deploymentID)

	// Update deployment status
	if err := o.store.UpdateDeploymentStatus(deploymentID, state.StatusTerminating); err != nil {
		return fmt.Errorf("failed to update deployment status: %w", err)
	}

	// Get all nodes for this deployment
	nodes, err := o.store.GetNodesByDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}

	// Terminate all nodes
	for _, node := range nodes {
		o.logger.Infof("Terminating node %s (instance: %s)", node.NodeID, node.InstanceID)
		// Immediately mark as terminated since we're not doing graceful shutdown yet
		// In Phase 3, this will actually terminate cloud instances
		o.store.UpdateNodeStatus(node.DeploymentID, node.NodeID, state.NodeStatusTerminated)
	}

	// Update deployment status immediately
	o.store.UpdateDeploymentStatus(deploymentID, state.StatusTerminated)
	o.logger.Infof("Deployment %s terminated", deploymentID)

	// Cleanup files in background
	go func() {
		time.Sleep(2 * time.Second)
		o.cleanupDeploymentFiles(deploymentID)
		o.logger.Infof("Deployment %s files cleaned up", deploymentID)
	}()

	return nil
}

// cleanupDeploymentFiles removes deployment files and extraction directories
func (o *Orchestrator) cleanupDeploymentFiles(deploymentID string) {
	deployment, err := o.store.GetDeployment(deploymentID)
	if err != nil {
		o.logger.Errorf("Failed to get deployment for cleanup: %v", err)
		return
	}

	// Clean up bundle file
	if deployment.BundlePath != "" {
		if err := os.Remove(deployment.BundlePath); err != nil {
			o.logger.Warnf("Failed to remove bundle file %s: %v", deployment.BundlePath, err)
		} else {
			o.logger.Infof("Removed bundle file: %s", deployment.BundlePath)
		}
	}

	// Clean up extraction directory
	extractionDir := filepath.Join(o.workingDir, deploymentID)
	if err := os.RemoveAll(extractionDir); err != nil {
		o.logger.Warnf("Failed to remove extraction directory %s: %v", extractionDir, err)
	} else {
		o.logger.Infof("Removed extraction directory: %s", extractionDir)
	}
}

// CleanupCompletedDeployments removes files for completed deployments
func (o *Orchestrator) CleanupCompletedDeployments() {
	deployments := o.store.GetAllDeployments()
	for _, dep := range deployments {
		if dep.Status == state.StatusCompleted || dep.Status == state.StatusFailed {
			// Only cleanup deployments that completed more than 1 hour ago
			if dep.CompletedAt != nil && time.Since(*dep.CompletedAt) > time.Hour {
				o.logger.Infof("Cleaning up old deployment: %s", dep.ID)
				o.cleanupDeploymentFiles(dep.ID)
			}
		}
	}
}

// CleanupDeployment removes deployment files and extracted directories
func (o *Orchestrator) CleanupDeployment(deploymentID string) error {
	o.logger.Infof("Cleaning up deployment: %s", deploymentID)

	// Get deployment info
	deployment, err := o.store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("failed to get deployment: %w", err)
	}

	// Remove bundle file if it exists
	if deployment.BundlePath != "" {
		if err := os.Remove(deployment.BundlePath); err != nil && !os.IsNotExist(err) {
			o.logger.Warnf("Failed to remove bundle file %s: %v", deployment.BundlePath, err)
		} else {
			o.logger.Infof("Removed bundle file: %s", deployment.BundlePath)
		}
	}

	// Remove extraction directory if it exists
	extractDir := filepath.Join(o.workingDir, deploymentID)
	if err := os.RemoveAll(extractDir); err != nil && !os.IsNotExist(err) {
		o.logger.Warnf("Failed to remove extraction directory %s: %v", extractDir, err)
	} else {
		o.logger.Infof("Removed extraction directory: %s", extractDir)
	}

	// Remove deployment and nodes from state store
	if err := o.store.DeleteDeployment(deploymentID); err != nil {
		o.logger.Warnf("Failed to remove deployment from store: %v", err)
	} else {
		o.logger.Infof("Removed deployment and nodes from state store: %s", deploymentID)
	}

	return nil
}

// CleanupAllCompleted cleans up all completed, failed, or terminated deployments
func (o *Orchestrator) CleanupAllCompleted() (int, int, error) {
	o.logger.Info("Cleaning up all completed deployments")

	deployments := o.store.GetAllDeployments()
	cleaned := 0
	failed := 0

	for _, dep := range deployments {
		if dep.Status == state.StatusCompleted ||
			dep.Status == state.StatusFailed ||
			dep.Status == state.StatusTerminated {

			if err := o.CleanupDeployment(dep.ID); err != nil {
				o.logger.Errorf("Failed to cleanup deployment %s: %v", dep.ID, err)
				failed++
			} else {
				cleaned++
			}
		}
	}

	o.logger.Infof("Cleanup completed: %d cleaned, %d failed", cleaned, failed)
	return cleaned, failed, nil
}

// generateID generates a random ID with the given prefix
func generateID(prefix string) (string, error) {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(bytes)), nil
}

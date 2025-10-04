package main

//go:generate go run ../build-agents/main.go

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"time"

	"github.com/JustinTimperio/TaskFly/internal/orchestrator"
	"github.com/JustinTimperio/TaskFly/internal/state"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

// Embed agent binaries (paths must be relative to this package directory)
//go:embed agents/taskfly-agent-darwin-amd64
var agentDarwinAmd64 []byte

//go:embed agents/taskfly-agent-darwin-arm64
var agentDarwinArm64 []byte

//go:embed agents/taskfly-agent-linux-amd64
var agentLinuxAmd64 []byte

//go:embed agents/taskfly-agent-linux-arm64
var agentLinuxArm64 []byte

//go:embed agents/taskfly-agent-windows-amd64.exe
var agentWindowsAmd64 []byte

// Global instances
var (
	store         state.StateStore
	orch          *orchestrator.Orchestrator
	logger        *logrus.Logger
	deploymentDir string
	daemonIP      string
	startTime     time.Time
)

func main() {
	app := &cli.App{
		Name:  "taskflyd",
		Usage: "TaskFly daemon server",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "listen-ip",
				Aliases: []string{"l"},
				Usage:   "IP address to listen on",
				Value:   "0.0.0.0",
				EnvVars: []string{"TASKFLY_LISTEN_IP"},
			},
			&cli.StringFlag{
				Name:    "listen-port",
				Aliases: []string{"p"},
				Usage:   "Port to listen on",
				Value:   "8080",
				EnvVars: []string{"TASKFLY_LISTEN_PORT"},
			},
			&cli.StringFlag{
				Name:    "daemon-ip",
				Aliases: []string{"d"},
				Usage:   "IP address that remote nodes should use to callback to this daemon",
				Value:   "localhost",
				EnvVars: []string{"TASKFLY_DAEMON_IP"},
			},
			&cli.StringFlag{
				Name:    "daemon-port",
				Usage:   "Port that remote nodes should use to callback to this daemon",
				Value:   "8080",
				EnvVars: []string{"TASKFLY_DAEMON_PORT"},
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Enable verbose logging",
				EnvVars: []string{"TASKFLY_VERBOSE"},
			},
			&cli.StringFlag{
				Name:    "deployment-dir",
				Usage:   "Directory to store deployment files",
				Value:   getDefaultDeploymentDir(),
				EnvVars: []string{"TASKFLY_DEPLOYMENT_DIR"},
			},
		},
		Action: runDaemon,
	}

	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}
}
// extractEmbeddedAgents writes the embedded agent binaries to the build/agent directory
func extractEmbeddedAgents() error {
	agentDir := "build/agent"
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return fmt.Errorf("failed to create agent directory: %w", err)
	}

	agents := map[string][]byte{
		"taskfly-agent-darwin-amd64":      agentDarwinAmd64,
		"taskfly-agent-darwin-arm64":      agentDarwinArm64,
		"taskfly-agent-linux-amd64":       agentLinuxAmd64,
		"taskfly-agent-linux-arm64":       agentLinuxArm64,
		"taskfly-agent-windows-amd64.exe": agentWindowsAmd64,
	}

	for name, data := range agents {
		path := filepath.Join(agentDir, name)
		if err := os.WriteFile(path, data, 0755); err != nil {
			return fmt.Errorf("failed to write agent %s: %w", name, err)
		}
		logger.Debugf("Extracted embedded agent: %s", path)
	}

	return nil
}

func runDaemon(c *cli.Context) error {
	// Setup and initialization
	startTime = time.Now()
	daemonIP = fmt.Sprintf("http://%s:%s", c.String("daemon-ip"), c.String("daemon-port"))

	// Initialize logger
	logger = logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})
	logger.SetLevel(logrus.InfoLevel)
	logger.Infof("Starting TaskFlyd daemon...")

	// Extract embedded agent binaries
	logger.Info("Extracting embedded agent binaries...")
	if err := extractEmbeddedAgents(); err != nil {
		logger.Fatalf("Failed to extract agent binaries: %v", err)
	}

	// Create deployment working directory
	var err error
	deploymentDir, err = filepath.Abs(c.String("deployment-dir"))
	if err != nil {
		logger.Fatalf("Invalid deployment directory: %v", err)
	}
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		logger.Fatalf("Failed to create deployment directory: %v", err)
	}
	logger.Infof("Using deployment directory: %s", deploymentDir)

	// Initialize disk-backed state store
	homeDir, err := os.UserHomeDir()
	if err != nil {
		logger.Fatalf("Failed to get user home directory: %v", err)
	}
	stateDir := filepath.Join(homeDir, ".taskfly", "state")
	store, err = state.NewDiskStore(stateDir)
	if err != nil {
		logger.Fatalf("Failed to initialize state store: %v", err)
	}
	logger.Infof("State store initialized at %s", stateDir)

	// Initialize orchestrator
	orch = orchestrator.NewOrchestrator(store, deploymentDir, daemonIP)
	logger.Info("Orchestrator initialized")

	// Start periodic cleanup goroutine
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			logger.Info("Running periodic cleanup...")
			orch.CleanupCompletedDeployments()
		}
	}()

	// Initialize Echo
	e := echo.New()
	e.HideBanner = true

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// API routes
	api := e.Group("/api/v1")

	// Deployment endpoints
	api.POST("/deployments", createDeployment)
	api.GET("/deployments", listDeployments)
	api.GET("/deployments/:id", getDeployment)
	api.DELETE("/deployments/:id", deleteDeployment)
	api.GET("/deployments/:id/logs", getDeploymentLogs)

	// Node endpoints
	api.POST("/nodes/register", registerNode)
	api.GET("/nodes/assets", getNodeAssets)
	api.POST("/nodes/heartbeat", nodeHeartbeat)
	api.POST("/nodes/status", updateNodeStatus)
	api.POST("/nodes/logs", pushNodeLogs)

	// Health and stats endpoints
	api.GET("/health", healthCheck)
	api.GET("/stats", getStats)
	api.GET("/metrics", getMetrics)

	// Cleanup endpoints
	api.POST("/deployments/:id/cleanup", cleanupDeployment)
	api.POST("/cleanup/all", cleanupAllCompleted)

	// Start periodic cleanup routine
	go func() {
		ticker := time.NewTicker(10 * time.Minute) // Cleanup every 10 minutes
		defer ticker.Stop()

		for range ticker.C {
			cleaned, failed, err := orch.CleanupAllCompleted()
			if err != nil {
				logger.Errorf("Periodic cleanup failed: %v", err)
			} else if cleaned > 0 || failed > 0 {
				logger.Infof("Periodic cleanup: %d cleaned, %d failed", cleaned, failed)
			}
		}
	}()

	// Start server
	listenAddr := fmt.Sprintf("%s:%s", c.String("listen-ip"), c.String("listen-port"))
	logger.Infof("Starting server on %s", listenAddr)
	go func() {
		if err := e.Start(listenAddr); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("shutting down the server: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server with a timeout of 10 seconds.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		logger.Fatal(err)
	}

	return nil
}

// Handler functions
func createDeployment(c echo.Context) error {
	logger.Info("Received deployment request")

	// Get the uploaded file
	file, err := c.FormFile("bundle")
	if err != nil {
		logger.Errorf("No bundle file provided: %v", err)
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "No bundle file provided",
		})
	}

	logger.Infof("Received bundle: %s (size: %d bytes)", file.Filename, file.Size)

	// Save the uploaded bundle
	src, err := file.Open()
	if err != nil {
		logger.Errorf("Failed to open uploaded file: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to process uploaded file",
		})
	}
	defer src.Close()

	// Create unique bundle filename with timestamp to avoid collisions
	timestamp := time.Now().Format("20060102_150405")
	uniqueFilename := fmt.Sprintf("%s_%s", timestamp, file.Filename)
	bundlePath := filepath.Join(deploymentDir, uniqueFilename)
	dst, err := os.Create(bundlePath)
	if err != nil {
		logger.Errorf("Failed to create bundle file: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to save bundle",
		})
	}
	defer dst.Close()

	// Copy the file
	if _, err = dst.ReadFrom(src); err != nil {
		logger.Errorf("Failed to save bundle: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to save bundle",
		})
	}

	// Process the deployment
	deployment, err := orch.ProcessDeployment(bundlePath)
	if err != nil {
		logger.Errorf("Failed to process deployment: %v", err)
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
	}

	logger.Infof("Created deployment %s with %d nodes", deployment.ID, deployment.TotalNodes)

	return c.JSON(http.StatusAccepted, map[string]interface{}{
		"deployment_id": deployment.ID,
		"message":       fmt.Sprintf("Deployment accepted. Provisioning %d nodes.", deployment.TotalNodes),
		"status_url":    fmt.Sprintf("/api/v1/deployments/%s", deployment.ID),
		"nodes":         deployment.TotalNodes,
		"status":        deployment.Status,
	})
}

func listDeployments(c echo.Context) error {
	deployments := store.GetAllDeployments()
	return c.JSON(http.StatusOK, deployments)
}

func getDeployment(c echo.Context) error {
	id := c.Param("id")
	logger.Infof("Getting deployment status for: %s", id)

	// Get deployment from state
	deployment, err := store.GetDeployment(id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": "Deployment not found",
		})
	}

	// Get nodes for this deployment
	nodes, err := store.GetNodesByDeployment(id)
	if err != nil {
		logger.Errorf("Failed to get nodes for deployment %s: %v", id, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to get deployment nodes",
		})
	}

	// Convert nodes to response format
	logger.Debugf("Found %d nodes for deployment %s", len(nodes), id)
	nodeResponses := make([]map[string]interface{}, len(nodes))
	for i, node := range nodes {
		logger.Debugf("Node %s: status=%s, last_update=%s", node.NodeID, node.Status, node.LastUpdate)
		nodeResponse := map[string]interface{}{
			"node_id":     node.NodeID,
			"node_index":  node.NodeIndex,
			"status":      node.Status,
			"last_update": node.LastUpdate,
		}
		if node.IPAddress != "" {
			nodeResponse["ip_address"] = node.IPAddress
		}
		if node.InstanceID != "" {
			nodeResponse["instance_id"] = node.InstanceID
		}
		if node.ErrorMessage != "" {
			nodeResponse["error_message"] = node.ErrorMessage
		}
		nodeResponses[i] = nodeResponse
	}

	response := map[string]interface{}{
		"deployment_id":   deployment.ID,
		"status":          deployment.Status,
		"cloud_provider":  deployment.CloudProvider,
		"total_nodes":     deployment.TotalNodes,
		"nodes_completed": deployment.NodesCompleted,
		"nodes_failed":    deployment.NodesFailed,
		"created_at":      deployment.CreatedAt,
		"updated_at":      deployment.UpdatedAt,
		"nodes":           nodeResponses,
	}

	if deployment.CompletedAt != nil {
		response["completed_at"] = deployment.CompletedAt
	}
	if deployment.ErrorMessage != "" {
		response["error_message"] = deployment.ErrorMessage
	}

	return c.JSON(http.StatusOK, response)
}

func deleteDeployment(c echo.Context) error {
	id := c.Param("id")
	logger.Infof("Terminating deployment: %s", id)

	// Check if deployment exists
	_, err := store.GetDeployment(id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": "Deployment not found",
		})
	}

	// Initiate termination
	if err := orch.TerminateDeployment(id); err != nil {
		logger.Errorf("Failed to terminate deployment %s: %v", id, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to initiate termination",
		})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "Deployment termination initiated"})
}

func registerNode(c echo.Context) error {
	logger.Info("Received registration request from a node")

	// Parse the registration request
	var req struct {
		ProvisionToken string `json:"provision_token"`
		IP             string `json:"ip"`
	}
	if err := c.Bind(&req); err != nil {
		logger.Errorf("Failed to parse registration request: %v", err)
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}
	logger.Infof("Registration attempt from IP %s with token %s", req.IP, req.ProvisionToken)

	// Find node by provision token
	// For now, we'll search through all nodes - in production this would be indexed
	var (
		foundNode *state.Node
		foundDep  *state.Deployment
	)

	deps := store.GetAllDeployments()
	for _, dep := range deps {
		nodes, _ := store.GetNodesByDeployment(dep.ID)
		for _, node := range nodes {
			if node.ProvisionToken == req.ProvisionToken {
				foundNode = node
				foundDep = dep
				break
			}
		}
		if foundNode != nil {
			break
		}
	}

	if foundNode == nil {
		logger.Warnf("Invalid provision token received: %s", req.ProvisionToken)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid provision token"})
	}
	logger.Infof("Found node %s for deployment %s", foundNode.NodeID, foundDep.ID)

	// Generate auth token for this node
	authToken := "auth-" + foundNode.NodeID

	// Update node with auth token and status
	err := store.UpdateNodeAuthToken(foundDep.ID, foundNode.NodeID, authToken)
	if err != nil {
		logger.Errorf("Failed to update auth token for node %s: %v", foundNode.NodeID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to update node auth token"})
	}

	// Update node status to registered
	err = store.UpdateNodeStatus(foundDep.ID, foundNode.NodeID, state.NodeStatusRegistering)
	if err != nil {
		logger.Errorf("Failed to update status for node %s: %v", foundNode.NodeID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to update node status"})
	}

	logger.Infof("Successfully registered node %s", foundNode.NodeID)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"auth_token":    authToken,
		"deployment_id": foundDep.ID,
		"node_id":       foundNode.NodeID,
		"message":       "Node registered successfully",
		"assets_url":    fmt.Sprintf("%s/api/v1/nodes/assets", daemonIP),
		"heartbeat_url": fmt.Sprintf("%s/api/v1/nodes/heartbeat", daemonIP),
		"status_url":    fmt.Sprintf("%s/api/v1/nodes/status", daemonIP),
		"logs_url":      fmt.Sprintf("%s/api/v1/nodes/logs", daemonIP),
		"config":        foundNode.Config, // Send node configuration
	})
}

func getNodeAssets(c echo.Context) error {
	authHeader := c.Request().Header.Get("Authorization")
	logger.Infof("Received asset request with auth header: %s", authHeader)

	// Validate auth token
	if authHeader == "" {
		logger.Warn("Asset request received with no auth token")
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Missing auth token"})
	}

	// Extract token from "Bearer <token>" format
	var authToken string
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		authToken = authHeader[7:]
	} else {
		logger.Warnf("Invalid authorization header format: %s", authHeader)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid authorization header format"})
	}

	logger.Infof("Extracted auth token: %s", authToken)

	// Get the node to find its deployment
	node, dep, err := store.FindNodeByAuthToken(authToken)
	if err != nil {
		logger.Warnf("Asset request with invalid auth token: %s", authToken)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid auth token"})
	}
	logger.Infof("Asset request validated for node %s in deployment %s", node.NodeID, dep.ID)

	// Validate the auth token matches the node
	if node.AuthToken != authToken {
		logger.Errorf("CRITICAL: Auth token mismatch for node %s. This should not happen.", node.NodeID)
		return c.JSON(http.StatusForbidden, map[string]string{"error": "Auth token mismatch"})
	}

	// Get the deployment to find the bundle path
	deployment, err := store.GetDeployment(dep.ID)
	if err != nil {
		logger.Errorf("Failed to get deployment %s for node %s: %v", dep.ID, node.NodeID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to get deployment"})
	}

	// Check if bundle file exists
	bundlePath := deployment.BundlePath
	if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
		logger.Errorf("Bundle file not found for deployment %s: %s", deployment.ID, bundlePath)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Bundle file not found"})
	}

	// Update node status to downloading
	store.UpdateNodeStatus(deployment.ID, node.NodeID, state.NodeStatusDownloading)
	logger.Infof("Node %s is downloading assets for deployment %s", node.NodeID, deployment.ID)

	// Serve the bundle file
	return c.File(bundlePath)
}

func nodeHeartbeat(c echo.Context) error {
	authHeader := c.Request().Header.Get("Authorization")
	logger.Debugf("Received heartbeat with auth header: %s", authHeader)

	// Validate auth token
	if authHeader == "" {
		logger.Warn("Heartbeat received with no auth token")
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Missing auth token"})
	}

	// Extract token from "Bearer <token>" format
	var authToken string
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		authToken = authHeader[7:]
	} else {
		logger.Warnf("Invalid authorization header format: %s", authHeader)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid authorization header format"})
	}

	// Find node by auth token
	node, dep, err := store.FindNodeByAuthToken(authToken)
	if err != nil {
		logger.Warnf("Heartbeat with invalid auth token: %s", authToken)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid auth token"})
	}

	// Parse heartbeat request body (may include metrics)
	var req struct {
		Metrics *state.SystemMetrics `json:"metrics"`
	}
	if err := c.Bind(&req); err == nil && req.Metrics != nil {
		// Store metrics
		if err := store.UpdateNodeMetrics(dep.ID, node.NodeID, req.Metrics); err != nil {
			logger.Errorf("Failed to update metrics for node %s: %v", node.NodeID, err)
		} else {
			logger.Debugf("Updated metrics for node %s: CPU=%d cores, Load=%.2f, Mem=%dMB/%dMB",
				node.NodeID, req.Metrics.CPUCores, req.Metrics.LoadAvg1,
				req.Metrics.MemoryUsed/1024/1024, req.Metrics.MemoryTotal/1024/1024)
		}
	}

	// Update last seen time
	err = store.UpdateNodeLastSeen(dep.ID, node.NodeID)
	if err != nil {
		logger.Errorf("Failed to update last seen for node %s: %v", node.NodeID, err)
		// Non-critical, so we don't return an error to the agent
	}

	// Update node status to running_script if it's not already in a terminal state
	// Don't overwrite completed/failed/terminated states
	if node.Status != state.NodeStatusRunning &&
		node.Status != state.NodeStatusCompleted &&
		node.Status != state.NodeStatusFailed &&
		node.Status != state.NodeStatusTerminated {
		err = store.UpdateNodeStatus(dep.ID, node.NodeID, state.NodeStatusRunning)
		if err != nil {
			logger.Errorf("Failed to update status to running for node %s: %v", node.NodeID, err)
		} else {
			logger.Infof("Node %s is now running", node.NodeID)
		}
	}

	// Return shutdown signal if node should shutdown
	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":   "ok",
		"shutdown": node.ShouldShutdown,
	})
}

func updateNodeStatus(c echo.Context) error {
	authHeader := c.Request().Header.Get("Authorization")
	logger.Debugf("Received status update with auth header: %s", authHeader)

	// Validate auth token
	if authHeader == "" {
		logger.Warn("Status update received with no auth token")
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Missing auth token"})
	}

	// Extract token from "Bearer <token>" format
	var authToken string
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		authToken = authHeader[7:]
	} else {
		logger.Warnf("Invalid authorization header format: %s", authHeader)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid authorization header format"})
	}

	// Parse status update request
	var req struct {
		Status  state.NodeStatus `json:"status"`
		Message string           `json:"message"`
	}
	if err := c.Bind(&req); err != nil {
		logger.Errorf("Failed to parse status update request: %v", err)
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}
	logger.Infof("Node status update: %s, message: %s", req.Status, req.Message)

	// Find node by auth token
	node, dep, err := store.FindNodeByAuthToken(authToken)
	if err != nil {
		logger.Warnf("Status update with invalid auth token: %s", authToken)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid auth token"})
	}

	// Update node status
	err = store.UpdateNodeStatus(dep.ID, node.NodeID, req.Status)
	if err != nil {
		logger.Errorf("Failed to update status for node %s: %v", node.NodeID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to update node status"})
	}

	// If there's a message, update that as well
	if req.Message != "" {
		err = store.UpdateNodeMessage(dep.ID, node.NodeID, req.Message)
		if err != nil {
			logger.Errorf("Failed to update message for node %s: %v", node.NodeID, err)
			// Non-critical, so we don't return an error
		}
	}

	logger.Infof("Successfully updated status for node %s to %s", node.NodeID, req.Status)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func getStats(c echo.Context) error {
	stats := store.GetStats()
	stats["uptime"] = time.Since(startTime).String()
	return c.JSON(http.StatusOK, stats)
}

func getMetrics(c echo.Context) error {
	deployments := store.GetAllDeployments()

	var totalCores int
	var totalMemory, totalMemoryUsed uint64
	var avgLoad float64
	nodeCount := 0

	type NodeMetrics struct {
		NodeID     string               `json:"node_id"`
		IPAddress  string               `json:"ip_address"`
		Status     state.NodeStatus     `json:"status"`
		Metrics    *state.SystemMetrics `json:"metrics"`
		LastUpdate string               `json:"last_update"`
	}

	// Use a map to deduplicate nodes by IP address (keep track of time.Time for comparison)
	type nodeEntry struct {
		metrics    NodeMetrics
		lastUpdate time.Time
	}
	nodesByIP := make(map[string]nodeEntry)

	for _, dep := range deployments {
		nodes, _ := store.GetNodesByDeployment(dep.ID)
		for _, node := range nodes {
			// Skip nodes without IP addresses
			if node.IPAddress == "" {
				continue
			}

			// Check if we already have this IP, keep the one with the most recent update
			existing, exists := nodesByIP[node.IPAddress]
			if !exists || node.LastUpdate.After(existing.lastUpdate) {
				nodesByIP[node.IPAddress] = nodeEntry{
					metrics: NodeMetrics{
						NodeID:     node.NodeID,
						IPAddress:  node.IPAddress,
						Status:     node.Status,
						Metrics:    node.Metrics,
						LastUpdate: node.LastUpdate.Format(time.RFC3339),
					},
					lastUpdate: node.LastUpdate,
				}
			}
		}
	}

	// Convert map to slice and calculate totals
	allNodes := []NodeMetrics{}
	for _, entry := range nodesByIP {
		if entry.metrics.Metrics != nil {
			totalCores += entry.metrics.Metrics.CPUCores
			totalMemory += entry.metrics.Metrics.MemoryTotal
			totalMemoryUsed += entry.metrics.Metrics.MemoryUsed
			avgLoad += entry.metrics.Metrics.LoadAvg1
			nodeCount++
		}
		allNodes = append(allNodes, entry.metrics)
	}

	// Sort nodes by IP address for deterministic ordering
	sort.Slice(allNodes, func(i, j int) bool {
		return allNodes[i].IPAddress < allNodes[j].IPAddress
	})

	if nodeCount > 0 {
		avgLoad /= float64(nodeCount)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"summary": map[string]interface{}{
			"total_cores":          totalCores,
			"total_memory_gb":      float64(totalMemory) / 1024 / 1024 / 1024,
			"total_memory_used_gb": float64(totalMemoryUsed) / 1024 / 1024 / 1024,
			"avg_load":             avgLoad,
			"nodes_with_metrics":   nodeCount,
		},
		"nodes": allNodes,
	})
}

func cleanupDeployment(c echo.Context) error {
	id := c.Param("id")
	logger.Infof("Cleaning up deployment: %s", id)

	// Check if deployment exists
	deployment, err := store.GetDeployment(id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": "Deployment not found",
		})
	}

	// Only allow cleanup if deployment is completed, failed, or terminated
	if deployment.Status != state.StatusCompleted &&
		deployment.Status != state.StatusFailed &&
		deployment.Status != state.StatusTerminated {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "Can only cleanup completed, failed, or terminated deployments",
		})
	}

	// Cleanup deployment files
	if err := orch.CleanupDeployment(id); err != nil {
		logger.Errorf("Failed to cleanup deployment %s: %v", id, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to cleanup deployment",
		})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "Deployment cleaned up successfully",
	})
}

func cleanupAllCompleted(c echo.Context) error {
	logger.Info("Cleaning up all completed deployments")

	cleaned, failed, err := orch.CleanupAllCompleted()
	if err != nil {
		logger.Errorf("Failed to cleanup completed deployments: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to cleanup deployments",
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":       "Cleanup completed",
		"cleaned_count": cleaned,
		"failed_count":  failed,
	})
}

func healthCheck(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func pushNodeLogs(c echo.Context) error {
	authHeader := c.Request().Header.Get("Authorization")

	// Validate auth token
	if authHeader == "" {
		logger.Warn("Log push received with no auth token")
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Missing auth token"})
	}

	// Extract token from "Bearer <token>" format
	var authToken string
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		authToken = authHeader[7:]
	} else {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid authorization header format"})
	}

	// Find node by auth token
	node, dep, err := store.FindNodeByAuthToken(authToken)
	if err != nil {
		logger.Warnf("Log push with invalid auth token: %s", authToken)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid auth token"})
	}

	// Parse log entries
	var req struct {
		Logs []state.LogEntry `json:"logs"`
	}
	if err := c.Bind(&req); err != nil {
		logger.Errorf("Failed to parse log push request: %v", err)
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request"})
	}

	// Set deployment ID and node ID for all logs
	for i := range req.Logs {
		req.Logs[i].DeploymentID = dep.ID
		req.Logs[i].NodeID = node.NodeID
	}

	// Store logs
	if err := store.AppendLogs(dep.ID, req.Logs); err != nil {
		logger.Errorf("Failed to store logs for node %s: %v", node.NodeID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to store logs"})
	}

	logger.Debugf("Received %d log entries from node %s", len(req.Logs), node.NodeID)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func getDeploymentLogs(c echo.Context) error {
	id := c.Param("id")
	nodeID := c.QueryParam("node")
	sinceStr := c.QueryParam("since")
	limitStr := c.QueryParam("limit")

	// Parse since parameter
	var since time.Time
	if sinceStr != "" {
		parsed, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid 'since' parameter, must be RFC3339 format"})
		}
		since = parsed
	}

	// Parse limit parameter
	limit := 1000 // default
	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
	}

	// Get logs
	logs, err := store.GetLogs(id, nodeID, since, limit)
	if err != nil {
		logger.Errorf("Failed to get logs for deployment %s: %v", id, err)
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Deployment not found"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"deployment_id": id,
		"logs":          logs,
		"count":         len(logs),
	})
}

// getDefaultDeploymentDir returns ~/.taskfly/deployments
func getDefaultDeploymentDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if we can't get home
		return "deployments"
	}
	return filepath.Join(homeDir, ".taskfly", "deployments")
}

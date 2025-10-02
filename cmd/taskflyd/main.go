package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/JustinTimperio/TaskFly/internal/orchestrator"
	"github.com/JustinTimperio/TaskFly/internal/state"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

// Global instances
var (
	store         *state.Store
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

	// Initialize state store
	store = state.NewStore()
	logger.Info("State store initialized")

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

	// Node endpoints
	api.POST("/nodes/register", registerNode)
	api.GET("/nodes/assets", getNodeAssets)
	api.POST("/nodes/heartbeat", nodeHeartbeat)
	api.POST("/nodes/status", updateNodeStatus)

	// Health and stats endpoints
	api.GET("/health", healthCheck)
	api.GET("/stats", getStats)

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
	return c.JSON(http.StatusOK, map[string]string{
		"auth_token":    authToken,
		"deployment_id": foundDep.ID,
		"node_id":       foundNode.NodeID,
		"message":       "Node registered successfully",
		"assets_url":    fmt.Sprintf("%s/api/v1/nodes/assets", daemonIP),
		"heartbeat_url": fmt.Sprintf("%s/api/v1/nodes/heartbeat", daemonIP),
		"status_url":    fmt.Sprintf("%s/api/v1/nodes/status", daemonIP),
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

	// Update last seen time
	err = store.UpdateNodeLastSeen(dep.ID, node.NodeID)
	if err != nil {
		logger.Errorf("Failed to update last seen for node %s: %v", node.NodeID, err)
		// Non-critical, so we don't return an error to the agent
	}

	// Update node status to running if it's not already
	if node.Status != state.NodeStatusRunning {
		err = store.UpdateNodeStatus(dep.ID, node.NodeID, state.NodeStatusRunning)
		if err != nil {
			logger.Errorf("Failed to update status to running for node %s: %v", node.NodeID, err)
		} else {
			logger.Infof("Node %s is now running", node.NodeID)
		}
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
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

// getDefaultDeploymentDir returns ~/.taskfly/deployments
func getDefaultDeploymentDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if we can't get home
		return "deployments"
	}
	return filepath.Join(homeDir, ".taskfly", "deployments")
}

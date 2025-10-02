package cloud

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Agent binaries are loaded from the build directory at runtime
// We don't embed them to avoid bloating the daemon binary

// GetAgentBinary returns the appropriate agent binary for the requested platform
func GetAgentBinary(goos, goarch string) ([]byte, error) {
	// Find the project root by looking for go.mod
	projectRoot, err := findProjectRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to find project root: %w", err)
	}

	// Construct path to agent binary
	binaryPath := filepath.Join(projectRoot, "build", "agent", fmt.Sprintf("%s-%s", goos, goarch), "taskfly-agent")

	// Check if binary exists
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("agent binary not found at %s. Run 'go generate ./internal/cloud' to build agent binaries", binaryPath)
	}

	// Read the binary
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent binary: %w", err)
	}

	return data, nil
}

// findProjectRoot finds the project root by walking up directories looking for go.mod
func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

// GetAgentBinaryForCurrentPlatform returns the agent binary for the current platform
func GetAgentBinaryForCurrentPlatform() ([]byte, error) {
	return GetAgentBinary(runtime.GOOS, runtime.GOARCH)
}

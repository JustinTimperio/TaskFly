package cloud

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Agent binaries are embedded in the daemon binary and extracted at runtime
// The daemon extracts agents to build/agent/ on startup

// GetAgentBinary returns the appropriate agent binary for the requested platform
func GetAgentBinary(goos, goarch string) ([]byte, error) {
	// Agent binaries are extracted by the daemon to build/agent/ relative to working directory
	// The filename format matches what the build script creates: taskfly-agent-{os}-{arch}
	binaryPath := filepath.Join("build", "agent", fmt.Sprintf("taskfly-agent-%s-%s", goos, goarch))

	// Add .exe extension for Windows
	if goos == "windows" {
		binaryPath += ".exe"
	}

	// Check if binary exists
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("agent binary not found at %s. The daemon should have extracted it on startup", binaryPath)
	}

	// Read the binary
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent binary: %w", err)
	}

	return data, nil
}

// GetAgentBinaryForCurrentPlatform returns the agent binary for the current platform
func GetAgentBinaryForCurrentPlatform() ([]byte, error) {
	return GetAgentBinary(runtime.GOOS, runtime.GOARCH)
}

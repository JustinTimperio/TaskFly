package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type BuildTarget struct {
	GOOS   string
	GOARCH string
}

var targets = []BuildTarget{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
}

func main() {
	log.Println("🚀 Building TaskFly agent binaries...")

	// Get project root from working directory (should be run from project root via go generate)
	projectRoot, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %v", err)
	}

	// Verify we're in the project root by checking for go.mod
	if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err != nil {
		log.Fatalf("go.mod not found. This tool must be run from the project root via 'go generate ./internal/cloud'")
	}

	log.Printf("Project root: %s", projectRoot)

	// Build agents concurrently
	var wg sync.WaitGroup
	errors := make(chan error, len(targets))

	for _, target := range targets {
		wg.Add(1)
		go func(t BuildTarget) {
			defer wg.Done()
			if err := buildAgent(projectRoot, t); err != nil {
				errors <- err
			}
		}(target)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	failed := false
	for err := range errors {
		log.Printf("❌ Build failed: %v", err)
		failed = true
	}

	if failed {
		os.Exit(1)
	}

	log.Println("✅ All agent binaries built successfully")
}

func buildAgent(projectRoot string, target BuildTarget) error {
	log.Printf("Building agent for %s/%s...", target.GOOS, target.GOARCH)

	// Create output directory
	outDir := filepath.Join(projectRoot, "build", "agent", fmt.Sprintf("%s-%s", target.GOOS, target.GOARCH))
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Output binary path
	outPath := filepath.Join(outDir, "taskfly-agent")

	// Source file
	srcPath := filepath.Join(projectRoot, "cmd", "taskfly-agent", "main.go")

	// Build command
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", outPath, srcPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GOOS=%s", target.GOOS),
		fmt.Sprintf("GOARCH=%s", target.GOARCH),
		"CGO_ENABLED=0",
	)
	cmd.Dir = projectRoot

	// Capture output
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to build %s/%s: %w\nOutput: %s", target.GOOS, target.GOARCH, err, string(output))
	}

	log.Printf("✓ Built agent for %s/%s", target.GOOS, target.GOARCH)
	return nil
}

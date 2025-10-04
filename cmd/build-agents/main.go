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
	{"windows", "amd64"},
}

func main() {
	log.Println("🚀 Building TaskFly agent binaries...")

	// Get project root - walk up from current directory until we find go.mod
	projectRoot, err := findProjectRoot()
	if err != nil {
		log.Fatalf("Failed to find project root: %v", err)
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

	// Copy agents to cmd/taskflyd/agents for embedding
	log.Println("Copying agents to cmd/taskflyd/agents for embedding...")
	if err := copyAgentsForEmbedding(projectRoot); err != nil {
		log.Fatalf("Failed to copy agents for embedding: %v", err)
	}

	log.Println("✅ All agent binaries built successfully")
}

func findProjectRoot() (string, error) {
	// Start from current working directory
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Walk up the directory tree until we find go.mod
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root directory
			return "", fmt.Errorf("go.mod not found in any parent directory")
		}
		dir = parent
	}
}

func buildAgent(projectRoot string, target BuildTarget) error {
	log.Printf("Building agent for %s/%s...", target.GOOS, target.GOARCH)

	// Create output directory
	outDir := filepath.Join(projectRoot, "build", "agent")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Output binary path - format: taskfly-agent-{os}-{arch}
	outPath := filepath.Join(outDir, fmt.Sprintf("taskfly-agent-%s-%s", target.GOOS, target.GOARCH))
	if target.GOOS == "windows" {
		outPath += ".exe"
	}

	// Source directory (build the whole package, not just main.go)
	srcPath := filepath.Join(projectRoot, "cmd", "taskfly-agent")

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

func copyAgentsForEmbedding(projectRoot string) error {
	srcDir := filepath.Join(projectRoot, "build", "agent")
	destDir := filepath.Join(projectRoot, "cmd", "taskflyd", "agents")

	// Create destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create agents directory: %w", err)
	}

	// Copy each agent binary
	for _, target := range targets {
		srcFile := filepath.Join(srcDir, fmt.Sprintf("taskfly-agent-%s-%s", target.GOOS, target.GOARCH))
		destFile := filepath.Join(destDir, fmt.Sprintf("taskfly-agent-%s-%s", target.GOOS, target.GOARCH))
		if target.GOOS == "windows" {
			srcFile += ".exe"
			destFile += ".exe"
		}

		data, err := os.ReadFile(srcFile)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", srcFile, err)
		}

		if err := os.WriteFile(destFile, data, 0755); err != nil {
			return fmt.Errorf("failed to write %s: %w", destFile, err)
		}
	}

	return nil
}

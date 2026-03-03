// Claude Code Runner Service - Worktree Edition
// Features:
// - Uses git worktree for each branch (isolated, no conflicts)
// - Worktrees stored in /tmp
// - One task per branch at a time (branch-level locking)
// - Concurrent execution across different branches/repos
//
// Compile: go build -o gh-claude main.go
// Run: ./gh-claude [-port=3456] [-work-dir=/tmp/claude-runner] [-max-concurrent=5]

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

var (
	port          = flag.Int("port", 3456, "HTTP server port")
	workDir       = flag.String("work-dir", "/tmp/claude-runner", "Base working directory")
	maxConcurrent = flag.Int("max-concurrent", 5, "Max concurrent tasks across all branches")
	githubToken   = flag.String("github-token", os.Getenv("GH_TOKEN"), "GitHub token for API calls")
	webhookURL    = flag.String("webhook-url", "", "URL to send status updates to")
)

func main() {
	flag.Parse()

	// Data directory for SQLite database
	dataDir := "/tmp/claude-data"

	// Initialize server
	svc, err := NewServer(*workDir, dataDir, *maxConcurrent, *githubToken, *webhookURL)
	if err != nil {
		log.Fatalf("Failed to initialize server: %v", err)
	}
	defer svc.Close()

	// Create base work directory
	if err := os.MkdirAll(*workDir, 0o755); err != nil {
		log.Fatalf("Failed to create work directory: %v", err)
	}

	// Restore pending tasks from database
	if err := svc.RestoreTasks(); err != nil {
		log.Printf("[WARN] Failed to restore pending tasks: %v", err)
	}

	// Initialize Claude settings
	codeSvc := NewCodeService()
	if err := codeSvc.InitSettings(); err != nil {
		log.Printf("[WARN] Failed to initialize Claude settings: %v", err)
	}

	// Setup HTTP handlers
	mux := http.NewServeMux()
	svc.SetupHandlers(mux)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting Claude Code Runner on %s", addr)
	log.Printf("Work directory: %s", *workDir)
	log.Printf("Max concurrent: %d", *maxConcurrent)

	// Start task processor
	svc.ProcessQueue()

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

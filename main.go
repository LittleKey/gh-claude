// Claude Code Runner Service - Worktree Edition
// Features:
// - Uses git worktree for each branch (isolated, no conflicts)
// - Worktrees stored in /tmp
// - One task per branch at a time (branch-level locking)
// - Concurrent execution across different branches/repos
//
// Compile: go build -o claude-runner claude-runner.go
// Run: ./claude-runner [-port=3456] [-work-dir=/tmp/claude-runner] [-max-concurrent=5]

package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	port          = flag.Int("port", 3456, "HTTP server port")
	workDir       = flag.String("work-dir", "/tmp/claude-runner", "Base working directory")
	maxConcurrent = flag.Int("max-concurrent", 5, "Max concurrent tasks across all branches")
	githubToken   = flag.String("github-token", os.Getenv("GH_TOKEN"), "GitHub token for API calls")
	webhookURL    = flag.String("webhook-url", "", "URL to send status updates to")
)

// GitHub API helper
type GitHub struct {
	token string
}

func NewGitHub(token string) *GitHub {
	return &GitHub{token: token}
}

func (g *GitHub) do(method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Body = nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return respBody, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// Add reaction to PR
func (g *GitHub) AddPRReaction(owner, repo string, prNumber int, reaction string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reactions", owner, repo, prNumber)
	payload := fmt.Sprintf(`{"content":"%s"}`, reaction)
	_, err := g.do("POST", url, []byte(payload))
	return err
}

// Remove reaction from PR
func (g *GitHub) RemovePRReaction(owner, repo string, prNumber int, reactionID int) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reactions/%d", owner, repo, prNumber, reactionID)
	_, err := g.do("DELETE", url, nil)
	return err
}

// Get current user ID from GitHub API
func (g *GitHub) GetCurrentUserID() (string, error) {
	url := "https://api.github.com/user"
	resp, err := g.do("GET", url, nil)
	if err != nil {
		return "", err
	}
	var user map[string]interface{}
	if err := json.Unmarshal(resp, &user); err != nil {
		return "", err
	}
	if id, ok := user["id"].(float64); ok {
		return strconv.FormatInt(int64(id), 10), nil
	}
	return "", nil
}

// Add comment to PR using gh CLI
func (g *GitHub) AddPRComment(owner, repo string, prNumber int, body string, branch string) error {
	repoFullName := owner + "/" + repo
	// Always comment on PR, ignore branch name
	cmd := exec.Command("gh", "pr", "comment", strconv.Itoa(prNumber), "--body", body, "-R", repoFullName)

	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add comment: %v, output: %s", err, string(output))
	}
	return nil
}

// Add comment to Issue using gh CLI
func (g *GitHub) AddIssueComment(owner, repo string, issueNumber int, body string) error {
	repoFullName := owner + "/" + repo
	cmd := exec.Command("gh", "issue", "comment", strconv.Itoa(issueNumber), "--body", body, "-R", repoFullName)

	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add issue comment: %v, output: %s", err, string(output))
	}
	return nil
}

// Get PR info
func (g *GitHub) GetPR(owner, repo string, prNumber int) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	resp, err := g.do("GET", url, nil)
	if err != nil {
		return nil, err
	}
	var pr map[string]interface{}
	if err := json.Unmarshal(resp, &pr); err != nil {
		return nil, err
	}
	return pr, nil
}

type Request struct {
	Repo   string `json:"repo"`             // owner/repo
	Task   string `json:"task"`             // Task for Claude Code
	Branch string `json:"branch,omitempty"` // Branch to work on (optional, will create new if not provided)
	PR     int    `json:"pr,omitempty"`     // PR number (optional, will checkout PR branch)
	Debug  bool   `json:"debug,omitempty"`  // Enable debug output
}

type Response struct {
	Success  bool          `json:"success"`
	TaskID   string        `json:"task_id,omitempty"`
	Status   string        `json:"status,omitempty"` // queued, running, completed, failed
	Repo     string        `json:"repo,omitempty"`
	Branch   string        `json:"branch,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
}

type Task struct {
	ID         string    `json:"id"`
	Repo       string    `json:"repo"` // owner/repo
	Branch     string    `json:"branch"`
	Task       string    `json:"task"`
	PR         int       `json:"pr"` // PR number (for GitHub interaction)
	Debug      bool      `json:"debug"`
	Status     string    `json:"status"` // queued, running, completed, failed
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	Result     string    `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
	WorkTree   string    `json:"worktree,omitempty"`
	ReactionID int       `json:"reaction_id,omitempty"` // For tracking reaction to remove
	CommentID  int       `json:"comment_id,omitempty"`  // For tracking comment
}

// Branch-level lock: one task per branch at a time
type BranchLock struct {
	mu      sync.Mutex
	queue   []*Task
	running *Task
	paused  bool
}

type Service struct {
	mu            sync.Mutex
	tasks         map[string]*Task       // taskID -> Task
	branchLocks   map[string]*BranchLock // "repo:branch" -> BranchLock
	taskQueue     []*Task                // Global queue for unassigned tasks
	workDir       string
	dataDir       string
	maxConcurrent int
	github        *GitHub
	webhookURL    string
	activeCount   int
	db            *sql.DB
	githubUserID  string // GitHub user ID of the bot
}

// Initialize Claude settings.json with custom env vars
func initClaudeSettings() {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir = "/home/appuser"
	}

	settingsDir := filepath.Join(homeDir, ".claude")
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// Create directory if not exists
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		log.Printf("[WARN] Failed to create settings directory: %v", err)
		return
	}

	// Read existing or create new settings
	settings := map[string]interface{}{
		"env":                               map[string]string{},
		"skipDangerousModePermissionPrompt": true,
	}

	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			log.Printf("[WARN] Failed to parse existing settings: %v", err)
		}
	}

	// Update env vars
	env := map[string]string{}
	if existingEnv, ok := settings["env"].(map[string]interface{}); ok {
		for k, v := range existingEnv {
			if strVal, ok := v.(string); ok {
				env[k] = strVal
			}
		}
	}

	// Set custom env vars from environment
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		env["ANTHROPIC_AUTH_TOKEN"] = apiKey
	}
	if baseURL := os.Getenv("CLAUDE_API_BASE_URL"); baseURL != "" {
		env["ANTHROPIC_BASE_URL"] = baseURL
	}
	if model := os.Getenv("CLAUDE_MODEL"); model != "" {
		env["ANTHROPIC_MODEL"] = model
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = model
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = model
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = model
		env["ANTHROPIC_SMALL_FAST_MODEL"] = model
	}
	env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"

	settings["env"] = env

	// Write back
	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		log.Printf("[WARN] Failed to marshal settings: %v", err)
		return
	}

	if err := os.WriteFile(settingsPath, newData, 0o644); err != nil {
		log.Printf("[WARN] Failed to write settings: %v", err)
		return
	}

	log.Printf("[INIT] Claude settings initialized at %s", settingsPath)

	// Configure git safe.directory to allow all directories
	cmd := exec.Command("git", "config", "--global", "--add", "safe.directory", "*")
	cmd.Run()

	// Configure git SSL (use system certs)
	cmd = exec.Command("git", "config", "--global", "http.sslCAInfo", "/etc/ssl/certs/ca-certificates.crt")
	cmd.Run()

	// Ensure work directory exists
	workDir := "/tmp/claude-runner"
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		log.Printf("[WARN] Failed to create work directory: %v", err)
	}
}

func NewService(db *sql.DB, dataDir string) *Service {
	gh := NewGitHub(*githubToken)
	// Get bot user ID from GitHub API
	githubUserID, err := gh.GetCurrentUserID()
	if err != nil {
		log.Printf("[WARN] Failed to get GitHub user ID: %v", err)
	} else {
		log.Printf("[INFO] GitHub bot user ID: %s", githubUserID)
	}
	return &Service{
		tasks:         make(map[string]*Task),
		branchLocks:   make(map[string]*BranchLock),
		taskQueue:     make([]*Task, 0),
		workDir:       *workDir,
		dataDir:       dataDir,
		maxConcurrent: *maxConcurrent,
		github:        gh,
		webhookURL:    *webhookURL,
		db:            db,
		githubUserID:  githubUserID,
	}
}

func main() {
	flag.Parse()

	// Data directory for SQLite database
	dataDir := "/tmp/claude-data"

	// Initialize database
	db, err := InitDB(dataDir)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	svc := NewService(db, dataDir)

	// Create base work directory
	if err := os.MkdirAll(svc.workDir, 0o755); err != nil {
		log.Fatalf("Failed to create work directory: %v", err)
	}

	// Restore pending tasks from database
	pendingTasks, err := LoadPendingTasks(db)
	if err != nil {
		log.Printf("[WARN] Failed to load pending tasks: %v", err)
	} else {
		for _, task := range pendingTasks {
			svc.restoreTask(task)
		}
	}

	// Initialize Claude settings.json
	initClaudeSettings()

	// HTTP handlers
	http.HandleFunc("/run", svc.handleRun)
	http.HandleFunc("/status", svc.handleStatus)
	http.HandleFunc("/queue", svc.handleQueue)
	http.HandleFunc("/cancel", svc.handleCancel)
	http.HandleFunc("/webhook", svc.handleWebhook)
	http.HandleFunc("/health", svc.handleHealth)
	http.HandleFunc("/", svc.handleRoot)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting Claude Code Runner on %s", addr)
	log.Printf("Work directory: %s", svc.workDir)
	log.Printf("Max concurrent: %d", svc.maxConcurrent)

	// Start task processor
	go svc.processQueue()

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func (s *Service) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"service":      "claude-code-runner",
		"version":      "3.0.0",
		"active_tasks": s.activeCount,
		"queued_tasks": len(s.taskQueue),
		"endpoints": []string{
			"/run - Submit a new task",
			"/webhook - GitHub webhook receiver",
			"/status?task_id=xxx - Get task status",
			"/queue - List all tasks",
			"/cancel?task_id=xxx - Cancel a task",
			"/health - Health check",
		},
	})
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	s.mu.Lock()
	defer s.mu.Unlock()

	branchCount := 0
	runningBranches := 0
	for _, bl := range s.branchLocks {
		branchCount++
		bl.mu.Lock()
		if bl.running != nil {
			runningBranches++
		}
		bl.mu.Unlock()
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "healthy",
		"active_tasks":     s.activeCount,
		"queued_tasks":     len(s.taskQueue),
		"total_branches":   branchCount,
		"running_branches": runningBranches,
		"timestamp":        time.Now().Unix(),
	})
}

func (s *Service) handleRun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Repo == "" || req.Task == "" {
		http.Error(w, "repo and task are required", http.StatusBadRequest)
		return
	}

	// Determine target branch
	branch := req.Branch
	if branch == "" && req.PR > 0 {
		// Use PR branch
		branch = fmt.Sprintf("pr-%d", req.PR)
	} else if branch == "" {
		// Default to task-specific branch
		branch = fmt.Sprintf("claude-%d", time.Now().Unix())
	}

	// Create task
	task := &Task{
		ID:        fmt.Sprintf("task-%d-%s", time.Now().UnixNano()%100000, branch),
		Repo:      req.Repo,
		Branch:    branch,
		Task:      req.Task,
		PR:        req.PR,
		Debug:     req.Debug,
		Status:    "queued",
		CreatedAt: time.Now(),
	}

	// Add reaction to PR if this is a PR task
	if req.PR > 0 && s.github != nil {
		parts := strings.Split(req.Repo, "/")
		if len(parts) == 2 {
			// Add "eyes" reaction to show we're working on it
			if err := s.github.AddPRReaction(parts[0], parts[1], req.PR, "eyes"); err != nil {
				log.Printf("[WARN] Failed to add reaction to PR #%d: %v", req.PR, err)
			} else {
				log.Printf("[GITHUB] Added reaction to PR #%d", req.PR)
			}
		}
	}

	// Submit task
	s.submitTask(task)

	log.Printf("[RUN] Task %s: repo=%s, branch=%s, task=%s", task.ID, req.Repo, branch, truncate(req.Task, 50))

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  task.ID,
		Status:  "queued",
		Repo:    req.Repo,
		Branch:  branch,
	})
}

func (s *Service) submitTask(task *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[task.ID] = task

	// Save to database
	if s.db != nil {
		if err := SaveTask(s.db, task); err != nil {
			log.Printf("[WARN] Failed to save task to database: %v", err)
		}
	}

	// Get or create branch lock
	lockKey := fmt.Sprintf("%s:%s", task.Repo, task.Branch)
	bl, exists := s.branchLocks[lockKey]
	if !exists {
		bl = &BranchLock{
			queue: make([]*Task, 0),
		}
		s.branchLocks[lockKey] = bl
	}

	bl.mu.Lock()
	defer bl.mu.Unlock()

	if bl.running != nil {
		// Branch is busy, add to queue
		bl.queue = append(bl.queue, task)
		log.Printf("[QUEUE] Task %s added to queue for branch %s:%s", task.ID, task.Repo, task.Branch)
	} else {
		// Can run immediately
		s.taskQueue = append(s.taskQueue, task)
		log.Printf("[QUEUE] Task %s submitted for branch %s:%s", task.ID, task.Repo, task.Branch)
	}
}

// restoreTask restores a task from database to memory (without re-adding GitHub reaction)
func (s *Service) restoreTask(task *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[task.ID] = task

	// Get or create branch lock
	lockKey := fmt.Sprintf("%s:%s", task.Repo, task.Branch)
	bl, exists := s.branchLocks[lockKey]
	if !exists {
		bl = &BranchLock{
			queue: make([]*Task, 0),
		}
		s.branchLocks[lockKey] = bl
	}

	bl.mu.Lock()
	defer bl.mu.Unlock()

	// For restored tasks, reset status to queued and add to queue
	task.Status = "queued"

	if bl.running != nil {
		// Branch is busy, add to queue
		bl.queue = append(bl.queue, task)
		log.Printf("[RESTORE] Task %s restored to queue for branch %s:%s", task.ID, task.Repo, task.Branch)
	} else {
		// Can run immediately
		s.taskQueue = append(s.taskQueue, task)
		log.Printf("[RESTORE] Task %s restored for branch %s:%s", task.ID, task.Repo, task.Branch)
	}
}

func (s *Service) processQueue() {
	for {
		time.Sleep(500 * time.Millisecond)

		s.mu.Lock()

		// Check if we can run more tasks
		if s.activeCount >= s.maxConcurrent || len(s.taskQueue) == 0 {
			s.mu.Unlock()
			continue
		}

		// Get next task
		task := s.taskQueue[0]
		s.taskQueue = s.taskQueue[1:]
		s.activeCount++

		// Mark branch as running
		lockKey := fmt.Sprintf("%s:%s", task.Repo, task.Branch)
		bl := s.branchLocks[lockKey]
		bl.running = task

		s.mu.Unlock()

		// Run task in goroutine
		go s.executeTask(task, bl, lockKey)
	}
}

func (s *Service) executeTask(task *Task, bl *BranchLock, lockKey string) {
	log.Printf("[EXEC] Starting task %s for repo=%s branch=%s", task.ID, task.Repo, task.Branch)

	task.Status = "running"
	task.StartedAt = time.Now()

	// Save task status to database
	if s.db != nil {
		if err := SaveTask(s.db, task); err != nil {
			log.Printf("[WARN] Failed to save task status to database: %v", err)
		}
	}

	// Create worktree path: /tmp/claude-runner/{owner-repo}/{branch}
	repoSafe := strings.ReplaceAll(task.Repo, "/", "-")
	worktreePath := filepath.Join(s.workDir, repoSafe, task.Branch)
	task.WorkTree = worktreePath

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		log.Printf("[EXEC] ERROR: failed to create worktree dir: %v", err)
		task.Status = "failed"
		task.Error = "failed to create worktree dir: " + err.Error()
		task.EndedAt = time.Now()
		s.completeTask(task, bl, lockKey)
		return
	}

	// Check if worktree already exists
	worktreeExists := false
	if _, err := os.Stat(worktreePath); err == nil {
		worktreeExists = true
	}

	// Use main repo directory as reference
	mainRepoPath := filepath.Join(s.workDir, repoSafe, ".main")

	if !worktreeExists {
		// Need to create worktree
		if err := s.setupWorktree(task, mainRepoPath, worktreePath); err != nil {
			log.Printf("[EXEC] ERROR: failed to setup worktree: %v", err)
			task.Status = "failed"
			task.Error = "failed to setup worktree: " + err.Error()
			task.EndedAt = time.Now()
			s.completeTask(task, bl, lockKey)
			return
		}
	}

	// Sync worktree with remote and resolve any conflicts
	if err := s.syncWorktree(task, worktreePath); err != nil {
		log.Printf("[WARN] Failed to sync worktree: %v", err)
	}

	// Run Claude Code
	log.Printf("[EXEC] Running Claude Code: %s", truncate(task.Task, 100))

	// Update Claude settings.json with custom env vars
	settingsPath := filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err == nil {
			env := map[string]string{}
			if existingEnv, ok := settings["env"].(map[string]interface{}); ok {
				for k, v := range existingEnv {
					if strVal, ok := v.(string); ok {
						env[k] = strVal
					}
				}
			}
			// Set custom env vars
			if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
				env["ANTHROPIC_AUTH_TOKEN"] = apiKey
			}
			if baseURL := os.Getenv("CLAUDE_API_BASE_URL"); baseURL != "" {
				env["ANTHROPIC_BASE_URL"] = baseURL
			}
			if model := os.Getenv("CLAUDE_MODEL"); model != "" {
				env["ANTHROPIC_MODEL"] = model
				env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = model
				env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = model
				env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = model
				env["ANTHROPIC_SMALL_FAST_MODEL"] = model
			}
			env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
			settings["env"] = env
			if newData, err := json.MarshalIndent(settings, "", "  "); err == nil {
				os.WriteFile(settingsPath, newData, 0o644)
			}
		}
	}

	// Run Claude with proper environment
	claudeCmd := exec.Command("claude", "--dangerously-skip-permissions", task.Task)
	claudeCmd.Dir = worktreePath
	claudeCmd.Env = append(os.Environ(),
		"CLAUDE_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"),
		"ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"),
	)
	// Add custom API base URL if set
	if baseURL := os.Getenv("CLAUDE_API_BASE_URL"); baseURL != "" {
		claudeCmd.Env = append(claudeCmd.Env, "CLAUDE_API_BASE_URL="+baseURL)
	}
	// Add custom model if set
	if model := os.Getenv("CLAUDE_MODEL"); model != "" {
		claudeCmd.Env = append(claudeCmd.Env, "CLAUDE_MODEL="+model)
	}

	output, err := claudeCmd.CombinedOutput()

	if len(output) > 0 {
		log.Printf("[EXEC] Claude output: %s", truncate(string(output), 500))
		task.Result = string(output)
	}

	task.EndedAt = time.Now()

	if err != nil {
		log.Printf("[EXEC] Claude command error: %v", err)
		task.Status = "failed"
		task.Error = fmt.Sprintf("claude error: %v\n%s", err, string(output))
		// Notify GitHub of failure
		if task.PR > 0 {
			s.notifyGitHub(task, false)
		}
	} else {
		task.Status = "completed"
		// Push changes
		s.pushChanges(task)
		// Notify GitHub of success
		if task.PR > 0 {
			s.notifyGitHub(task, true)
		}
	}

	s.completeTask(task, bl, lockKey)
}

func (s *Service) setupWorktree(task *Task, mainRepoPath, worktreePath string) error {
	cloneURL := fmt.Sprintf("https://%s@github.com/%s.git", s.github.token, task.Repo)

	// Clone main repo if not exists
	if _, err := os.Stat(mainRepoPath); os.IsNotExist(err) {
		log.Printf("[GIT] Cloning %s to %s", task.Repo, mainRepoPath)
		cmd := exec.Command("git", "clone", "--bare", cloneURL, mainRepoPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to clone: %v", err)
		}
	} else {
		// Sync with remote to get latest changes
		log.Printf("[GIT] Fetching latest changes for %s", task.Repo)
		cmd := exec.Command("git", "fetch", "origin")
		cmd.Dir = mainRepoPath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("[WARN] Failed to fetch origin: %v", err)
		}

		// Update local main branch to match remote
		log.Printf("[GIT] Updating local main branch to match remote")
		cmd = exec.Command("git", "checkout", "origin/main", "-B", "main")
		cmd.Dir = mainRepoPath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("[WARN] Failed to update main branch: %v", err)
		}
	}

	// Determine base branch
	baseBranch := "main"
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = mainRepoPath
	if output, err := cmd.Output(); err == nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			baseBranch = trimmed
		}
	}

	// For PR, fetch the PR branch
	if task.PR > 0 {
		cmd = exec.Command("git", "fetch", "origin", fmt.Sprintf("pull/%d/head:pr-%d", task.PR, task.PR))
		cmd.Dir = mainRepoPath
		cmd.Run() // Ignore error, branch might not exist
	}

	// Create worktree
	log.Printf("[GIT] Creating worktree at %s for branch %s", worktreePath, task.Branch)

	// Check if branch exists locally
	checkCmd := exec.Command("git", "rev-parse", "--verify", fmt.Sprintf("refs/heads/%s", task.Branch))
	checkCmd.Dir = mainRepoPath
	branchExists := checkCmd.Run() == nil

	if branchExists {
		// Branch exists, use regular worktree add
		cmd = exec.Command("git", "worktree", "add", "-f", worktreePath, task.Branch)
		cmd.Dir = mainRepoPath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create worktree: %v", err)
		}
	} else {
		// Branch doesn't exist, create from base branch
		log.Printf("[GIT] Branch %s does not exist, creating from %s", task.Branch, baseBranch)
		cmd = exec.Command("git", "worktree", "add", "-f", "-B", task.Branch, worktreePath, baseBranch)
		cmd.Dir = mainRepoPath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create worktree: %v", err)
		}
	}

	// Configure git user in worktree
	cmds := []*exec.Cmd{
		exec.Command("git", "config", "user.email", "claude-runner@local"),
		exec.Command("git", "config", "user.name", "Claude Runner"),
	}
	for _, c := range cmds {
		c.Dir = worktreePath
		c.Run()
	}

	// Set remote URL with credentials for push operations
	remoteURL := fmt.Sprintf("https://%s@github.com/%s.git", s.github.token, task.Repo)
	remoteCmd := exec.Command("git", "remote", "set-url", "origin", remoteURL)
	remoteCmd.Dir = worktreePath
	if err := remoteCmd.Run(); err != nil {
		log.Printf("[WARN] Failed to set remote URL with credentials: %v", err)
	}

	return nil
}

// syncWorktree syncs the worktree with remote and resolves any conflicts
func (s *Service) syncWorktree(task *Task, worktreePath string) error {
	log.Printf("[GIT] Syncing worktree at %s with remote", worktreePath)

	// Fetch latest changes
	cmd := exec.Command("git", "fetch", "origin")
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("[WARN] Failed to fetch in worktree: %v", err)
		// Continue anyway, might work with local state
	}

	// Get current branch name
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = worktreePath
	branch, err := cmd.Output()
	if err != nil {
		log.Printf("[WARN] Failed to get current branch: %v", err)
		return nil // Not critical, continue
	}
	branch = []byte(strings.TrimSpace(string(branch)))

	if len(branch) == 0 {
		log.Printf("[WARN] No current branch in worktree")
		return nil
	}

	// Check if there are any changes to pull
	cmd = exec.Command("git", "rev-list", "--count", fmt.Sprintf("HEAD..origin/%s", string(branch)))
	cmd.Dir = worktreePath
	behindCount, err := cmd.Output()
	if err == nil {
		count := strings.TrimSpace(string(behindCount))
		if count != "0" {
			log.Printf("[GIT] Branch %s is behind origin/%s by %s commits, pulling...", string(branch), string(branch), count)
			cmd = exec.Command("git", "pull", "--rebase", "origin", string(branch))
			cmd.Dir = worktreePath
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				log.Printf("[WARN] Failed to pull changes: %v", err)
				// Try to rebase or reset to remote
				log.Printf("[GIT] Attempting to reset to origin/%s", string(branch))
				cmd = exec.Command("git", "reset", "--hard", fmt.Sprintf("origin/%s", string(branch)))
				cmd.Dir = worktreePath
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					log.Printf("[ERROR] Failed to reset to origin/%s: %v", string(branch), err)
				}
			}
		} else {
			log.Printf("[GIT] Branch %s is up to date with origin/%s", string(branch), string(branch))
		}
	}

	return nil
}

// createPRIfNeeded creates a PR if it doesn't exist for issue-based branches
// Returns the PR number if a PR was created or already exists, 0 otherwise
func (s *Service) createPRIfNeeded(task *Task) int {
	// Extract issue number from branch name (e.g., fix-issue-2 -> 2)
	issueNum := 0
	if _, err := fmt.Sscanf(task.Branch, "fix-issue-%d", &issueNum); err != nil {
		log.Printf("[PR] Failed to extract issue number from branch %s: %v", task.Branch, err)
		return 0
	}

	// Check if PR already exists
	parts := strings.Split(task.Repo, "/")
	if len(parts) != 2 {
		log.Printf("[PR] Invalid repo format: %s", task.Repo)
		return 0
	}

	// Use gh CLI to check if PR exists
	checkCmd := exec.Command("gh", "pr", "view", strconv.Itoa(issueNum), "--json", "url,number", "-R", task.Repo)
	checkOutput, err := checkCmd.CombinedOutput()

	if err == nil {
		// PR already exists, extract PR number from gh output
		log.Printf("[PR] PR for issue #%d already exists", issueNum)
		// Try to get PR number from the output
		var prData struct {
			Number int `json:"number"`
		}
		if json.Unmarshal(checkOutput, &prData) == nil && prData.Number > 0 {
			return prData.Number
		}
		return issueNum
	}

	// PR doesn't exist, create it
	log.Printf("[PR] Creating PR for issue #%d from branch %s", issueNum, task.Branch)

	// Get issue title for PR title
	title := fmt.Sprintf("Fix issue #%d", issueNum)
	body := fmt.Sprintf("This PR fixes issue #%d\n\nTask: %s", issueNum, truncate(task.Task, 200))

	createCmd := exec.Command("gh", "pr", "create",
		"--title", title,
		"--body", body,
		"--base", "main",
		"--head", task.Branch,
		"-R", task.Repo,
	)
	createOutput, err := createCmd.CombinedOutput()
	if err != nil {
		log.Printf("[PR] Failed to create PR: %v, output: %s", err, string(createOutput))
		return 0
	}

	log.Printf("[PR] Successfully created PR for branch %s", task.Branch)

	// Get the newly created PR number using gh
	viewCmd := exec.Command("gh", "pr", "view", "--json", "number", "-R", task.Repo, task.Branch)
	viewOutput, err := viewCmd.CombinedOutput()
	if err != nil {
		log.Printf("[PR] Failed to get PR number: %v, output: %s", err, string(viewOutput))
		return issueNum // Return issue number as fallback
	}

	var prData struct {
		Number int `json:"number"`
	}
	if json.Unmarshal(viewOutput, &prData) == nil && prData.Number > 0 {
		return prData.Number
	}

	return issueNum
}

// getPRBranch gets the actual branch name for a PR using GitHub API
func (s *Service) getPRBranch(repo string, prNum int) string {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNum), "--json", "headRefName", "-R", repo)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[WEBHOOK] Failed to get PR #%d branch: %v, output: %s", prNum, err, string(output))
		return ""
	}

	var result struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[WEBHOOK] Failed to parse PR branch: %v", err)
		return ""
	}

	log.Printf("[WEBHOOK] Got PR #%d branch: %s", prNum, result.HeadRefName)
	return result.HeadRefName
}

func (s *Service) pushChanges(task *Task) {
	if task.WorkTree == "" || task.Status != "completed" {
		return
	}

	// Check if there are changes
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = task.WorkTree
	output, err := cmd.Output()

	if err != nil || len(output) == 0 {
		log.Printf("[PUSH] No changes to push for task %s", task.ID)
		return
	}

	log.Printf("[PUSH] Changes detected, committing and pushing for task %s", task.ID)

	// Stage all changes
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = task.WorkTree
	if output, err := addCmd.CombinedOutput(); err != nil {
		task.Error += fmt.Sprintf("\nGit add failed: %v\n%s", err, string(output))
		task.Status = "failed"
		return
	}

	// Create commit
	commitMsg := fmt.Sprintf("Claude: %s", truncate(task.Task, 50))
	commitCmd := exec.Command("git", "commit", "-m", commitMsg)
	commitCmd.Dir = task.WorkTree
	if output, err := commitCmd.CombinedOutput(); err != nil {
		// No changes to commit (maybe just formatting or already committed)
		log.Printf("[PUSH] No commit needed or commit failed: %s", string(output))
	}

	// Push
	pushCmd := exec.Command("git", "push", "origin", task.Branch)
	pushCmd.Dir = task.WorkTree
	pushCmd.Env = append(os.Environ(), "GIT_ASKPASS=true")

	pushOutput, err := pushCmd.CombinedOutput()
	if err != nil {
		task.Error += fmt.Sprintf("\nPush failed: %v\n%s", err, string(pushOutput))
		task.Status = "failed"
	} else {
		log.Printf("[PUSH] Successfully pushed branch %s for task %s", task.Branch, task.ID)

		// Create PR if it doesn't exist (for issue-based branches)
		if strings.HasPrefix(task.Branch, "fix-issue-") && task.PR == 0 {
			prNum := s.createPRIfNeeded(task)
			if prNum > 0 {
				task.PR = prNum
			}
		}
	}
}

// notifyGitHub removes reaction and adds a comment to the PR
func (s *Service) notifyGitHub(task *Task, success bool) {
	if s.github == nil {
		return
	}

	parts := strings.Split(task.Repo, "/")
	if len(parts) != 2 {
		return
	}
	owner, repo := parts[0], parts[1]

	// Try to remove reaction (ignore errors, might not exist)
	// Note: We can't easily remove the reaction without the reaction ID
	// So we'll just add a new comment instead

	// Build comment message
	var comment string
	if success {
		duration := task.EndedAt.Sub(task.StartedAt)
		// Use Claude's result output as the completion message
		resultSummary := truncate(task.Result, 1000)
		if resultSummary != "" {
			comment = fmt.Sprintf("✅ **Task Completed**\n\nBranch `%s` has been updated and pushed.\n\n**Result:**\n%s\n\nDuration: %v",
				task.Branch, resultSummary, duration)
		} else {
			comment = fmt.Sprintf("✅ **Task Completed**\n\nBranch `%s` has been updated and pushed.\n\nDuration: %v",
				task.Branch, duration)
		}
	} else {
		comment = fmt.Sprintf("❌ **Task Failed**\n\nBranch `%s`\n\nError: %s\n\nTask: %s",
			task.Branch, truncate(task.Error, 500), truncate(task.Task, 200))
	}

	// Determine where to comment: PR or Issue
	// - If PR > 0, comment on PR
	// - If branch starts with "fix-issue-" and PR == 0, comment on Issue
	if task.PR > 0 {
		// Comment on PR
		if err := s.github.AddPRComment(owner, repo, task.PR, comment, task.Branch); err != nil {
			log.Printf("[GITHUB] Failed to add comment to PR #%d: %v", task.PR, err)
		} else {
			log.Printf("[GITHUB] Added completion comment to PR #%d", task.PR)
		}
	} else if strings.HasPrefix(task.Branch, "fix-issue-") {
		// Comment on Issue (no PR was created)
		issueNumStr := strings.TrimPrefix(task.Branch, "fix-issue-")
		issueNum, err := strconv.Atoi(issueNumStr)
		if err != nil {
			log.Printf("[GITHUB] Failed to parse issue number from branch %s: %v", task.Branch, err)
			return
		}
		if err := s.github.AddIssueComment(owner, repo, issueNum, comment); err != nil {
			log.Printf("[GITHUB] Failed to add comment to Issue #%d: %v", issueNum, err)
		} else {
			log.Printf("[GITHUB] Added completion comment to Issue #%d", issueNum)
		}
	}
}

func (s *Service) completeTask(task *Task, bl *BranchLock, lockKey string) {
	s.mu.Lock()
	s.activeCount--

	bl.mu.Lock()
	bl.running = nil

	// Get next task from queue
	if len(bl.queue) > 0 {
		nextTask := bl.queue[0]
		bl.queue = bl.queue[1:]
		bl.mu.Unlock()

		s.mu.Lock()
		s.taskQueue = append(s.taskQueue, nextTask)
		s.mu.Unlock()

		log.Printf("[QUEUE] Started next task %s for branch %s", nextTask.ID, lockKey)
	} else {
		bl.mu.Unlock()
	}

	s.mu.Unlock()

	// Save final task status to database
	if s.db != nil {
		if err := SaveTask(s.db, task); err != nil {
			log.Printf("[WARN] Failed to save task completion to database: %v", err)
		}
	}

	duration := task.EndedAt.Sub(task.StartedAt)
	log.Printf("[DONE] Task %s: %s (duration: %v)", task.ID, task.Status, duration)

	// Send webhook if configured
	if s.webhookURL != "" {
		s.sendWebhook(task)
	}
}

func (s *Service) sendWebhook(task *Task) {
	payload := map[string]interface{}{
		"task_id":  task.ID,
		"repo":     task.Repo,
		"branch":   task.Branch,
		"status":   task.Status,
		"pr":       task.PR,
		"result":   task.Result,
		"error":    task.Error,
		"duration": task.EndedAt.Sub(task.StartedAt).String(),
	}

	body, _ := json.Marshal(payload)
	_ = body

	resp, err := http.Post(s.webhookURL, "application/json", nil)
	if err == nil {
		resp.Body.Close()
	}
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	task, exists := s.tasks[taskID]
	s.mu.Unlock()

	if !exists {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(task)
}

func (s *Service) handleQueue(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	s.mu.Lock()
	defer s.mu.Unlock()

	type QueueInfo struct {
		ActiveTasks int                    `json:"active_tasks"`
		QueuedTasks int                    `json:"queued_tasks"`
		TotalTasks  int                    `json:"total_tasks"`
		Branches    map[string]interface{} `json:"branches"`
	}

	branchInfo := make(map[string]interface{})
	for key, bl := range s.branchLocks {
		bl.mu.Lock()
		runningTask := bl.running
		branchInfo[key] = map[string]interface{}{
			"running": runningTask,
			"queue":   len(bl.queue),
		}
		bl.mu.Unlock()
	}

	info := QueueInfo{
		ActiveTasks: s.activeCount,
		QueuedTasks: len(s.taskQueue),
		TotalTasks:  len(s.tasks),
		Branches:    branchInfo,
	}

	json.NewEncoder(w).Encode(info)
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	task, exists := s.tasks[taskID]
	if !exists {
		s.mu.Unlock()
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	if task.Status == "running" {
		s.mu.Unlock()
		http.Error(w, "cannot cancel running task", http.StatusBadRequest)
		return
	}

	task.Status = "cancelled"
	delete(s.tasks, taskID)
	s.mu.Unlock()

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  taskID,
		Status:  "cancelled",
	})
}

func (s *Service) handleWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse GitHub event
	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	log.Printf("[WEBHOOK] Received event: %s", eventType)

	var task *Task
	var repo, branch, taskDesc string
	var prNum int

	switch eventType {
	case "issue_comment":
		// Debug: log full payload keys
		log.Printf("[WEBHOOK] Full payload keys: %v", reflect.ValueOf(payload).MapKeys())

		// Check action - only process new comments
		if action, ok := payload["action"].(string); ok && action != "created" {
			log.Printf("[WEBHOOK] Ignoring comment with action: %s", action)
			return
		}
		// Check sender - ignore comments from the bot itself
		log.Printf("[WEBHOOK] Bot user ID: %s", s.githubUserID)

		// Check sender at top level
		if sender, ok := payload["sender"].(map[string]interface{}); ok {
			log.Printf("[WEBHOOK] Sender found in payload: %+v", sender)
			if senderID, ok := sender["id"].(float64); ok {
				senderIDStr := strconv.FormatInt(int64(senderID), 10)
				log.Printf("[WEBHOOK] Sender ID: %s, Bot ID: %s", senderIDStr, s.githubUserID)
				if s.githubUserID != "" && senderIDStr == s.githubUserID {
					log.Printf("[WEBHOOK] Ignoring comment from bot user (senderID=%s, botID=%s)", senderIDStr, s.githubUserID)
					return
				}
			}
		} else {
			log.Printf("[WEBHOOK] No sender found in payload")
		}

		// Also check comment.author as fallback
		if comment, ok := payload["comment"].(map[string]interface{}); ok {
			log.Printf("[WEBHOOK] Comment keys: %v", reflect.ValueOf(comment).MapKeys())
			if author, ok := comment["user"].(map[string]interface{}); ok {
				log.Printf("[WEBHOOK] Comment user: %+v", author)
				if authorID, ok := author["id"].(float64); ok {
					authorIDStr := strconv.FormatInt(int64(authorID), 10)
					log.Printf("[WEBHOOK] Comment author ID: %s, Bot ID: %s", authorIDStr, s.githubUserID)
					if s.githubUserID != "" && authorIDStr == s.githubUserID {
						log.Printf("[WEBHOOK] Ignoring comment from bot user (via comment.user)")
						return
					}
				}
			}
		}
		if comment, ok := payload["comment"].(map[string]interface{}); ok {
			if body, ok := comment["body"].(string); ok {
				// Check for @claude command
				if strings.Contains(body, "@claude") || strings.HasPrefix(strings.TrimSpace(body), "/claude") {
					// Get issue/PR context (title and body)
					var issueTitle, issueBody string
					if issue, ok := payload["issue"].(map[string]interface{}); ok {
						if n, ok := issue["number"].(float64); ok {
							// Check if this is a PR comment or issue comment
							if pr, ok := issue["pull_request"].(map[string]interface{}); ok && pr != nil {
								// For PR comments, prNum is the PR number
								prNum = int(n)
								// Get the actual branch from GitHub API
								if r, ok := payload["repository"].(map[string]interface{}); ok {
									if fullName, ok := r["full_name"].(string); ok {
										branch = s.getPRBranch(fullName, prNum)
									}
								}
								// Note: If branch is empty, we fail later in the check
							} else {
								// For issue comments, prNum should be 0 (not an actual PR)
								// Branch is derived from issue number
								prNum = 0
								branch = fmt.Sprintf("fix-issue-%d", int(n))
							}
						}
						// Extract title and body for context
						if title, ok := issue["title"].(string); ok {
							issueTitle = title
						}
						if b, ok := issue["body"].(string); ok {
							issueBody = b
						}
					}

					// Build task description with context
					taskCmd := extractTask(body)
					if issueTitle != "" || issueBody != "" {
						var context strings.Builder
						if issueTitle != "" {
							context.WriteString(fmt.Sprintf("Issue/PR Title: %s\n\n", issueTitle))
						}
						if issueBody != "" {
							context.WriteString(fmt.Sprintf("Issue/PR Description:\n%s\n\n", issueBody))
						}
						context.WriteString(fmt.Sprintf("User Command:\n%s", taskCmd))
						taskDesc = context.String()
					} else {
						taskDesc = taskCmd
					}

					// repository is at payload level, not under issue
					if r, ok := payload["repository"].(map[string]interface{}); ok {
						if fullName, ok := r["full_name"].(string); ok {
							repo = fullName
						}
					}
				}
			}
		}

	case "pull_request_review":
		// Check action - only process new reviews
		if action, ok := payload["action"].(string); ok && action != "submitted" {
			log.Printf("[WEBHOOK] Ignoring review with action: %s", action)
			return
		}
		// Check sender - ignore reviews from the bot itself
		log.Printf("[WEBHOOK] Bot user ID: %s", s.githubUserID)
		if sender, ok := payload["sender"].(map[string]interface{}); ok {
			if senderID, ok := sender["id"].(float64); ok {
				senderIDStr := strconv.FormatInt(int64(senderID), 10)
				if s.githubUserID != "" && senderIDStr == s.githubUserID {
					log.Printf("[WEBHOOK] Ignoring review from bot user")
					return
				}
			}
		}
		var prTitle, prBody string
		if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
			if n, ok := pr["number"].(float64); ok {
				prNum = int(n)
			}
			// Get repository full name first
			var repoFullName string
			if r, ok := pr["base"].(map[string]interface{})["repo"].(map[string]interface{}); ok {
				if fullName, ok := r["full_name"].(string); ok {
					repoFullName = fullName
					repo = fullName
				}
			}
			// Get actual branch name from GitHub API
			if repoFullName != "" && prNum > 0 {
				branch = s.getPRBranch(repoFullName, prNum)
			}
			// Note: If branch is empty, we fail later in the check
			// Extract PR title and body for context
			if title, ok := pr["title"].(string); ok {
				prTitle = title
			}
			if b, ok := pr["body"].(string); ok {
				prBody = b
			}
		}
		if review, ok := payload["review"].(map[string]interface{}); ok {
			if body, ok := review["body"].(string); ok {
				// Build task description with context
				var context strings.Builder
				if prTitle != "" {
					context.WriteString(fmt.Sprintf("PR Title: %s\n\n", prTitle))
				}
				if prBody != "" {
					context.WriteString(fmt.Sprintf("PR Description:\n%s\n\n", prBody))
				}
				context.WriteString(fmt.Sprintf("Review Message:\n%s", body))
				taskDesc = context.String()
			}
		}

	case "pull_request_review_comment":
		// Check action - only process new comments
		if action, ok := payload["action"].(string); ok && action != "created" {
			log.Printf("[WEBHOOK] Ignoring review comment with action: %s", action)
			return
		}
		// Check sender - ignore comments from the bot itself
		log.Printf("[WEBHOOK] Bot user ID: %s", s.githubUserID)
		if sender, ok := payload["sender"].(map[string]interface{}); ok {
			if senderID, ok := sender["id"].(float64); ok {
				senderIDStr := strconv.FormatInt(int64(senderID), 10)
				if s.githubUserID != "" && senderIDStr == s.githubUserID {
					log.Printf("[WEBHOOK] Ignoring review comment from bot user")
					return
				}
			}
		}
		var prTitle, prBody string
		if comment, ok := payload["comment"].(map[string]interface{}); ok {
			commentBody := ""
			if body, ok := comment["body"].(string); ok {
				commentBody = body
			}
			if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
				if n, ok := pr["number"].(float64); ok {
					prNum = int(n)
				}
				// Get repository full name first
				var repoFullName string
				if r, ok := pr["base"].(map[string]interface{})["repo"].(map[string]interface{}); ok {
					if fullName, ok := r["full_name"].(string); ok {
						repoFullName = fullName
						repo = fullName
					}
				}
				// Get actual branch name from GitHub API
				if repoFullName != "" && prNum > 0 {
					branch = s.getPRBranch(repoFullName, prNum)
				}
				// Note: If branch is empty, we fail later in the check
				// Extract PR title and body for context
				if title, ok := pr["title"].(string); ok {
					prTitle = title
				}
				if b, ok := pr["body"].(string); ok {
					prBody = b
				}
			}
			// Build task description with context
			if prTitle != "" || prBody != "" {
				var context strings.Builder
				if prTitle != "" {
					context.WriteString(fmt.Sprintf("PR Title: %s\n\n", prTitle))
				}
				if prBody != "" {
					context.WriteString(fmt.Sprintf("PR Description:\n%s\n\n", prBody))
				}
				context.WriteString(fmt.Sprintf("Comment:\n%s", commentBody))
				taskDesc = context.String()
			} else {
				taskDesc = commentBody
			}
		}
	}

	if repo == "" || taskDesc == "" {
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	// For PR comments, if branch is not found, fail the task
	if prNum > 0 && branch == "" {
		log.Printf("[WEBHOOK] PR branch not found for PR #%d, skipping task", prNum)
		json.NewEncoder(w).Encode(Response{
			Success: false,
			Error:   fmt.Sprintf("PR branch not found for PR #%d", prNum),
		})
		return
	}

	// Create task
	task = &Task{
		ID:        fmt.Sprintf("task-%d-%s", time.Now().UnixNano()%100000, branch),
		Repo:      repo,
		Branch:    branch,
		Task:      taskDesc,
		PR:        prNum,
		Status:    "queued",
		CreatedAt: time.Now(),
	}

	s.submitTask(task)
	log.Printf("[WEBHOOK] Created task %s for %s:%s", task.ID, repo, branch)

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  task.ID,
		Status:  "queued",
		Repo:    repo,
		Branch:  branch,
	})
}

func extractTask(body string) string {
	task := body
	task = strings.ReplaceAll(task, "@claude", "")
	task = strings.ReplaceAll(task, "/claude", "")
	task = strings.TrimSpace(task)

	lines := strings.Split(task, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, ">") && !strings.HasPrefix(line, "```") {
			cleaned = append(cleaned, line)
		}
	}

	return strings.Join(cleaned, "\n")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

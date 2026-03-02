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
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/sqlite"
)

var (
	port          = flag.Int("port", 3456, "HTTP server port")
	workDir       = flag.String("work-dir", "/tmp/claude-runner", "Base working directory")
	maxConcurrent = flag.Int("max-concurrent", 5, "Max concurrent tasks across all branches")
	githubToken   = flag.String("github-token", os.Getenv("GH_TOKEN"), "GitHub token for API calls")
	webhookURL    = flag.String("webhook-url", "", "URL to send status updates to")
)

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
	github        *GithubService
	repo          *RepoService
	code          *CodeService
	taskRepo      *TaskRepo
	webhookURL    string
	activeCount   int
	githubUserID  string // GitHub user ID of the bot
}

// ==================== Service ====================

func NewService(taskRepo *TaskRepo, dataDir string) *Service {
	gh := NewGithubService(*githubToken)
	// Get bot user ID from GitHub API
	githubUserID, err := gh.GetCurrentUserID()
	if err != nil {
		log.Printf("[WARN] Failed to get GitHub user ID: %v", err)
	} else {
		log.Printf("[INFO] GitHub bot user ID: %s", githubUserID)
	}

	repo := NewRepoService(*workDir, gh)
	code := NewCodeService()

	return &Service{
		tasks:         make(map[string]*Task),
		branchLocks:   make(map[string]*BranchLock),
		taskQueue:     make([]*Task, 0),
		workDir:       *workDir,
		dataDir:       dataDir,
		maxConcurrent: *maxConcurrent,
		github:        gh,
		repo:          repo,
		code:          code,
		taskRepo:      taskRepo,
		webhookURL:    *webhookURL,
		githubUserID:  githubUserID,
	}
}

func main() {
	flag.Parse()

	// Data directory for SQLite database
	dataDir := "/tmp/claude-data"

	// Initialize task repository
	taskRepo := NewTaskRepo(nil, dataDir)
	if err := taskRepo.InitDB(); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer taskRepo.Close()

	svc := NewService(taskRepo, dataDir)

	// Create base work directory
	if err := os.MkdirAll(svc.workDir, 0o755); err != nil {
		log.Fatalf("Failed to create work directory: %v", err)
	}

	// Restore pending tasks from database
	pendingTasks, err := taskRepo.LoadPendingTasks()
	if err != nil {
		log.Printf("[WARN] Failed to load pending tasks: %v", err)
	} else {
		for _, task := range pendingTasks {
			svc.restoreTask(task)
		}
	}

	// Initialize Claude settings
	if err := svc.code.InitSettings(); err != nil {
		log.Printf("[WARN] Failed to initialize Claude settings: %v", err)
	}

	// Configure git safe.directory to allow all directories
	cmd := exec.Command("git", "config", "--global", "--add", "safe.directory", "*")
	cmd.Run()

	// Configure git SSL (use system certs)
	cmd = exec.Command("git", "config", "--global", "http.sslCAInfo", "/etc/ssl/certs/ca-certificates.crt")
	cmd.Run()

	// Create HTTP server
	addr := fmt.Sprintf(":%d", *port)
	httpServer := NewHTTPServer(addr, svc)

	log.Printf("Starting Claude Code Runner on %s", addr)
	log.Printf("Work directory: %s", svc.workDir)
	log.Printf("Max concurrent: %d", svc.maxConcurrent)

	// Start task processor
	go svc.processQueue()

	if err := httpServer.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func (s *Service) submitTask(task *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[task.ID] = task

	// Save to database
	if s.taskRepo != nil {
		if err := s.taskRepo.SaveTask(task); err != nil {
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
	if s.taskRepo != nil {
		if err := s.taskRepo.SaveTask(task); err != nil {
			log.Printf("[WARN] Failed to save task status to database: %v", err)
		}
	}

	// Setup worktree using RepoService
	worktreePath, err := s.repo.SetupWorktree(task.Repo, task.Branch, task.PR)
	if err != nil {
		log.Printf("[EXEC] ERROR: failed to setup worktree: %v", err)
		task.Status = "failed"
		task.Error = "failed to setup worktree: " + err.Error()
		task.EndedAt = time.Now()
		s.completeTask(task, bl, lockKey)
		return
	}
	task.WorkTree = worktreePath

	// Run Claude Code using CodeService
	result, err := s.code.Run(worktreePath, task.Task)
	task.Result = result
	task.EndedAt = time.Now()

	if err != nil {
		log.Printf("[EXEC] Claude command error: %v", err)
		task.Status = "failed"
		task.Error = fmt.Sprintf("claude error: %v\n%s", err, result)
		// Notify GitHub of failure
		if task.PR > 0 {
			s.notifyGitHub(task, false)
		}
	} else {
		task.Status = "completed"
		// Push changes using RepoService
		s.pushChanges(task)
		// Notify GitHub of success
		if task.PR > 0 {
			s.notifyGitHub(task, true)
		}
	}

	s.completeTask(task, bl, lockKey)
}

// createPRIfNeeded creates a PR if it doesn't exist for issue-based branches
func (s *Service) createPRIfNeeded(task *Task) {
	s.repo.CreatePRIfNeeded(task)
}

// getPRBranch gets the actual branch name for a PR using GitHub API
func (s *Service) getPRBranch(repo string, prNum int) string {
	branch, err := s.github.GetPRBranch(repo, prNum)
	if err != nil {
		log.Printf("[WEBHOOK] Failed to get PR #%d branch: %v", prNum, err)
		return ""
	}
	if branch != "" {
		log.Printf("[WEBHOOK] Got PR #%d branch: %s", prNum, branch)
	}
	return branch
}

func (s *Service) pushChanges(task *Task) {
	if task.WorkTree == "" || task.Status != "completed" {
		return
	}

	// Check if there are changes using RepoService
	hasChanges, err := s.repo.HasChanges(task.WorkTree)
	if err != nil || !hasChanges {
		log.Printf("[PUSH] No changes to push for task %s", task.ID)
		return
	}

	log.Printf("[PUSH] Changes detected, committing and pushing for task %s", task.ID)

	// Commit and push using RepoService
	commitMsg := fmt.Sprintf("Claude: %s", truncate(task.Task, 50))
	if err := s.repo.CommitAndPush(task.WorkTree, task.Branch, commitMsg); err != nil {
		task.Error += fmt.Sprintf("\nPush failed: %v", err)
		task.Status = "failed"
	} else {
		// Create PR if it doesn't exist (for issue-based branches)
		if strings.HasPrefix(task.Branch, "fix-issue-") {
			s.createPRIfNeeded(task)
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
			comment = fmt.Sprintf("**Task Completed**\n\nBranch `%s` has been updated and pushed.\n\n**Result:**\n%s\n\nDuration: %v",
				task.Branch, resultSummary, duration)
		} else {
			comment = fmt.Sprintf("**Task Completed**\n\nBranch `%s` has been updated and pushed.\n\nDuration: %v",
				task.Branch, duration)
		}
	} else {
		comment = fmt.Sprintf("**Task Failed**\n\nBranch `%s`\n\nError: %s\n\nTask: %s",
			task.Branch, truncate(task.Error, 500), truncate(task.Task, 200))
	}

	// Determine where to comment: PR or Issue
	// - If PR > 0, comment on PR
	// - If branch starts with "fix-issue-" and PR == 0, comment on Issue
	if task.PR > 0 {
		// Comment on PR
		if err := s.github.AddPRComment(owner, repo, task.PR, comment); err != nil {
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
	if s.taskRepo != nil {
		if err := s.taskRepo.SaveTask(task); err != nil {
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

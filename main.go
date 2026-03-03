// Claude Code Runner Service - Worktree Edition
// Features:
// - Uses git worktree for each branch (isolated, no conflicts)
// - Worktrees stored in /tmp
// - One task per branch at a time (branch-level locking)
// - Concurrent execution across different branches/repos
//
// Compile: go build -o gh-claude
// Run: ./gh-claude [-port=3456] [-work-dir=/tmp/claude-runner] [-max-concurrent=5]

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
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
	dataDir       = flag.String("data-dir", "/tmp/claude-data", "Data directory for database")
)

// Request represents a task request
type Request struct {
	Repo      string `json:"repo"`
	Task      string `json:"task"`
	Branch    string `json:"branch,omitempty"`
	PR        int    `json:"pr,omitempty"`
	Debug     bool   `json:"debug,omitempty"`
	SkipBuild bool   `json:"skip_build,omitempty"`
}

// Response represents a response
type Response struct {
	Success  bool          `json:"success"`
	TaskID   string        `json:"task_id,omitempty"`
	Status   string        `json:"status,omitempty"`
	Repo     string        `json:"repo,omitempty"`
	Branch   string        `json:"branch,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
}

// Task represents a task
type Task struct {
	ID         string    `json:"id"`
	Repo       string    `json:"repo"`
	Branch     string    `json:"branch"`
	Task       string    `json:"task"`
	PR         int       `json:"pr"`
	Debug      bool      `json:"debug"`
	SkipBuild  bool      `json:"skip_build"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	Result     string    `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
	WorkTree   string    `json:"worktree,omitempty"`
	ReactionID int       `json:"reaction_id,omitempty"`
	CommentID  int       `json:"comment_id,omitempty"`
}

// BranchLock represents a branch-level lock
type BranchLock struct {
	mu      sync.Mutex
	queue   []*Task
	running *Task
}

// Service is the main service struct
type Service struct {
	mu            sync.Mutex
	tasks         map[string]*Task
	branchLocks   map[string]*BranchLock
	taskQueue     []*Task
	workDir       string
	dataDir       string
	maxConcurrent int

	// Services
	github      *GithubService
	repoService *RepoService
	codeService *CodeService
	taskRepo    *TaskRepo

	webhookURL   string
	activeCount  int
	githubUserID string
}

// NewService creates a new Service
func NewService(workDir, dataDir string, maxConcurrent int, ghToken, webhookURL string) (*Service, error) {
	// Initialize GitHub service
	github := NewGithubService(ghToken)

	// Get bot user ID
	githubUserID, err := github.GetCurrentUserID()
	if err != nil {
		log.Printf("[WARN] Failed to get GitHub user ID: %v", err)
	} else {
		log.Printf("[INFO] GitHub bot user ID: %s", githubUserID)
	}

	// Initialize repository service
	repoService := NewRepoService(workDir, ghToken)

	// Initialize code service
	codeService := NewCodeService(false)

	// Initialize task repository
	taskRepo, err := NewTaskRepo(dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize task repository: %w", err)
	}

	return &Service{
		tasks:         make(map[string]*Task),
		branchLocks:   make(map[string]*BranchLock),
		taskQueue:     make([]*Task, 0),
		workDir:       workDir,
		dataDir:       dataDir,
		maxConcurrent: maxConcurrent,
		github:        github,
		repoService:   repoService,
		codeService:   codeService,
		taskRepo:      taskRepo,
		webhookURL:    webhookURL,
		githubUserID:  githubUserID,
	}, nil
}

func main() {
	flag.Parse()

	// Create service
	svc, err := NewService(*workDir, *dataDir, *maxConcurrent, *githubToken, *webhookURL)
	if err != nil {
		log.Fatalf("Failed to create service: %v", err)
	}
	defer svc.taskRepo.Close()

	// Create base work directory
	if err := os.MkdirAll(svc.workDir, 0o755); err != nil {
		log.Fatalf("Failed to create work directory: %v", err)
	}

	// Restore pending tasks
	pendingTasks, err := svc.taskRepo.LoadPending()
	if err != nil {
		log.Printf("[WARN] Failed to load pending tasks: %v", err)
	} else {
		for _, task := range pendingTasks {
			svc.restoreTask(task)
		}
	}

	// Initialize Claude settings
	svc.codeService.EnsureClaudeSettings()

	// Register HTTP handlers
	httpServer := NewHttpServer(svc)
	httpServer.RegisterHandlers()

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

func (s *Service) submitTask(task *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[task.ID] = task

	// Save to database
	if s.taskRepo != nil {
		if err := s.taskRepo.Save(task); err != nil {
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
		bl.queue = append(bl.queue, task)
		log.Printf("[QUEUE] Task %s added to queue for branch %s", task.ID, lockKey)
	} else {
		s.taskQueue = append(s.taskQueue, task)
		log.Printf("[QUEUE] Task %s submitted for branch %s", task.ID, lockKey)
	}
}

func (s *Service) restoreTask(task *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[task.ID] = task

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

	task.Status = "queued"

	if bl.running != nil {
		bl.queue = append(bl.queue, task)
		log.Printf("[RESTORE] Task %s restored to queue for branch %s", task.ID, lockKey)
	} else {
		s.taskQueue = append(s.taskQueue, task)
		log.Printf("[RESTORE] Task %s restored for branch %s", task.ID, lockKey)
	}
}

func (s *Service) processQueue() {
	for {
		time.Sleep(500 * time.Millisecond)

		s.mu.Lock()

		if s.activeCount >= s.maxConcurrent || len(s.taskQueue) == 0 {
			s.mu.Unlock()
			continue
		}

		task := s.taskQueue[0]
		s.taskQueue = s.taskQueue[1:]
		s.activeCount++

		lockKey := fmt.Sprintf("%s:%s", task.Repo, task.Branch)
		bl := s.branchLocks[lockKey]
		bl.running = task

		s.mu.Unlock()

		go s.executeTask(task, bl, lockKey)
	}
}

func (s *Service) executeTask(task *Task, bl *BranchLock, lockKey string) {
	log.Printf("[EXEC] Starting task %s for repo=%s branch=%s PR=%d", task.ID, task.Repo, task.Branch, task.PR)

	// Fetch previous task context (only for PR-triggered tasks, not for issue-triggered tasks)
	var prevTaskContext string
	if task.PR > 0 && task.Repo != "" && task.Branch != "" {
		if prevTask, err := s.taskRepo.GetLatestByPR(task.Repo, task.PR); err == nil && prevTask != nil && prevTask.ID != task.ID {
			prevTaskContext = buildPreviousTaskContext(prevTask)
		} else if prevTask, err := s.taskRepo.GetLatestByBranch(task.Repo, task.Branch); err == nil && prevTask != nil && prevTask.ID != task.ID {
			prevTaskContext = buildPreviousTaskContext(prevTask)
		}
	}

	if prevTaskContext != "" {
		task.Task = prevTaskContext + task.Task
	}

	task.Status = "running"
	task.StartedAt = time.Now()

	// Save task status
	if s.taskRepo != nil {
		s.taskRepo.Save(task)
	}

	// Setup worktree
	worktreePath, err := s.repoService.SetupWorktree(task.Repo, task.Branch, task.PR)
	if err != nil {
		log.Printf("[EXEC] ERROR: failed to setup worktree: %v", err)
		task.Status = "failed"
		task.Error = "failed to setup worktree: " + err.Error()
		task.EndedAt = time.Now()
		s.notifyGitHub(task, false)
		s.completeTask(task, bl, lockKey)
		return
	}
	task.WorkTree = worktreePath

	// Sync worktree
	if err := s.repoService.SyncWorktree(worktreePath); err != nil {
		log.Printf("[EXEC] ERROR: failed to sync worktree: %v", err)
		task.Status = "failed"
		task.Error = "failed to sync worktree: " + err.Error()
		task.EndedAt = time.Now()
		s.notifyGitHub(task, false)
		s.completeTask(task, bl, lockKey)
		return
	}

	// Update Claude settings
	s.codeService.UpdateSettingsForWorktree(worktreePath)

	// Run build step
	if !task.SkipBuild {
		_, err := s.codeService.RunBuildStep(worktreePath)
		if err != nil {
			log.Printf("[EXEC] Build step failed: %v", err)
			task.Status = "failed"
			task.Error = fmt.Sprintf("Build step failed: %v", err)
			task.EndedAt = time.Now()
			s.notifyGitHub(task, false)
			s.completeTask(task, bl, lockKey)
			return
		}
	}

	// Run actual task
	result, err := s.codeService.RunTask(worktreePath, task.Task)
	task.Result = result
	task.EndedAt = time.Now()

	if err != nil {
		log.Printf("[EXEC] Claude error: %v", err)
		task.Status = "failed"
		task.Error = fmt.Sprintf("claude error: %v\n%s", err, result)
	} else {
		task.Status = "completed"
		// Push changes
		if err := s.pushChanges(task); err != nil {
			task.Status = "failed"
			task.Error += "\nPush failed: " + err.Error()
		}
	}

	s.notifyGitHub(task, task.Status == "completed")
	s.completeTask(task, bl, lockKey)
}

func (s *Service) pushChanges(task *Task) error {
	if task.WorkTree == "" || task.Status != "completed" {
		return nil
	}

	hasChanges, err := s.repoService.HasChanges(task.WorkTree)
	if err != nil || !hasChanges {
		log.Printf("[PUSH] No changes to push for task %s", task.ID)
		return nil
	}

	log.Printf("[PUSH] Changes detected, committing and pushing for task %s", task.ID)

	commitMsg := fmt.Sprintf("Claude: %s", truncate(task.Task, 50))
	if err := s.repoService.CommitAndPush(task.WorkTree, task.Branch, commitMsg); err != nil {
		return err
	}

	// Create PR if needed
	if strings.HasPrefix(task.Branch, "fix-issue-") && task.PR == 0 {
		prNum := s.createPRIfNeeded(task)
		if prNum > 0 {
			task.PR = prNum
		}
	}

	return nil
}

func (s *Service) createPRIfNeeded(task *Task) int {
	issueNum := 0
	if _, err := fmt.Sscanf(task.Branch, "fix-issue-%d", &issueNum); err != nil {
		return 0
	}

	exists, err := s.github.CheckPRExists(task.Repo, issueNum)
	if err != nil || exists {
		return issueNum
	}

	log.Printf("[PR] Creating PR for issue #%d from branch %s", issueNum, task.Branch)

	title := fmt.Sprintf("Fix issue #%d", issueNum)
	body := fmt.Sprintf("This PR fixes issue #%d\n\nTask: %s", issueNum, truncate(task.Task, 200))

	prNum, err := s.github.CreatePR(task.Repo, title, body, "main", task.Branch)
	if err != nil {
		log.Printf("[PR] Failed to create PR: %v", err)
		return 0
	}

	return prNum
}

func (s *Service) notifyGitHub(task *Task, success bool) {
	if s.github == nil {
		return
	}

	parts := strings.Split(task.Repo, "/")
	if len(parts) != 2 {
		return
	}
	owner, repo := parts[0], parts[1]

	var comment string
	taskIDFooter := fmt.Sprintf("---\nTask ID: %s\nUse @claude to continue this task", task.ID)
	if success {
		duration := task.EndedAt.Sub(task.StartedAt)
		resultSummary := truncate(task.Result, 1000)
		if resultSummary != "" {
			comment = fmt.Sprintf("✅ **Task Completed**\n\nBranch `%s` has been updated and pushed.\n\n**Result:**\n%s\n\nDuration: %v\n\n%s",
				task.Branch, resultSummary, duration, taskIDFooter)
		} else {
			comment = fmt.Sprintf("✅ **Task Completed**\n\nBranch `%s` has been updated and pushed.\n\nDuration: %v\n\n%s",
				task.Branch, duration, taskIDFooter)
		}
	} else {
		comment = fmt.Sprintf("❌ **Task Failed**\n\nBranch `%s`\n\nError: %s\n\nTask: %s\n\n%s",
			task.Branch, truncate(task.Error, 500), truncate(task.Task, 200), taskIDFooter)
	}

	if task.PR > 0 {
		if err := s.github.AddPRComment(owner, repo, task.PR, comment); err != nil {
			log.Printf("[GITHUB] Failed to add comment to PR #%d: %v", task.PR, err)
		}
	} else if strings.HasPrefix(task.Branch, "fix-issue-") {
		issueNumStr := strings.TrimPrefix(task.Branch, "fix-issue-")
		issueNum, err := strconv.Atoi(issueNumStr)
		if err != nil {
			return
		}
		if err := s.github.AddIssueComment(owner, repo, issueNum, comment); err != nil {
			log.Printf("[GITHUB] Failed to add comment to Issue #%d: %v", issueNum, err)
		}
	}
}

func (s *Service) completeTask(task *Task, bl *BranchLock, lockKey string) {
	s.mu.Lock()
	s.activeCount--

	bl.mu.Lock()
	bl.running = nil

	if len(bl.queue) > 0 {
		nextTask := bl.queue[0]
		bl.queue = bl.queue[1:]
		bl.mu.Unlock()

		s.taskQueue = append(s.taskQueue, nextTask)
		log.Printf("[QUEUE] Started next task %s for branch %s", nextTask.ID, lockKey)
	} else {
		bl.mu.Unlock()
	}

	s.mu.Unlock()

	// Save final task status
	if s.taskRepo != nil {
		s.taskRepo.Save(task)
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

func buildPreviousTaskContext(prevTask *Task) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Previous Task (Branch: %s, Status: %s):\n", prevTask.Branch, prevTask.Status))
	b.WriteString(fmt.Sprintf("Original Task: %s\n\n", truncate(prevTask.Task, 500)))

	if prevTask.Result != "" {
		b.WriteString(fmt.Sprintf("Result: %s\n\n", truncate(prevTask.Result, 500)))
	} else if prevTask.Error != "" {
		b.WriteString(fmt.Sprintf("Error: %s\n\n", truncate(prevTask.Error, 500)))
	}

	b.WriteString("---\n\n")
	return b.String()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

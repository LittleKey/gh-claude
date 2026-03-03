// Package server provides HTTP server and handlers
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Request represents a task submission request
type Request struct {
	Repo      string `json:"repo"`               // owner/repo
	Task      string `json:"task"`               // Task for Claude Code
	Branch    string `json:"branch,omitempty"`   // Branch to work on (optional, will create new if not provided)
	PR        int    `json:"pr,omitempty"`       // PR number (optional, will checkout PR branch)
	Debug     bool   `json:"debug,omitempty"`    // Enable debug output
	SkipBuild bool   `json:"skip_build,omitempty"` // Skip build step (default: false)
}

// Response represents an API response
type Response struct {
	Success  bool          `json:"success"`
	TaskID   string        `json:"task_id,omitempty"`
	Status   string        `json:"status,omitempty"` // queued, running, completed, failed
	Repo     string        `json:"repo,omitempty"`
	Branch   string        `json:"branch,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
}

// BranchLock represents a branch-level lock
type BranchLock struct {
	mu      sync.Mutex
	queue   []*Task
	running *Task
}

// Server provides HTTP server functionality
type Server struct {
	mu            sync.Mutex
	tasks         map[string]*Task       // taskID -> Task
	branchLocks   map[string]*BranchLock // "repo:branch" -> BranchLock
	taskQueue     []*Task                 // Global queue for unassigned tasks
	workDir       string
	dataDir       string
	maxConcurrent int
	github        *GitHubService
	repoService   *RepoService
	codeService   *CodeService
	taskRepo      *TaskRepo
	webhookURL    string
	activeCount   int
}

// NewServer creates a new HTTP server
func NewServer(workDir, dataDir string, maxConcurrent int, githubToken, webhookURL string) (*Server, error) {
	gh := NewGitHub(githubToken)
	rs := NewRepoService(workDir, githubToken)
	cs := NewCodeService()
	tr, err := NewTaskRepo(dataDir)
	if err != nil {
		return nil, err
	}

	// Get bot user ID from GitHub API
	githubUserID, err := gh.GetCurrentUserID()
	if err != nil {
		log.Printf("[WARN] Failed to get GitHub user ID: %v", err)
	} else {
		log.Printf("[INFO] GitHub bot user ID: %s", githubUserID)
	}

	return &Server{
		tasks:         make(map[string]*Task),
		branchLocks:   make(map[string]*BranchLock),
		taskQueue:     make([]*Task, 0),
		workDir:       workDir,
		dataDir:       dataDir,
		maxConcurrent: maxConcurrent,
		github:        gh,
		repoService:   rs,
		codeService:   cs,
		taskRepo:      tr,
		webhookURL:    webhookURL,
	}, nil
}

// Close closes the server and its resources
func (s *Server) Close() error {
	return s.taskRepo.Close()
}

// RestoreTasks restores pending tasks from database
func (s *Server) RestoreTasks() error {
	pendingTasks, err := s.taskRepo.LoadPending()
	if err != nil {
		return err
	}
	for _, t := range pendingTasks {
		s.restoreTask(t)
	}
	return nil
}

// SetupHandlers sets up HTTP handlers
func (s *Server) SetupHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/run", s.handleRun)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/queue", s.handleQueue)
	mux.HandleFunc("/cancel", s.handleCancel)
	mux.HandleFunc("/webhook", s.handleWebhook)
	mux.HandleFunc("/health", s.handleHealth)
}

// ProcessQueue starts the task queue processor
func (s *Server) ProcessQueue() {
	go s.processQueue()
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
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
		branch = fmt.Sprintf("pr-%d", req.PR)
	} else if branch == "" {
		branch = fmt.Sprintf("claude-%d", time.Now().Unix())
	}

	// Create task
	t := &Task{
		ID:        fmt.Sprintf("task-%d-%s", time.Now().UnixNano()%100000, branch),
		Repo:      req.Repo,
		Branch:    branch,
		Task:      req.Task,
		PR:        req.PR,
		Debug:     req.Debug,
		SkipBuild: req.SkipBuild,
		Status:    "queued",
		CreatedAt: time.Now(),
	}

	// Submit task
	s.submitTask(t)

	log.Printf("[RUN] Task %s: repo=%s, branch=%s, task=%s", t.ID, req.Repo, branch, truncate(req.Task, 50))

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  t.ID,
		Status:  "queued",
		Repo:    req.Repo,
		Branch:  branch,
	})
}

func (s *Server) submitTask(t *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[t.ID] = t

	// Save to database
	if s.taskRepo != nil {
		if err := s.taskRepo.Save(t); err != nil {
			log.Printf("[WARN] Failed to save task to database: %v", err)
		}
	}

	// Get or create branch lock
	lockKey := fmt.Sprintf("%s:%s", t.Repo, t.Branch)
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
		bl.queue = append(bl.queue, t)
		log.Printf("[QUEUE] Task %s added to queue for branch %s:%s", t.ID, t.Repo, t.Branch)
	} else {
		// Can run immediately
		s.taskQueue = append(s.taskQueue, t)
		log.Printf("[QUEUE] Task %s submitted for branch %s:%s", t.ID, t.Repo, t.Branch)
	}
}

func (s *Server) restoreTask(t *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[t.ID] = t

	// Get or create branch lock
	lockKey := fmt.Sprintf("%s:%s", t.Repo, t.Branch)
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
	t.Status = "queued"

	if bl.running != nil {
		bl.queue = append(bl.queue, t)
		log.Printf("[RESTORE] Task %s restored to queue for branch %s:%s", t.ID, t.Repo, t.Branch)
	} else {
		s.taskQueue = append(s.taskQueue, t)
		log.Printf("[RESTORE] Task %s restored for branch %s:%s", t.ID, t.Repo, t.Branch)
	}
}

func (s *Server) processQueue() {
	for {
		time.Sleep(500 * time.Millisecond)

		s.mu.Lock()

		// Check if we can run more tasks
		if s.activeCount >= s.maxConcurrent || len(s.taskQueue) == 0 {
			s.mu.Unlock()
			continue
		}

		// Get next task
		t := s.taskQueue[0]
		s.taskQueue = s.taskQueue[1:]
		s.activeCount++

		// Mark branch as running
		lockKey := fmt.Sprintf("%s:%s", t.Repo, t.Branch)
		bl := s.branchLocks[lockKey]
		bl.running = t

		s.mu.Unlock()

		// Run task in goroutine
		go s.executeTask(t, bl, lockKey)
	}
}

func (s *Server) executeTask(t *Task, bl *BranchLock, lockKey string) {
	log.Printf("[EXEC] Starting task %s for repo=%s branch=%s", t.ID, t.Repo, t.Branch)

	t.Status = "running"
	t.StartedAt = time.Now()

	// Save task status to database
	if s.taskRepo != nil {
		if err := s.taskRepo.Save(t); err != nil {
			log.Printf("[WARN] Failed to save task status to database: %v", err)
		}
	}

	// Create/ensure worktree
	worktreePath, err := s.repoService.EnsureWorktree(t.Repo, t.Branch, t.PR)
	if err != nil {
		log.Printf("[EXEC] ERROR: failed to setup worktree: %v", err)
		t.Status = "failed"
		t.Error = "failed to setup worktree: " + err.Error()
		t.EndedAt = time.Now()
		s.notifyGitHub(t, false)
		s.completeTask(t, bl, lockKey)
		return
	}
	t.WorkTree = worktreePath

	// Update Claude settings for worktree
	if err := s.codeService.UpdateSettingsForWorktree(worktreePath); err != nil {
		log.Printf("[WARN] Failed to update settings for worktree: %v", err)
	}

	// Build step
	if !t.SkipBuild {
		log.Printf("[EXEC] Running build step for task %s", t.ID)
		buildResult := s.codeService.RunBuild(worktreePath)

		if buildResult.Error != nil {
			log.Printf("[EXEC] Build step failed: %v", buildResult.Error)
			t.Status = "failed"
			t.Error = fmt.Sprintf("Build step failed: %v\n\nOutput:\n%s", buildResult.Error, buildResult.Output)
			t.EndedAt = time.Now()
			s.notifyGitHub(t, false)
			s.completeTask(t, bl, lockKey)
			return
		}
		log.Printf("[EXEC] Build step completed successfully")
	}

	// Run actual task
	log.Printf("[EXEC] Running actual task: %s", truncate(t.Task, 100))
	result := s.codeService.Run(worktreePath, t.Task)

	if len(result.Output) > 0 {
		log.Printf("[EXEC] Claude output: %s", truncate(result.Output, 500))
		t.Result = result.Output
	}

	t.EndedAt = time.Now()

	if result.Error != nil {
		log.Printf("[EXEC] Claude command error: %v", result.Error)
		t.Status = "failed"
		t.Error = fmt.Sprintf("claude error: %v\n%s", result.Error, result.Output)
	} else {
		t.Status = "completed"
		// Push changes
		s.pushChanges(t)
	}

	// Always notify GitHub
	s.notifyGitHub(t, t.Status == "completed")

	s.completeTask(t, bl, lockKey)
}

func (s *Server) pushChanges(t *Task) {
	if t.WorkTree == "" || t.Status != "completed" {
		return
	}

	commitMsg := fmt.Sprintf("Claude: %s", truncate(t.Task, 50))
	err := s.repoService.PushChanges(t.WorkTree, t.Branch, commitMsg)
	if err != nil {
		t.Error += fmt.Sprintf("\nPush failed: %v", err)
		t.Status = "failed"
		return
	}

	log.Printf("[PUSH] Successfully pushed branch %s for task %s", t.Branch, t.ID)

	// Create PR if needed (for issue-based branches)
	if strings.HasPrefix(t.Branch, "fix-issue-") && t.PR == 0 {
		prNum := s.createPRIfNeeded(t)
		if prNum > 0 {
			t.PR = prNum
		}
	}
}

func (s *Server) createPRIfNeeded(t *Task) int {
	// Extract issue number from branch name
	issueNum := 0
	if _, err := fmt.Sscanf(t.Branch, "fix-issue-%d", &issueNum); err != nil {
		log.Printf("[PR] Failed to extract issue number from branch %s: %v", t.Branch, err)
		return 0
	}

	// Check if PR already exists
	exists, _ := s.github.CheckPRExists(t.Repo, issueNum)
	if exists {
		log.Printf("[PR] PR for issue #%d already exists", issueNum)
		return issueNum
	}

	// Create PR
	log.Printf("[PR] Creating PR for issue #%d from branch %s", issueNum, t.Branch)
	title := fmt.Sprintf("Fix issue #%d", issueNum)
	body := fmt.Sprintf("This PR fixes issue #%d\n\nTask: %s", issueNum, truncate(t.Task, 200))

	prNum, err := s.github.CreatePR(t.Repo, t.Branch, title, body, "main")
	if err != nil {
		log.Printf("[PR] Failed to create PR: %v", err)
		return 0
	}

	log.Printf("[PR] Successfully created PR for branch %s", t.Branch)
	return prNum
}

func (s *Server) notifyGitHub(t *Task, success bool) {
	if s.github == nil {
		return
	}

	parts := strings.Split(t.Repo, "/")
	if len(parts) != 2 {
		return
	}
	owner, repo := parts[0], parts[1]

	// Build comment message
	var comment string
	taskIDFooter := fmt.Sprintf("---\nTask ID: %s\nUse @claude to continue this task", t.ID)
	if success {
		duration := t.EndedAt.Sub(t.StartedAt)
		resultSummary := truncate(t.Result, 1000)
		if resultSummary != "" {
			comment = fmt.Sprintf("✅ **Task Completed**\n\nBranch `%s` has been updated and pushed.\n\n**Result:**\n%s\n\nDuration: %v\n\n%s",
				t.Branch, resultSummary, duration, taskIDFooter)
		} else {
			comment = fmt.Sprintf("✅ **Task Completed**\n\nBranch `%s` has been updated and pushed.\n\nDuration: %v\n\n%s",
				t.Branch, duration, taskIDFooter)
		}
	} else {
		comment = fmt.Sprintf("❌ **Task Failed**\n\nBranch `%s`\n\nError: %s\n\nTask: %s\n\n%s",
			t.Branch, truncate(t.Error, 500), truncate(t.Task, 200), taskIDFooter)
	}

	// Determine where to comment: PR or Issue
	if t.PR > 0 {
		if err := s.github.AddPRComment(owner, repo, t.PR, comment); err != nil {
			log.Printf("[GITHUB] Failed to add comment to PR #%d: %v", t.PR, err)
		} else {
			log.Printf("[GITHUB] Added completion comment to PR #%d", t.PR)
		}
	} else if strings.HasPrefix(t.Branch, "fix-issue-") {
		issueNumStr := strings.TrimPrefix(t.Branch, "fix-issue-")
		issueNum, err := strconv.Atoi(issueNumStr)
		if err != nil {
			log.Printf("[GITHUB] Failed to parse issue number from branch %s: %v", t.Branch, err)
			return
		}
		if err := s.github.AddIssueComment(owner, repo, issueNum, comment); err != nil {
			log.Printf("[GITHUB] Failed to add comment to Issue #%d: %v", issueNum, err)
		} else {
			log.Printf("[GITHUB] Added completion comment to Issue #%d", issueNum)
		}
	}
}

func (s *Server) completeTask(t *Task, bl *BranchLock, lockKey string) {
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
		if err := s.taskRepo.Save(t); err != nil {
			log.Printf("[WARN] Failed to save task completion to database: %v", err)
		}
	}

	duration := t.EndedAt.Sub(t.StartedAt)
	log.Printf("[DONE] Task %s: %s (duration: %v)", t.ID, t.Status, duration)

	// Send webhook if configured
	if s.webhookURL != "" {
		s.sendWebhook(t)
	}
}

func (s *Server) sendWebhook(t *Task) {
	payload := map[string]interface{}{
		"task_id":  t.ID,
		"repo":     t.Repo,
		"branch":   t.Branch,
		"status":   t.Status,
		"pr":       t.PR,
		"result":   t.Result,
		"error":    t.Error,
		"duration": t.EndedAt.Sub(t.StartedAt).String(),
	}

	body, _ := json.Marshal(payload)
	_ = body

	resp, err := http.Post(s.webhookURL, "application/json", nil)
	if err == nil {
		resp.Body.Close()
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	t, exists := s.tasks[taskID]
	s.mu.Unlock()

	if !exists {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(t)
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	t, exists := s.tasks[taskID]
	if !exists {
		s.mu.Unlock()
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	if t.Status == "running" {
		s.mu.Unlock()
		http.Error(w, "cannot cancel running task", http.StatusBadRequest)
		return
	}

	t.Status = "cancelled"
	delete(s.tasks, taskID)
	s.mu.Unlock()

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  taskID,
		Status:  "cancelled",
	})
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
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

	var t *Task
	var repo, branch, taskDesc string
	var prNum int

	switch eventType {
	case "issue_comment":
		t, repo, branch, prNum, taskDesc = s.handleIssueComment(payload)
	case "pull_request_review":
		t, repo, branch, prNum, taskDesc = s.handlePullRequestReview(payload)
	case "pull_request_review_comment":
		t, repo, branch, prNum, taskDesc = s.handlePullRequestReviewComment(payload)
	}

	if repo == "" || taskDesc == "" {
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	// Expand @filepath references if worktree exists
	worktreePath := s.repoService.WorktreePath(repo, branch)
	if _, err := os.Stat(worktreePath); err == nil {
		taskDesc = expandAtReferences(taskDesc, worktreePath)
		log.Printf("[WEBHOOK] Expanded @references in worktree %s", worktreePath)
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
	t = &Task{
		ID:        fmt.Sprintf("task-%d-%s", time.Now().UnixNano()%100000, branch),
		Repo:      repo,
		Branch:    branch,
		Task:      taskDesc,
		PR:        prNum,
		Status:    "queued",
		CreatedAt: time.Now(),
	}

	s.submitTask(t)
	log.Printf("[WEBHOOK] Created task %s for %s:%s", t.ID, repo, branch)

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  t.ID,
		Status:  "queued",
		Repo:    repo,
		Branch:  branch,
	})
}

func (s *Server) handleIssueComment(payload map[string]interface{}) (*Task, string, string, int, string) {
	// Check action - only process new comments
	if action, ok := payload["action"].(string); ok && action != "created" {
		return nil, "", "", 0, ""
	}

	// Get task description from comment
	var taskDesc string
	var repo, branch string
	var prNum int

	if comment, ok := payload["comment"].(map[string]interface{}); ok {
		if body, ok := comment["body"].(string); ok {
			// Check for @claude command
			if strings.Contains(body, "@claude") || strings.HasPrefix(strings.TrimSpace(body), "/claude") {
				// Get issue/PR context
				var issueTitle, issueBody string
				if issue, ok := payload["issue"].(map[string]interface{}); ok {
					if n, ok := issue["number"].(float64); ok {
						if pr, ok := issue["pull_request"].(map[string]interface{}); pr != nil {
							prNum = int(n)
							if r, ok := payload["repository"].(map[string]interface{}); ok {
								if fullName, ok := r["full_name"].(string); ok {
									branch, _ = s.github.GetPRBranch(fullName, prNum)
								}
							}
						} else {
							prNum = 0
							branch = fmt.Sprintf("fix-issue-%d", int(n))
						}
					}
					if title, ok := issue["title"].(string); ok {
						issueTitle = title
					}
					if b, ok := issue["body"].(string); ok {
						issueBody = b
					}
				}

				taskCmd := extractTask(body)

				// Build task description with context
				var context strings.Builder
				if issueTitle != "" {
					context.WriteString(fmt.Sprintf("Issue/PR Title: %s\n\n", issueTitle))
				}
				if issueBody != "" {
					context.WriteString(fmt.Sprintf("Issue/PR Description:\n%s\n\n", issueBody))
				}
				context.WriteString(fmt.Sprintf("User Command:\n%s", taskCmd))
				taskDesc = context.String()

				// Get repository
				if r, ok := payload["repository"].(map[string]interface{}); ok {
					if fullName, ok := r["full_name"].(string); ok {
						repo = fullName
					}
				}
			}
		}
	}

	return nil, repo, branch, prNum, taskDesc
}

func (s *Server) handlePullRequestReview(payload map[string]interface{}) (*Task, string, string, int, string) {
	if action, ok := payload["action"].(string); ok && action != "submitted" {
		return nil, "", "", 0, ""
	}

	var repo, branch, taskDesc string
	var prNum int
	var prTitle, prBody string

	if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
		if n, ok := pr["number"].(float64); ok {
			prNum = int(n)
		}
		if r, ok := pr["base"].(map[string]interface{})["repo"].(map[string]interface{}); ok {
			if fullName, ok := r["full_name"].(string); ok {
				repo = fullName
			}
		}
		if repo != "" && prNum > 0 {
			branch, _ = s.github.GetPRBranch(repo, prNum)
		}
		if title, ok := pr["title"].(string); ok {
			prTitle = title
		}
		if b, ok := pr["body"].(string); ok {
			prBody = b
		}
	}

	if review, ok := payload["review"].(map[string]interface{}); ok {
		if body, ok := review["body"].(string); ok {
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

	return nil, repo, branch, prNum, taskDesc
}

func (s *Server) handlePullRequestReviewComment(payload map[string]interface{}) (*Task, string, string, int, string) {
	if action, ok := payload["action"].(string); ok && action != "created" {
		return nil, "", "", 0, ""
	}

	var repo, branch, taskDesc string
	var prNum int
	var prTitle, prBody string

	if comment, ok := payload["comment"].(map[string]interface{}); ok {
		if body, ok := comment["body"].(string); ok {
			if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
				if n, ok := pr["number"].(float64); ok {
					prNum = int(n)
				}
				if r, ok := pr["base"].(map[string]interface{})["repo"].(map[string]interface{}); ok {
					if fullName, ok := r["full_name"].(string); ok {
						repo = fullName
					}
				}
				if repo != "" && prNum > 0 {
					branch, _ = s.github.GetPRBranch(repo, prNum)
				}
				if title, ok := pr["title"].(string); ok {
					prTitle = title
				}
				if b, ok := pr["body"].(string); ok {
					prBody = b
				}
			}

			var context strings.Builder
			if prTitle != "" {
				context.WriteString(fmt.Sprintf("PR Title: %s\n\n", prTitle))
			}
			if prBody != "" {
				context.WriteString(fmt.Sprintf("PR Description:\n%s\n\n", prBody))
			}
			context.WriteString(fmt.Sprintf("Comment:\n%s", body))
			taskDesc = context.String()
		}
	}

	return nil, repo, branch, prNum, taskDesc
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

// expandAtReferences expands @filename references in task description
func expandAtReferences(taskDesc, worktreePath string) string {
	re := regexp.MustCompile(`@([^\s@]+)`)

	absWorktreePath, err := filepath.Abs(worktreePath)
	if err != nil {
		log.Printf("[EXPAND] Failed to get absolute path of worktree: %v", err)
		return taskDesc
	}

	return re.ReplaceAllStringFunc(taskDesc, func(match string) string {
		relPath := strings.TrimPrefix(match, "@")

		if filepath.IsAbs(relPath) || strings.Contains(relPath, "\x00") {
			log.Printf("[EXPAND] Rejected absolute path or invalid path: %s", relPath)
			return match
		}

		fullPath := filepath.Clean(filepath.Join(worktreePath, relPath))

		absFullPath, err := filepath.Abs(fullPath)
		if err != nil {
			log.Printf("[EXPAND] Failed to get absolute path: %v", err)
			return match
		}

		if !strings.HasPrefix(absFullPath, absWorktreePath+string(filepath.Separator)) &&
			absFullPath != absWorktreePath {
			log.Printf("[EXPAND] Rejected path traversal attempt: %s -> %s", relPath, absFullPath)
			return match
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			log.Printf("[EXPAND] File not found: %s (error: %v)", fullPath, err)
			return match
		}

		return fmt.Sprintf("\n--- File: %s ---\n%s\n--- End of %s ---\n",
			relPath, string(content), relPath)
	})
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

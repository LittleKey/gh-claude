// HttpServer provides HTTP handlers for the service
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
	"time"
)

// HttpServer provides HTTP endpoints for the service
type HttpServer struct {
	service *Service
}

// NewHttpServer creates a new HttpServer
func NewHttpServer(service *Service) *HttpServer {
	return &HttpServer{service: service}
}

// RegisterHandlers registers all HTTP handlers
func (h *HttpServer) RegisterHandlers() {
	http.HandleFunc("/run", h.handleRun)
	http.HandleFunc("/status", h.handleStatus)
	http.HandleFunc("/queue", h.handleQueue)
	http.HandleFunc("/cancel", h.handleCancel)
	http.HandleFunc("/webhook", h.handleWebhook)
	http.HandleFunc("/health", h.handleHealth)
	http.HandleFunc("/", h.handleRoot)
}

// handleRoot handles the root endpoint
func (h *HttpServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"service":      "claude-code-runner",
		"version":      "3.0.0",
		"active_tasks": h.service.activeCount,
		"queued_tasks": len(h.service.taskQueue),
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

// handleHealth handles the health check endpoint
func (h *HttpServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	h.service.mu.Lock()
	defer h.service.mu.Unlock()

	branchCount := 0
	runningBranches := 0
	for _, bl := range h.service.branchLocks {
		branchCount++
		bl.mu.Lock()
		if bl.running != nil {
			runningBranches++
		}
		bl.mu.Unlock()
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "healthy",
		"active_tasks":     h.service.activeCount,
		"queued_tasks":     len(h.service.taskQueue),
		"total_branches":   branchCount,
		"running_branches": runningBranches,
		"timestamp":        time.Now().Unix(),
	})
}

// handleRun handles the run endpoint
func (h *HttpServer) handleRun(w http.ResponseWriter, r *http.Request) {
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
	task := &Task{
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

	// Add reaction to PR if this is a PR task
	if req.PR > 0 && h.service.github != nil {
		parts := strings.Split(req.Repo, "/")
		if len(parts) == 2 {
			if err := h.service.github.AddPRReaction(parts[0], parts[1], req.PR, "eyes"); err != nil {
				log.Printf("[WARN] Failed to add reaction to PR #%d: %v", req.PR, err)
			} else {
				log.Printf("[GITHUB] Added reaction to PR #%d", req.PR)
			}
		}
	}

	// Submit task
	h.service.submitTask(task)

	log.Printf("[RUN] Task %s: repo=%s, branch=%s, task=%s", task.ID, req.Repo, branch, truncate(req.Task, 50))

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  task.ID,
		Status:  "queued",
		Repo:    req.Repo,
		Branch:  branch,
	})
}

// handleStatus handles the status endpoint
func (h *HttpServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}

	h.service.mu.Lock()
	task, exists := h.service.tasks[taskID]
	h.service.mu.Unlock()

	if !exists {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(task)
}

// handleQueue handles the queue endpoint
func (h *HttpServer) handleQueue(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	h.service.mu.Lock()
	defer h.service.mu.Unlock()

	type QueueInfo struct {
		ActiveTasks int                    `json:"active_tasks"`
		QueuedTasks int                    `json:"queued_tasks"`
		TotalTasks  int                    `json:"total_tasks"`
		Branches    map[string]interface{} `json:"branches"`
	}

	branchInfo := make(map[string]interface{})
	for key, bl := range h.service.branchLocks {
		bl.mu.Lock()
		runningTask := bl.running
		branchInfo[key] = map[string]interface{}{
			"running": runningTask,
			"queue":   len(bl.queue),
		}
		bl.mu.Unlock()
	}

	info := QueueInfo{
		ActiveTasks: h.service.activeCount,
		QueuedTasks: len(h.service.taskQueue),
		TotalTasks:  len(h.service.tasks),
		Branches:    branchInfo,
	}

	json.NewEncoder(w).Encode(info)
}

// handleCancel handles the cancel endpoint
func (h *HttpServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}

	h.service.mu.Lock()
	task, exists := h.service.tasks[taskID]
	if !exists {
		h.service.mu.Unlock()
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	if task.Status == "running" {
		h.service.mu.Unlock()
		http.Error(w, "cannot cancel running task", http.StatusBadRequest)
		return
	}

	task.Status = "cancelled"
	delete(h.service.tasks, taskID)
	h.service.mu.Unlock()

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  taskID,
		Status:  "cancelled",
	})
}

// handleWebhook handles the webhook endpoint
func (h *HttpServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
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
		taskDesc, repo, branch, prNum = h.handleIssueComment(payload)
	case "pull_request_review":
		taskDesc, repo, branch, prNum = h.handlePullRequestReview(payload)
	case "pull_request_review_comment":
		taskDesc, repo, branch, prNum = h.handlePullRequestReviewComment(payload)
	}

	if repo == "" || taskDesc == "" {
		json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
		return
	}

	// Expand @filepath references if worktree exists
	worktreePath := h.service.repoService.GetWorktreePath(repo, branch)
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
	task = &Task{
		ID:        fmt.Sprintf("task-%d-%s", time.Now().UnixNano()%100000, branch),
		Repo:      repo,
		Branch:    branch,
		Task:      taskDesc,
		PR:        prNum,
		Status:    "queued",
		CreatedAt: time.Now(),
	}

	h.service.submitTask(task)
	log.Printf("[WEBHOOK] Created task %s for %s:%s", task.ID, repo, branch)

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  task.ID,
		Status:  "queued",
		Repo:    repo,
		Branch:  branch,
	})
}

func (h *HttpServer) handleIssueComment(payload map[string]interface{}) (string, string, string, int) {
	var taskDesc, repo, branch string
	var prNum int

	// Check action
	if action, ok := payload["action"].(string); ok && action != "created" {
		return "", "", "", 0
	}

	// Check sender - ignore bot comments
	if sender, ok := payload["sender"].(map[string]interface{}); ok {
		if senderID, ok := sender["id"].(float64); ok {
			senderIDStr := strconv.FormatInt(int64(senderID), 10)
			if h.service.githubUserID != "" && senderIDStr == h.service.githubUserID {
				return "", "", "", 0
			}
		}
	}

	// Check comment body for @claude command
	if comment, ok := payload["comment"].(map[string]interface{}); ok {
		if body, ok := comment["body"].(string); ok {
			if strings.Contains(body, "@claude") || strings.HasPrefix(strings.TrimSpace(body), "/claude") {
				// Get issue/PR context
				if issue, ok := payload["issue"].(map[string]interface{}); ok {
					if n, ok := issue["number"].(float64); ok {
						if pr, ok := issue["pull_request"].(map[string]interface{}); ok && pr != nil {
							prNum = int(n)
							if r, ok := payload["repository"].(map[string]interface{}); ok {
								if fullName, ok := r["full_name"].(string); ok {
									branch = h.service.github.GetPRBranch(fullName, prNum)
								}
							}
						} else {
							prNum = 0
							branch = fmt.Sprintf("fix-issue-%d", int(n))
						}
					}
				}

				// Build task description
				taskCmd := extractTask(body)
				taskDesc = fmt.Sprintf("User Command:\n%s", taskCmd)

				// Get repository
				if r, ok := payload["repository"].(map[string]interface{}); ok {
					if fullName, ok := r["full_name"].(string); ok {
						repo = fullName
					}
				}
			}
		}
	}

	return taskDesc, repo, branch, prNum
}

func (h *HttpServer) handlePullRequestReview(payload map[string]interface{}) (string, string, string, int) {
	var taskDesc, repo, branch string
	var prNum int

	// Check action
	if action, ok := payload["action"].(string); ok && action != "submitted" {
		return "", "", "", 0
	}

	// Check sender
	if sender, ok := payload["sender"].(map[string]interface{}); ok {
		if senderID, ok := sender["id"].(float64); ok {
			senderIDStr := strconv.FormatInt(int64(senderID), 10)
			if h.service.githubUserID != "" && senderIDStr == h.service.githubUserID {
				return "", "", "", 0
			}
		}
	}

	// Get PR info
	if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
		if n, ok := pr["number"].(float64); ok {
			prNum = int(n)
		}
		if r, ok := pr["base"].(map[string]interface{})["repo"].(map[string]interface{}); ok {
			if fullName, ok := r["full_name"].(string); ok {
				repo = fullName
				branch = h.service.github.GetPRBranch(repo, prNum)
			}
		}
	}

	// Get review body
	if review, ok := payload["review"].(map[string]interface{}); ok {
		if body, ok := review["body"].(string); ok {
			taskDesc = fmt.Sprintf("Review Message:\n%s", body)
		}
	}

	return taskDesc, repo, branch, prNum
}

func (h *HttpServer) handlePullRequestReviewComment(payload map[string]interface{}) (string, string, string, int) {
	var taskDesc, repo, branch string
	var prNum int

	// Check action
	if action, ok := payload["action"].(string); ok && action != "created" {
		return "", "", "", 0
	}

	// Check sender
	if sender, ok := payload["sender"].(map[string]interface{}); ok {
		if senderID, ok := sender["id"].(float64); ok {
			senderIDStr := strconv.FormatInt(int64(senderID), 10)
			if h.service.githubUserID != "" && senderIDStr == h.service.githubUserID {
				return "", "", "", 0
			}
		}
	}

	// Get PR info
	if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
		if n, ok := pr["number"].(float64); ok {
			prNum = int(n)
		}
		if r, ok := pr["base"].(map[string]interface{})["repo"].(map[string]interface{}); ok {
			if fullName, ok := r["full_name"].(string); ok {
				repo = fullName
				branch = h.service.github.GetPRBranch(repo, prNum)
			}
		}
	}

	// Get comment body
	if comment, ok := payload["comment"].(map[string]interface{}); ok {
		if body, ok := comment["body"].(string); ok {
			taskDesc = fmt.Sprintf("Comment:\n%s", body)
		}
	}

	return taskDesc, repo, branch, prNum
}

// expandAtReferences expands @filename references in task description
func expandAtReferences(taskDesc, worktreePath string) string {
	re := regexp.MustCompile(`@([^\s@]+)`)

	absWorktreePath, err := filepath.Abs(worktreePath)
	if err != nil {
		return taskDesc
	}

	return re.ReplaceAllStringFunc(taskDesc, func(match string) string {
		relPath := strings.TrimPrefix(match, "@")

		if filepath.IsAbs(relPath) || strings.Contains(relPath, "\x00") {
			return match
		}

		fullPath := filepath.Clean(filepath.Join(worktreePath, relPath))
		absFullPath, err := filepath.Abs(fullPath)
		if err != nil {
			return match
		}

		if !strings.HasPrefix(absFullPath, absWorktreePath+string(filepath.Separator)) &&
			absFullPath != absWorktreePath {
			return match
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			return match
		}

		return fmt.Sprintf("\n--- File: %s ---\n%s\n--- End of %s ---\n",
			relPath, string(content), relPath)
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

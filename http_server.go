package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// HTTPServer handles HTTP server and request routing
type HTTPServer struct {
	addr     string
	service  *Service
	mux      *http.ServeMux
}

// NewHTTPServer creates a new HTTPServer
func NewHTTPServer(addr string, service *Service) *HTTPServer {
	mux := http.NewServeMux()
	server := &HTTPServer{
		addr:    addr,
		service: service,
		mux:     mux,
	}

	// Register handlers
	mux.HandleFunc("/", server.handleRoot)
	mux.HandleFunc("/run", server.handleRun)
	mux.HandleFunc("/status", server.handleStatus)
	mux.HandleFunc("/queue", server.handleQueue)
	mux.HandleFunc("/cancel", server.handleCancel)
	mux.HandleFunc("/webhook", server.handleWebhook)
	mux.HandleFunc("/health", server.handleHealth)

	return server
}

// Start starts the HTTP server
func (hs *HTTPServer) Start() error {
	log.Printf("Starting Claude Code Runner on %s", hs.addr)
	log.Printf("Work directory: %s", hs.service.workDir)
	log.Printf("Max concurrent: %d", hs.service.maxConcurrent)

	if err := http.ListenAndServe(hs.addr, hs.mux); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

func (hs *HTTPServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"service":      "claude-code-runner",
		"version":      "3.0.0",
		"active_tasks": hs.service.activeCount,
		"queued_tasks": len(hs.service.taskQueue),
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

func (hs *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	hs.service.mu.Lock()
	defer hs.service.mu.Unlock()

	branchCount := 0
	runningBranches := 0
	for _, bl := range hs.service.branchLocks {
		branchCount++
		bl.mu.Lock()
		if bl.running != nil {
			runningBranches++
		}
		bl.mu.Unlock()
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "healthy",
		"active_tasks":     hs.service.activeCount,
		"queued_tasks":     len(hs.service.taskQueue),
		"total_branches":   branchCount,
		"running_branches": runningBranches,
		"timestamp":        time.Now().Unix(),
	})
}

func (hs *HTTPServer) handleRun(w http.ResponseWriter, r *http.Request) {
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
	if req.PR > 0 && hs.service.github != nil {
		parts := strings.Split(req.Repo, "/")
		if len(parts) == 2 {
			// Add "eyes" reaction to show we're working on it
			if err := hs.service.github.AddPRReaction(parts[0], parts[1], req.PR, "eyes"); err != nil {
				log.Printf("[WARN] Failed to add reaction to PR #%d: %v", req.PR, err)
			} else {
				log.Printf("[GITHUB] Added reaction to PR #%d", req.PR)
			}
		}
	}

	// Submit task
	hs.service.submitTask(task)

	log.Printf("[RUN] Task %s: repo=%s, branch=%s, task=%s", task.ID, req.Repo, branch, truncate(req.Task, 50))

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  task.ID,
		Status:  "queued",
		Repo:    req.Repo,
		Branch:  branch,
	})
}

func (hs *HTTPServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}

	hs.service.mu.Lock()
	task, exists := hs.service.tasks[taskID]
	hs.service.mu.Unlock()

	if !exists {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(task)
}

func (hs *HTTPServer) handleQueue(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	hs.service.mu.Lock()
	defer hs.service.mu.Unlock()

	type QueueInfo struct {
		ActiveTasks int                    `json:"active_tasks"`
		QueuedTasks int                    `json:"queued_tasks"`
		TotalTasks  int                    `json:"total_tasks"`
		Branches    map[string]interface{} `json:"branches"`
	}

	branchInfo := make(map[string]interface{})
	for key, bl := range hs.service.branchLocks {
		bl.mu.Lock()
		runningTask := bl.running
		branchInfo[key] = map[string]interface{}{
			"running": runningTask,
			"queue":   len(bl.queue),
		}
		bl.mu.Unlock()
	}

	info := QueueInfo{
		ActiveTasks: hs.service.activeCount,
		QueuedTasks: len(hs.service.taskQueue),
		TotalTasks:  len(hs.service.tasks),
		Branches:    branchInfo,
	}

	json.NewEncoder(w).Encode(info)
}

func (hs *HTTPServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}

	hs.service.mu.Lock()
	task, exists := hs.service.tasks[taskID]
	if !exists {
		hs.service.mu.Unlock()
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	if task.Status == "running" {
		hs.service.mu.Unlock()
		http.Error(w, "cannot cancel running task", http.StatusBadRequest)
		return
	}

	task.Status = "cancelled"
	delete(hs.service.tasks, taskID)
	hs.service.mu.Unlock()

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  taskID,
		Status:  "cancelled",
	})
}

func (hs *HTTPServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
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
		log.Printf("[WEBHOOK] Bot user ID: %s", hs.service.githubUserID)

		// Check sender at top level
		if sender, ok := payload["sender"].(map[string]interface{}); ok {
			log.Printf("[WEBHOOK] Sender found in payload: %+v", sender)
			if senderID, ok := sender["id"].(float64); ok {
				senderIDStr := strconv.FormatInt(int64(senderID), 10)
				log.Printf("[WEBHOOK] Sender ID: %s, Bot ID: %s", senderIDStr, hs.service.githubUserID)
				if hs.service.githubUserID != "" && senderIDStr == hs.service.githubUserID {
					log.Printf("[WEBHOOK] Ignoring comment from bot user (senderID=%s, botID=%s)", senderIDStr, hs.service.githubUserID)
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
					log.Printf("[WEBHOOK] Comment author ID: %s, Bot ID: %s", authorIDStr, hs.service.githubUserID)
					if hs.service.githubUserID != "" && authorIDStr == hs.service.githubUserID {
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
							prNum = int(n)
							// Check if this is a PR comment or issue comment
							if pr, ok := issue["pull_request"].(map[string]interface{}); ok && pr != nil {
								// For PR comments, get the actual branch from GitHub API
								if r, ok := payload["repository"].(map[string]interface{}); ok {
									if fullName, ok := r["full_name"].(string); ok {
										branch = hs.service.getPRBranch(fullName, prNum)
									}
								}
								// Note: If branch is empty, we fail later in the check
							} else {
								branch = fmt.Sprintf("fix-issue-%d", prNum)
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
		log.Printf("[WEBHOOK] Bot user ID: %s", hs.service.githubUserID)
		if sender, ok := payload["sender"].(map[string]interface{}); ok {
			if senderID, ok := sender["id"].(float64); ok {
				senderIDStr := strconv.FormatInt(int64(senderID), 10)
				if hs.service.githubUserID != "" && senderIDStr == hs.service.githubUserID {
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
				branch = hs.service.getPRBranch(repoFullName, prNum)
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
		log.Printf("[WEBHOOK] Bot user ID: %s", hs.service.githubUserID)
		if sender, ok := payload["sender"].(map[string]interface{}); ok {
			if senderID, ok := sender["id"].(float64); ok {
				senderIDStr := strconv.FormatInt(int64(senderID), 10)
				if hs.service.githubUserID != "" && senderIDStr == hs.service.githubUserID {
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
					branch = hs.service.getPRBranch(repoFullName, prNum)
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

	hs.service.submitTask(task)
	log.Printf("[WEBHOOK] Created task %s for %s:%s", task.ID, repo, branch)

	json.NewEncoder(w).Encode(Response{
		Success: true,
		TaskID:  task.ID,
		Status:  "queued",
		Repo:    repo,
		Branch:  branch,
	})
}

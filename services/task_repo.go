// Package services provides independent services for gh-claude
package services

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "github.com/glebarez/sqlite"
)

// TaskRepo handles task persistence
type TaskRepo struct {
	db *sql.DB
}

// NewTaskRepo creates a new TaskRepo
func NewTaskRepo(dataDir string) (*TaskRepo, error) {
	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(dataDir, "tasks.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create tasks table
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		repo TEXT NOT NULL,
		branch TEXT NOT NULL,
		task_desc TEXT NOT NULL,
		pr_num INTEGER DEFAULT 0,
		debug INTEGER DEFAULT 0,
		skip_build INTEGER DEFAULT 0,
		status TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		started_at INTEGER,
		ended_at INTEGER,
		result TEXT,
		error TEXT,
		worktree TEXT,
		reaction_id INTEGER,
		comment_id INTEGER
	);
	`

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	log.Printf("[TaskRepo] Database initialized at %s", dbPath)
	return &TaskRepo{db: db}, nil
}

// SaveTask saves or updates a task in the database
func (r *TaskRepo) SaveTask(task *Task) error {
	log.Printf("[TaskRepo] Saving task %s (status: %s)", task.ID, task.Status)

	// Convert bool to int for SQLite
	debugInt := 0
	if task.Debug {
		debugInt = 1
	}
	skipBuildInt := 0
	if task.SkipBuild {
		skipBuildInt = 1
	}

	// Convert time.Time to Unix timestamps
	var createdAt, startedAt, endedAt int64
	if !task.CreatedAt.IsZero() {
		createdAt = task.CreatedAt.Unix()
	}
	if !task.StartedAt.IsZero() {
		startedAt = task.StartedAt.Unix()
	}
	if !task.EndedAt.IsZero() {
		endedAt = task.EndedAt.Unix()
	}

	query := `
	INSERT OR REPLACE INTO tasks (
		id, repo, branch, task_desc, pr_num, debug, skip_build, status,
		created_at, started_at, ended_at, result, error, worktree,
		reaction_id, comment_id
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := r.db.Exec(query,
		task.ID,
		task.Repo,
		task.Branch,
		task.Task,
		task.PR,
		debugInt,
		skipBuildInt,
		task.Status,
		createdAt,
		startedAt,
		endedAt,
		task.Result,
		task.Error,
		task.WorkTree,
		task.ReactionID,
		task.CommentID,
	)

	if err != nil {
		log.Printf("[TaskRepo] ERROR: failed to save task %s: %v", task.ID, err)
		return fmt.Errorf("failed to save task: %w", err)
	}

	log.Printf("[TaskRepo] Task %s saved successfully", task.ID)
	return nil
}

// LoadPendingTasks loads all tasks with status "queued" or "running"
func (r *TaskRepo) LoadPendingTasks() ([]*Task, error) {
	log.Printf("[TaskRepo] Loading pending tasks from database...")

	query := `
	SELECT id, repo, branch, task_desc, pr_num, debug, skip_build, status,
		   created_at, started_at, ended_at, result, error, worktree,
		   reaction_id, comment_id
	FROM tasks
	WHERE status IN ('queued', 'running')
	ORDER BY created_at ASC
	`

	rows, err := r.db.Query(query)
	if err != nil {
		log.Printf("[TaskRepo] ERROR: failed to query tasks: %v", err)
		return nil, fmt.Errorf("failed to query tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*Task

	for rows.Next() {
		var task Task
		var taskDesc, result, errorStr, worktree string
		var prNum, debugInt, skipBuildInt, reactionID, commentID sql.NullInt64
		var createdAt, startedAt, endedAt int64
		var status string

		err := rows.Scan(
			&task.ID,
			&task.Repo,
			&task.Branch,
			&taskDesc,
			&prNum,
			&debugInt,
			&skipBuildInt,
			&status,
			&createdAt,
			&startedAt,
			&endedAt,
			&result,
			&errorStr,
			&worktree,
			&reactionID,
			&commentID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan task: %w", err)
		}

		task.Task = taskDesc
		task.Result = result
		task.Error = errorStr
		task.WorkTree = worktree
		task.Status = status
		task.PR = int(prNum.Int64)
		task.Debug = debugInt.Int64 == 1
		task.SkipBuild = skipBuildInt.Int64 == 1
		task.ReactionID = int(reactionID.Int64)
		task.CommentID = int(commentID.Int64)
		task.CreatedAt = time.Unix(createdAt, 0)
		if startedAt > 0 {
			task.StartedAt = time.Unix(startedAt, 0)
		}
		if endedAt > 0 {
			task.EndedAt = time.Unix(endedAt, 0)
		}

		tasks = append(tasks, &task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tasks: %w", err)
	}

	log.Printf("[TaskRepo] Loaded %d pending tasks from database", len(tasks))
	return tasks, nil
}

// GetByID returns a task by ID
func (r *TaskRepo) GetByID(id string) (*Task, error) {
	query := `
	SELECT id, repo, branch, task_desc, pr_num, debug, skip_build, status,
		   created_at, started_at, ended_at, result, error, worktree,
		   reaction_id, comment_id
	FROM tasks
	WHERE id = ?
	`

	var task Task
	var taskDesc, result, errorStr, worktree string
	var prNum, debugInt, skipBuildInt, reactionID, commentID sql.NullInt64
	var createdAt, startedAt, endedAt int64
	var status string

	err := r.db.QueryRow(query, id).Scan(
		&task.ID,
		&task.Repo,
		&task.Branch,
		&taskDesc,
		&prNum,
		&debugInt,
		&skipBuildInt,
		&status,
		&createdAt,
		&startedAt,
		&endedAt,
		&result,
		&errorStr,
		&worktree,
		&reactionID,
		&commentID,
	)
	if err != nil {
		return nil, err
	}

	task.Task = taskDesc
	task.Result = result
	task.Error = errorStr
	task.WorkTree = worktree
	task.Status = status
	task.PR = int(prNum.Int64)
	task.Debug = debugInt.Int64 == 1
	task.SkipBuild = skipBuildInt.Int64 == 1
	task.ReactionID = int(reactionID.Int64)
	task.CommentID = int(commentID.Int64)
	task.CreatedAt = time.Unix(createdAt, 0)
	if startedAt > 0 {
		task.StartedAt = time.Unix(startedAt, 0)
	}
	if endedAt > 0 {
		task.EndedAt = time.Unix(endedAt, 0)
	}

	return &task, nil
}

// Close closes the database connection
func (r *TaskRepo) Close() error {
	return r.db.Close()
}

// GetDB returns the underlying database
func (r *TaskRepo) GetDB() *sql.DB {
	return r.db
}

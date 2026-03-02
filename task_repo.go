package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// TaskRepo handles task persistence
type TaskRepo struct {
	db      *sql.DB
	dataDir string
}

// NewTaskRepo creates a new TaskRepo
func NewTaskRepo(db *sql.DB, dataDir string) *TaskRepo {
	return &TaskRepo{
		db:      db,
		dataDir: dataDir,
	}
}

// InitDB initializes the database and creates tables
func (tr *TaskRepo) InitDB() error {
	// Ensure data directory exists
	if err := os.MkdirAll(tr.dataDir, 0o755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(tr.dataDir, "tasks.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	tr.db = db

	// Create tasks table
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		repo TEXT NOT NULL,
		branch TEXT NOT NULL,
		task_desc TEXT NOT NULL,
		pr_num INTEGER DEFAULT 0,
		debug INTEGER DEFAULT 0,
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

	if _, err := tr.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	log.Printf("[DB] Database initialized at %s", dbPath)
	return nil
}

// SaveTask saves or updates a task in the database
func (tr *TaskRepo) SaveTask(task *Task) error {
	log.Printf("[DB] Saving task %s (status: %s) to database", task.ID, task.Status)

	// Convert bool to int for SQLite
	debugInt := 0
	if task.Debug {
		debugInt = 1
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
		id, repo, branch, task_desc, pr_num, debug, status,
		created_at, started_at, ended_at, result, error, worktree,
		reaction_id, comment_id
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := tr.db.Exec(query,
		task.ID,
		task.Repo,
		task.Branch,
		task.Task,
		task.PR,
		debugInt,
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
		log.Printf("[DB] ERROR: failed to save task %s: %v", task.ID, err)
		return fmt.Errorf("failed to save task: %w", err)
	}

	log.Printf("[DB] Task %s saved successfully", task.ID)
	return nil
}

// LoadPendingTasks loads all tasks with status "queued" or "running"
func (tr *TaskRepo) LoadPendingTasks() ([]*Task, error) {
	log.Printf("[DB] Loading pending tasks from database...")

	query := `
	SELECT id, repo, branch, task_desc, pr_num, debug, status,
		   created_at, started_at, ended_at, result, error, worktree,
		   reaction_id, comment_id
	FROM tasks
	WHERE status IN ('queued', 'running')
	ORDER BY created_at ASC
	`

	rows, err := tr.db.Query(query)
	if err != nil {
		log.Printf("[DB] ERROR: failed to query tasks: %v", err)
		return nil, fmt.Errorf("failed to query tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*Task

	for rows.Next() {
		var task Task
		var taskDesc, result, errorStr, worktree string
		var prNum, debugInt, reactionID, commentID sql.NullInt64
		var createdAt, startedAt, endedAt int64
		var status string

		err := rows.Scan(
			&task.ID,
			&task.Repo,
			&task.Branch,
			&taskDesc,
			&prNum,
			&debugInt,
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

	log.Printf("[DB] Loaded %d pending tasks from database", len(tasks))
	return tasks, nil
}

// GetDB returns the database connection
func (tr *TaskRepo) GetDB() *sql.DB {
	return tr.db
}

// Close closes the database connection
func (tr *TaskRepo) Close() error {
	if tr.db != nil {
		return tr.db.Close()
	}
	return nil
}

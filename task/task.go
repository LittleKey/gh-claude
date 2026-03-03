// Package task provides task persistence services
package task

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "github.com/glebarez/sqlite"
)

// Task represents a task in the system
type Task struct {
	ID         string    `json:"id"`
	Repo       string    `json:"repo"` // owner/repo
	Branch     string    `json:"branch"`
	Task       string    `json:"task"`
	PR         int       `json:"pr"` // PR number (for GitHub interaction)
	Debug      bool      `json:"debug"`
	SkipBuild  bool      `json:"skip_build"` // Skip build step
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

// Repo provides task persistence operations
type Repo struct {
	db     *sql.DB
	dbPath string
}

// New creates a new task repository
func New(dataDir string) (*Repo, error) {
	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(dataDir, "tasks.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	repo := &Repo{
		db:     db,
		dbPath: dbPath,
	}

	if err := repo.init(); err != nil {
		return nil, err
	}

	log.Printf("[DB] Database initialized at %s", dbPath)
	return repo, nil
}

// Close closes the database connection
func (r *Repo) Close() error {
	return r.db.Close()
}

// init creates the database schema
func (r *Repo) init() error {
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

	if _, err := r.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	return nil
}

// Save saves or updates a task in the database
func (r *Repo) Save(task *Task) error {
	log.Printf("[DB] Saving task %s (status: %s) to database", task.ID, task.Status)

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
		log.Printf("[DB] ERROR: failed to save task %s: %v", task.ID, err)
		return fmt.Errorf("failed to save task: %w", err)
	}

	log.Printf("[DB] Task %s saved successfully", task.ID)
	return nil
}

// LoadPending loads all tasks with status "queued" or "running"
func (r *Repo) LoadPending() ([]*Task, error) {
	log.Printf("[DB] Loading pending tasks from database...")

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
		log.Printf("[DB] ERROR: failed to query tasks: %v", err)
		return nil, fmt.Errorf("failed to query tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*Task

	for rows.Next() {
		task, err := r.scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tasks: %w", err)
	}

	log.Printf("[DB] Loaded %d pending tasks from database", len(tasks))
	return tasks, nil
}

// GetByPR gets the latest completed task for a given PR
func (r *Repo) GetByPR(repo string, prNum int) (*Task, error) {
	query := `
	SELECT id, repo, branch, task_desc, pr_num, debug, skip_build, status,
		   created_at, started_at, ended_at, result, error, worktree,
		   reaction_id, comment_id
	FROM tasks
	WHERE repo = ? AND pr_num = ? AND status IN ('completed', 'failed')
	ORDER BY created_at DESC
	LIMIT 1
	`

	row := r.db.QueryRow(query, repo, prNum)
	return r.scanTaskRow(row)
}

// GetByBranch gets the latest completed task for a given branch
func (r *Repo) GetByBranch(repo, branch string) (*Task, error) {
	query := `
	SELECT id, repo, branch, task_desc, pr_num, debug, skip_build, status,
		   created_at, started_at, ended_at, result, error, worktree,
		   reaction_id, comment_id
	FROM tasks
	WHERE repo = ? AND branch = ? AND status IN ('completed', 'failed')
	ORDER BY created_at DESC
	LIMIT 1
	`

	row := r.db.QueryRow(query, repo, branch)
	return r.scanTaskRow(row)
}

// scanTask scans a task from a rows iterator
func (r *Repo) scanTask(rows *sql.Rows) (*Task, error) {
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

	return r.hydrateTask(&task, taskDesc, result, errorStr, worktree, status, prNum, debugInt, skipBuildInt, reactionID, commentID, createdAt, startedAt, endedAt), nil
}

// scanTaskRow scans a task from a single row
func (r *Repo) scanTaskRow(row *sql.Row) (*Task, error) {
	var task Task
	var taskDesc, result, errorStr, worktree string
	var prNum, debugInt, skipBuildInt, reactionID, commentID sql.NullInt64
	var createdAt, startedAt, endedAt int64
	var status string

	err := row.Scan(
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
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan task: %w", err)
	}

	return r.hydrateTask(&task, taskDesc, result, errorStr, worktree, status, prNum, debugInt, skipBuildInt, reactionID, commentID, createdAt, startedAt, endedAt), nil
}

// hydrateTask converts database fields to Task struct
func (r *Repo) hydrateTask(task *Task, taskDesc, result, errorStr, worktree, status string, prNum, debugInt, skipBuildInt, reactionID, commentID sql.NullInt64, createdAt, startedAt, endedAt int64) *Task {
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
	return task
}

// DB returns the underlying database connection
func (r *Repo) DB() *sql.DB {
	return r.db
}

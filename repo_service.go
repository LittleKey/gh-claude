package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RepoService handles git and repository operations
type RepoService struct {
	workDir  string
	github   *GithubService
}

// NewRepoService creates a new RepoService
func NewRepoService(workDir string, github *GithubService) *RepoService {
	return &RepoService{
		workDir: workDir,
		github:  github,
	}
}

// GetWorktreePath returns the worktree path for a given repo and branch
func (r *RepoService) GetWorktreePath(repo, branch string) string {
	repoSafe := strings.ReplaceAll(repo, "/", "-")
	return filepath.Join(r.workDir, repoSafe, branch)
}

// GetMainRepoPath returns the main bare repo path for a given repo
func (r *RepoService) GetMainRepoPath(repo string) string {
	repoSafe := strings.ReplaceAll(repo, "/", "-")
	return filepath.Join(r.workDir, repoSafe, ".main")
}

// SetupWorktree creates a git worktree for the given task
func (r *RepoService) SetupWorktree(repo, branch string, prNum int) (string, error) {
	worktreePath := r.GetWorktreePath(repo, branch)
	mainRepoPath := r.GetMainRepoPath(repo)

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, nil
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", fmt.Errorf("failed to create worktree dir: %w", err)
	}

	// Clone main repo if not exists using gh repo clone (handles auth automatically)
	if _, err := os.Stat(mainRepoPath); os.IsNotExist(err) {
		log.Printf("[GH] Cloning %s to %s using gh", repo, mainRepoPath)
		// gh repo clone doesn't support --bare, so we clone to a temp location first
		tempClonePath := mainRepoPath + ".tmp"
		cmd := exec.Command("gh", "repo", "clone", repo, tempClonePath)
		cmd.Env = append(os.Environ(), "GH_TOKEN="+r.github.token)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to clone repo: %w", err)
		}
		// Convert to bare repo
		cmd = exec.Command("git", "clone", "--bare", tempClonePath, mainRepoPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to create bare repo: %w", err)
		}
		// Clean up temp clone
		os.RemoveAll(tempClonePath)
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

	// For PR, fetch the PR branch using gh
	if prNum > 0 {
		// Use gh pr checkout to fetch PR branch - handles auth automatically
		prCmd := exec.Command("gh", "pr", "checkout", strconv.Itoa(prNum), "--branch", fmt.Sprintf("pr-%d", prNum), "-R", repo)
		prCmd.Dir = mainRepoPath
		prCmd.Env = append(os.Environ(), "GH_TOKEN="+r.github.token)
		prCmd.Run() // Ignore error, branch might not exist
	}

	// Create worktree
	log.Printf("[GIT] Creating worktree at %s for branch %s", worktreePath, branch)
	cmd = exec.Command("git", "worktree", "add", "-f", worktreePath, branch)
	cmd.Dir = mainRepoPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// If worktree add fails, try with -B (reset branch)
		cmd = exec.Command("git", "worktree", "add", "-f", "-B", branch, worktreePath, baseBranch)
		cmd.Dir = mainRepoPath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to create worktree: %w", err)
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

	return worktreePath, nil
}

// HasChanges checks if there are uncommitted changes in the worktree
func (r *RepoService) HasChanges(worktreePath string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(output) > 0, nil
}

// CommitAndPush commits and pushes changes in the worktree
func (r *RepoService) CommitAndPush(worktreePath, branch, commitMsg string) error {
	// Stage all changes
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = worktreePath
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %v, output: %s", err, string(output))
	}

	// Create commit
	commitCmd := exec.Command("git", "commit", "-m", commitMsg)
	commitCmd.Dir = worktreePath
	if _, err := commitCmd.CombinedOutput(); err != nil {
		// No changes to commit (maybe just formatting or already committed)
		log.Printf("[PUSH] No commit needed or commit failed")
	}

	// Push
	pushCmd := exec.Command("git", "push", "origin", branch)
	pushCmd.Dir = worktreePath
	pushCmd.Env = append(os.Environ(), "GIT_ASKPASS=true")

	_, err := pushCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	log.Printf("[PUSH] Successfully pushed branch %s", branch)
	return nil
}

// CreatePRIfNeeded creates a PR if it doesn't exist for issue-based branches
func (r *RepoService) CreatePRIfNeeded(task *Task) {
	// Extract issue number from branch name (e.g., fix-issue-2 -> 2)
	issueNum := 0
	if _, err := fmt.Sscanf(task.Branch, "fix-issue-%d", &issueNum); err != nil {
		log.Printf("[PR] Failed to extract issue number from branch %s: %v", task.Branch, err)
		return
	}

	// Check if PR already exists
	parts := strings.Split(task.Repo, "/")
	if len(parts) != 2 {
		log.Printf("[PR] Invalid repo format: %s", task.Repo)
		return
	}
	owner, repo := parts[0], parts[1]

	// Use gh CLI to check if PR exists
	if r.github.PRExists(owner, repo, issueNum) {
		log.Printf("[PR] PR #%d already exists", issueNum)
		return
	}

	// PR doesn't exist, create it
	log.Printf("[PR] Creating PR for issue #%d from branch %s", issueNum, task.Branch)

	// Get issue title for PR title
	title := fmt.Sprintf("Fix issue #%d", issueNum)
	body := fmt.Sprintf("This PR fixes issue #%d\n\nTask: %s", issueNum, truncate(task.Task, 200))

	if err := r.github.CreatePR(owner, repo, task.Branch, title, body, "main"); err != nil {
		log.Printf("[PR] Failed to create PR: %v", err)
		return
	}

	log.Printf("[PR] Successfully created PR for branch %s", task.Branch)
}

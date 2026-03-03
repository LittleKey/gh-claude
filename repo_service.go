// RepoService handles git operations for local repositories
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoService provides git operations for working with repositories
type RepoService struct {
	workDir  string
	ghToken  string
}

// NewRepoService creates a new RepoService
func NewRepoService(workDir, ghToken string) *RepoService {
	return &RepoService{
		workDir: workDir,
		ghToken: ghToken,
	}
}

// GetWorktreePath returns the path for a worktree
func (r *RepoService) GetWorktreePath(repo, branch string) string {
	repoSafe := strings.ReplaceAll(repo, "/", "-")
	return filepath.Join(r.workDir, repoSafe, branch)
}

// GetMainRepoPath returns the path for the main (bare) repo
func (r *RepoService) GetMainRepoPath(repo string) string {
	repoSafe := strings.ReplaceAll(repo, "/", "-")
	return filepath.Join(r.workDir, repoSafe, ".main")
}

// SetupWorktree creates and configures a git worktree for a branch
func (r *RepoService) SetupWorktree(repo, branch string, prNum int) (string, error) {
	cloneURL := fmt.Sprintf("https://%s@github.com/%s.git", r.ghToken, repo)
	mainRepoPath := r.GetMainRepoPath(repo)
	worktreePath := r.GetWorktreePath(repo, branch)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", fmt.Errorf("failed to create worktree dir: %w", err)
	}

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		log.Printf("[REPO] Worktree already exists at %s", worktreePath)
		return worktreePath, nil
	}

	// Clone main repo if not exists
	if _, err := os.Stat(mainRepoPath); os.IsNotExist(err) {
		log.Printf("[REPO] Cloning %s to %s", repo, mainRepoPath)
		cmd := exec.Command("git", "clone", "--bare", cloneURL, mainRepoPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to clone: %w", err)
		}
	} else {
		// Sync with remote
		if err := r.fetchMainRepo(mainRepoPath); err != nil {
			log.Printf("[WARN] Failed to fetch main repo: %v", err)
		}
	}

	// For PR, fetch and update PR branch and base branch
	if prNum > 0 {
		if err := r.fetchPRBranches(mainRepoPath, prNum); err != nil {
			log.Printf("[WARN] Failed to fetch PR branches: %v", err)
		}
	}

	// Create worktree
	if err := r.createWorktree(mainRepoPath, worktreePath, branch); err != nil {
		return "", err
	}

	// Configure git user
	r.configureGitUser(worktreePath)

	// Set remote URL with credentials
	if err := r.setRemoteURL(worktreePath, cloneURL); err != nil {
		log.Printf("[WARN] Failed to set remote URL: %v", err)
	}

	return worktreePath, nil
}

// fetchMainRepo fetches latest changes from remote
func (r *RepoService) fetchMainRepo(mainRepoPath string) error {
	log.Printf("[REPO] Fetching latest changes for main repo")

	// Fetch origin
	cmd := exec.Command("git", "fetch", "origin", "--depth=1")
	cmd.Dir = mainRepoPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	// Fetch all refs
	cmd = exec.Command("git", "fetch", "origin", "refs/heads/*:refs/remotes/origin/*", "--depth=1")
	cmd.Dir = mainRepoPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	// Get default branch
	defaultBranch := r.getDefaultBranch(mainRepoPath)
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	// Update local branch
	cmd = exec.Command("git", "fetch", "--force", "origin", fmt.Sprintf("refs/heads/%s:%s", defaultBranch, defaultBranch))
	cmd.Dir = mainRepoPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	return nil
}

// getDefaultBranch detects the default branch from remote
func (r *RepoService) getDefaultBranch(mainRepoPath string) string {
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = mainRepoPath
	output, err := cmd.Output()
	if err != nil {
		// Try common names
		for _, name := range []string{"main", "master"} {
			checkCmd := exec.Command("git", "rev-parse", fmt.Sprintf("refs/remotes/origin/%s", name))
			checkCmd.Dir = mainRepoPath
			if checkCmd.Run() == nil {
				return name
			}
		}
		return ""
	}

	ref := strings.TrimSpace(string(output))
	if strings.HasPrefix(ref, "refs/remotes/origin/") {
		return strings.TrimPrefix(ref, "refs/remotes/origin/")
	}
	return ""
}

// fetchPRBranches fetches PR-specific branches
func (r *RepoService) fetchPRBranches(mainRepoPath string, prNum int) error {
	// Fetch PR branch
	prCmd := exec.Command("git", "fetch", "--force", "origin", fmt.Sprintf("pull/%d/head:pr-%d", prNum, prNum))
	prCmd.Dir = mainRepoPath
	prCmd.Run()

	return nil
}

// createWorktree creates a new git worktree
func (r *RepoService) createWorktree(mainRepoPath, worktreePath, branch string) error {
	log.Printf("[REPO] Creating worktree at %s for branch %s", worktreePath, branch)

	// Check if branch exists locally
	checkCmd := exec.Command("git", "rev-parse", "--verify", fmt.Sprintf("refs/heads/%s", branch))
	checkCmd.Dir = mainRepoPath
	branchExists := checkCmd.Run() == nil

	if branchExists {
		// Branch exists, use regular worktree add
		wtCmd := exec.Command("git", "worktree", "add", "-f", worktreePath, branch)
		wtCmd.Dir = mainRepoPath
		wtCmd.Stdout = os.Stdout
		wtCmd.Stderr = os.Stderr
		if err := wtCmd.Run(); err != nil {
			return fmt.Errorf("failed to create worktree: %w", err)
		}
	} else {
		// Branch doesn't exist, create from remote's default branch
		if err := r.createWorktreeFromDefault(mainRepoPath, worktreePath, branch); err != nil {
			return err
		}
	}

	return nil
}

// createWorktreeFromDefault creates a worktree from the default branch
func (r *RepoService) createWorktreeFromDefault(mainRepoPath, worktreePath, branch string) error {
	log.Printf("[REPO] Branch %s does not exist, creating from default branch", branch)

	defaultBranch := r.getDefaultBranch(mainRepoPath)
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	// Try different branch sources
	sources := []string{"origin/" + defaultBranch, "origin/main", "origin/master", defaultBranch}
	var lastErr error
	for _, source := range sources {
		wtCmd := exec.Command("git", "worktree", "add", "-f", "-B", branch, worktreePath, source)
		wtCmd.Dir = mainRepoPath
		wtCmd.Stdout = os.Stdout
		wtCmd.Stderr = os.Stderr
		err := wtCmd.Run()
		if err == nil {
			return nil
		}
		lastErr = err
		log.Printf("[WARN] Failed to create worktree from %s: %v", source, err)
	}

	return fmt.Errorf("failed to create worktree: %w", lastErr)
}

// configureGitUser configures git user for the worktree
func (r *RepoService) configureGitUser(worktreePath string) {
	cmds := []*exec.Cmd{
		exec.Command("git", "config", "user.email", "claude-runner@local"),
		exec.Command("git", "config", "user.name", "Claude Runner"),
	}
	for _, c := range cmds {
		c.Dir = worktreePath
		c.Run()
	}
}

// setRemoteURL sets the remote URL with credentials
func (r *RepoService) setRemoteURL(worktreePath, remoteURL string) error {
	cmd := exec.Command("git", "remote", "set-url", "origin", remoteURL)
	cmd.Dir = worktreePath
	return cmd.Run()
}

// SyncWorktree syncs the worktree with remote
func (r *RepoService) SyncWorktree(worktreePath string) error {
	log.Printf("[REPO] Syncing worktree at %s with remote", worktreePath)

	// Fetch latest changes
	cmd := exec.Command("git", "fetch", "origin")
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	// Get current branch
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = worktreePath
	branch, err := cmd.Output()
	if err != nil {
		return nil
	}
	branchName := strings.TrimSpace(string(branch))
	if branchName == "" {
		return nil
	}

	// Check if behind
	cmd = exec.Command("git", "rev-list", "--count", fmt.Sprintf("HEAD..origin/%s", branchName))
	cmd.Dir = worktreePath
	behindCount, err := cmd.Output()
	if err != nil {
		return nil
	}

	count := strings.TrimSpace(string(behindCount))
	if count != "0" {
		log.Printf("[REPO] Branch %s is behind origin/%s by %s commits", branchName, branchName, count)
		cmd = exec.Command("git", "pull", "--rebase", "origin", branchName)
		cmd.Dir = worktreePath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("[WARN] Failed to pull: %v, resetting to remote", err)
			cmd = exec.Command("git", "reset", "--hard", fmt.Sprintf("origin/%s", branchName))
			cmd.Dir = worktreePath
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
		}
	}

	return nil
}

// HasChanges checks if there are uncommitted changes
func (r *RepoService) HasChanges(worktreePath string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(output) > 0, nil
}

// CommitAndPush commits and pushes changes
func (r *RepoService) CommitAndPush(worktreePath, branch, commitMsg string) error {
	// Stage all changes
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = worktreePath
	if _, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stage changes: %w", err)
	}

	// Create commit
	commitCmd := exec.Command("git", "commit", "-m", commitMsg)
	commitCmd.Dir = worktreePath
	commitCmd.Run() // Ignore error if nothing to commit

	// Push
	pushCmd := exec.Command("git", "push", "origin", branch)
	pushCmd.Dir = worktreePath
	pushCmd.Env = append(os.Environ(), "GIT_ASKPASS=true")
	output, err := pushCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push: %w, output: %s", err, string(output))
	}

	log.Printf("[REPO] Successfully pushed branch %s", branch)
	return nil
}

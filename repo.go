// Package repo provides repository (git worktree) operations
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type RepoService struct {
	workDir string
	token   string
}

func NewRepoService(workDir, token string) *RepoService {
	return &RepoService{
		workDir: workDir,
		token:   token,
	}
}

// CloneURL returns the clone URL with credentials
func (s *RepoService) CloneURL(repo string) string {
	return fmt.Sprintf("https://%s@github.com/%s.git", s.token, repo)
}

// MainRepoPath returns the path to the main (bare) repo
func (s *RepoService) MainRepoPath(repo string) string {
	repoSafe := strings.ReplaceAll(repo, "/", "-")
	return filepath.Join(s.workDir, repoSafe, ".main")
}

// WorktreePath returns the path to a worktree for the given branch
func (s *RepoService) WorktreePath(repo, branch string) string {
	repoSafe := strings.ReplaceAll(repo, "/", "-")
	return filepath.Join(s.workDir, repoSafe, branch)
}

// EnsureMainRepo ensures the main (bare) repo exists, cloning if needed
func (s *RepoService) EnsureMainRepo(repo string) error {
	mainPath := s.MainRepoPath(repo)
	if _, err := os.Stat(mainPath); os.IsNotExist(err) {
		cloneURL := s.CloneURL(repo)
		cmd := exec.Command("git", "clone", "--bare", cloneURL, mainPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to clone: %v", err)
		}
		return nil
	}
	// Sync with remote
	return s.fetchMainRepo(repo)
}

// fetchMainRepo fetches latest changes from remote
func (s *RepoService) fetchMainRepo(repo string) error {
	mainPath := s.MainRepoPath(repo)

	// Fetch both the default branch and origin/HEAD
	cmd := exec.Command("git", "fetch", "origin", "--depth=1")
	cmd.Dir = mainPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to fetch origin: %v", err)
	}

	// Also fetch origin/HEAD explicitly
	cmd = exec.Command("git", "fetch", "origin", "refs/heads/*:refs/remotes/origin/*", "--depth=1")
	cmd.Dir = mainPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	return nil
}

// GetDefaultBranch gets the default branch name from remote
func (s *RepoService) GetDefaultBranch(repo string) (string, error) {
	mainPath := s.MainRepoPath(repo)

	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = mainPath
	output, err := cmd.Output()
	if err != nil {
		// Fallback: try common default branch names
		for _, name := range []string{"main", "master"} {
			checkCmd := exec.Command("git", "rev-parse", fmt.Sprintf("refs/remotes/origin/%s", name))
			checkCmd.Dir = mainPath
			if checkCmd.Run() == nil {
				return name, nil
			}
		}
		return "main", nil
	}

	ref := strings.TrimSpace(string(output))
	if strings.HasPrefix(ref, "refs/remotes/origin/") {
		return strings.TrimPrefix(ref, "refs/remotes/origin/"), nil
	}
	return "main", nil
}

// UpdateDefaultBranch updates the local default branch to match remote
func (s *RepoService) UpdateDefaultBranch(repo, defaultBranch string) error {
	mainPath := s.MainRepoPath(repo)

	cmd := exec.Command("git", "fetch", "--force", "origin", fmt.Sprintf("refs/heads/%s:%s", defaultBranch, defaultBranch))
	cmd.Dir = mainPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// FetchPRBranch fetches the PR branch from remote
func (s *RepoService) FetchPRBranch(repo string, prNum int) error {
	mainPath := s.MainRepoPath(repo)

	cmd := exec.Command("git", "fetch", "origin", fmt.Sprintf("pull/%d/head:pr-%d", prNum, prNum))
	cmd.Dir = mainPath
	return cmd.Run()
}

// BranchExists checks if a branch exists locally
func (s *RepoService) BranchExists(repo, branch string) bool {
	mainPath := s.MainRepoPath(repo)

	checkCmd := exec.Command("git", "rev-parse", "--verify", fmt.Sprintf("refs/heads/%s", branch))
	checkCmd.Dir = mainPath
	return checkCmd.Run() == nil
}

// CreateWorktree creates a new worktree for the given branch
func (s *RepoService) CreateWorktree(repo, branch, worktreePath, baseBranch string) error {
	mainPath := s.MainRepoPath(repo)

	// Ensure main repo exists
	if err := s.EnsureMainRepo(repo); err != nil {
		return err
	}

	branchExists := s.BranchExists(repo, branch)

	var cmd *exec.Cmd
	if branchExists {
		// Branch exists, use regular worktree add
		cmd = exec.Command("git", "worktree", "add", "-f", worktreePath, branch)
	} else {
		// Branch doesn't exist, create from remote's default branch
		// Try origin/main first, then origin/master, then local main
		cmd = exec.Command("git", "worktree", "add", "-f", "-B", branch, worktreePath, "origin/main")
		if err := cmd.Run(); err != nil {
			// Try origin/master
			cmd = exec.Command("git", "worktree", "add", "-f", "-B", branch, worktreePath, "origin/master")
			if err := cmd.Run(); err != nil {
				// Try local main
				cmd = exec.Command("git", "worktree", "add", "-f", "-B", branch, worktreePath, "main")
			}
		}
	}
	cmd.Dir = mainPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ConfigureWorktree configures git user and remote for the worktree
func (s *RepoService) ConfigureWorktree(worktreePath, repo string) error {
	// Configure git user
	cmds := []*exec.Cmd{
		exec.Command("git", "config", "user.email", "claude-runner@local"),
		exec.Command("git", "config", "user.name", "Claude Runner"),
	}
	for _, c := range cmds {
		c.Dir = worktreePath
		c.Run()
	}

	// Set remote URL with credentials
	remoteURL := s.CloneURL(repo)
	remoteCmd := exec.Command("git", "remote", "set-url", "origin", remoteURL)
	remoteCmd.Dir = worktreePath
	return remoteCmd.Run()
}

// Fetch fetches latest changes in worktree
func (s *RepoService) Fetch(worktreePath string) error {
	cmd := exec.Command("git", "fetch", "origin")
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// GetCurrentBranch gets the current branch name
func (s *RepoService) GetCurrentBranch(worktreePath string) (string, error) {
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = worktreePath
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// IsBehindRemote checks if branch is behind origin
func (s *RepoService) IsBehindRemote(worktreePath, branch string) (bool, error) {
	cmd := exec.Command("git", "rev-list", "--count", fmt.Sprintf("HEAD..origin/%s", branch))
	cmd.Dir = worktreePath
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	count := strings.TrimSpace(string(output))
	return count != "0", nil
}

// Pull pulls changes from remote
func (s *RepoService) Pull(worktreePath, branch string) error {
	cmd := exec.Command("git", "pull", "--rebase", "origin", branch)
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ResetToRemote resets branch to match origin
func (s *RepoService) ResetToRemote(worktreePath, branch string) error {
	cmd := exec.Command("git", "reset", "--hard", fmt.Sprintf("origin/%s", branch))
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// SyncWorktree syncs worktree with remote, handling conflicts
func (s *RepoService) SyncWorktree(worktreePath string) error {
	// Fetch latest changes
	if err := s.Fetch(worktreePath); err != nil {
		return err
	}

	// Get current branch
	branch, err := s.GetCurrentBranch(worktreePath)
	if err != nil {
		return err
	}
	if branch == "" {
		return nil
	}

	// Check if behind remote
	behind, err := s.IsBehindRemote(worktreePath, branch)
	if err != nil || !behind {
		return nil
	}

	// Try to pull
	if err := s.Pull(worktreePath, branch); err != nil {
		// Try to reset to origin
		return s.ResetToRemote(worktreePath, branch)
	}

	return nil
}

// HasChanges checks if there are uncommitted changes
func (s *RepoService) HasChanges(worktreePath string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(output) > 0, nil
}

// StageChanges stages all changes
func (s *RepoService) StageChanges(worktreePath string) error {
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = worktreePath
	_, err := cmd.CombinedOutput()
	return err
}

// Commit creates a commit with the given message
func (s *RepoService) Commit(worktreePath, msg string) error {
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = worktreePath
	_, err := cmd.CombinedOutput()
	return err
}

// Push pushes changes to remote
func (s *RepoService) Push(worktreePath, branch string) error {
	cmd := exec.Command("git", "push", "origin", branch)
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(), "GIT_ASKPASS=true")
	_, err := cmd.CombinedOutput()
	return err
}

// PushChanges commits and pushes all changes
func (s *RepoService) PushChanges(worktreePath, branch, commitMsg string) error {
	hasChanges, err := s.HasChanges(worktreePath)
	if err != nil || !hasChanges {
		return err
	}

	if err := s.StageChanges(worktreePath); err != nil {
		return err
	}

	if err := s.Commit(worktreePath, commitMsg); err != nil {
		// No changes to commit (maybe just formatting or already committed)
		return nil
	}

	return s.Push(worktreePath, branch)
}

// EnsureWorktree ensures a worktree exists and is synced for the given branch
func (s *RepoService) EnsureWorktree(repo, branch string, prNum int) (string, error) {
	worktreePath := s.WorktreePath(repo, branch)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", err
	}

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		// Sync existing worktree
		if err := s.SyncWorktree(worktreePath); err != nil {
			return "", err
		}
		return worktreePath, nil
	}

	// Create new worktree
	if err := s.CreateWorktree(repo, branch, worktreePath, "main"); err != nil {
		return "", err
	}

	// Configure worktree
	if err := s.ConfigureWorktree(worktreePath, repo); err != nil {
		return "", err
	}

	// Fetch PR branch if needed
	if prNum > 0 {
		s.FetchPRBranch(repo, prNum)
	}

	// Sync worktree
	if err := s.SyncWorktree(worktreePath); err != nil {
		return "", err
	}

	return worktreePath, nil
}

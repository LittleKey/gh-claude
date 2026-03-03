// Package services provides independent services for gh-claude
package services

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RepoService handles local repository operations (git/worktree)
type RepoService struct {
	workDir  string
	ghToken  string
}

// NewRepoService creates a new RepoService
func NewRepoService(workDir string, ghToken string) *RepoService {
	return &RepoService{
		workDir: workDir,
		ghToken: ghToken,
	}
}

// SetupWorktree creates or updates a worktree for the given repo and branch
func (r *RepoService) SetupWorktree(repo, owner, branch string, prNum int) (string, error) {
	// Parse repo (owner/repo format)
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid repo format: %s", repo)
	}
	ownerName := parts[0]
	repoName := parts[1]

	// Create worktree directory path
	repoDir := strings.ReplaceAll(repo, "/", "-")
	worktreePath := filepath.Join(r.workDir, repoDir, branch)

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		log.Printf("[RepoService] Worktree already exists at %s", worktreePath)
		return worktreePath, nil
	}

	// Create parent directory
	parentDir := filepath.Join(r.workDir, repoDir)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create parent directory: %w", err)
	}

	// Clone URL with token
	cloneURL := fmt.Sprintf("https://%s@github.com/%s.git", r.ghToken, repo)

	// Create bare clone for reference
	mainRepoPath := filepath.Join(parentDir, ".main")
	if _, err := os.Stat(mainRepoPath); err != nil {
		log.Printf("[RepoService] Creating bare clone at %s", mainRepoPath)
		cmd := exec.Command("git", "clone", "--bare", cloneURL, mainRepoPath)
		cmd.Dir = "/"
		if output, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[RepoService] Bare clone output: %s", string(output))
			return "", fmt.Errorf("failed to clone bare repo: %w", err)
		}
	}

	// Configure git
	r.configureGit(mainRepoPath)

	// Fetch latest
	cmd := exec.Command("git", "fetch", "origin", "--depth=1")
	cmd.Dir = mainRepoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[RepoService] Fetch output: %s", string(output))
	}

	// Determine default branch
	defaultBranch := r.getDefaultBranch(mainRepoPath)

	// Update local default branch
	updateCmd := exec.Command("git", "fetch", "--force", "origin",
		fmt.Sprintf("%s:%s", defaultBranch, defaultBranch))
	updateCmd.Dir = mainRepoPath
	if output, err := updateCmd.CombinedOutput(); err != nil {
		log.Printf("[RepoService] Update default branch output: %s", string(output))
	}

	// If PR number is provided, fetch PR branch
	if prNum > 0 {
		prBranch := fmt.Sprintf("pr-%d", prNum)
		prCmd := exec.Command("git", "fetch", "origin",
			fmt.Sprintf("pull/%d/head:%s", prNum, prBranch))
		prCmd.Dir = mainRepoPath
		if output, err := prCmd.CombinedOutput(); err != nil {
			log.Printf("[RepoService] Fetch PR output: %s", string(output))
			// Try without depth for PRs that might not be available
		}
		// Check if branch exists
		checkCmd := exec.Command("git", "rev-parse", "--verify",
			fmt.Sprintf("refs/heads/%s", prBranch))
		checkCmd.Dir = mainRepoPath
		if err := checkCmd.Run(); err == nil {
			branch = prBranch
		}
	}

	// Check if branch already exists locally
	checkCmd := exec.Command("git", "rev-parse", "--verify",
		fmt.Sprintf("refs/heads/%s", branch))
	checkCmd.Dir = mainRepoPath
	branchExists := checkCmd.Run() == nil

	// Create worktree
	var wtCmd *exec.Cmd
	if branchExists {
		wtCmd = exec.Command("git", "worktree", "add", "-f", worktreePath, branch)
	} else {
		wtCmd = exec.Command("git", "worktree", "add", "-f", "-B", branch, worktreePath,
			fmt.Sprintf("origin/%s", defaultBranch))
	}
	wtCmd.Dir = mainRepoPath
	if output, err := wtCmd.CombinedOutput(); err != nil {
		log.Printf("[RepoService] Worktree add output: %s", string(output))
		// Try with main as fallback
		wtCmd = exec.Command("git", "worktree", "add", "-f", "-B", branch, worktreePath, "origin/main")
		wtCmd.Dir = mainRepoPath
		if output, err := wtCmd.CombinedOutput(); err != nil {
			log.Printf("[RepoService] Worktree add with main output: %s", string(output))
			wtCmd = exec.Command("git", "worktree", "add", "-f", "-B", branch, worktreePath, "main")
			wtCmd.Dir = mainRepoPath
			if output, err := wtCmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("failed to create worktree: %w, output: %s", err, string(output))
			}
		}
	}

	// Configure git user in worktree
	exec.Command("git", "config", "user.email", "claude-runner@local").Dir = nil
	exec.Command("git", "config", "user.name", "Claude Runner").Dir = nil

	// Set remote URL with token
	remoteURL := fmt.Sprintf("https://%s@github.com/%s.git", r.ghToken, repo)
	remoteCmd := exec.Command("git", "remote", "set-url", "origin", remoteURL)
	remoteCmd.Dir = worktreePath
	if output, err := remoteCmd.CombinedOutput(); err != nil {
		log.Printf("[RepoService] Set remote URL output: %s", string(output))
	}

	log.Printf("[RepoService] Worktree created at %s", worktreePath)
	return worktreePath, nil
}

// configureGit configures git settings
func (r *RepoService) configureGit(repoPath string) {
	cmds := []*exec.Cmd{
		exec.Command("git", "config", "--global", "--add", "safe.directory", "*"),
		exec.Command("git", "config", "--global", "http.sslCAInfo", "/etc/ssl/certs/ca-certificates.crt"),
	}
	for _, cmd := range cmds {
		cmd.Dir = "/"
		if output, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[RepoService] Git config output: %s", string(output))
		}
	}
}

// getDefaultBranch returns the default branch name
func (r *RepoService) getDefaultBranch(repoPath string) string {
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "main"
	}
	ref := strings.TrimSpace(string(output))
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

// SyncWorktree syncs the worktree with remote
func (r *RepoService) SyncWorktree(worktreePath, branch string) error {
	// Fetch latest
	cmd := exec.Command("git", "fetch", "origin")
	cmd.Dir = worktreePath
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[RepoService] Fetch output: %s", string(output))
	}

	// Get current branch
	currentCmd := exec.Command("git", "branch", "--show-current")
	currentCmd.Dir = worktreePath
	currentBranch, err := currentCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	branch = strings.TrimSpace(string(currentBranch))

	// Check if behind remote
	countCmd := exec.Command("git", "rev-list", "--count",
		fmt.Sprintf("HEAD..origin/%s", branch))
	countCmd.Dir = worktreePath
	countOutput, err := countCmd.Output()
	if err != nil {
		log.Printf("[RepoService] Rev-list output: %s", string(countOutput))
	} else {
		count := strings.TrimSpace(string(countOutput))
		if count != "0" {
			// Pull with rebase
			pullCmd := exec.Command("git", "pull", "--rebase", "origin", branch)
			pullCmd.Dir = worktreePath
			if output, err := pullCmd.CombinedOutput(); err != nil {
				log.Printf("[RepoService] Pull output: %s", string(output))
				// Try reset
				resetCmd := exec.Command("git", "reset", "--hard",
					fmt.Sprintf("origin/%s", branch))
				resetCmd.Dir = worktreePath
				if output, err := resetCmd.CombinedOutput(); err != nil {
					log.Printf("[RepoService] Reset output: %s", string(output))
				}
			}
		}
	}

	log.Printf("[RepoService] Worktree synced at %s", worktreePath)
	return nil
}

// PushChanges commits and pushes changes
func (r *RepoService) PushChanges(worktreePath, branch, commitMsg string) error {
	// Check for changes
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check status: %w", err)
	}

	if len(strings.TrimSpace(string(output))) == 0 {
		log.Printf("[RepoService] No changes to push")
		return nil
	}

	// Add all changes
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = worktreePath
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add changes: %w, output: %s", err, string(output))
	}

	// Commit
	commitCmd := exec.Command("git", "commit", "-m", commitMsg)
	commitCmd.Dir = worktreePath
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to commit: %w, output: %s", err, string(output))
	}

	// Push
	pushCmd := exec.Command("git", "push", "origin", branch)
	pushCmd.Dir = worktreePath
	if output, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to push: %w, output: %s", err, string(output))
	}

	log.Printf("[RepoService] Changes pushed to %s", branch)
	return nil
}

// CreatePRIfNeeded creates a PR if it doesn't exist
func (r *RepoService) CreatePRIfNeeded(repo, branch, title, body string) (int, error) {
	// Check if PR already exists
	cmd := exec.Command("gh", "pr", "view", "--json", "number", "-R", repo, branch)
	cmd.Env = append(os.Environ(), "GH_TOKEN="+r.ghToken)
	output, err := cmd.Output()
	if err == nil {
		// PR exists, extract number
		var pr struct {
			Number int `json:"number"`
		}
		if jsonErr := json.Unmarshal(output, &pr); jsonErr == nil {
			return pr.Number, nil
		}
	}

	// Create PR
	createCmd := exec.Command("gh", "pr", "create",
		"--title", title,
		"--body", body,
		"--base", "main",
		"--head", branch,
		"-R", repo)
	createCmd.Env = append(os.Environ(), "GH_TOKEN="+r.ghToken)
	createOutput, err := createCmd.CombinedOutput()
	if err != nil {
		// Try with master as base
		createCmd = exec.Command("gh", "pr", "create",
			"--title", title,
			"--body", body,
			"--base", "master",
			"--head", branch,
			"-R", repo)
		createCmd.Env = append(os.Environ(), "GH_TOKEN="+r.ghToken)
		createOutput, err = createCmd.CombinedOutput()
		if err != nil {
			return 0, fmt.Errorf("failed to create PR: %w, output: %s", err, string(createOutput))
		}
	}

	// Extract PR number from output
	outputStr := string(createOutput)
	if strings.Contains(outputStr, "https://github.com/") {
		parts := strings.Split(outputStr, "/")
		for i, part := range parts {
			if part == "pull" && i+1 < len(parts) {
				prNum, err := strconv.Atoi(strings.TrimSuffix(parts[i+1], "\n"))
				if err == nil {
					return prNum, nil
				}
			}
		}
	}

	return 0, nil
}

// GetPRBranch returns the branch name for a PR
func (r *RepoService) GetPRBranch(repo string, prNum int) (string, error) {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNum), "--json", "headRefName", "-R", repo)
	cmd.Env = append(os.Environ(), "GH_TOKEN="+r.ghToken)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get PR branch: %w", err)
	}

	var pr struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(output, &pr); err != nil {
		return "", err
	}
	return pr.HeadRefName, nil
}

// GetWorkDir returns the work directory
func (r *RepoService) GetWorkDir() string {
	return r.workDir
}

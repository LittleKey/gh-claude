// Package github provides GitHub interaction services using gh CLI
package github

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// Service handles GitHub interactions (PR and Issue operations)
type Service struct {
	token string
}

// New creates a new GitHub service
func New(token string) *Service {
	return &Service{token: token}
}

// AddPRComment adds a comment to a PR using gh CLI
func (s *Service) AddPRComment(owner, repo string, prNumber int, body string) error {
	repoFullName := owner + "/" + repo
	cmd := exec.Command("gh", "pr", "comment", strconv.Itoa(prNumber), "--body", body, "-R", repoFullName)
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+s.token)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add comment: %v, output: %s", err, string(output))
	}
	return nil
}

// AddIssueComment adds a comment to an Issue using gh CLI
func (s *Service) AddIssueComment(owner, repo string, issueNumber int, body string) error {
	repoFullName := owner + "/" + repo
	cmd := exec.Command("gh", "issue", "comment", strconv.Itoa(issueNumber), "--body", body, "-R", repoFullName)
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+s.token)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add issue comment: %v, output: %s", err, string(output))
	}
	return nil
}

// GetPRBranch gets the actual branch name for a PR using gh CLI
func (s *Service) GetPRBranch(repo string, prNum int) (string, error) {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNum), "--json", "headRefName", "-R", repo)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get PR #%d branch: %v, output: %s", prNum, err, string(output))
	}

	var result struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("failed to parse PR branch: %v", err)
	}

	return result.HeadRefName, nil
}

// CheckPRExists checks if a PR already exists for a given issue number
func (s *Service) CheckPRExists(repo string, issueNum int) (bool, error) {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(issueNum), "--json", "url,number", "-R", repo)
	_, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	return false, nil
}

// CreatePR creates a PR for a branch
func (s *Service) CreatePR(repo, branch, title, body, baseBranch string) (int, error) {
	cmd := exec.Command("gh", "pr", "create",
		"--title", title,
		"--body", body,
		"--base", baseBranch,
		"--head", branch,
		"-R", repo,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to create PR: %v, output: %s", err, string(output))
	}

	// Get the newly created PR number
	viewCmd := exec.Command("gh", "pr", "view", "--json", "number", "-R", repo, branch)
	viewOutput, err := viewCmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to get PR number: %v, output: %s", err, string(viewOutput))
	}

	var prData struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(viewOutput, &prData); err != nil {
		return 0, err
	}

	return prData.Number, nil
}

// GetCurrentUserID gets the current GitHub user ID
func (s *Service) GetCurrentUserID() (string, error) {
	cmd := exec.Command("gh", "api", "user", "--jq", ".id")
	cmd.Env = append(os.Environ(), "GH_TOKEN="+s.token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get current user: %v, output: %s", err, string(output))
	}
	return string(output), nil
}

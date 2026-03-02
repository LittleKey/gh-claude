package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// GithubService handles GitHub API interactions
type GithubService struct {
	token string
}

// NewGithubService creates a new GithubService
func NewGithubService(token string) *GithubService {
	return &GithubService{token: token}
}

// repoFullName combines owner and repo into full repo name
func (g *GithubService) repoFullName(owner, repo string) string {
	return owner + "/" + repo
}

// runGhCmd runs a gh CLI command with proper token setup
func (g *GithubService) runGhCmd(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh command failed: %v, output: %s", err, string(output))
	}
	return strings.TrimSpace(string(output)), nil
}

// runGhCmdWithRepo runs a gh CLI command with repo flag
func (g *GithubService) runGhCmdWithRepo(repo, operation string, args ...string) (string, error) {
	fullArgs := append(args, "-R", repo)
	return g.runGhCmd(fullArgs...)
}

// AddPRReaction adds a reaction to a PR using gh CLI
func (g *GithubService) AddPRReaction(owner, repo string, prNumber int, reaction string) error {
	repoFullName := g.repoFullName(owner, repo)
	_, err := g.runGhCmd("api", "-X", "POST",
		fmt.Sprintf("repos/%s/%s/pulls/%d/reactions", owner, repo, prNumber),
		"-f", "content="+reaction,
		"-R", repoFullName)
	return err
}

// RemovePRReaction removes a reaction from a PR using gh CLI
func (g *GithubService) RemovePRReaction(owner, repo string, prNumber int, reactionID int) error {
	repoFullName := g.repoFullName(owner, repo)
	_, err := g.runGhCmd("api", "-X", "DELETE",
		fmt.Sprintf("repos/%s/%s/pulls/%d/reactions/%d", owner, repo, prNumber, reactionID),
		"-R", repoFullName)
	return err
}

// GetCurrentUserID gets the current user ID using gh CLI
func (g *GithubService) GetCurrentUserID() (string, error) {
	return g.runGhCmd("api", "user", "--jq", ".id")
}

// AddPRComment adds a comment to a PR using gh CLI
func (g *GithubService) AddPRComment(owner, repo string, prNumber int, body string) error {
	repoFullName := g.repoFullName(owner, repo)
	cmd := exec.Command("gh", "pr", "comment", strconv.Itoa(prNumber), "--body", body, "-R", repoFullName)
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add comment: %v, output: %s", err, string(output))
	}
	return nil
}

// AddIssueComment adds a comment to an Issue using gh CLI
func (g *GithubService) AddIssueComment(owner, repo string, issueNumber int, body string) error {
	repoFullName := g.repoFullName(owner, repo)
	cmd := exec.Command("gh", "issue", "comment", strconv.Itoa(issueNumber), "--body", body, "-R", repoFullName)
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add issue comment: %v, output: %s", err, string(output))
	}
	return nil
}

// GetPR gets PR information using gh CLI
func (g *GithubService) GetPR(owner, repo string, prNumber int) (map[string]interface{}, error) {
	repoFullName := g.repoFullName(owner, repo)
	output, err := g.runGhCmd("pr", "view", strconv.Itoa(prNumber), "--json",
		"number,title,body,headRefName,baseRefName,url,state", "-R", repoFullName)
	if err != nil {
		return nil, err
	}
	var pr map[string]interface{}
	if err := json.Unmarshal([]byte(output), &pr); err != nil {
		return nil, err
	}
	return pr, nil
}

// GetPRBranch gets the actual branch name for a PR using gh CLI
func (g *GithubService) GetPRBranch(repo string, prNum int) (string, error) {
	output, err := g.runGhCmd("pr", "view", strconv.Itoa(prNum), "--json", "headRefName", "-R", repo)
	if err != nil {
		return "", err
	}
	var result struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", err
	}
	return result.HeadRefName, nil
}

// CreatePR creates a PR using gh CLI
func (g *GithubService) CreatePR(owner, repo, branch, title, body, baseBranch string) error {
	repoFullName := g.repoFullName(owner, repo)
	_, err := g.runGhCmd("pr", "create",
		"--title", title,
		"--body", body,
		"--base", baseBranch,
		"--head", branch,
		"-R", repoFullName)
	return err
}

// PRExists checks if a PR exists for an issue/PR number
func (g *GithubService) PRExists(owner, repo string, num int) bool {
	repoFullName := g.repoFullName(owner, repo)
	_, err := g.runGhCmd("pr", "view", strconv.Itoa(num), "--json", "url", "-R", repoFullName)
	return err == nil
}

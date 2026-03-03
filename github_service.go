// GithubService handles GitHub API and CLI interactions
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// GithubService provides GitHub interactions using gh CLI and HTTP API
type GithubService struct {
	token string
}

// NewGithubService creates a new GithubService
func NewGithubService(token string) *GithubService {
	return &GithubService{token: token}
}

// GetCurrentUserID gets the current GitHub user ID
func (g *GithubService) GetCurrentUserID() (string, error) {
	cmd := exec.Command("gh", "api", "user", "--jq", ".id")
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get user ID: %v", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// AddPRComment adds a comment to a PR
func (g *GithubService) AddPRComment(owner, repo string, prNumber int, body string) error {
	repoFullName := owner + "/" + repo
	cmd := exec.Command("gh", "pr", "comment", strconv.Itoa(prNumber), "--body", body, "-R", repoFullName)
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add comment: %v, output: %s", err, string(output))
	}
	return nil
}

// AddIssueComment adds a comment to an issue
func (g *GithubService) AddIssueComment(owner, repo string, issueNumber int, body string) error {
	repoFullName := owner + "/" + repo
	cmd := exec.Command("gh", "issue", "comment", strconv.Itoa(issueNumber), "--body", body, "-R", repoFullName)
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add issue comment: %v, output: %s", err, string(output))
	}
	return nil
}

// GetPRBranch gets the branch name for a PR
func (g *GithubService) GetPRBranch(repo string, prNum int) string {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNum), "--json", "headRefName", "-R", repo)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[GITHUB] Failed to get PR #%d branch: %v, output: %s", prNum, err, string(output))
		return ""
	}

	var result struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[GITHUB] Failed to parse PR branch: %v", err)
		return ""
	}

	return result.HeadRefName
}

// GetPRBaseBranch gets the base branch name for a PR
func (g *GithubService) GetPRBaseBranch(repo string, prNum int) string {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNum), "--json", "baseRefName", "-R", repo)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[GITHUB] Failed to get PR #%d base branch: %v, output: %s", prNum, err, string(output))
		return ""
	}

	var result struct {
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		log.Printf("[GITHUB] Failed to parse PR base branch: %v", err)
		return ""
	}

	return result.BaseRefName
}

// GetPRInfo gets PR information using gh CLI
func (g *GithubService) GetPRInfo(repo string, prNum int) (map[string]interface{}, error) {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNum), "--json", "number,title,body,headRefName,baseRefName", "-R", repo)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get PR info: %v, output: %s", err, string(output))
	}

	var pr map[string]interface{}
	if err := json.Unmarshal(output, &pr); err != nil {
		return nil, err
	}
	return pr, nil
}

// CreatePR creates a PR for a branch
func (g *GithubService) CreatePR(repo, title, body, baseBranch, headBranch string) (int, error) {
	cmd := exec.Command("gh", "pr", "create",
		"--title", title,
		"--body", body,
		"--base", baseBranch,
		"--head", headBranch,
		"-R", repo,
	)
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to create PR: %v, output: %s", err, string(output))
	}

	// Parse PR URL from output and extract number
	// gh pr create outputs the URL like https://github.com/owner/repo/pull/123
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "github.com") && strings.Contains(line, "/pull/") {
			parts := strings.Split(line, "/pull/")
			if len(parts) == 2 {
				prNum, err := strconv.Atoi(strings.TrimSuffix(parts[1], "\n"))
				if err == nil {
					return prNum, nil
				}
			}
		}
	}

	return 0, nil
}

// CheckPRExists checks if a PR exists for an issue
func (g *GithubService) CheckPRExists(repo string, issueNum int) (bool, error) {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(issueNum), "--json", "url,number", "-R", repo)
	_, err := cmd.CombinedOutput()
	if err != nil {
		return false, nil
	}
	return true, nil
}

// GetPRNumberFromBranch gets the PR number for a branch
func (g *GithubService) GetPRNumberFromBranch(repo, branch string) (int, error) {
	cmd := exec.Command("gh", "pr", "list", "--head", branch, "--json", "number", "-R", repo, "--limit", "1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to find PR for branch: %v", err)
	}

	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(output, &prs); err != nil {
		return 0, err
	}

	if len(prs) > 0 {
		return prs[0].Number, nil
	}
	return 0, nil
}

// AddPRReaction adds a reaction to a PR (using HTTP API)
func (g *GithubService) AddPRReaction(owner, repo string, prNumber int, reaction string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reactions", owner, repo, prNumber)
	payload := fmt.Sprintf(`{"content":"%s"}`, reaction)
	_, err := g.do("POST", url, []byte(payload))
	return err
}

// RemovePRReaction removes a reaction from a PR (using HTTP API)
func (g *GithubService) RemovePRReaction(owner, repo string, prNumber int, reactionID int) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reactions/%d", owner, repo, prNumber, reactionID)
	_, err := g.do("DELETE", url, nil)
	return err
}

// do makes an authenticated HTTP request to GitHub API
func (g *GithubService) do(method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return respBody, fmt.Errorf("GitHub API error: %d - %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// Package services provides independent services for gh-claude
package services

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
)

// GithubService handles GitHub interactions
type GithubService struct {
	token string
}

// NewGithubService creates a new GithubService
func NewGithubService(token string) *GithubService {
	return &GithubService{token: token}
}

// do makes an HTTP request to GitHub API
func (g *GithubService) do(method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Body = nil
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

// AddPRReaction adds a reaction to a PR
func (g *GithubService) AddPRReaction(owner, repo string, prNumber int, reaction string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reactions", owner, repo, prNumber)
	payload := fmt.Sprintf(`{"content":"%s"}`, reaction)
	_, err := g.do("POST", url, []byte(payload))
	return err
}

// RemovePRReaction removes a reaction from a PR
func (g *GithubService) RemovePRReaction(owner, repo string, prNumber int, reactionID int) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reactions/%d", owner, repo, prNumber, reactionID)
	_, err := g.do("DELETE", url, nil)
	return err
}

// GetCurrentUserID returns the current GitHub user ID
func (g *GithubService) GetCurrentUserID() (string, error) {
	url := "https://api.github.com/user"
	resp, err := g.do("GET", url, nil)
	if err != nil {
		return "", err
	}
	var user map[string]interface{}
	if err := json.Unmarshal(resp, &user); err != nil {
		return "", err
	}
	if id, ok := user["id"].(float64); ok {
		return strconv.FormatInt(int64(id), 10), nil
	}
	return "", nil
}

// AddPRComment adds a comment to a PR using gh CLI
func (g *GithubService) AddPRComment(owner, repo string, prNumber int, body string) error {
	repoFullName := owner + "/" + repo
	cmd := exec.Command("gh", "pr", "comment", strconv.Itoa(prNumber), "--body", body, "-R", repoFullName)

	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add comment: %v, output: %s", err, string(output))
	}
	log.Printf("[GithubService] Added PR comment to %s #%d", repoFullName, prNumber)
	return nil
}

// AddIssueComment adds a comment to an issue using gh CLI
func (g *GithubService) AddIssueComment(owner, repo string, issueNumber int, body string) error {
	repoFullName := owner + "/" + repo
	cmd := exec.Command("gh", "issue", "comment", strconv.Itoa(issueNumber), "--body", body, "-R", repoFullName)

	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), "GH_TOKEN="+g.token)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add issue comment: %v, output: %s", err, string(output))
	}
	log.Printf("[GithubService] Added issue comment to %s #%d", repoFullName, issueNumber)
	return nil
}

// GetPR returns PR details
func (g *GithubService) GetPR(owner, repo string, prNumber int) (map[string]interface{}, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	resp, err := g.do("GET", url, nil)
	if err != nil {
		return nil, err
	}
	var pr map[string]interface{}
	if err := json.Unmarshal(resp, &pr); err != nil {
		return nil, err
	}
	return pr, nil
}

// GetToken returns the GitHub token
func (g *GithubService) GetToken() string {
	return g.token
}

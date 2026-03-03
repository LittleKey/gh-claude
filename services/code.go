// Package services provides independent services for gh-claude
package services

import (
	"fmt"
	"log"
	"os"
	"os/exec"
)

// CodeService handles Claude Code execution
type CodeService struct {
	anthropicAPIKey string
	apiBaseURL     string
	model          string
}

// NewCodeService creates a new CodeService
func NewCodeService(anthropicAPIKey, apiBaseURL, model string) *CodeService {
	return &CodeService{
		anthropicAPIKey: anthropicAPIKey,
		apiBaseURL:     apiBaseURL,
		model:          model,
	}
}

// Run executes a Claude Code task
func (c *CodeService) Run(worktreePath, task string, debug bool) (string, error) {
	log.Printf("[CodeService] Running Claude Code task in %s", worktreePath)

	// Build environment
	env := os.Environ()
	if c.anthropicAPIKey != "" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+c.anthropicAPIKey)
		env = append(env, "CLAUDE_API_KEY="+c.anthropicAPIKey)
	}
	if c.apiBaseURL != "" {
		env = append(env, "ANTHROPIC_BASE_URL="+c.apiBaseURL)
		env = append(env, "CLAUDE_API_BASE_URL="+c.apiBaseURL)
	}
	if c.model != "" {
		env = append(env, "ANTHROPIC_MODEL="+c.model)
		env = append(env, "CLAUDE_MODEL="+c.model)
	}

	// Prepare command
	args := []string{"--dangerously-skip-permissions"}
	if debug {
		args = append(args, "-d")
	}
	args = append(args, task)

	cmd := exec.Command("claude", args...)
	cmd.Dir = worktreePath
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude execution failed: %w", err)
	}

	log.Printf("[CodeService] Claude Code task completed")
	return "", nil
}

// RunBuild executes a build task
func (c *CodeService) RunBuild(worktreePath, buildTask string) (string, error) {
	log.Printf("[CodeService] Running build task in %s", worktreePath)

	// Build environment
	env := os.Environ()
	if c.anthropicAPIKey != "" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+c.anthropicAPIKey)
		env = append(env, "CLAUDE_API_KEY="+c.anthropicAPIKey)
	}
	if c.apiBaseURL != "" {
		env = append(env, "ANTHROPIC_BASE_URL="+c.apiBaseURL)
		env = append(env, "CLAUDE_API_BASE_URL="+c.apiBaseURL)
	}
	if c.model != "" {
		env = append(env, "ANTHROPIC_MODEL="+c.model)
		env = append(env, "CLAUDE_MODEL="+c.model)
	}

	// Prepare command
	args := []string{"--dangerously-skip-permissions", buildTask}

	cmd := exec.Command("claude", args...)
	cmd.Dir = worktreePath
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build execution failed: %w", err)
	}

	log.Printf("[CodeService] Build task completed")
	return "", nil
}

// GetEnvVars returns environment variables for Claude
func (c *CodeService) GetEnvVars() []string {
	env := []string{}
	if c.anthropicAPIKey != "" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+c.anthropicAPIKey)
		env = append(env, "CLAUDE_API_KEY="+c.anthropicAPIKey)
	}
	if c.apiBaseURL != "" {
		env = append(env, "ANTHROPIC_BASE_URL="+c.apiBaseURL)
		env = append(env, "CLAUDE_API_BASE_URL="+c.apiBaseURL)
	}
	if c.model != "" {
		env = append(env, "ANTHROPIC_MODEL="+c.model)
		env = append(env, "CLAUDE_MODEL="+c.model)
	}
	return env
}

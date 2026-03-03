// Package code provides Claude Code execution services
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type CodeService struct{}

// NewCodeService creates a new Code service
func NewCodeService() *CodeService {
	return &CodeService{}
}

// CodeResult holds the execution result
type CodeResult struct {
	Output string
	Error  error
}

// UpdateSettings updates Claude settings.json with environment variables
func (s *CodeService) UpdateSettings() error {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir = "/home/appuser"
	}

	settingsDir := filepath.Join(homeDir, ".claude")
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// Create directory if not exists
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create settings directory: %w", err)
	}

	// Read existing or create new settings
	settings := map[string]interface{}{
		"env":                               map[string]string{},
		"skipDangerousModePermissionPrompt": true,
	}

	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("failed to parse existing settings: %w", err)
		}
	}

	// Update env vars
	env := map[string]string{}
	if existingEnv, ok := settings["env"].(map[string]interface{}); ok {
		for k, v := range existingEnv {
			if strVal, ok := v.(string); ok {
				env[k] = strVal
			}
		}
	}

	// Set custom env vars from environment
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		env["ANTHROPIC_AUTH_TOKEN"] = apiKey
	}
	if baseURL := os.Getenv("CLAUDE_API_BASE_URL"); baseURL != "" {
		env["ANTHROPIC_BASE_URL"] = baseURL
	}
	if model := os.Getenv("CLAUDE_MODEL"); model != "" {
		env["ANTHROPIC_MODEL"] = model
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = model
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = model
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = model
		env["ANTHROPIC_SMALL_FAST_MODEL"] = model
	}
	env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"

	settings["env"] = env

	// Write back
	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, newData, 0o644); err != nil {
		return fmt.Errorf("failed to write settings: %w", err)
	}

	return nil
}

// UpdateSettingsForWorktree updates settings for a specific worktree execution
func (s *CodeService) UpdateSettingsForWorktree(worktreePath string) error {
	settingsPath := filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return err
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}

	env := map[string]string{}
	if existingEnv, ok := settings["env"].(map[string]interface{}); ok {
		for k, v := range existingEnv {
			if strVal, ok := v.(string); ok {
				env[k] = strVal
			}
		}
	}

	// Set custom env vars
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		env["ANTHROPIC_AUTH_TOKEN"] = apiKey
	}
	if baseURL := os.Getenv("CLAUDE_API_BASE_URL"); baseURL != "" {
		env["ANTHROPIC_BASE_URL"] = baseURL
	}
	if model := os.Getenv("CLAUDE_MODEL"); model != "" {
		env["ANTHROPIC_MODEL"] = model
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = model
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = model
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = model
		env["ANTHROPIC_SMALL_FAST_MODEL"] = model
	}
	env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"

	settings["env"] = env
	if newData, err := json.MarshalIndent(settings, "", "  "); err == nil {
		return os.WriteFile(settingsPath, newData, 0o644)
	}

	return nil
}

// BuildTask returns a build task description
func (s *CodeService) BuildTask() string {
	return `请执行以下操作：
1. 安装项目依赖
2. 编译项目确认可以构建成功
3. 如果有任何依赖缺失，请自动安装

完成后报告结果。`
}

// RunBuild runs the build step in a worktree
func (s *CodeService) RunBuild(worktreePath string) CodeResult {
	buildTask := s.BuildTask()
	return s.Run(worktreePath, buildTask)
}

// Run runs a Claude task in a worktree
func (s *CodeService) Run(worktreePath, task string) CodeResult {
	cmd := exec.Command("claude", "--dangerously-skip-permissions", task)
	cmd.Dir = worktreePath
	cmd.Env = s.buildEnv()

	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout

	err := cmd.Run()
	output := stdout.String() + stderr.String()

	if err != nil {
		return CodeResult{
			Output: output,
			Error:  fmt.Errorf("claude error: %v", err),
		}
	}

	return CodeResult{
		Output: output,
		Error:  nil,
	}
}

// RunWithOutput runs a Claude task and streams output to the given writer
func (s *CodeService) RunWithOutput(worktreePath, task string, output func(string)) CodeResult {
	cmd := exec.Command("claude", "--dangerously-skip-permissions", task)
	cmd.Dir = worktreePath
	cmd.Env = s.buildEnv()

	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout

	err := cmd.Run()
	output(stdout.String())
	output(stderr.String())

	if err != nil {
		return CodeResult{
			Output: stdout.String() + stderr.String(),
			Error:  fmt.Errorf("claude error: %v", err),
		}
	}

	return CodeResult{
		Output: stdout.String() + stderr.String(),
		Error:  nil,
	}
}

// buildEnv builds the environment variables for Claude execution
func (s *CodeService) buildEnv() []string {
	env := []string{
		"CLAUDE_API_KEY=" + os.Getenv("ANTHROPIC_API_KEY"),
		"ANTHROPIC_API_KEY=" + os.Getenv("ANTHROPIC_API_KEY"),
	}

	// Add custom API base URL if set
	if baseURL := os.Getenv("CLAUDE_API_BASE_URL"); baseURL != "" {
		env = append(env, "CLAUDE_API_BASE_URL="+baseURL)
	}

	// Add custom model if set
	if model := os.Getenv("CLAUDE_MODEL"); model != "" {
		env = append(env, "CLAUDE_MODEL="+model)
	}

	return env
}

// InitSettings initializes Claude settings and git config
func (s *CodeService) InitSettings() error {
	// Update settings
	if err := s.UpdateSettings(); err != nil {
		return err
	}

	// Configure git safe.directory
	cmd := exec.Command("git", "config", "--global", "--add", "safe.directory", "*")
	cmd.Run()

	// Configure git SSL
	cmd = exec.Command("git", "config", "--global", "http.sslCAInfo", "/etc/ssl/certs/ca-certificates.crt")
	cmd.Run()

	return nil
}

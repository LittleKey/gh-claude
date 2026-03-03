// CodeService handles Claude Code execution
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// CodeService provides Claude Code execution capabilities
type CodeService struct {
	skipBuild bool
}

// NewCodeService creates a new CodeService
func NewCodeService(skipBuild bool) *CodeService {
	return &CodeService{
		skipBuild: skipBuild,
	}
}

// EnsureClaudeSettings ensures Claude settings.json is properly configured
func (c *CodeService) EnsureClaudeSettings() error {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir = "/home/appuser"
	}

	settingsDir := filepath.Join(homeDir, ".claude")
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// Create directory if not exists
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		log.Printf("[WARN] Failed to create settings directory: %v", err)
		return err
	}

	// Read existing or create new settings
	settings := map[string]interface{}{
		"env":                               map[string]string{},
		"skipDangerousModePermissionPrompt": true,
	}

	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			log.Printf("[WARN] Failed to parse existing settings: %v", err)
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
		log.Printf("[WARN] Failed to marshal settings: %v", err)
		return err
	}

	if err := os.WriteFile(settingsPath, newData, 0o644); err != nil {
		log.Printf("[WARN] Failed to write settings: %v", err)
		return err
	}

	log.Printf("[CODE] Claude settings initialized at %s", settingsPath)

	// Configure git safe.directory
	cmd := exec.Command("git", "config", "--global", "--add", "safe.directory", "*")
	cmd.Run()

	// Configure git SSL
	cmd = exec.Command("git", "config", "--global", "http.sslCAInfo", "/etc/ssl/certs/ca-certificates.crt")
	cmd.Run()

	return nil
}

// UpdateSettingsForWorktree updates Claude settings for a specific worktree
func (c *CodeService) UpdateSettingsForWorktree(worktreePath string) error {
	settingsPath := filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err == nil {
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
				os.WriteFile(settingsPath, newData, 0o644)
			}
		}
	}
	return nil
}

// RunBuildStep runs the build step to install dependencies and compile
func (c *CodeService) RunBuildStep(worktreePath string) (string, error) {
	if c.skipBuild {
		return "", nil
	}

	log.Printf("[CODE] Running build step in %s", worktreePath)

	buildTask := "请执行以下操作：\n1. 安装项目依赖\n2. 编译项目确认可以构建成功\n3. 如果有任何依赖缺失，请自动安装\n\n完成后报告结果。"

	cmd := exec.Command("claude", "--dangerously-skip-permissions", buildTask)
	cmd.Dir = worktreePath
	cmd.Env = c.buildEnv()

	output, err := cmd.CombinedOutput()

	if len(output) > 0 {
		log.Printf("[CODE] Build step output: %s", truncate(string(output), 500))
	}

	if err != nil {
		return string(output), fmt.Errorf("build step failed: %w", err)
	}

	log.Printf("[CODE] Build step completed successfully")
	return string(output), nil
}

// RunTask runs the actual Claude task
func (c *CodeService) RunTask(worktreePath, task string) (string, error) {
	log.Printf("[CODE] Running Claude task in %s", worktreePath)

	cmd := exec.Command("claude", "--dangerously-skip-permissions", task)
	cmd.Dir = worktreePath
	cmd.Env = c.buildEnv()

	output, err := cmd.CombinedOutput()

	if len(output) > 0 {
		log.Printf("[CODE] Claude output: %s", truncate(string(output), 500))
	}

	if err != nil {
		return string(output), fmt.Errorf("claude execution failed: %w", err)
	}

	return string(output), nil
}

// buildEnv builds the environment variables for Claude execution
func (c *CodeService) buildEnv() []string {
	env := append(os.Environ(),
		"CLAUDE_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"),
		"ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"),
	)

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

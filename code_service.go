package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CodeService handles Claude Code execution
type CodeService struct {
	settingsPath string
}

// NewCodeService creates a new CodeService
func NewCodeService() *CodeService {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir = "/home/appuser"
	}
	return &CodeService{
		settingsPath: filepath.Join(homeDir, ".claude", "settings.json"),
	}
}

// Run executes a Claude Code task in the specified worktree
func (c *CodeService) Run(worktreePath, task string) (string, error) {
	log.Printf("[EXEC] Running Claude Code: %s", truncate(task, 100))

	// Run Claude with proper environment
	claudeCmd := exec.Command("claude", "--dangerously-skip-permissions", task)
	claudeCmd.Dir = worktreePath
	claudeCmd.Env = append(os.Environ(),
		"CLAUDE_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"),
		"ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"),
	)
	// Add custom API base URL if set
	if baseURL := os.Getenv("CLAUDE_API_BASE_URL"); baseURL != "" {
		claudeCmd.Env = append(claudeCmd.Env, "CLAUDE_API_BASE_URL="+baseURL)
	}
	// Add custom model if set
	if model := os.Getenv("CLAUDE_MODEL"); model != "" {
		claudeCmd.Env = append(claudeCmd.Env, "CLAUDE_MODEL="+model)
	}

	output, err := claudeCmd.CombinedOutput()
	result := string(output)

	if len(output) > 0 {
		log.Printf("[EXEC] Claude output: %s", truncate(result, 500))
	}

	if err != nil {
		return result, fmt.Errorf("claude error: %w", err)
	}

	return result, nil
}

// buildEnvSettings builds the environment variables map from existing settings
func (c *CodeService) buildEnvSettings(existingEnv map[string]interface{}) map[string]string {
	env := map[string]string{}
	if existingEnv != nil {
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
	return env
}

// InitSettings initializes the Claude settings.json with custom env vars
func (c *CodeService) InitSettings() error {
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
			log.Printf("[WARN] Failed to parse existing settings: %v", err)
		}
	}

	// Update env vars
	var existingEnv map[string]interface{}
	if e, ok := settings["env"].(map[string]interface{}); ok {
		existingEnv = e
	}
	settings["env"] = c.buildEnvSettings(existingEnv)

	// Write back
	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, newData, 0o644); err != nil {
		return fmt.Errorf("failed to write settings: %w", err)
	}

	log.Printf("[INIT] Claude settings initialized at %s", settingsPath)
	return nil
}

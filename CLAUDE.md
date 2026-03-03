# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

gh-claude is a Claude Code Runner Service that executes Claude Code tasks on GitHub repositories. It uses git worktrees for branch isolation, allowing concurrent execution across different branches/repos while ensuring one task per branch at a time.

## Commands

```bash
# Build the service
go build -o gh-claude

# Run the service
./gh-claude [-port=3456] [-work-dir=/tmp/claude-runner] [-max-concurrent=5]

# Environment variables (required)
export GH_TOKEN=your_github_token
export ANTHROPIC_API_KEY=your_anthropic_api_key
```

## Architecture

The service is a single-file Go application (main.go) providing an HTTP API:

- **Branch-level locking**: One task per branch at a time. Tasks queue if branch is busy.
- **Worktree isolation**: Each branch gets its own git worktree in `/tmp/claude-runner/{owner-repo}/{branch}`
- **Concurrent execution**: Configurable max concurrent tasks (default 5) across all branches

### API Endpoints

| Endpoint   | Method | Description                                 |
| ---------- | ------ | ------------------------------------------- |
| `/run`     | POST   | Submit a new task                           |
| `/status`  | GET    | Get task status by `task_id`                |
| `/queue`   | GET    | List all tasks and branch queues            |
| `/cancel`  | POST   | Cancel a queued task by `task_id`           |
| `/webhook` | POST   | GitHub webhook receiver                     |
| `/health`  | GET    | Health check with active/queued task counts |

### Request/Response Format

**POST /run**:

```json
{
  "repo": "owner/repo",
  "task": "Task description for Claude Code",
  "branch": "optional-branch-name",
  "pr": 123,
  "debug": false
}
```

### GitHub Integration

- Adds "eyes" reaction to PR when task starts
- Comments on PR with success/failure results
- Supports webhook triggers from issue comments (`@claude` or `/claude`) and PR review events

### Task Execution Flow

1. Task submitted to queue (or branch-specific queue if branch busy)
2. Worktree created/checked out for the branch
3. Claude Code runs with the specified task
4. Changes committed and pushed to branch
5. GitHub notified of completion/failure

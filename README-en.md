# gh-claude

🌐 [中文 README](./README.md)

Claude Code Runner Service - A tool for automatically executing Claude Code tasks via GitHub Webhooks.

## Features

- **GitHub Webhook Integration**: Listens to Issue and PR comments, automatically triggering tasks
- **Git Worktree Isolation**: Each branch uses an isolated worktree to prevent conflicts
- **Branch-level Locking**: Only one task can run per branch at a time
- **Concurrent Execution**: Configurable maximum number of concurrent tasks
- **Auto-push**: Automatically commits and pushes changes after task completion
- **PR Status Sync**: Posts comments on PRs with task execution results

## GitHub Configuration

### 1. Create a Personal Access Token

1. Go to GitHub → Settings → Developer settings → Personal access tokens → Tokens (classic)
2. Generate a new token with the following permissions:
   - `repo` (Full repository access)
   - `workflow` (If you need to trigger GitHub Actions)

### 2. Configure Webhook

1. Go to Repository → Settings → Webhooks → Add webhook
2. Configure the following:
   - **Payload URL**: `http://your-server-ip:3456/webhook`
   - **Content type**: `application/json`
   - **Events**: Select the following:
     - `Issue comments`
     - `Pull request reviews`
     - `Pull request review comments`

### 3. How to Trigger Tasks

**Method 1: Issue Comment**

```
@claude Fix this bug: user login failure doesn't show error message
```

**Method 2: PR Review**
Add a task description in a PR Review

**Method 3: PR Review Comment**
Add a comment on the PR:

```
/claude Refactor this function to make naming clearer
```

## Local Deployment

### Requirements

- Go 1.26+
- Git
- Claude Code CLI (`claude` command)
- GitHub Token
- Anthropic API Key

### Build

```bash
make build
```

Or manually build:

```bash
go build -o gh-claude main.go
```

### Run

```bash
export GH_TOKEN=your_github_token
export ANTHROPIC_API_KEY=your_anthropic_api_key

./gh-claude [-port=3456] [-work-dir=/tmp/claude-runner] [-max-concurrent=5]
```

### Run with Docker

```bash
docker run -d \
  --name gh-claude \
  -p 3456:3456 \
  -v /tmp/claude-runner:/tmp/claude-runner \
  -e GH_TOKEN=your_github_token \
  -e ANTHROPIC_API_KEY=your_anthropic_api_key \
  gh-claude
```

### Run as a Systemd Service (Linux)

Create `/etc/systemd/system/gh-claude.service`:

```ini
[Unit]
Description=gh-claude service
After=network.target

[Service]
Type=simple
User=your-user
WorkingDirectory=/path/to/gh-claude
Environment=GH_TOKEN=your_github_token
Environment=ANTHROPIC_API_KEY=your_anthropic_api_key
ExecStart=/path/to/gh-claude/gh-claude
Restart=always

[Install]
WantedBy=multi-user.target
```

Enable the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable gh-claude
sudo systemctl start gh-claude
```

## API Endpoints

| Endpoint   | Method | Description                    |
|------------|--------|--------------------------------|
| `/run`     | POST   | Submit a new task              |
| `/status`  | GET    | Get task status by task_id     |
| `/queue`   | GET    | List all tasks and queues      |
| `/cancel`  | POST   | Cancel a queued task           |
| `/webhook` | POST   | GitHub webhook receiver        |
| `/health`  | GET    | Health check                   |

### Submit Task Example

```bash
curl -X POST http://localhost:3456/run \
  -H "Content-Type: application/json" \
  -d '{
    "repo": "owner/repo",
    "task": "Add user login feature",
    "branch": "feature/login"
  }'
```

## Workflow

1. **Receive Webhook**: Service listens for GitHub events
2. **Parse Task**: Extract task description and target repo/branch
3. **Create Worktree**: Create worktree at `/tmp/claude-runner/{owner-repo}/{branch}`
4. **Execute Task**: Run `claude` command to perform the task
5. **Commit & Push**: Automatically commit changes and push to remote
6. **Feedback**: Add execution result comment on the PR

## Agent Integration

gh-claude supports AI Agents (like OpenCLAW) to drive code modifications through GitHub.

### Install Skill for Claude Code

Copy the skill file to Claude Code configuration directory:

```bash
mkdir -p ~/.claude/skills
cp skills/gh-claude.md ~/.claude/skills/
```

### Install Skill for OpenCLAW

OpenCLAW automatically loads skill files from the project's `skills/` directory.

Since this documentation exists in `skills/gh-claude.md`, OpenCLAW can directly use this skill.

To include this skill in OpenCLAW's workflow, ensure the `skills/gh-claude.md` file exists in the project root.

### Features

- **Issue Trigger**: Use `@claude` or `/claude` at the start of an Issue comment
- **PR Trigger**: Use `@claude` or `/claude` in a PR review or review comment
- **Auto-execute**: gh-claude automatically creates branches, executes tasks, and commits code
- **Result Feedback**: Execution results are posted as comments on the original Issue/PR

### Usage Example

```bash
# 1. Create an Issue
gh issue create --title "Fix login bug" --body "User login fails silently"

# 2. Trigger the task
gh issue comment 1 --body "@claude Fix the silent login failure"

# 3. Get results
gh issue view 1 --comments
```

For detailed usage instructions, refer to [skills/gh-claude.md](skills/gh-claude.md).

## Configuration

| Parameter          | Default Value        | Description                              |
|--------------------|---------------------|------------------------------------------|
| `-port`            | 3456                | HTTP server port                         |
| `-work-dir`        | /tmp/claude-runner  | Worktree storage directory               |
| `-max-concurrent`  | 5                   | Maximum concurrent tasks                 |
| `-github-token`    | env var GH_TOKEN    | GitHub access token                      |
| `-webhook-url`     | empty               | Callback URL after task completion       |

## License

MIT

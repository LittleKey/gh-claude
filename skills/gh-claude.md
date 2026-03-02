# gh-claude Skill

This skill enables AI agents (like OpenCLAW) to interact with gh-claude for automated code modifications through GitHub.

## When to Use

Use gh-claude when:
- Modifying code in a GitHub repository
- Adding new features to a GitHub repository
- Fixing bugs in a GitHub repository
- Refactoring code in a GitHub repository
- Adding or updating tests in a GitHub repository

## Overview

gh-claude is a GitHub bot that executes Claude Code tasks on repositories. It responds to triggers in Issues and Pull Requests, performs code modifications, and reports results back.

## Triggering Tasks

### Issue Comments

To trigger a task from an issue, add a comment starting with `@claude` or `/claude`:

```
@claude Fix the bug where user login failure doesn't show error message
```

or

```
/claude Add user authentication feature
```

**Note**: If triggered from an issue without an associated PR, gh-claude will create a branch named `fix-issue-{issue_number}`.

### Pull Request Reviews

To trigger a task from a PR, add a task description in a review or review comment:

```
@claude Refactor this function to improve readability
```

## Finding Related PRs and Issues

### Issue → PR Mapping

When a task is triggered from Issue #N:
1. gh-claude creates/uses branch: `fix-issue-{N}`
2. The PR title typically contains: "Fix issue #N"

To find the PR from an issue number:
1. Look for branches matching `fix-issue-{N}`
2. Search for PRs with title "Fix issue #N"

### PR → Issue Mapping

From a PR, you can find the related issue by:
1. Checking the branch name (if it follows `fix-issue-{N}` pattern)
2. Looking at the PR title for "Fix issue #N" pattern

## Getting Execution Results

### Task ID Format

When a task is submitted, it receives an ID in format:
```
task-{timestamp}-{branch}
```
Example: `task-1700000000-fix-issue-2`

### Result Retrieval

Results are posted as comments on the original trigger:

**Success**:
```
✅ **Task Completed**

Branch `fix-issue-2` has been updated and pushed.

**Result:**
{result output}

Duration: 1m30s
```

**Failure**:
```
❌ **Task Failed**

Branch `fix-issue-2`

Error: {error message}

Task: {task description}
```

### Polling for Results

To get results:
1. Wait for a comment on the Issue/PR
2. Use GitHub API to poll for new comments
3. Check for messages starting with "✅ **Task Completed**" or "❌ **Task Failed**"

## Usage Example for Agents

### Step 1: Create an Issue

```bash
# Create issue to trigger the task
gh issue create --title "Fix login error handling" --body "Need to add error display"
```

### Step 2: Trigger the Task

```bash
# Comment on the issue to trigger
gh issue comment 1 --body "@claude Add error message display for failed login attempts"
```

### Step 3: Wait for Completion

Poll for the result comment on the issue or PR.

### Step 4: Check Results

```bash
# Get the issue comments to find results
gh issue view 1 --comments
```

Or if there's a PR:
```bash
# Get PR comments
gh pr view 1 --comments
```

## API Reference

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/run` | POST | Submit a new task |
| `/status` | GET | Get task status by `task_id` |
| `/queue` | GET | List all tasks and branch queues |
| `/cancel` | POST | Cancel a queued task |
| `/webhook` | POST | GitHub webhook receiver |
| `/health` | GET | Health check |

## Notes

- Only one task can run per branch at a time
- Tasks are queued if the branch is busy
- The agent must have appropriate GitHub permissions to read/write issues and PRs

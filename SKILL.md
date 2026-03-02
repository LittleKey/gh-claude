# SKILL.md

## What It Does

gh-claude is a GitHub bot that executes Claude Code tasks on GitHub repositories. It uses git worktrees for branch isolation, allowing concurrent execution across different branches/repos while ensuring one task per branch at a time.

## When to Use

Use gh-claude when:
- Modifying code in a GitHub repository
- Adding new features to a GitHub repository
- Fixing bugs in a GitHub repository
- Refactoring code in a GitHub repository
- Adding or updating tests in a GitHub repository

## Trigger Method

1. Create an issue describing the task
2. Comment on the issue with `@claude` to trigger the task

Alternatively, you can use the `/run` API endpoint directly with:
```json
{
  "repo": "owner/repo",
  "task": "Task description for Claude Code",
  "branch": "optional-branch-name",
  "pr": 123
}
```

## Common Use Cases

- **Code modifications**: Making changes to existing code files
- **Adding features**: Implementing new functionality in a repository
- **Fixing bugs**: Identifying and fixing issues in the codebase
- **Adding tests**: Writing or updating test files
- **Refactoring**: Improving code structure without changing behavior
- **Documentation**: Adding or updating README and other docs

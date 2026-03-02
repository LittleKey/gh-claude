# 代码重构计划：抽出独立Service

## Context

当前 `main.go` 是一个约1100行的单文件，混合了三种不同类型的逻辑：
1. **GitHub API交互** (struct GitHub): 添加PR评论、反应、获取PR信息等
2. **Git/Repo操作**: clone, worktree创建, commit, push等
3. **Claude Code执行**: 运行claude命令、配置环境变量等

这导致代码难以阅读和维护。Issue要求逐步抽出独立Service。

## 目标架构

```
main.go          - HTTP handler和调度逻辑
├── github.go    - GithubService: GitHub API交互
├── repo.go      - RepoService: Git/仓库操作
├── code.go      - CodeService: Claude Code执行能力
└── types.go     - 共享类型定义
```

## 重构步骤（逐步进行，每步验证）

### Step 1: 创建 types.go - 抽出共享类型

抽出所有共享的结构体和接口：
- Request, Response, Task, BranchLock
- 定义 Service 接口

### Step 2: 创建 github.go - GithubService

从 main.go 移出 GitHub struct 相关代码：
- `GitHub` struct
- `NewGitHub`
- `do()` 方法
- `AddPRReaction`, `RemovePRReaction`
- `AddPRComment`, `AddIssueComment`
- `GetPR`
- `GetCurrentUserID`

### Step 3: 创建 repo.go - RepoService

从 main.go 移出 git 操作相关代码：
- `RepoService` struct
- `NewRepoService`
- `SetupWorktree` - 创建worktree
- `PushChanges` - 提交推送
- `CreatePRIfNeeded` - 创建PR
- `GetPRBranch` - 获取PR分支

### Step 4: 创建 code.go - CodeService

从 main.go 移出 Claude Code 执行相关代码：
- `CodeService` struct
- `NewCodeService`
- `Run` - 执行 Claude Code
- 配置环境变量逻辑

### Step 5: 重构 main.go

更新主服务，集成三个Service：
- 删除移出的代码
- 通过接口调用各Service
- 保持现有功能不变

## 关键文件

- **源文件**: `/tmp/claude-runner/LittleKey-gh-claude/fix-issue-16/main.go`
- **目标文件**:
  - `types.go` - 共享类型
  - `github.go` - GithubService
  - `repo.go` - RepoService
  - `code.go` - CodeService
  - `main.go` - 更新后的主文件

## 验证方式

1. 每创建完一个文件，验证 Go 语法正确: `go build ./...`
2. 完整重构后，运行服务并测试 API:
   - `POST /health` - 健康检查
   - `POST /run` - 提交任务
3. 可以使用现有测试用例或手动测试

## 注意事项

- 保持现有 API 行为完全不变
- 逐步进行，每步验证
- 不一次性改动过多代码

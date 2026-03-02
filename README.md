# gh-claude

[English Version](./README_en.md)

Claude Code Runner Service - 通过 GitHub Webhook 自动执行 Claude Code 任务的工具。

## 功能特性

- **GitHub Webhook 集成**: 监听 Issue 和 PR 评论，自动触发任务
- **Git Worktree 隔离**: 每个分支使用独立的 worktree，避免冲突
- **分支级锁**: 同一分支同时只能执行一个任务
- **并发控制**: 支持配置最大并发任务数
- **自动推送**: 任务完成后自动提交并推送代码
- **PR 状态同步**: 在 PR 上添加评论反馈任务执行结果

## GitHub 配置

### 1. 创建 Personal Access Token

1. 访问 GitHub → Settings → Developer settings → Personal access tokens → Tokens (classic)
2. 生成新令牌，需要权限:
   - `repo` (完整仓库访问)
   - `workflow` (如果需要触发 GitHub Actions)

### 2. 配置 Webhook

1. 进入仓库 → Settings → Webhooks → Add webhook
2. 配置以下选项:
   - **Payload URL**: `http://你的服务器IP:3456/webhook`
   - **Content type**: `application/json`
   - **Events**: 选择以下:
     - `Issue comments`
     - `Pull request reviews`
     - `Pull request review comments`

### 3. 触发任务的方式

**方式一: Issue 评论**

```
@claude 修复这个bug: 当用户登录失败时没有显示错误提示
```

**方式二: PR Review**
在 PR Review 中添加任务描述

**方式三: PR Review Comment**
在 PR 上添加评论:

```
/claude 重构这个函数的命名使其更清晰
```

## 本地部署

### 环境要求

- Go 1.26+
- Git
- Claude Code CLI (`claude` 命令)
- GitHub Token
- Anthropic API Key

### 编译

```bash
make build
```

或手动编译:

```bash
go build -o gh-claude main.go
```

### 运行

```bash
export GH_TOKEN=your_github_token
export ANTHROPIC_API_KEY=your_anthropic_api_key

./gh-claude [-port=3456] [-work-dir=/tmp/claude-runner] [-max-concurrent=5]
```

### 使用 Docker 运行

```bash
docker run -d \
  --name gh-claude \
  -p 3456:3456 \
  -v /tmp/claude-runner:/tmp/claude-runner \
  -e GH_TOKEN=your_github_token \
  -e ANTHROPIC_API_KEY=your_anthropic_api_key \
  gh-claude
```

### 使用 Systemd 服务 (Linux)

创建 `/etc/systemd/system/gh-claude.service`:

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

启用服务:

```bash
sudo systemctl daemon-reload
sudo systemctl enable gh-claude
sudo systemctl start gh-claude
```

## API 接口

| 接口       | 方法 | 说明                  |
| ---------- | ---- | --------------------- |
| `/run`     | POST | 提交新任务            |
| `/status`  | GET  | 查询任务状态          |
| `/queue`   | GET  | 查看任务队列          |
| `/cancel`  | POST | 取消任务              |
| `/webhook` | POST | GitHub Webhook 接收器 |
| `/health`  | GET  | 健康检查              |

### 提交任务示例

```bash
curl -X POST http://localhost:3456/run \
  -H "Content-Type: application/json" \
  -d '{
    "repo": "owner/repo",
    "task": "添加用户登录功能",
    "branch": "feature/login"
  }'
```

## 工作流程

1. **接收 Webhook**: 服务监听 GitHub 事件
2. **解析任务**: 提取任务描述和目标仓库/分支
3. **创建 Worktree**: 在 `/tmp/claude-runner/{owner-repo}/{branch}` 创建 worktree
4. **执行任务**: 运行 `claude` 命令执行任务
5. **提交推送**: 自动提交修改并推送到远程
6. **反馈结果**: 在 PR 上添加执行结果评论

## Agent 集成

gh-claude 支持 AI Agent（如 OpenCLAW）通过 GitHub 自动驱动代码修改。

### Claude Code 安装 Skill

将 skill 文件复制到 Claude Code 配置目录：

```bash
mkdir -p ~/.claude/skills
cp skills/gh-claude.md ~/.claude/skills/
```

### OpenCLAW 安装 Skill

OpenCLAW 会自动从项目根目录的 `skills/` 目录加载 skill 文件。

由于此文档已在 `skills/gh-claude.md`，OpenCLAW 可以直接使用此 skill。

如需将此 skill 包含在 OpenCLAW 的工作流程中，请在项目根目录确保 `skills/gh-claude.md` 文件存在。

### 功能说明

- **Issue 触发**: 在 Issue 评论中使用 `@claude` 或 `/claude` 开头
- **PR 触发**: 在 PR review 或 review comment 中使用 `@claude` 或 `/claude`
- **自动执行**: gh-claude 自动创建分支、执行任务、提交代码
- **结果反馈**: 执行结果通过评论发布在原始 Issue/PR 上

### 使用示例

```bash
# 1. 创建 Issue
gh issue create --title "Fix login bug" --body "User login fails silently"

# 2. 触发任务
gh issue comment 1 --body "@claude Fix the silent login failure"

# 3. 获取结果
gh issue view 1 --comments
```

详细使用说明请参考 [skills/gh-claude.md](skills/gh-claude.md)。

## 配置说明

| 参数              | 默认值             | 说明                 |
| ----------------- | ------------------ | -------------------- |
| `-port`           | 3456               | HTTP 服务端口        |
| `-work-dir`       | /tmp/claude-runner | Worktree 存储目录    |
| `-max-concurrent` | 5                  | 最大并发任务数       |
| `-github-token`   | 环境变量 GH_TOKEN  | GitHub 访问令牌      |
| `-webhook-url`    | 空                 | 任务完成后的回调 URL |

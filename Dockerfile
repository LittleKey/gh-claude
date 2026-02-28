FROM golang:1.26-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

# 设置 GOCACHE 环境变量来指定缓存目录
ENV GOCACHE=/root/.cache/go-build

# Copy source code
COPY . .

# Build arguments for version info
ARG VERSION=dev
ARG BUILD_TIME=unknown

# Build the application
RUN --mount=type=cache,target="/root/.cache/go-build" CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}" \
    -x \
    -trimpath \
    -o gh-claude

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl gh && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI from Google Cloud Storage (use glibc version)
RUN CLAUDE_VERSION=$(curl -fsSL https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/latest) && \
    curl -fsSL "https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/${CLAUDE_VERSION}/linux-x64/claude" -o /usr/local/bin/claude && \
    chmod +x /usr/local/bin/claude

# Create non-root user
RUN useradd -m -s /bin/bash appuser

WORKDIR /app

COPY --from=builder /app/gh-claude .

# Set ownership
RUN chown -R appuser:appuser /app

# Switch to non-root user
USER appuser

EXPOSE 3456

CMD ["./gh-claude"]

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

FROM alpine:3.19

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY --from=builder /app/gh-claude .

EXPOSE 3456

CMD ["./gh-claude"]

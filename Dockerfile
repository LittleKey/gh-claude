FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY main.go ./
RUN go build -o gh-claude main.go

FROM alpine:3.19

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY --from=builder /app/gh-claude .

EXPOSE 3456

CMD ["./gh-claude"]

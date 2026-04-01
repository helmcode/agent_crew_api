# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /usr/local/bin/rag-mcp \
    ./cmd/rag-mcp

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates curl tzdata \
    && addgroup -S agentcrew \
    && adduser -S -G agentcrew agentcrew

COPY --from=builder /usr/local/bin/rag-mcp /usr/local/bin/rag-mcp

USER agentcrew

EXPOSE 8090

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8090/health || exit 1

ENTRYPOINT ["rag-mcp"]

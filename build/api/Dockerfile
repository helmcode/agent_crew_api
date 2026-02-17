# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /usr/local/bin/api \
    ./cmd/api

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates sqlite-libs curl \
    && addgroup -S agentcrew \
    && adduser -S -G agentcrew agentcrew

COPY --from=builder /usr/local/bin/api /usr/local/bin/api

RUN mkdir -p /data && chown agentcrew:agentcrew /data

USER agentcrew

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

ENTRYPOINT ["api"]

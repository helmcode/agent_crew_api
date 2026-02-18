.PHONY: build-api build-sidecar build-all run-api test lint clean \
	build-api-image build-agent-image build-images \
	docker-compose-up docker-compose-down docker-compose-logs

BIN_DIR := bin
IMAGE_TAG ?= latest

# ---------- Go builds ----------

build-api:
	go build -o $(BIN_DIR)/api ./cmd/api

build-sidecar:
	go build -o $(BIN_DIR)/sidecar ./cmd/sidecar

build-all: build-api build-sidecar

run-api: build-api
	LISTEN_ADDR=:3000 \
	NATS_URL=nats://localhost:4222 \
	NATS_AUTH_TOKEN=dev-token \
	DATABASE_PATH=/tmp/agentcrew.db \
	RUNTIME=docker \
	./$(BIN_DIR)/api

# ---------- Docker images ----------

build-api-image:
	docker build -t agentcrew-api:$(IMAGE_TAG) -f build/api/Dockerfile .

build-agent-image: build-sidecar
	cp $(BIN_DIR)/sidecar build/agent/sidecar
	docker build -t agentcrew-agent:$(IMAGE_TAG) -f build/agent/Dockerfile build/agent
	rm -f build/agent/sidecar

build-images: build-api-image build-agent-image

# ---------- Docker Compose ----------

docker-compose-up:
	docker compose up -d

docker-compose-down:
	docker compose down

docker-compose-logs:
	docker compose logs -f

# ---------- Test & Lint ----------

test:
	go test -v -race -cover ./...

lint:
	golangci-lint run ./...

# ---------- Clean ----------

clean:
	rm -rf $(BIN_DIR)
	go clean -testcache
	rm -f build/agent/sidecar

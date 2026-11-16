.PHONY: build-dashboard build-agent build run run-live seed fmt test lint security coverage clean \
	build-linux-dashboard build-linux-agent build-linux deploy-agent deploy-dashboard deploy \
	docker-build docker-up docker-down docker-logs docker-import docker-compact

BIN_DIR := bin
DASHBOARD_BIN := $(BIN_DIR)/klever-node-hub
AGENT_BIN := $(BIN_DIR)/klever-agent

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -s -w \
	-X github.com/CTJaeger/KleverNodeHub/internal/version.Version=$(VERSION) \
	-X github.com/CTJaeger/KleverNodeHub/internal/version.GitCommit=$(GIT_COMMIT) \
	-X github.com/CTJaeger/KleverNodeHub/internal/version.BuildTime=$(BUILD_TIME)

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

build-dashboard: $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(DASHBOARD_BIN) ./cmd/dashboard

build-agent: $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(AGENT_BIN) ./cmd/agent

build: build-dashboard build-agent

# Cross-compile for Linux (amd64)
LINUX_GOOS := linux
LINUX_GOARCH ?= amd64
LINUX_DASHBOARD_BIN := $(BIN_DIR)/klever-node-hub-linux
LINUX_AGENT_BIN := $(BIN_DIR)/klever-agent-linux

# Remote deployment
REMOTE_USER ?= root
REMOTE_HOST ?=
REMOTE_PATH ?= /opt/klever
SSH_KEY ?=
SSH_OPTS := $(if $(SSH_KEY),-i $(SSH_KEY),)

build-linux-dashboard: $(BIN_DIR)
	CGO_ENABLED=0 GOOS=$(LINUX_GOOS) GOARCH=$(LINUX_GOARCH) go build -ldflags="$(LDFLAGS)" -o $(LINUX_DASHBOARD_BIN) ./cmd/dashboard

build-linux-agent: $(BIN_DIR)
	CGO_ENABLED=0 GOOS=$(LINUX_GOOS) GOARCH=$(LINUX_GOARCH) go build -ldflags="$(LDFLAGS)" -o $(LINUX_AGENT_BIN) ./cmd/agent

build-linux: build-linux-dashboard build-linux-agent

deploy-agent: build-linux-agent
	@test -n "$(REMOTE_HOST)" || (echo "Error: REMOTE_HOST is required (e.g., make deploy-agent REMOTE_HOST=myserver)" && exit 1)
	ssh $(SSH_OPTS) $(REMOTE_USER)@$(REMOTE_HOST) 'mkdir -p $(REMOTE_PATH)'
	scp $(SSH_OPTS) $(LINUX_AGENT_BIN) $(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_PATH)/klever-agent
	ssh $(SSH_OPTS) $(REMOTE_USER)@$(REMOTE_HOST) 'systemctl restart klever-agent'

deploy-dashboard: build-linux-dashboard
	@test -n "$(REMOTE_HOST)" || (echo "Error: REMOTE_HOST is required (e.g., make deploy-dashboard REMOTE_HOST=myserver)" && exit 1)
	ssh $(SSH_OPTS) $(REMOTE_USER)@$(REMOTE_HOST) 'mkdir -p $(REMOTE_PATH)'
	scp $(SSH_OPTS) $(LINUX_DASHBOARD_BIN) $(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_PATH)/klever-node-hub
	ssh $(SSH_OPTS) $(REMOTE_USER)@$(REMOTE_HOST) 'systemctl restart klever-node-hub'

deploy: build-linux
	@test -n "$(REMOTE_HOST)" || (echo "Error: REMOTE_HOST is required" && exit 1)
	ssh $(SSH_OPTS) $(REMOTE_USER)@$(REMOTE_HOST) 'mkdir -p $(REMOTE_PATH)'
	scp $(SSH_OPTS) $(LINUX_AGENT_BIN) $(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_PATH)/klever-agent
	scp $(SSH_OPTS) $(LINUX_DASHBOARD_BIN) $(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_PATH)/klever-node-hub
	ssh $(SSH_OPTS) $(REMOTE_USER)@$(REMOTE_HOST) 'systemctl restart klever-agent klever-node-hub'

# Local Docker (see docker-compose.yml). Version metadata is exported so
# compose can pass it to the image build as build args.
DOCKER_ENV := VERSION=$(VERSION) GIT_COMMIT=$(GIT_COMMIT) BUILD_TIME=$(BUILD_TIME)

docker-build:
	$(DOCKER_ENV) docker compose build

docker-up:
	$(DOCKER_ENV) docker compose up -d --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f dashboard

# Restore a backup into the local volume: make docker-import BACKUP=hub.knhbak
# Stops the dashboard first (import refuses while it runs); prompts for the passphrase.
docker-import:
	@test -n "$(BACKUP)" || (echo "Error: BACKUP is required (e.g., make docker-import BACKUP=hub.knhbak)" && exit 1)
	@test -f "$(BACKUP)" || (echo "Error: $(BACKUP) not found" && exit 1)
	docker compose down
	docker compose run --rm -v "$(abspath $(BACKUP)):/b.knhbak:ro" dashboard --import-backup /b.knhbak --force
	@echo "Imported. Start with: make docker-up"

# Shrink the local database (removes history of unrecorded metrics + VACUUM).
docker-compact:
	docker compose down
	docker compose run --rm dashboard --compact-db

run:
	go run -ldflags="$(LDFLAGS)" ./cmd/dashboard

run-live:
	air

seed:
	go run ./cmd/seed --clear

fmt:
	goimports -w .

test:
	go test ./... -v -race

lint:
	golangci-lint run ./...
	go vet ./...

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

security:
	govulncheck ./...

clean:
	rm -rf $(BIN_DIR)
	rm -f coverage.out

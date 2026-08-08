# InChat — make contract (DEVOPS.md §3: `make dev-up`, `make test`, `make migrate`,
# `make lint`, `make build`, `make ci`). Never a bespoke script per engineer.

SHELL := /bin/bash
VENV ?= ./venv
PY := $(VENV)/bin/python

# Compose targets are pinned to the local stack.
COMPOSE := docker compose -f infra/docker/docker-compose.yml
SERVER_DIR := server

.PHONY: help dev-up dev-down dev-api run logs ps \
	migrate migrate-down migrate-new \
	build image \
	test test-race vet lint fmt \
	health smoke ci clean

help: ## Show this help
	@echo "InChat make targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk -F ':.*?## ' '{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# --- Local development (DEVOPS.md §4) ---

dev-up: ## Start PostgreSQL + Redis compose stack
	$(COMPOSE) up -d postgres redis

dev-down: ## Stop and remove the compose stack
	$(COMPOSE) down

dev-api: ## Run the api-server locally (uses server/.env.local if present)
	@bash scripts/dev.sh

run: ## Build and run the full compose stack (api-server included)
	$(COMPOSE) up --build api-server

logs: ## Follow all compose logs
	$(COMPOSE) logs -f

ps: ## Show compose status
	$(COMPOSE) ps

# --- Database (DATABASE.md, DEVOPS.md §8) ---

migrate: ## Apply all pending migrations
	$(COMPOSE) --profile tools run --rm migrate

migrate-down: ## Roll back the last migration (local use only)
	$(COMPOSE) --profile tools run --rm migrate down 1

migrate-new: ## Create a new numbered migration (usage: make migrate-new NAME=add_foo)
	@test -n "$(NAME)" || (echo "usage: make migrate-new NAME=<snake_case>"; exit 1)
	@ts=$$(date +%Y%m%d%H%M%S); \
	touch $(SERVER_DIR)/migrations/$${ts}_$(NAME).up.sql $(SERVER_DIR)/migrations/$${ts}_$(NAME).down.sql; \
	echo "created migrations/$${ts}_$(NAME).{up,down}.sql"

# --- Build (DEVOPS.md §5) ---

build: ## Compile the api-server binary into server/bin/
	cd $(SERVER_DIR) && go build -trimpath -o bin/api-server ./cmd/api-server

image: ## Build the production Docker image (tag via IMAGE=v1.0.0)
	$(COMPOSE) build api-server

# --- Quality (ENGINEERING.md §32–36, DEVOPS.md §8) ---

test: ## Run all unit tests
	cd $(SERVER_DIR) && go test ./...

test-race: ## Run all unit tests with the race detector
	cd $(SERVER_DIR) && go test -race ./...

test-integration: ## Run integration tests against the dev compose stack (make dev-up first)
	@test -f infra/docker/.env || (echo "infra/docker/.env missing — the local stack is not set up"; exit 1)
	@cd $(SERVER_DIR) && \
		APP_PG_DSN="postgres://app:$$(grep -E '^APP_DB_PASSWORD=' ../infra/docker/.env | cut -d= -f2)@localhost:5432/$$(grep -E '^POSTGRES_DB=' ../infra/docker/.env | cut -d= -f2)?sslmode=disable" \
		go test -tags integration -count=1 -p 1 ./internal/auth/infra/postgres/... ./internal/chat/infra/postgres/... ./internal/realtime/...

vet: ## Run go vet
	cd $(SERVER_DIR) && go vet ./...

fmt: ## Format Go code
	cd $(SERVER_DIR) && gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

lint: ## Run golangci-lint if installed, else fall back to go vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		cd $(SERVER_DIR) && golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; falling back to go vet"; \
		cd $(SERVER_DIR) && go vet ./...; \
	fi

ci: ## The CI gate, run locally: vet + race tests + build
	$(MAKE) vet
	$(MAKE) test-race
	$(MAKE) build

# --- Health & smoke (DEVOPS.md §8 smoke) ---

health: ## Probe /healthz and /readyz (uses the venv python, per project convention)
	$(PY) scripts/smoke.py

smoke: ## Same as health, via the compose stack
	$(PY) scripts/smoke.py

clean: ## Remove build artifacts
	rm -rf $(SERVER_DIR)/bin
	$(COMPOSE) down

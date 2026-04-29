.PHONY: build agent run dev start stop restart clean \
       docker docker-rebuild docker-compose-up docker-compose-down docker-compose-logs

PID_FILE = bin/llama-toolchest.pid

# Auto-detect GPU vendor (override with: make docker-rebuild GPU=cuda)
GPU ?= $(shell ./setup.sh detect 2>/dev/null || echo "rocm")
COMPOSE_FILE = docker-compose.$(GPU).yml

# Local development
build:
	go build -o bin/llama-toolchest ./cmd/llama-toolchest
	go build -o bin/agent ./cmd/agent

agent:
	go build -o bin/agent ./cmd/agent

run: build
	./bin/llama-toolchest --config config.yaml

dev:
	go run ./cmd/llama-toolchest --config config.yaml

start: build
	@echo "Starting llama-toolchest..."
	@./bin/llama-toolchest --config config.yaml & echo $$! > $(PID_FILE)
	@echo "PID $$(cat $(PID_FILE)) written to $(PID_FILE)"

stop:
	@if [ -f $(PID_FILE) ]; then \
		kill $$(cat $(PID_FILE)) 2>/dev/null && echo "Stopped PID $$(cat $(PID_FILE))" || true; \
		rm -f $(PID_FILE); \
	fi
	@PID=$$(lsof -ti:3000 2>/dev/null) && kill $$PID 2>/dev/null && echo "Killed process $$PID on :3000" || true

restart: stop start

clean: stop
	rm -rf bin/

# Container builds (vendor-aware)
docker:
	docker compose -f $(COMPOSE_FILE) build

docker-rebuild:
	docker compose -f $(COMPOSE_FILE) down
	docker compose -f $(COMPOSE_FILE) build --no-cache
	docker compose -f $(COMPOSE_FILE) up -d

docker-compose-up:
	docker compose -f $(COMPOSE_FILE) up -d

docker-compose-down:
	docker compose -f $(COMPOSE_FILE) down

docker-compose-logs:
	docker compose -f $(COMPOSE_FILE) logs -f

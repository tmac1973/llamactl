.PHONY: build run dev start stop restart clean

PID_FILE = bin/llamactl.pid

build:
	go build -o bin/llamactl ./cmd/llamactl

run: build
	./bin/llamactl --config config.yaml

dev:
	go run ./cmd/llamactl --config config.yaml

start: build
	@echo "Starting llamactl..."
	@./bin/llamactl --config config.yaml & echo $$! > $(PID_FILE)
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

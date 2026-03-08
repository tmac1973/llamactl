.PHONY: build run dev start stop restart clean \
       docker docker-rebuild docker-run docker-compose-up docker-compose-down docker-compose-logs

PID_FILE = bin/llamactl.pid

# Local development
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

# Container builds
docker:
	docker build -t llamactl .

docker-rebuild:
	docker compose down
	docker compose build --no-cache
	docker compose up -d

docker-run: docker
	docker run -it --rm \
		-p 3000:3000 \
		-p 8080:8080 \
		-v llamactl-data:/data \
		-v /etc/vulkan:/etc/vulkan:ro \
		-v /usr/share/vulkan:/usr/share/vulkan:ro \
		--device /dev/kfd \
		--device /dev/dri \
		--group-add video \
		--group-add render \
		--security-opt seccomp=unconfined \
		llamactl

docker-compose-up:
	docker compose up -d

docker-compose-down:
	docker compose down

docker-compose-logs:
	docker compose logs -f

.PHONY: ui build run docker

# Build UI then copy dist into embed path
ui:
	npm run build --prefix ui
	cp -r ui/dist internal/api/dist

# Build Go binary (requires ui to be built first)
build: ui
	go build -o breaker ./cmd/breaker

# Run locally without Docker (loopback, no auth required)
run: ui
	go run ./cmd/breaker serve --bind 127.0.0.1 --port 7777 --db /tmp/shipbreaker.db

# Docker multi-arch build
docker:
	docker buildx build --platform linux/amd64,linux/arm64 -t shipbreaker:latest .

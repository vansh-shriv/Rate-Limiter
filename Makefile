.PHONY: build run test test-race up down bench-build bench

build:
	go build -o bin/server ./cmd/server
run:
	go run ./cmd/server
test:
	go test ./...
test-race:
	go test -race ./...
up:
	docker compose up -d --build
down:
	docker compose down

# Benchmarking: build Linux binaries, then run loadtest/bench.sh on a Linux host or inside WSL2
# (needs redis-server + redis-cli there). Details: docs/BENCHMARKS.md
bench-build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/linux/server ./cmd/server
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/linux/loadgen ./cmd/loadgen
bench: bench-build
	BIN=./bin/linux REPS=3 loadtest/bench.sh

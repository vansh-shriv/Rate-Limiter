.PHONY: build run test up down
build:
	go build -o bin/server ./cmd/server
run:
	go run ./cmd/server
test:
	go test -race ./...
up:
	docker compose up -d --build
down:
	docker compose down

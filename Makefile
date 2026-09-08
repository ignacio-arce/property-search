.PHONY: fmt vet test build probe run image

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...

build:
	go build -o bin/bot ./cmd/bot

probe:
	go run ./cmd/probe

run:
	go run ./cmd/bot

image:
	docker build -t zonaprop-bot:arm64 .
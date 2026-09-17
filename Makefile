.NAME := property-search
SHELL := /bin/bash

.PHONY: fmt vet test build probe run up down logs image

# nix develop -c <cmd> es el toolchain reproducible: esta máquina no tiene Go global.
GO ?= nix develop -c go

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-all:
	$(GO) test -count=1 ./...

build:
	$(GO) build -o bin/bot ./cmd/bot

# Probe de riesgo: fetch real contra Zonaprop + parseo. No toca la base.
#   make probe URL='https://www.zonaprop.com.ar/departamentos-venta-...html'
probe:
	$(GO) run ./cmd/probe -out data/probe_capture.html $(URL)

# Dry-run local: sin TELEGRAM_BOT_TOKEN imprime las alertas por consola.
run:
	$(GO) run ./cmd/bot

# Postgres y FlareSolverr para desarrollo. El bot no se levanta acá.
up:
	docker compose up -d postgres flaresolverr

down:
	docker compose down --remove-orphans

logs:
	docker compose logs -f --tail=100

# La imagen se construye en el Pi; acá solo se valida el cross-build arm64.
image:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o bin/bot-arm64 ./cmd/bot

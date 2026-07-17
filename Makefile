SHELL := /usr/bin/env bash

.PHONY: help build test fmt web-install web-build dev-server dev-client dev-collector dev-executor kind-up kind-down kind-reset debug-deps

help:
	@echo "make build          Build the Go binary and React client"
	@echo "make test           Run Go and client checks"
	@echo "make dev-server     Run API server against forwarded Kind dependencies"
	@echo "make dev-client     Run Vite with API proxy and source maps"
	@echo "make dev-collector  Run collector with the current kubeconfig"
	@echo "make dev-executor   Run executor with the current kubeconfig"
	@echo "make kind-up        Build and deploy the complete Kind environment"
	@echo "make kind-down      Delete the Kind environment"
	@echo "make debug-deps     Port-forward Postgres and Prometheus"

build: web-build
	mkdir -p bin
	go build -o bin/kubesqueeze ./cmd/kubesqueeze

test:
	go test ./...
	cd web && npm run check

fmt:
	gofmt -w cmd internal

web-install:
	cd web && npm ci

web-build:
	cd web && npm ci && npm run build

dev-server:
	DATABASE_URL=postgres://kubesqueeze:kubesqueeze@127.0.0.1:5432/kubesqueeze?sslmode=disable \
	PROMETHEUS_URL=http://127.0.0.1:9090 \
	WEB_DIST_DIR=web/dist \
	go run ./cmd/kubesqueeze server

dev-client:
	cd web && npm run dev

dev-collector:
	DATABASE_URL=postgres://kubesqueeze:kubesqueeze@127.0.0.1:5432/kubesqueeze?sslmode=disable \
	PROMETHEUS_URL=http://127.0.0.1:9090 \
	KUBECONFIG=$${KUBECONFIG:-$${HOME}/.kube/config} \
	go run ./cmd/kubesqueeze collector

dev-executor:
	DATABASE_URL=postgres://kubesqueeze:kubesqueeze@127.0.0.1:5432/kubesqueeze?sslmode=disable \
	KUBECONFIG=$${KUBECONFIG:-$${HOME}/.kube/config} \
	go run ./cmd/kubesqueeze executor

kind-up:
	LLM_BASE_URL=$${LLM_BASE_URL:-http://host.docker.internal:11434/v1} \
	LLM_MODEL=$${LLM_MODEL:-qwen2.5-coder:7b} \
	./scripts/kind-up.sh

kind-down:
	./scripts/kind-down.sh

kind-reset: kind-down kind-up

debug-deps:
	./scripts/debug-deps.sh

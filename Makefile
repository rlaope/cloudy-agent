SHELL := /bin/bash
BIN   := cloudy
PKG   := github.com/rlaope/cloudy
VER   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(PKG)/internal/buildinfo.Version=$(VER)

.PHONY: all build test race lint vet fmt tidy verify clean run

all: lint test build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd

test:
	go test ./...

race:
	go test -race -count=1 ./internal/wiring

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint v2.12 run --timeout=5m ./...; \
	else \
		echo "golangci-lint not installed; running go vet instead"; \
		go vet ./...; \
	fi

verify: test race build lint

run: build
	./$(BIN)

clean:
	rm -f $(BIN)
	rm -rf dist

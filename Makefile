GO ?= go
BINARY ?= bin/dockerdless

.PHONY: build test vet lint run

build:
	mkdir -p $(dir $(BINARY))
	$(GO) build -o $(BINARY) ./cmd/dockerdless

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

run:
	$(GO) run ./cmd/dockerdless

GO ?= go
BINARY ?= bin/dockerdless
BENCH_PKGS ?= ./internal/streams/... ./internal/adapters/cni/...
BENCHTIME ?= 200ms

.PHONY: build test vet fmt lint verify integration hooks bench release run clean

build:
	mkdir -p $(dir $(BINARY))
	$(GO) build -o $(BINARY) ./cmd/dockerdless

test:
	$(GO) test -count=1 ./...

fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt found unformatted files:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	$(GO) vet ./...

# golangci-lint is optional: this host's build cannot load a go1.27 module.
# fmt + vet are the authoritative static gates; see docs/operations.md.
lint:
	golangci-lint run ./...

# verify is the release gate: format, static analysis, race-enabled unit tests.
verify: fmt vet
	$(GO) test -race -count=1 ./...

integration:
	$(GO) test -tags=integration -count=1 -v ./integration/...

# bench is bounded and deterministic: no network, no daemon, -run=^$ skips tests.
bench:
	$(GO) test -run=^$$ -bench=. -benchtime=$(BENCHTIME) $(BENCH_PKGS)

# release runs every gate that does not require the host's containerd/BuildKit.
release: verify
	$(MAKE) bench

run:
	$(GO) run ./cmd/dockerdless

clean:
	rm -rf bin

# hooks installs the repository's git hooks (commit-msg convention + DCO check).
hooks:
	git config core.hooksPath .githooks
	@echo "hooks installed: core.hooksPath -> .githooks"

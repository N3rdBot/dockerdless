GO ?= go
BINARY ?= bin/dockerdless
BENCH_PKGS ?= ./internal/streams/... ./internal/adapters/cni/...
BENCHTIME ?= 200ms

# Pinned developer tools, installed OUT of the Go module graph by `make tools`
# so the daemon's go.mod/go.sum stay byte-identical: golangci-lint lands in
# $(TOOLS_DIR)/bin via GOBIN, and markdownlint-cli2 comes from the committed
# package-lock.json via npm ci.
TOOLS_DIR ?= .tools
GOLANGCI_LINT_VERSION ?= v2.13.2
MARKDOWNLINT_CLI2_VERSION ?= 0.22.1

# Prefer the locally installed pinned tools over whatever is on the system PATH,
# so lint and lint-md use the versions `make tools` installed.
export PATH := $(CURDIR)/$(TOOLS_DIR)/bin:$(CURDIR)/node_modules/.bin:$(PATH)

.PHONY: build test vet fmt lint lint-fix lint-md verify integration hooks bench release run clean tools

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

# lint enforces the .golangci.yml rule set, including the hexagonal depguard
# boundaries, the integration build tag, zero issue truncation, and the
# three-group import order (standard | third-party | localmodule).
lint:
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not found - run 'make tools'"; exit 1; }
	golangci-lint config verify
	golangci-lint run ./...

# lint-fix applies the mechanical fixes golangci-lint can make (gci, gofmt,
# modernize, intrange, perfsprint, ...). Findings that remain need a hand fix.
lint-fix:
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not found - run 'make tools'"; exit 1; }
	golangci-lint run --fix ./...

# lint-md runs markdownlint-cli2 over every Markdown file; the globs and
# ignores live in .markdownlint-cli2.jsonc.
lint-md:
	@command -v markdownlint-cli2 >/dev/null || { echo "markdownlint-cli2 not found - run 'make tools'"; exit 1; }
	markdownlint-cli2 "**/*.md"

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

# tools installs both pinned developer tools locally and idempotently. It never
# touches go.mod/go.sum: `go install pkg@version` ignores the surrounding module.
tools:
	mkdir -p $(TOOLS_DIR)/bin
	GOBIN=$(CURDIR)/$(TOOLS_DIR)/bin $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	npm ci --no-audit --no-fund
	@echo "installed tools: golangci-lint $(GOLANGCI_LINT_VERSION), markdownlint-cli2 $(MARKDOWNLINT_CLI2_VERSION)"

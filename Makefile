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

# The lint targets run exactly these binaries; they never resolve a bare name
# through PATH. The defaults are the local pins that `make tools` installs, and
# an explicit override (e.g. `make lint GOLANGCI_LINT=golangci-lint` in CI) is
# the only way to run a different one.
GOLANGCI_LINT ?= $(CURDIR)/$(TOOLS_DIR)/bin/golangci-lint
MARKDOWNLINT_CLI2 ?= $(CURDIR)/node_modules/.bin/markdownlint-cli2

# PATH only makes `node`, `go`, and the tools' sibling helpers reachable at
# runtime; $(GOLANGCI_LINT) and $(MARKDOWNLINT_CLI2) above - not PATH
# resolution - decide which lint binaries run.
export PATH := $(CURDIR)/$(TOOLS_DIR)/bin:$(CURDIR)/node_modules/.bin:$(PATH)

.PHONY: build test vet fmt lint lint-fix lint-md modernize verify integration hooks bench release run clean tools

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
	@case "$(GOLANGCI_LINT)" in */*) test -f "$(GOLANGCI_LINT)" && test -x "$(GOLANGCI_LINT)" ;; *) command -v "$(GOLANGCI_LINT)" >/dev/null 2>&1 ;; esac || { echo "golangci-lint not found - run 'make tools' (expected $(GOLANGCI_LINT))"; exit 1; }
	$(GOLANGCI_LINT) config verify
	$(GOLANGCI_LINT) run ./...

# lint-fix applies the mechanical fixes golangci-lint can make (gci, gofmt,
# modernize, intrange, perfsprint, ...). Findings that remain need a hand fix.
lint-fix:
	@case "$(GOLANGCI_LINT)" in */*) test -f "$(GOLANGCI_LINT)" && test -x "$(GOLANGCI_LINT)" ;; *) command -v "$(GOLANGCI_LINT)" >/dev/null 2>&1 ;; esac || { echo "golangci-lint not found - run 'make tools' (expected $(GOLANGCI_LINT))"; exit 1; }
	$(GOLANGCI_LINT) run --fix ./...

# lint-md runs markdownlint-cli2 over every Markdown file; the globs and
# ignores live in .markdownlint-cli2.jsonc.
lint-md:
	@case "$(MARKDOWNLINT_CLI2)" in */*) test -f "$(MARKDOWNLINT_CLI2)" && test -x "$(MARKDOWNLINT_CLI2)" ;; *) command -v "$(MARKDOWNLINT_CLI2)" >/dev/null 2>&1 ;; esac || { echo "markdownlint-cli2 not found - run 'make tools' (expected $(MARKDOWNLINT_CLI2))"; exit 1; }
	$(MARKDOWNLINT_CLI2) "**/*.md"

# verify is the release gate: format, static analysis, race-enabled unit tests.
verify: fmt vet modernize
	$(GO) test -race -count=1 ./...

# `go fix` sees modernizations that golangci-lint's modernize linter cannot:
# the toolchain threads the module's Go version into type information, so
# version-gated analyzers fire here but are skipped by golangci-lint. This is a
# check, never an auto-apply - a `go fix` suggestion is not guaranteed to
# compile (e.g. its errors.AsType fix for an interface that does not satisfy
# error), so review each one, apply only what builds, then re-run 'make verify'.
modernize:
	@pending="$$($(GO) fix -diff ./... 2>&1)"; status=$$?; \
	if [ -n "$$pending" ] || [ $$status -ne 0 ]; then \
		echo "go fix reported pending modernizations (or failed):"; \
		echo "$$pending"; \
		echo "review each suggestion, apply only the ones that compile, then re-run 'make verify'"; \
		exit 1; \
	fi

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
# It then asserts each installed binary reports the pinned version, so a stale
# or foreign binary cannot masquerade as the pin.
tools:
	mkdir -p $(TOOLS_DIR)/bin
	GOBIN=$(CURDIR)/$(TOOLS_DIR)/bin $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	npm ci --no-audit --no-fund
	@want_golangci="$$(printf '%s' '$(GOLANGCI_LINT_VERSION)' | sed 's/^v//')"; \
	want_md="$$(printf '%s' '$(MARKDOWNLINT_CLI2_VERSION)' | sed 's/^v//')"; \
	got_golangci="$$( $(GOLANGCI_LINT) version --short 2>/dev/null | head -n 1 | tr -d '[:space:]' )"; \
	if [ "$$got_golangci" != "$$want_golangci" ]; then \
		echo "golangci-lint version mismatch: pinned $$want_golangci, $(GOLANGCI_LINT) reports $${got_golangci:-<none>}"; \
		echo "remove $(TOOLS_DIR) and re-run 'make tools'"; \
		exit 1; \
	fi; \
	got_md="$$( $(MARKDOWNLINT_CLI2) --help 2>&1 | awk 'NR == 1 { for (i = 1; i <= NF; i++) if ($$i ~ /^v[0-9]/) { print substr($$i, 2); exit } }' )"; \
	if [ "$$got_md" != "$$want_md" ]; then \
		echo "markdownlint-cli2 version mismatch: pinned $$want_md, $(MARKDOWNLINT_CLI2) reports $${got_md:-<none>}"; \
		echo "remove node_modules and re-run 'make tools'"; \
		exit 1; \
	fi; \
	echo "installed tools: golangci-lint $$got_golangci, markdownlint-cli2 $$got_md"

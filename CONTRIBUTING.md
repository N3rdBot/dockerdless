# Contributing to dockerdless

Thanks for helping. dockerdless is a Linux-rootful Docker-compatible daemon
built on containerd, BuildKit, and CNI. The MVP serves the Docker Engine API
surface that testcontainers-go needs. This guide covers how to build, test, and
land a change.

Three process docs back this guide:

- [docs/process/collaboration.md](docs/process/collaboration.md) for how work is
  split, claimed, and tracked.
- [docs/process/review-checklist.md](docs/process/review-checklist.md) for what a
  reviewer looks for.
- [docs/process/definition-of-done.md](docs/process/definition-of-done.md) for
  what "done" means before a change ships.

## Prerequisites

- **Go 1.27.1**, the version pinned in [`go.mod`](go.mod). Check with
  `go version`.
- **Node.js 20+ with npm**, for `make tools`, which installs the pinned
  `markdownlint-cli2`.
- **Linux with root (euid 0).** Network namespaces, CNI bridges, and iptables
  rules require it. There is no rootless mode.
- **containerd** listening on `/run/containerd/containerd.sock`.
- **BuildKit** listening on `/run/buildkit/buildkitd.sock`.
- **CNI plugins** in `/opt/cni/bin`: `bridge`, `host-local`, `portmap`,
  `firewall`, `loopback`.
- **git** with a GitHub account that can push to your fork.

The daemon writes its Unix socket into the parent directory you point it at, so
that directory must be writable by the user running the daemon. See
[docs/security.md](docs/security.md) for the trust boundary and
[docs/operations.md](docs/operations.md) for the full runbook.

## Local setup

```bash
# Clone your fork.
git clone git@github.com:<you>/dockerdless.git
cd dockerdless

# Fetch dependencies.
go mod download

# Install the pinned developer tools (golangci-lint + markdownlint-cli2) once.
make tools

# Build the daemon into bin/dockerdless.
make build

# Run it on a private socket (default is /var/run/dockerdless.sock).
DOCKERDLESS_SOCKET_PATH=/tmp/dockerdless.sock sudo -E ./bin/dockerdless

# In another shell, confirm it is alive.
curl --unix-socket /tmp/dockerdless.sock http://localhost/_ping
# -> OK
```

To point a Docker client or testcontainers-go at the daemon:

```bash
export DOCKER_HOST=unix:///tmp/dockerdless.sock
export DOCKER_API_VERSION=1.44
export TESTCONTAINERS_RYUK_DISABLED=true
export TESTCONTAINERS_HOST_OVERRIDE="$(hostname -I | awk '{print $1}')"
```

`DOCKER_API_VERSION=1.44` is required for clients that do not negotiate. Ryuk is
intentionally unsupported, so disable it and clean up with
`testcontainers.CleanupContainer` or `t.Cleanup`.

## Build and test commands

These are the exact commands. The Makefile targets wrap the ones that matter.

| What you want | Command |
| --- | --- |
| Install pinned dev tools | `make tools` (once per clone, then after a pin bump) |
| Format check | `make fmt` (or `gofmt -l .`, must print nothing) |
| Static analysis | `make vet` (`go vet ./...`) |
| Go lint | `make lint` (`golangci-lint run ./...`) |
| Go lint, with auto-fix | `make lint-fix` (`golangci-lint run --fix ./...`) |
| Markdown lint | `make lint-md` |
| Unit tests | `go test ./...` |
| Race-enabled unit tests | `go test -race ./...` or `go test -race -count=1 ./...` |
| Integration tests | `go test -tags=integration ./integration/...` or `make integration` |
| Benchmarks | `make bench` |
| Full release gate | `make release` (`make verify` then `make bench`) |
| Build the daemon | `make build` |
| Run from source | `make run` |

`make verify` runs the release gate: `gofmt`, `go vet`, then
`go test -race -count=1 ./...`. `make release` adds the bounded benchmarks.

`make integration` needs a real backend. When the host lacks root, containerd,
BuildKit, or the CNI plugins, the suite **skips with an explicit reason**. It
never silently passes, so do not read a skip as a green run. Integration tests
use the `integration` build tag, so ordinary `go test ./...` does not compile
them.

Run `make tools` once per clone before linting. It installs the pinned
`golangci-lint` v2.13.2 into `.tools/bin` and `markdownlint-cli2` 0.22.1 into
`node_modules` from the committed `package.json`/`package-lock.json`, outside
the Go module graph, so `go.mod` and `go.sum` stay untouched. The Makefile puts
those directories first on `PATH`, so `make lint`, `make lint-fix`, and
`make lint-md` use the pinned versions automatically and point you back to
`make tools` when a tool is missing.

`make lint` runs `golangci-lint` v2 against the `.golangci.yml` rule set,
including the three-group import order enforced by `gci`; `make lint-fix`
applies its mechanical fixes. `make lint-md` runs the Markdown lint. Lint is part
of the pre-PR gate, so run `make verify`, `make lint`, and `make lint-md` before
you push. See [docs/operations.md](docs/operations.md#static-analysis) for
details.

## Commit convention

This repo uses [Conventional Commits](https://www.conventionalcommits.org/).
Write every commit as:

```text
<type>(<scope>): <short imperative summary>

<optional body explaining why>

Signed-off-by: Your Name <you@example.com>
```

The full grammar, allowed types, scope vocabulary, body/footer rules, and
examples are in
[docs/process/commit-conventions.md](docs/process/commit-conventions.md).

The sign-off line is **mandatory**. This project uses the
[Developer Certificate of Origin](https://developercertificate.org/), so every
commit must carry it. Create it automatically with:

```bash
git commit -s -m "fix(runtime): release reserved ports once"
```

`-s` appends the `Signed-off-by` trailer from your configured `user.name` and
`user.email`. A PR with unsigned commits will be asked to amend before review.

Install the local hook once per clone so the format is checked before your commit
lands:

```bash
make hooks
```

It sets `core.hooksPath=.githooks` and enforces the message format locally.

## Branch naming

Cut a branch off `main` and name it by type and topic:

```text
feat/exec-stream-hijack
fix/reconcile-exit-time
docs/operations-troubleshooting
chore/bump-containerd
```

Keep one logical change per branch. Rebase on `main` rather than merging it in,
and keep your history linear.

## Pull request flow

1. Open an issue or proposal first when the change is not trivial. See
   [How to propose a change](#how-to-propose-a-change) below.
2. Branch off `main`.
3. Make the change, with tests. Follow the
   [definition of done](docs/process/definition-of-done.md).
4. Run the gates locally: `make verify`, `make lint`, `make lint-md`, and, when
   you have a backend, `make integration`.
5. Push and open a PR. The
   [PR template](.github/PULL_REQUEST_TEMPLATE.md) loads automatically. Fill
   every section.
6. Update the docs that your change affects: `docs/compatibility.md` for API
   surface changes, `docs/operations.md` for operational changes, and the
   relevant proposal, design, or ADR.
7. Wait for review. Address feedback with new commits, not force-pushes, until
   the reviewer asks you to squash.

## Review process

A reviewer follows
[docs/process/review-checklist.md](docs/process/review-checklist.md). Expect
scrutiny of the failure paths, not just the happy path: unsupported requests must
answer with Docker-shaped errors, and a failed operation must not leak
containers, network namespaces, bridges, or temp files.

At least one approval is required before merge. A maintainer merges with a
squash or a linear rebase, depending on the change size. The
[collaboration doc](docs/process/collaboration.md) describes who owns which area
and how `.github/CODEOWNERS` routes reviews.

## How to propose a change

Pick the lightest artifact that fits:

- **Small, contained change** (bug fix, docs, test, refactor with no API
  impact): open a PR directly.
- **New endpoint, feature, or any change to the compatibility surface** (a new
  route, a field moving between handled/ignored/rejected, a behavior change
  clients can observe): write a
  [feature proposal](docs/proposals/0000-template.md) under `docs/proposals/`
  and get it agreed before coding.
- **Design-level change** (interfaces, data/state model, error mapping across
  layers): write a [design doc](docs/design/0000-template.md) under
  `docs/design/` once the proposal is accepted.
- **Irreversible or hard-to-reverse decision** (a dependency that shapes the
  architecture, a wire-format commitment, a compatibility contract): record it
  as an [ADR](docs/adr/0000-template.md) under `docs/adr/`.

When in doubt, start with a proposal. It is cheaper to change a paragraph than a
merged PR.

## Reporting bugs and security issues

Use the issue forms in `.github/ISSUE_TEMPLATE/` for bugs and proposals. For a
vulnerability, do **not** open a public issue. Follow [SECURITY.md](SECURITY.md).

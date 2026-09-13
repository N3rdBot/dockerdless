# Definition of Done

A change is done when every applicable line below is true. "Done" is not "the
code compiles" and not "my test passed." The release gates in this document are
the same ones wrapped by the `Makefile` and documented in
[`README.md`](../../README.md#release-gates) and
[`docs/operations.md`](../operations.md#release-command-set). If a gate cannot
run on the host, the skip must be recorded with its exact reason, never hidden.

Copy the [checklist](#copyable-checklist) into the pull request or the evidence
artifact.

## Table of contents

- [Code and tests](#code-and-tests)
- [Integration coverage](#integration-coverage)
- [Documentation](#documentation)
- [Evidence](#evidence)
- [Commits](#commits)
- [No leaked resources](#no-leaked-resources)
- [Copyable checklist](#copyable-checklist)

## Code and tests

Run the static and race gates from the repository root:

```bash
gofmt -l .                     # must print nothing
go vet ./...                   # must pass
go test -race -count=1 ./...   # unit + race detector, no cache
```

Or the equivalent wrapper:

```bash
make verify    # gofmt + go vet + go test -race -count=1 ./...
```

Requirements:

- [ ] `gofmt -l .` prints nothing. Every touched file is formatted.
- [ ] `go vet ./...` passes with no new findings.
- [ ] `go test -race -count=1 ./...` passes. `-race` is not optional.
- [ ] New behavior has a real test with a real assertion.
- [ ] Error translation is covered: canonical `ports.Err*` sentinel, not a raw
      backend error.
- [ ] No test was weakened, no timeout inflated, and no `t.Skip` added to turn a
      red test green.
- [ ] `go build ./...` succeeds. `make build` produces `bin/dockerdless` when the
      change affects the binary.

## Integration coverage

When the change touches a runtime surface (containerd, BuildKit, CNI, the socket
lifecycle, or a Docker streaming path), the live suite is required:

```bash
make integration
# equivalent:
go test -tags=integration -count=1 -v ./integration/...
```

- [ ] The relevant live case runs against real containerd, BuildKit, and CNI, or
      skips with an explicit, actionable reason naming the missing prerequisite.
- [ ] A skip is never used to hide a failure. If the host lacks a prerequisite,
      the skip text says exactly what is missing.
- [ ] The testcontainers-go compatibility path is exercised when the change
      affects a call that library makes.
- [ ] Failure-injection behavior is exercised where the change adds an error
      path (`integration/failures_test.go`).
- [ ] The recorded run's host containerd/BuildKit versions are noted, because
      behavior differs across them.

## Documentation

- [ ] [`docs/compatibility.md`](../compatibility.md) reflects any endpoint,
      field, or error-status change, in the **same commit** as the behavior.
- [ ] [`docs/operations.md`](../operations.md) covers any new log location,
      config key, or runbook step.
- [ ] [`docs/architecture.md`](../architecture.md) is updated if a layer or a
      data flow changed.
- [ ] [`docs/security.md`](../security.md) is updated for any trust-boundary or
      credential change.
- [ ] The proposal and design (if any) are marked `Implemented` and link the
      merged commit.
- [ ] `README.md` configuration reference is updated if a setting changed. (Owned
      by the documentation workstream; request the change rather than editing it.)

## Evidence

- [ ] The evidence is recorded in the pull request.
- [ ] It names the exact command(s) run and pastes the raw result.
- [ ] It records any skip and its reason.
- [ ] It records the commit SHA the run corresponds to.
- [ ] A reviewer can reproduce the result from the artifact alone.

## Commits

- [ ] Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/).
- [ ] Every commit is signed off under the DCO with `git commit -s`; each commit
      carries a `Signed-off-by` trailer.
- [ ] Commits are atomic and reviewable.
- [ ] No secrets, no `bin/` artifacts, no unrelated churn.

## No leaked resources

Containers, network namespaces, CNI bridges, host-port reservations, and temp
directories must not outlive the test or the change. The project has been bitten
by each of these.

- [ ] No container or task is left in the configured namespace. Check with
      `ctr -n <namespace> containers list` and `ctr -n <namespace> tasks list`.
- [ ] No network namespace is left mounted. Sweep the run directory and check
      `ip netns list`.
- [ ] No CNI bridge (`dls*` or the bridge named in the conflist) is left behind.
      Check `ip link show type bridge | grep dls`.
- [ ] No host-port reservation leaks. The allocator released every reservation,
      including the rollback path.
- [ ] No CNI conflist is left in the temp config directory.
- [ ] No temp dir or log file survives the test. The harness cleanup in
      `integration/cleanup.go` covers the case.
- [ ] A paused or stopped container is reachable by a follow-up operation, not
      orphaned.

## Copyable checklist

```markdown
## Definition of Done

### Code and tests
- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...` passes
- [ ] `go test -race -count=1 ./...` passes
- [ ] New behavior has a real test with a real assertion
- [ ] Error translation covered by canonical `ports.Err*`
- [ ] No weakened assertion, no inflated timeout, no skip-to-green

### Integration (when a runtime surface is touched)
- [ ] `make integration` green, or skip with an exact missing-prerequisite reason
- [ ] testcontainers-go compatibility path exercised where relevant
- [ ] Host containerd/BuildKit versions recorded

### Documentation
- [ ] `docs/compatibility.md` updated in the same commit
- [ ] `docs/operations.md` updated for new logs/config/runbook
- [ ] `docs/architecture.md` updated for layer or flow change
- [ ] `docs/security.md` updated for trust-boundary change
- [ ] Proposal/design marked `Implemented`

### Evidence
- [ ] The pull request records the command, raw result, skip, and SHA

### Commits
- [ ] Conventional Commits messages
- [ ] Every commit signed off with `git commit -s`
- [ ] Atomic commits, no secrets or binaries

### No leaked resources
- [ ] No container or task left behind
- [ ] No network namespace left mounted
- [ ] No CNI bridge left behind
- [ ] No host-port reservation leaked
- [ ] No CNI conflist or temp dir left behind
```

The review side of this lives in
[`docs/process/review-checklist.md`](review-checklist.md). The end-to-end flow is
in [`docs/process/collaboration.md`](collaboration.md).

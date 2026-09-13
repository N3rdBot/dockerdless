# Review checklist

Reviewers walk this list, not just read the diff. Every item is a real failure
mode this project has hit or is one containerd, BuildKit, CNI, or the Docker
contract will produce. Check the box or write why it does not apply.

The author runs this before requesting review (see
[`docs/process/collaboration.md`](collaboration.md#stage-7-self-review)); the
reviewer runs it again and cites the section when raising a defect.

## Table of contents

- [Correctness](#correctness)
- [Docker compatibility](#docker-compatibility)
- [Architecture boundaries](#architecture-boundaries)
- [Security](#security)
- [Concurrency and races](#concurrency-and-races)
- [Tests](#tests)
- [Documentation](#documentation)
- [Commits](#commits)

## Correctness

- [ ] The change does what the accepted proposal or design says. No scope creep.
- [ ] State transitions are legal. Only the transitions in the design's state
      machine are reachable; everything else returns a conflict.
- [ ] Resource cleanup is complete on every path, including error paths:
      containers, tasks, netns, CNI bridges, host-port reservations, temp dirs,
      and log files.
- [ ] Error paths leave the system in a consistent state. A failed backend call
      does not leave the registry claiming success.
- [ ] Idempotence matches Docker: a repeated stop, remove, or disconnect either
      succeeds quietly or conflicts, exactly as the compatibility doc says.
- [ ] Nil and zero values are handled. A nil writer, empty command, or zero host
      port does not panic or silently no-op.
- [ ] No `context.Background()` hidden inside a use case; the request context is
      threaded through.
- [ ] No `panic`, no `log.Fatal`, and no swallowed error (`_ = err`) outside a
      documented best-effort cleanup.

## Docker compatibility

- [ ] Every new or changed endpoint is reflected in
      [`docs/compatibility.md`](../compatibility.md#capability-mapping) with a
      status that matches reality (`verified live` only after the live case
      passes).
- [ ] Error shape is the Docker envelope: exactly `{"message": "..."}`. Status
      and kind stay out of JSON, matching `internal/api/errors.go`.
- [ ] Status codes match Docker Engine API `1.44`: `400`, `404`, `409`, `500`,
      `501` are chosen deliberately, not defaulted to `500`.
- [ ] Every error crosses the app boundary as a canonical `ports.Err*` sentinel.
      No raw gRPC or containerd error type reaches the HTTP layer.
- [ ] New `HostConfig` or `container.Config` fields are classified (`handled`,
      `rejected`, `ignored`) in `internal/api/container_fields.go`. A new
      unsupported field is rejected with a Docker-shaped `501`, not silently
      dropped.
- [ ] `TestContainerCreateFieldPolicyIsExhaustive` still passes after any
      `moby/moby/api` bump.
- [ ] Unsupported routes and features still return the documented `404` or `501`
      with the exact message in the compatibility doc.
- [ ] `Config.ExposedPorts` on image inspect stays non-nil, because
      testcontainers-go dereferences it on every create.

## Architecture boundaries

- [ ] `internal/domain` imports no infrastructure package. No containerd,
      BuildKit, CNI, net/http, or zap type appears there.
- [ ] `internal/ports` defines interfaces only; it does not import an adapter.
- [ ] Adapters do not import one another. `containerd`, `buildkit`, and `cni`
      stay mutually independent.
- [ ] `internal/api` never imports an adapter package. It depends on
      application services and ports.
- [ ] The app layer is the single place that translates adapter errors into
      `ports.Err*` sentinels.
- [ ] New behavior extends an existing port before it invents a new one. A new
      interface carries a design doc that justified it.
- [ ] The composition root in `cmd/dockerdless` is the only place that
      constructs adapters.

## Security

- [ ] No secret is logged, returned in an error, or written to disk. Registry
      credentials render only presence flags, per
      [`docs/security.md`](../security.md#credentials).
- [ ] The API socket stays `0660` and daemon-uid owned. The startup verification
      in the socket lifecycle is untouched.
- [ ] No new TCP listener, no new socket, and no new privilege without an
      explicit security review.
- [ ] containerd and BuildKit sockets are never proxied to API clients.
- [ ] Request bodies are bounded; a large or malformed body returns `400`, not
      an unbounded read.
- [ ] A credential-bearing request produces neither a leaking response body nor
      a leaking access-log record. The regression tests in
      `internal/api/server_security_test.go` still pass.
- [ ] Published-port behavior is unchanged unless the proposal says otherwise;
      host exposure is documented, not silently widened.

## Concurrency and races

- [ ] `go test -race ./...` is green. Not "was green before"; green on this
      change.
- [ ] Every new long-lived goroutine has a stop condition and is joined on
      shutdown.
- [ ] Shared mutable state (registry, exec map, port allocator) is guarded and
      read-modify-write happens inside the same critical section.
- [ ] Cancellation is honored. A canceled request stops the backend call
      instead of running to completion.
- [ ] Streams (logs follow, hijacked exec) are bounded by the existing stream
      limiter and are released on disconnect.
- [ ] A concurrent pair of operations on the same container has a defined
      winner; the test proves it under `-race`.

## Tests

- [ ] New behavior has a real assertion. No test asserts `err == nil` and calls
      it coverage.
- [ ] Unit tests use the in-memory fakes (`internal/app/fakes_test.go`,
      `internal/adapters/*/fakes_test.go`), not a live backend.
- [ ] Adapter error translation has a table test asserting the canonical
      sentinel, especially `ErrConflict` versus `ErrServerError`.
- [ ] The change is inside `integration/` behind `//go:build integration` when it
      touches a runtime surface, and the live case exercises the actual
      containerd, BuildKit, or CNI path.
- [ ] An integration skip states the exact missing prerequisite. No skip is
      added to hide a failure.
- [ ] No assertion was weakened, no timeout was inflated, and no `t.Skip` was
      added to make a red test go green.
- [ ] The testcontainers-go compatibility path is exercised when the change
      affects a call that library makes.
- [ ] The pull request records the exact command and the raw result.

## Documentation

- [ ] `docs/compatibility.md` is updated in the same commit as the behavior
      change. Both the capability table and the unsupported list moved.
- [ ] `docs/operations.md` covers any new log location, config key, or runbook
      step.
- [ ] `docs/architecture.md` is updated if a layer or data flow changed.
- [ ] `docs/security.md` is updated for any trust-boundary or credential change.
- [ ] The proposal and design (if any) are moved to `Implemented` and link the
      merged commit.
- [ ] Code comments explain why, not what, and stay short.

## Commits

- [ ] Messages follow the [commit conventions](commit-conventions.md) spec:
      `feat(api):`, `fix(containerd):`, `docs(compatibility):`, `test(integration):`.
- [ ] Every commit is signed off under the DCO with `git commit -s`.
  - [ ] The `Signed-off-by` trailer is present on every commit.
- [ ] Commits are atomic. A compatibility-table update is in the commit that
      changes the behavior, not a separate drive-by.
- [ ] No secrets, no generated binaries (`bin/`), and no unrelated whitespace
      churn.
- [ ] The subject line is imperative and under roughly 72 characters; the body
      explains the why when it is not obvious.

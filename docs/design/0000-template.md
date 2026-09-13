<!--
Copy this file to docs/design/NNNN-short-slug.md. Never edit this template in
place. A design doc is the "how"; its proposal is the "what and why". Fill every
section, then delete guidance comments you no longer need.
Conventions and the split rule: docs/design/README.md.
-->

# Detailed Design: Short, descriptive title

## Metadata

| Field | Value |
| --- | --- |
| **Title** | Container pause and unpause runtime path |
| **Status** | Draft <!-- Draft, In Review, Accepted, Implemented, Superseded --> |
| **Proposal** | [`docs/proposals/NNNN-container-pause.md`](../proposals/NNNN-container-pause.md) |
| **Design owner** | `@author` |
| **Implementer(s)** | `@author`, `@pair` |
| **Reviewers** | `@reviewer-one`, `@reviewer-two` |
| **Created** | 2026-09-13 |
| **Updated** | 2026-09-13 |
| **Related ADRs** | `docs/adr/NNNN-*.md` or `none` |
| **Evidence** | recorded in the pull request: exact command + raw output |

<!--
A design doc without an accepted proposal is premature unless the proposal is
explicitly waived under docs/proposals/README.md. Link the proposal, do not
restate it.
-->

## Scope and invariants

**In scope**

- `POST /containers/{id}/pause` and `POST /containers/{id}/unpause`.
- A `RuntimeController.Pause` / `Resume` port contract and its containerd
  implementation.
- Domain transitions `running -> paused` and `paused -> running` with the
  existing `container.paused` / `container.resumed` events.

**Out of scope**

- Pause of exec processes, `created` containers, or containers under an
  in-flight stop.
- Cgroup freezer tuning beyond containerd defaults.

**Invariants**

- The state registry is the single source of truth the HTTP layer renders.
  Containerd is the authority for the runtime state; the registry mirrors it.
- No adapter imports another adapter (`containerd`, `buildkit`, `cni` stay
  independent). See [`docs/architecture.md`](../architecture.md#layers).
- `internal/domain` never imports an infrastructure package.
- Every backend error crosses the app boundary as one of the canonical
  `ports.Err*` sentinels, never as a containerd or gRPC type.
- Inspect after pause reports `"State":{"Status":"paused","Running":false,"Paused":true}`.

## Component and package layout

The change is confined to four layers. The domain is unchanged except for using
an existing state constant.

| Package | Responsibility in this change |
| --- | --- |
| `internal/domain` | No new file. Reuse `ContainerStatePaused` and the existing events. |
| `internal/ports` | Add two methods to `RuntimeController`. No new interface file. |
| `internal/app` | Add `ContainerPause` / `ContainerResume` use cases; own the state transition and registry write. |
| `internal/adapters/containerd` | Implement the two methods against `Task.Pause` / `Task.Resume` and translate errors. |
| `internal/api` | Add two routes and handlers; map results to `204` and errors to the Docker envelope. |
| `cmd/dockerdless` | No change; the composition root already wires the adapter behind the port. |

```text
internal/api/handlers_containers.go   (driving adapter)
        │  app.Service.ContainerPause
        ▼
internal/app/containers.go            (use case + registry)
        │  ports.RuntimeController.Pause
        ▼
internal/ports/ports.go               (interface)
        │
        ▼
internal/adapters/containerd/adapter.go (driven adapter)
        │  containerd client.Task.Pause
        ▼
     containerd
```

## Interfaces

<!-- Real Go signatures. Reviewers check these before code exists. -->

**`internal/ports/ports.go`** (extend the existing `RuntimeController` interface):

```go
// Pause suspends a running container task. It returns ports.ErrConflict when
// the container is not running and ports.ErrNotImplemented when the configured
// runtime cannot pause tasks.
Pause(context.Context, domain.ContainerID) error

// Resume continues a paused container task. It returns ports.ErrConflict when
// the container is not paused.
Resume(context.Context, domain.ContainerID) error
```

**`internal/app/containers.go`** (use case, mirrors `ContainerStop`):

```go
// ContainerPause suspends a running container addressed by id, name, or id
// prefix. It returns ports.ErrNotFound for an unknown reference and
// ports.ErrConflict for a container that is not running.
func (s *Service) ContainerPause(ctx context.Context, ref string) error

// ContainerResume continues a paused container addressed by id, name, or id
// prefix.
func (s *Service) ContainerResume(ctx context.Context, ref string) error
```

**`internal/adapters/containerd/adapter.go`**:

```go
// Pause suspends the running task behind id.
func (a *Adapter) Pause(ctx context.Context, id domain.ContainerID) error

// Resume continues the paused task behind id.
func (a *Adapter) Resume(ctx context.Context, id domain.ContainerID) error
```

**`internal/api/handlers_containers.go`** (driving adapter):

```go
func (h *handlers) containerPause(w http.ResponseWriter, r *http.Request)
func (h *handlers) containerUnpause(w http.ResponseWriter, r *http.Request)
```

Route registration goes in `internal/api/routes.go` and must be covered by
`internal/api/routes_test.go`.

## Data models and state machine

The domain `Container` aggregate already carries `State ContainerState`. Pause
uses `ContainerStatePaused`; resume returns the aggregate to
`ContainerStateRunning`. No new struct fields.

Valid transitions for this change:

```text
        pause                unpause
running ───────► paused ─────────────► running
   │                                      ▲
   │ (self-transition rejected: 409)      │
   └──────────────────────────────────────┘
```

Full Docker state vocabulary for context (all already defined in
`internal/domain/model.go`): `created`, `running`, `paused`, `restarting`,
`removing`, `exited`, `dead`. Pause is legal only from `running`. Unpause is
legal only from `paused`. Everything else returns `409`.

| Current state | `pause` | `unpause` |
| --- | --- | --- |
| `created` | `409` | `409` |
| `running` | `running -> paused` | `409` |
| `paused` | `409` (already paused) | `paused -> running` |
| `restarting` | `409` | `409` |
| `removing` | `409` | `409` |
| `exited` | `409` | `409` |
| `dead` | `409` | `409` |

## Request and data flow

```text
Client → POST /containers/{id}/pause
  api.router          matches route (internal/api/routes.go)
  h.containerPause    extracts {id}, calls app
  app.Service.ContainerPause
    registry.Resolve(ref)         → domain.Container or ErrNotFound
    require State == running      → else ErrConflict
    runtime.Pause(ctx, id)        → ports sentinel on failure
    container.State = paused
    registry.Save(container)      → emits container.paused event
    return nil
  handler             WriteHeader(204)
```

Resume is the mirror image with `unpause` and the `running` target. A backend
failure at `runtime.Pause` leaves the registry untouched; the container remains
`running` in inspect until a successful pause.

## Error taxonomy and Docker status mapping

<!--
Every error path names its canonical ports sentinel and the resulting HTTP
status. The mapping already exists in internal/api/errors.go; do not invent a
parallel one.
-->

| Condition | Canonical error | HTTP status | Docker `message` |
| --- | --- | --- | --- |
| Unknown id/name/prefix | `ports.ErrNotFound` | `404` | `No such container: <ref>` |
| Not running (pause) | `ports.ErrConflict` | `409` | `Container <id> is not running` |
| Not paused (unpause) | `ports.ErrConflict` | `409` | `Container <id> is not paused` |
| Runtime cannot pause | `ports.ErrNotImplemented` | `501` | `container pause is not supported by the configured runtime` |
| containerd task missing | `ports.ErrNotFound` | `404` | `No such container: <ref>` |
| containerd/gRPC failure | `ports.ErrServerError` | `500` | redacted backend message |
| Malformed id | `ports.ErrInvalidArgument` | `400` | `invalid container reference` |

The app layer wraps adapter errors with `%w` so `errors.Is` in
`internal/api/errors.go` maps them. Do not return a raw gRPC status from a port.

## Concurrency and lifecycle

- **Contexts:** every port method takes `context.Context`; the HTTP request
  context cancels the containerd call. No `context.Background()` inside the use
  case.
- **Request timeout:** pause and unpause are non-streaming, so they run under
  the configured `request-timeout` through the existing middleware path.
- **Registry safety:** `registry.Save` already serializes writes; the use case
  must re-read state inside the same critical section it writes in, so a
  concurrent stop cannot interleave a pause and write a stale state.
- **Race safety:** the transition test runs pause and stop concurrently against
  the in-memory fake under `-race`, asserting exactly one transition wins and
  the loser returns `409`.
- **Graceful shutdown:** pause holds no long-lived resource, so shutdown waits
  only for the in-flight request to drain. No new goroutine is spawned.

## Configuration

<!-- State every field, and whether it is hot-reloadable or startup-only. -->

No new configuration keys. Pause behavior uses the existing
`DOCKERDLESS_REQUEST_TIMEOUT` bound. If a future runtime-capability flag is
needed, it is startup-only under the rules in
[`docs/architecture.md`](../architecture.md#configuration-and-reload).

| Key | Type | Default | Reload |
| --- | --- | --- | --- |
| (none) | n/a | n/a | n/a |

## Observability

- **Logs:** `info` on success (`container paused`, `container resumed`) with
  `container_id`, `namespace`. `warn` on `409`; `error` on `500`. No credentials
  or request bodies, matching [`docs/security.md`](../security.md#credentials).
- **Metrics:** counter `dockerdless.container.lifecycle` with
  `operation=pause|unpause`, `result=ok|error`.
- **Traces:** extend the existing `app.Service` span with a child span around
  the containerd call, tagged `container.id` and `namespace`.

## Test plan

<!--
TDD first. Unit uses fakes, adapter uses tables, integration uses the live
harness. Every claim needs a named test and, for live paths, an evidence file.
-->

| Layer | Test | Location | Assertion |
| --- | --- | --- | --- |
| Unit (write first) | `TestContainerPauseTransitionsRunningToPaused` | `internal/app/containers_test.go` | fake runtime called once, registry state `paused`, event emitted |
| Unit | `TestContainerPauseConflictWhenNotRunning` | `internal/app/containers_test.go` | returns `ports.ErrConflict`, runtime not called |
| Unit | `TestContainerPauseNotFound` | `internal/app/containers_test.go` | returns `ports.ErrNotFound` |
| Unit | `TestContainerResumeConflictWhenNotPaused` | `internal/app/containers_test.go` | returns `ports.ErrConflict` |
| Race | `TestPauseStopRaceSingleWinner` | `internal/app/containers_test.go` | run with `-race`; one winner, loser `409` |
| Adapter | `TestPauseTranslatesAlreadyPaused` | `internal/adapters/containerd/errors_test.go` | already-paused maps to `ErrConflict`, not `ErrServerError` |
| Handler | `TestContainerPauseStatus204` | `internal/api/errors_mapping_test.go` | `204` on success, `409` body shape |
| Integration | `TestCompatContainerPauseUnpause` | `integration/compat_runtime_test.go` (`//go:build integration`) | inspect reports `paused`, exec works after resume |
| Compat | testcontainers-go `DockerContainer.Pause` / `Unpause` | `integration/compat_runtime_test.go` | no `501`, suite stays green |

**Evidence:** record `make integration` output and the testcontainers-go run in
the pull request, with the commit SHA and host containerd version. Integration
skips must state the exact missing prerequisite, never weaken an assertion.

## Migration and compatibility

Purely additive. New endpoints; no existing route, field, or error changes.
Update [`docs/compatibility.md`](../compatibility.md) in the same commit that
lands the handler: remove the two rows from unsupported, add them to the
capability table as `verified live` after the integration case passes. No
state or config migration.

## Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
| --- | --- | --- | --- |
| containerd `Task.Pause` returns before status reflects `paused` | Medium | Inspect shows stale `running` | Spike in M1; if needed, poll `Task.Status` with a short bounded timeout |
| Pause/stop race writes a stale state | Medium | Inspect lies | Re-read inside the registry write section; race test |
| `501` path is dead code but adds a branch | Low | Maintenance | Drop the branch if the spike proves pause is universal; record the decision |
| Integration test leaks a paused container on failure | Low | Host resource leak | Use the existing cleanup harness; assert in `integration/cleanup.go` coverage |

## Milestone checklist

<!-- Check off as implementation lands. Each item maps to a commit. -->

- [ ] M1: port methods + containerd adapter, adapter tests green
- [ ] M2: app use cases + state transition, unit and race tests green
- [ ] M3: handlers + routes + error mapping, handler tests green
- [ ] M4: `docs/compatibility.md` updated in the same commit as M3
- [ ] M5: live integration case green, evidence recorded
- [ ] M6: `make verify` and `make integration` green, Definition of Done met

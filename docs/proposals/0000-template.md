<!--
Copy this file to docs/proposals/NNNN-short-slug.md. Never edit this template in
place. Replace every placeholder, then delete guidance comments you do not need.
Numbering and lifecycle rules: docs/proposals/README.md.
-->

# Feature Proposal: Short, descriptive title

## Metadata

| Field | Value |
| --- | --- |
| **Title** | Support container pause and unpause |
| **Status** | Draft <!-- Draft, In Review, Accepted, Rejected, Implemented, Superseded --> |
| **Authors** | `@author` |
| **Reviewers** | `@reviewer-one`, `@reviewer-two` |
| **Created** | 2026-09-13 |
| **Updated** | 2026-09-13 <!-- bump on every substantive edit --> |
| **Supersedes** | Proposal `NNNN` or `none` |
| **Superseded by** | Proposal `NNNN` or `n/a` |
| **Related proposals** | `docs/proposals/NNNN-slug.md` or `none` |
| **Related designs** | `docs/design/NNNN-slug.md` or `none` <!-- filled once split, see "Detailed design" --> |
| **Related ADRs** | `docs/adr/NNNN-slug.md` or `none` |
| **Related issues** | `#NNN`, or link if the tracker is external |

## Summary

<!-- At most three sentences. If you need four, the idea is not sharp yet. -->

Add Docker-compatible `POST /containers/{id}/pause` and
`POST /containers/{id}/unpause`, implemented on top of containerd task
pause/resume, so testcontainers-go and Docker clients can suspend a running
container instead of tearing it down. The domain already models the `paused`
state and emits `container.paused` / `container.resumed` events, so this
proposal wires an existing model to a new HTTP surface.

## Motivation

<!-- The problem, the pain, and who feels it. Name the concrete consumer. -->

`docs/compatibility.md` lists pause and unpause as unsupported. Any suite that
calls `DockerContainer.Pause` today gets a `501` and has to special-case the
daemon. Suspending a busy container between test phases avoids a full stop and
restart, which for slow images is the difference between a fast suite and a
flaky one.

## Goals and non-goals

**Goals**

- Serve `POST /containers/{id}/pause` and `POST /containers/{id}/unpause` with
  Docker-compatible status codes and error bodies.
- Transition the domain state between `running`, `paused`, and back through the
  existing state machine and event taxonomy.
- Keep pause idempotence and error semantics identical to Docker Engine API
  `1.44`.

**Non-goals**

- No `pause` support for exec processes or for containers in `created`,
  `exited`, `restarting`, or `dead` states.
- No cgroup freezer tuning or SIGSTOP fallback for runtimes that do not support
  task pause.
- No change to stop, kill, or restart behavior.

## Background and prior art

<!-- Link the references a reviewer needs. Keep the list short and specific. -->

- [Docker Engine API `1.44`, container pause](https://docs.docker.com/engine/api/v1.44/#tag/Container/operation/ContainerPause)
  defines the endpoint, the `204` success code, and the `409` conflict on a
  non-running container.
- containerd exposes task pause and resume through `Task.Pause` / `Task.Resume`
  and reports the resulting status as `paused`.
- [nerdctl](https://github.com/containerd/nerdctl) implements `nerdctl pause`
  against the same containerd calls; it is a useful behavior reference for the
  error wording, not a code dependency.
- CRI models this as `RuntimeService.ExecSync`-adjacent task state, but CRI is
  explicitly out of MVP scope (`docs/compatibility.md#unsupported-features`).
- BuildKit is not involved. State that explicitly so reviewers know the blast
  radius is containerd-only.

## Proposed design (high level)

<!-- One paragraph plus a sketch. Full mechanics go in the detailed design. -->

The API handler parses the container id, calls
`app.Service.ContainerPause` / `ContainerResume`, and maps the result to `204`.
The application layer validates the current state from the state registry,
calls the new `RuntimeController.Pause` / `Resume` port methods, and updates the
stored aggregate. The containerd adapter translates those port calls into
`Task.Pause` / `Task.Resume`. A container that is not running returns
`409 Conflict` with the Docker-shaped body `{"message":"Container <id> is not running"}`.

```text
POST /containers/{id}/pause
  internal/api/handlers_containers.go -> app.Service.ContainerPause
    -> ports.RuntimeController.Pause
      -> adapters/containerd.Adapter.Pause -> containerd Task.Pause
    -> registry.Save(container with State=paused)
  -> 204 No Content
```

## Detailed design

<!--
If the change touches more than one package, adds a port method, or changes the
state machine, split a design doc and link it here. See docs/design/README.md.
-->

Detailed design: `docs/design/NNNN-container-pause.md` <!-- or "Not required: rationale" -->

Reason to split: adding two `ports.RuntimeController` methods, a state-machine
edge, and a new adapter path crosses package boundaries and needs an interface
contract reviewers can check before code exists.

## Docker API and contract impact

<!--
Every row here must also be reflected in docs/compatibility.md. The endpoint
row, the create-field policy (if any), and the unsupported list all move.
-->

**Endpoints**

| Endpoint | Request fields | Response | Status codes |
| --- | --- | --- | --- |
| `POST /containers/{id}/pause` | path `id` only | empty body | `204`, `404`, `409`, `500`, `501` |
| `POST /containers/{id}/unpause` | path `id` only | empty body | `204`, `404`, `409`, `500`, `501` |

**Request and response fields:** none beyond the path parameter. No new JSON
fields, no `HostConfig` field changes, so the create-field policy in
[`docs/compatibility.md`](../compatibility.md#container-create-field-policy) is
untouched.

**Error codes**

| Condition | Status | `message` |
| --- | --- | --- |
| Container id unknown | `404` | `No such container: <id>` |
| Container not running (pause) or not paused (unpause) | `409` | `Container <id> is not running` / `is not paused` |
| Runtime does not support task pause | `501` | `container pause is not supported by the configured runtime` |
| Backend failure | `500` | translated backend message, credential-free |

**`docs/compatibility.md` rows to change**

- Move `POST /containers/{id}/pause` and `.../unpause` out of
  [Unsupported features](../compatibility.md#unsupported-features) into the
  [Capability mapping](../compatibility.md#capability-mapping) table with status
  `verified live` once the integration case passes.
- Add the two rows to the testcontainers-go consumer column
  (`DockerContainer.Pause` / `Unpause`).

## Native mapping

<!-- Which native primitive does the work? One line each. -->

| Docker operation | containerd | BuildKit | CNI |
| --- | --- | --- | --- |
| pause | `Task.Pause` (cgroup freezer) | none | none |
| unpause | `Task.Resume` | none | none |

State is re-read from `Task.Status` rather than inferred, so a pause issued
outside the daemon (for example through `ctr`) is still observed.

## Alternatives considered

<!-- Each alternative gets a reason for rejection. "We did not think of one" is not a reason. -->

| Alternative | Why rejected |
| --- | --- |
| Implement pause by sending `SIGSTOP` through the existing `Kill` path | Docker pause is a cgroup freezer operation; `SIGSTOP` is observable in the container and would not round-trip through containerd's `paused` status. |
| Keep it unsupported and document pause as out of scope | The domain state and events already exist; the missing piece is a thin HTTP and adapter path. Deferring costs more than implementing. |
| Serve pause as a no-op that returns `204` | Violates the project rule against routes that falsely report success (`docs/compatibility.md`), and would corrupt inspect state. |

## Compatibility and migration

<!--
What breaks, what keeps working, and what an operator must do. For a purely
additive endpoint, say so explicitly.
-->

Additive and non-breaking. No config key changes, no wire-shape changes, no
state migration. Containers already in `paused` state created through `ctr` are
already recovered by startup reconciliation and remain inspectable. Clients
that do not call pause are unaffected.

## Security considerations

<!--
Reference docs/security.md. State any new trust surface, even "none".
-->

No new socket, no new privilege, and no new credential path. Pause is reachable
only through the existing `0660` Unix socket, and it cannot escalate a
container beyond what start already grants. Confirm that pause does not bypass
the create-time field policy: a paused container is still bound by the rejected
fields recorded at create time. Update the hardening checklist only if the
operations surface changes.

## Observability

<!-- Logs, metrics, traces. Name the existing signal you extend. -->

- **Logs:** one `info` line per transition: `container paused` /
  `container resumed` with `container_id` and `namespace`. Failures log at
  `warn` with the canonical error class.
- **Metrics:** increment a `dockerdless.container.lifecycle` counter with
  `operation=pause|unpause` and `result=ok|error`.
- **Traces:** the existing `app.Service` span covers the use case; add a child
  span around the containerd call so backend latency is separable.

## Testing strategy

<!--
TDD expectation first. Name the fakes and the live fixture. State the
testcontainers-go case.
-->

- **Unit (TDD):** write `app.Service.ContainerPause` tests first against the
  in-memory fakes in `internal/app/fakes_test.go`. Cover running to paused,
  paused to unpause, `409` on wrong state, `404` on unknown id, and the `501`
  path when the fake runtime reports no pause support.
- **Adapter:** table tests for the containerd error translation
  (`internal/adapters/containerd/errors.go`), asserting an already-paused task
  maps to `ports.ErrConflict`, not `ports.ErrServerError`.
- **Integration:** add a case to `integration/compat_runtime_test.go` behind
  `//go:build integration`. Start a long-lived container, pause it, assert
  `docker inspect` shape via the Moby client reports `paused`, then unpause.
- **testcontainers-go compat:** call `DockerContainer.Pause` and assert the
  container still responds to exec after resume. Record the run as evidence.
- **Evidence:** record the integration output in the pull request and link it
  from the design doc.

## Rollout and milestones

<!-- Small, checkable steps. Each milestone should be independently verifiable. -->

| Milestone | Deliverable | Verification |
| --- | --- | --- |
| M1 | Port methods and containerd adapter path | `go test -race ./internal/...` |
| M2 | Application use case and state transition | unit tests green, race-clean |
| M3 | HTTP handlers and error mapping | handler tests green, `docs/compatibility.md` updated |
| M4 | Live integration and testcontainers-go case | `make integration` recorded |

## Open questions

<!--
Numbered, owner-tagged, and resolvable. Convert each to a decision-log entry
when answered.
-->

1. `@author`: Does containerd on the pinned `v2.3.5` return `paused` from
   `Task.Status` immediately after `Task.Pause` returns, or is a short poll
   needed? Resolve with a spike before M1.
2. `@reviewer-one`: Should the `501` path ever be reachable, given pause is
   mandatory in containerd runc? If not, drop the case and the test.

## Decision log

<!-- Append-only. Date, decision, rationale, author. -->

| Date | Decision | Rationale | Author |
| --- | --- | --- | --- |
| 2026-09-13 | Split a detailed design | Crosses ports, app, and adapter boundaries | `@author` |
| 2026-09-14 | Reuse domain `paused` state instead of a new field | State machine already models it | `@author` |

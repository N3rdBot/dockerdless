# ADR-0004: Docker identity and state mapping

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: dockerdless maintainers
- **Related**: [docs/architecture.md](../architecture.md)

## Context and problem statement

containerd and Docker model identity and state differently, and the daemon
must present Docker's view across a restart.

In containerd, **metadata and tasks are separate services**: a container
metadata record can exist with no task, and a task can exist independently of
the metadata used to recreate it. Reading only metadata cannot tell you
whether a container is running, and a naive startup restore would report
stale or ambiguous state. Docker clients, and testcontainers-go specifically,
read `State`, `Image`, name, and command from inspect and list responses, and
they read them immediately after startup and after a client process restart.

The daemon's in-memory registry is intentionally volatile, so on restart it
must reconstruct a faithful Docker view from what containerd still holds.

## Decision

**Keep an explicit Docker identity and name registry, reconcile it from
containerd metadata and tasks at startup, and never derive `running` from
metadata alone.**

- `internal/domain/registry.go` is the authoritative in-memory registry,
  keyed by Docker container identity and supporting name lookup, save, rename,
  and remove.
- At startup `cmd/dockerdless/reconciliation.go` lists containerd containers
  and their tasks in the active namespace and feeds both views to
  `domain.Reconcile` (`internal/domain/reconcile.go`).
- Reconciliation rules: metadata with a task takes the task's state; metadata
  **without** a task is restored as `exited` and reported as stale; a task
  without metadata is reported for cleanup. A reconciliation failure is
  logged and the daemon still starts.
- Because metadata alone cannot carry everything Docker inspect needs, the
  daemon pins the **image digest** and the **container command** into
  container labels (`io.dockerdless.image-digest`,
  `io.dockerdless.command`, plus `io.dockerdless.name`). These survive a
  restart and are the source for `ImageDigest` and `Spec.Command` on restore.
- Recovered states are logged through the containerd-topic to Docker-action
  mapping in `internal/domain/events.go`, so the mapping stays a live contract.

## Consequences

**Positive**

- Docker inspect and list report correct `running`/`exited` state after a
  daemon restart.
- Stale metadata and orphan tasks are surfaced rather than silently hidden.
- Image digest and command survive because they live in labels, not only in
  volatile memory.
- The reconciliation function is pure and unit-testable without containerd.

**Negative**

- Restore fidelity depends on labels being written at create time; a container
  created outside the daemon will not carry them.
- Volatile registry state means a reconciliation pass must succeed at startup
  for full fidelity, and a failure degrades to an empty registry with a
  warning.

**Neutral**

- Labels are visible through inspect; internal labels are filtered from the
  Docker-facing output where appropriate.

## Alternatives considered

- **Derive `running` from metadata**: metadata has no task state, so it
  over-reports running and misses exits.
- **Persist the registry to disk**: duplicates containerd as a source of
  truth and risks drift; reconciliation from the substrate is simpler.
- **Rebuild from containerd only, no labels**: loses digest and command that
  Docker inspect requires across a restart.

## References

- `internal/domain/model.go`: `Container`, `ContainerState`, identity types
- `internal/domain/reconcile.go`: metadata and task merge rules
- `internal/domain/registry.go`: identity and name registry
- `internal/domain/events.go`: containerd topic to Docker action map
- `cmd/dockerdless/reconciliation.go`: startup snapshot load and restore
- Commit `feat(state): add Docker identity and runtime reconciliation model`
- Commit `feat(config): wire hot reload and startup state reconciliation`

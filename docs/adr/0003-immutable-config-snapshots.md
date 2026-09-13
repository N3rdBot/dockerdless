# ADR-0003: Immutable configuration snapshots

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: dockerdless maintainers
- **Related**: [docs/architecture.md](../architecture.md)

## Context and problem statement

The daemon needs environment and YAML configuration, validation, and hot
reload. Viper provides environment binding and file decoding, but it is also a
mutable, goroutine-visible global: any worker that reads Viper directly races
with a reload. A reload that produces an invalid configuration must not take
down or half-reconfigure a running daemon, and most settings (sockets,
namespace, CNI and BuildKit paths, telemetry) cannot change under live
connections anyway.

Left unconstrained, configuration access becomes ambient mutable state spread
across the codebase, and a bad edit can silently change part of the daemon.

## Decision

**Viper is confined to `internal/config`. Workers read immutable snapshots
published atomically.**

- `Store` owns the only Viper instance. `Store.Current()` returns a **value
  copy** of the last valid `Config`; callers never touch Viper
  (`internal/config/store.go`).
- The snapshot is published with an atomic pointer store after successful
  decode and `Validate()`. A failed load reports the error through the store's
  handler/channel and leaves the previous snapshot untouched.
- The watcher (`internal/config/watch.go`) triggers reloads. The composition
  root compares the previous and current snapshots and acts on differences
  (`cmd/dockerdless/reload.go`).
- **Only `log-level` is hot-applied**, through zap's race-safe `AtomicLevel`.
  Every other field, including timeouts and the reserved feature flags, is
  classified as startup-only and emits a `requires restart` warning.
- `Load()` validates before publishing; an invalid reload never becomes the
  current snapshot.

## Consequences

**Positive**

- Readers hold an immutable value, so a reload cannot tear a struct a worker
  is reading.
- An invalid edit is rejected and the daemon keeps serving from the last valid
  configuration.
- The set of hot-reloadable settings is explicit and small, which keeps
  runtime behavior predictable.
- Config schema, validation, and decode live behind one package boundary.

**Negative**

- Changing a startup-only setting requires an operator restart; the daemon
  warns but does not adapt.
- Snapshot comparison must enumerate every field, so a new field needs a
  corresponding entry in the change classification.

**Neutral**

- `Store.Current()` returns a copy, so large reads are cheap and safe; the
  timeout fields are durations and the rest are small scalars or paths.

## Alternatives considered

- **Read Viper directly in workers**: data races with reload and no
  validation boundary.
- **Hot-apply everything**: sockets, namespaces, and backend clients are
  already constructed; mutating them live would be unsafe and complex.
- **No reload at all**: heavier operator iteration than an MVP needs, and the
  immutable snapshot makes safe reload cheap.

## References

- `internal/config/config.go`: `Config`, `Validate`, per-field decode
- `internal/config/store.go`: `Store`, `Current`, atomic publish
- `internal/config/watch.go`: fsnotify-driven reload
- `cmd/dockerdless/reload.go`: snapshot diff, hot `log-level`, restart notices
- Commit `feat(config): add validated immutable hot-reload snapshots`
- Commit `feat(config): wire hot reload and startup state reconciliation`

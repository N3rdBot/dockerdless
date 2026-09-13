# ADR-0005: Disable Ryuk in the MVP

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: dockerdless maintainers
- **Related**: [docs/compatibility.md](../compatibility.md), `integration/`

## Context and problem statement

testcontainers-go registers a reaper container named Ryuk by default. Ryuk is
a separate container that mounts the Docker socket, watches testcontainers for
labels, and tears containers down when a test process ends, including when it
crashes. It is a convenience service that most Docker users never think about.

For this MVP, Ryuk is a poor fit. It is itself a container that must be
provisioned through the same daemon under test, which means the verification
harness would depend on pulling and running an external image. In an offline
or air-gapped CI environment there is no registry egress, so Ryuk cannot be
provisioned at all. It also adds a second API surface (container create, start,
label watch, remove) that runs while the harness is trying to test the first
one, which muddies failure attribution.

The MVP targets a deterministic single-host flow: one daemon, one real
containerd, BuildKit, and CNI, with cleanup the test itself controls.

## Decision

**The MVP requires `TESTCONTAINERS_RYUK_DISABLED=true` and implements no
reaper.**

- The integration harness sets `TESTCONTAINERS_RYUK_DISABLED=true` before
  testcontainers reads its config, and asserts the setting is honored.
- A runtime compatibility test fails if a Ryuk or reaper container appears
  while the harness runs, so the guarantee cannot silently regress.
- Cleanup is explicit: tests call `t.Cleanup` and
  `testcontainers.CleanupContainer` rather than relying on a reaper.
- The README and compatibility document state the requirement and the reason.

## Consequences

**Positive**

- The harness runs fully offline with no registry pull for the reaper.
- No reaper container competes with the containers under test, so failures
  point at the daemon, not at Ryuk.
- Cleanup is deterministic and visible in the test, which suits the MVP's
  single-host, explicit-lifetime model.

**Negative**

- A test process that crashes hard can leave containers behind; the next run
  or an operator must clean them up.
- Every consumer of the daemon, not just this repo's tests, must set the
  environment variable or provision Ryuk themselves.

**Neutral**

- This is a harness and documentation contract, not a daemon feature. The
  daemon neither knows nor cares that Ryuk is disabled.

## Alternatives considered

- **Implement Ryuk support now**: requires reliable image pull and a second
  container lifecycle running in parallel with the tested surface, and it
  breaks offline CI.
- **Ignore Ryuk and let consumers hit the failure**: poor experience: a
  confusing "container cannot be provisioned" error with no explanation.
- **Ship a custom reaper**: more moving parts than explicit test cleanup for
  an MVP.

## Future work

A future release could support Ryuk by serving the container lifecycle and
label-watch surface it needs, or by allowing a pre-pulled reaper image. Until
then, `TESTCONTAINERS_RYUK_DISABLED=true` and explicit cleanup are the
supported configuration.

## References

- `integration/harness_test.go`: sets and relies on Ryuk being disabled
- `integration/compat_test.go`: asserts `config.RyukDisabled`
- `integration/compat_runtime_test.go`: fails if a reaper container appears
- `docs/compatibility.md`: Ryuk listed under unsupported features
- `README.md`: user-facing requirement
- Commit `test(integration): add real runtime fixture harness`
- Commit `test(compat): verify testcontainers-go Docker API matrix`

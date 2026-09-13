# Architecture Decision Records

An Architecture Decision Record (ADR) captures one significant, hard-to-reverse
decision, the context that forced it, and its consequences. The record is the
decision's paper trail: it explains *why* the code looks the way it does to
anyone who joins later or who wants to change it.

ADRs here use the [Michael Nygard format](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions):
Context, Decision, Consequences, plus alternatives and references.

## When an ADR is required

Write an ADR before committing to a decision that is irreversible or
cross-cutting. In practice that means any change to:

- **Substrate or backend**: what runtime, image, network, or build system the
  daemon sits on, and how it talks to it.
- **Compatibility contract**: what Docker API surface is served, what is
  rejected, and what error shape a client sees.
- **Configuration surface**: which settings exist, which are hot-reloadable,
  and which are startup-only.
- **Data or state shape**: how resources are identified, persisted, and
  reconciled across restarts.
- **Dependencies**: adopting a heavyweight library or a new process the daemon
  must keep alive.
- **Security boundary**: the trust model, socket permissions, root
  requirements, or credential handling.

A small refactor, a bug fix that restores intended behavior, or a test-only
change does not need an ADR. If you are unsure, write one: an extra record is
cheap, and a missing one costs archaeology later.

## Lifecycle

1. **Proposed**: someone opens an ADR as a PR and gathers feedback.
2. **Accepted**: the decision is in force and the code reflects it. Most
   backfilled records here are `Accepted` because the code already embodies
   them.
3. **Deprecated**: no longer recommended, but not replaced yet.
4. **Superseded by ADR-NNNN**: a newer record replaces it. The superseded file
   stays in place; only its status header changes, and it links forward.

Do not rewrite an accepted decision in place. Change it by adding a new ADR and
marking the old one superseded.

## Numbering

File names are `NNNN-slug.md`, for example `0003-immutable-config-snapshots.md`.
Numbers are four digits, zero padded, and assigned in order of creation.
**Never reuse a number**, even when a record is deprecated or superseded.
`0000-template.md` is reserved for the template and is not a decision.

## Relationship to proposals and designs

A **proposal** argues for a direction before a decision is made. A **design
document** describes how an accepted direction is built. An **ADR** records the
decision itself, in one page, and is the durable anchor both point at.

The flow is: proposal (optional, exploratory) -> design (optional, detailed) ->
ADR (the decision). A design usually references one or more ADRs. When a design
and its ADR disagree, the ADR wins until a new ADR changes it.

## Index

| ADR | Title | Status | Date | Summary |
| --- | --- | --- | --- | --- |
| [0001](0001-native-containerd-primary.md) | Native containerd as the primary substrate | Accepted | 2026-09-13 | Implement the daemon on the containerd gRPC API, CNI, and BuildKit instead of wrapping `nerdctl` or treating CRI as the runtime. |
| [0002](0002-defer-cri-runtime-adapter.md) | Defer the CRI runtime adapter | Accepted | 2026-09-13 | CRI is out of scope for the MVP; `enable-cri` is an inert, reserved flag, and a future adapter would slot behind the existing runtime port. |
| [0003](0003-immutable-config-snapshots.md) | Immutable configuration snapshots | Accepted | 2026-09-13 | Viper is confined to `internal/config`; workers read immutable snapshots published atomically, invalid reloads keep the last valid state, and only `log-level` is hot-applied. |
| [0004](0004-docker-identity-and-state-mapping.md) | Docker identity and state mapping | Accepted | 2026-09-13 | Keep an explicit Docker name/identity registry reconciled from containerd metadata and tasks at startup, and pin image digest and command in labels. |
| [0005](0005-disable-ryuk-in-the-mvp.md) | Disable Ryuk in the MVP | Accepted | 2026-09-13 | The MVP requires `TESTCONTAINERS_RYUK_DISABLED=true` and ships no reaper; cleanup is explicit through the test harness. |
| [0006](0006-no-moby-daemon-fork.md) | Do not fork the Moby daemon | Accepted | 2026-09-13 | Reuse only `github.com/moby/moby/api` and `client` types and implement a thin translation layer rather than forking Moby, libnetwork, swarm, or plugins. |
| [0007](0007-explicit-compatibility-surface.md) | Explicit compatibility surface | Accepted | 2026-09-13 | Every Docker create field and route is classified handled, rejected, or documented-ignored, enforced by an exhaustive reflective test, with Docker-shaped errors. |

## Writing a new ADR

1. Copy `0000-template.md` to the next free number.
2. Fill in the metadata header.
3. Keep it decision-first and roughly 40 to 90 lines.
4. Reference real file paths and, where useful, commit subjects.
5. Open it as `Proposed`; flip to `Accepted` once the decision holds.
6. Add a row to the index table above.

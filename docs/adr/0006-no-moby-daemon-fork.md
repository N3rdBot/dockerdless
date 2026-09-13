# ADR-0006: Do not fork the Moby daemon

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: dockerdless maintainers
- **Related**: [docs/compatibility.md](../compatibility.md), ADR-0001

## Context and problem statement

Docker's API is defined by Moby: the `dockerd` daemon, its API types, its
networking (libnetwork), swarm mode, and the plugin system. A daemon that
wants to be Docker-compatible must decide how much of Moby to adopt.

Forking the daemon, or vendoring libnetwork and swarm wholesale, would inherit
a very large, tightly coupled codebase and its release cadence. The Docker
Engine daemon is not designed to be embedded; pulling it in would drag in
libnetwork, the plugin manager, swarm, and much of the storage stack. That is
a multi-year maintenance commitment and makes it far harder to keep the
substrate (containerd, BuildKit, CNI) replaceable behind ports.

At the same time, the wire format is a contract. Clients are built against
Moby's Go types, and hand-rolling a parallel type tree would drift and break
subtle fields.

## Decision

**Do not fork the Moby daemon, libnetwork, swarm, or plugins. Reuse only the
`github.com/moby/moby/api` and `github.com/moby/moby/client` types and
implement a thin translation layer.**

- `go.mod` depends on `github.com/moby/moby/api` for the Docker wire types
  (`container.Config`, `container.HostConfig`, image and network types) and
  `github.com/moby/moby/client` where a Docker-shaped client is needed.
- `internal/api/` decodes requests into those types, classifies and validates
  them, and translates them into daemon-neutral application requests.
- The application and adapters use `internal/ports` and `internal/domain`
  types, not Moby types, so the wire contract is confined to the HTTP
  boundary.
- Routing, socket handling, error envelopes, and hijacked streams are
  implemented here; no Moby server code is imported.

## Consequences

**Positive**

- The wire contract tracks the upstream types, so a client sees Docker-shaped
  payloads without a hand-maintained copy of the type tree.
- The substrate stays replaceable: Moby types do not leak past `internal/api`.
- The codebase remains small and auditable, with no daemon fork to keep in
  sync with upstream.
- A dependency bump is the mechanism for adopting new API fields, and the
  exhaustive field-policy test catches any new field that needs a decision
  (see ADR-0007).

**Negative**

- Behavior that lives inside the Moby daemon (restart policies, resource
  limits, many network features) is not inherited and must be implemented,
  rejected, or documented as ignored.
- The daemon reimplements what Moby provides, so some Docker features remain
  unsupported by design.

**Neutral**

- Moby API types are a versioned dependency; upgrading them is a deliberate
  act, not an automatic sync.

## Alternatives considered

- **Fork `dockerd` and embed it**: inherits libnetwork, swarm, and the plugin
  manager; huge maintenance burden and destroys substrate replaceability.
- **Hand-roll Docker wire types**: drifts from the contract and breaks subtle
  fields clients depend on.
- **Depend on Moby server packages without forking**: still couples the
  daemon to Moby internals and the same heavy dependency tree.

## References

- `go.mod`: `github.com/moby/moby/api`, `github.com/moby/moby/client`
- `internal/api/`: the entire Moby-typed boundary
- `internal/ports/ports.go`: daemon-neutral contracts
- `internal/domain/`: daemon-neutral models
- Commit `build(scaffold): establish daemon module and package boundaries`
- Commit `build(deps): enable otelzap bridge dependency resolution`
- Commit `feat(api): define Docker compatibility contracts`

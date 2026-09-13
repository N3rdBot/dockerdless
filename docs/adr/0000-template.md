# ADR-NNNN: <short decision title in the imperative or noun phrase>

- **Status**: Proposed | Accepted | Deprecated | Superseded by ADR-NNNN
- **Date**: YYYY-MM-DD
- **Deciders**: <names, roles, or "dockerdless maintainers">
- **Related**: <proposal/design/PR links or paths, or "none">

## Context and problem statement

<The forces at play: the situation, the constraint, the problem that must be
answered. State what is true about the system and why a decision is needed now.
Two to four short paragraphs. No solution here.>

## Decision

<The choice, in one clear statement, then the specifics that make it concrete.
If there are rules or invariants that follow, list them.>

## Consequences

**Positive**

- <what gets better or simpler>

**Negative**

- <what gets worse, more expensive, or is given up>

**Neutral**

- <follow-on effects that are neither good nor bad, but worth recording>

## Alternatives considered

- **<alternative>**: <why it was not chosen>

## References

- `<path/to/file.go>`: <what it shows>
- Commit `<short-subject>`: <how it relates>

---

## Inline example of a filled record

The block below shows the expected shape of a completed ADR. Delete it from
your copy.

```markdown
# ADR-0000: Bind the API server to a Unix socket

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: dockerdless maintainers
- **Related**: docs/security.md

## Context and problem statement

Docker clients connect over a Unix socket by default, and the daemon runs as
root on a single host. A TCP listener would expose container management to the
network unless extra authentication were added, which the MVP does not have.

## Decision

Listen only on a Unix domain socket, created mode 0660 and owned by the daemon
uid. Do not add a TCP listener in the MVP.

## Consequences

**Positive**

- The Docker client and testcontainers-go connect with no extra configuration.
- The filesystem permission mode is the access control boundary.

**Negative**

- Remote clients cannot reach the daemon without an SSH tunnel or port-forward.

**Neutral**

- Socket path is a startup-only setting.

## Alternatives considered

- **TCP with TLS**: requires certificate issuance and rotation the MVP does
  not have.
- **Abstract Unix socket**: no filesystem permission boundary.

## References

- `internal/api/server.go`: socket lifecycle and permission mode
- Commit `feat(server): expose Docker-compatible MVP HTTP API`: initial wiring
```

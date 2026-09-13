# ADR-0007: Explicit compatibility surface

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: dockerdless maintainers
- **Related**: [docs/compatibility.md](../compatibility.md), ADR-0006

## Context and problem statement

dockerdless serves a subset of the Docker API. Docker clients are permissive:
they send a `container.Config` and `container.HostConfig` full of fields, most
of them zero-valued, and a daemon can accept a request it does not actually
honor. That is the dangerous failure mode. A client asks for a privileged
container, a memory limit, or a custom restart policy, the daemon ignores the
field, reports success, and the workload runs with different semantics than
the client believes.

The Moby API types also grow. A `github.com/moby/moby/api` bump can add a new
create field, and without a deliberate check the daemon would start silently
dropping it.

An honest compatibility surface needs two properties: every field is
classified, and the classification cannot rot when the dependency changes.

## Decision

**Every Docker create field and route is explicitly classified as handled,
rejected, or documented-ignored, and the classification is enforced by an
exhaustive reflective test.**

- `internal/api/container_fields.go` holds `configFieldPolicy` and
  `hostConfigFieldPolicy`, mapping every wire field name to `fieldHandled`,
  `fieldRejected`, or `fieldIgnored`. Rejected fields fail the create with a
  Docker 501 before any provisioning; malformed values fail with a 400
  `NewInvalidParameter`.
- `internal/api/container_fields_test.go`
  (`TestContainerCreateFieldPolicyIsExhaustive`) reflects over
  `container.Config` and `container.HostConfig`, walks the embedded
  `container.Resources`, and fails when a field has no classification or when
  the policy map has a stale entry. A Moby bump that adds a create field
  breaks the test instead of being silently dropped.
- Every ignored field is listed in `docs/compatibility.md` with the reason it
  is safe to ignore.
- Unsupported **routes** return a Docker-shaped error envelope,
  `{"message": "..."}`, with a `404` (unrouted) or `501` (recognized but not
  implemented) status, never a Go error page or an empty success.

## Consequences

**Positive**

- A client never receives a false success for a field the daemon cannot honor.
- The ignored list is auditable and bounded; new Moby fields cannot slip in
  unclassified.
- Error bodies match the shape Docker clients parse, so failures surface
  cleanly in testcontainers-go and the Docker CLI.
- Compatibility claims are testable, not prose: the test is the enforcement.

**Negative**

- Adding a create field to the API requires a policy entry and, when rejected,
  a corresponding compatibility-doc row, so the bar for a new field is
  deliberate.
- Rejecting fields is stricter than Docker; a workload that relies on an
  unsupported field fails fast instead of degrading.

**Neutral**

- The policy is data, not branching logic, so the review surface is a map plus
  a test rather than a growing `if` chain.

## Alternatives considered

- **Accept and ignore unknown fields silently**: the exact failure mode this
  ADR exists to prevent.
- **Reject every unhandled field**: too strict for fields that are harmless
  (attach flags, Windows-only fields) and would break routine client requests.
- **Rely on documentation alone**: docs drift; a reflective test does not.

## References

- `internal/api/container_fields.go`: the field policy and validation
- `internal/api/container_fields_test.go`: the exhaustiveness test
- `internal/api/errors.go`: Docker-shaped error envelopes and 501 mapping
- `internal/api/routes.go`: routed surface
- `docs/compatibility.md`: handled/rejected/ignored tables and exact errors
- Commit `feat(api): define Docker compatibility contracts`
- Commit `fix(server): reject unsupported create fields and document field policy`
- Commit `fix(server): classify every create field and validate mount sources`

# Collaboration process

This document is the end-to-end workflow for changing dockerdless: from a raw
idea to a merged commit. Every artifact has a job. Nothing here is optional
decoration; each step exists because skipping it has cost the project a
compatibility regression, a leaked host resource, or a rewrite.

## Table of contents

- [The flow at a glance](#the-flow-at-a-glance)
- [Stage 1: Idea](#stage-1-idea)
- [Stage 2: Feature proposal](#stage-2-feature-proposal)
- [Stage 3: Detailed design (optional)](#stage-3-detailed-design-optional)
- [Stage 4: ADR (decisions only)](#stage-4-adr-decisions-only)
- [Stage 5: Implementation plan](#stage-5-implementation-plan)
- [Stage 6: TDD implementation](#stage-6-tdd-implementation)
- [Stage 7: Self-review](#stage-7-self-review)
- [Stage 8: Peer review](#stage-8-peer-review)
- [Stage 9: Merge](#stage-9-merge)
- [Who reviews what](#who-reviews-what)
- [Review gates](#review-gates)
- [Working artifacts and what gets committed](#working-artifacts-and-what-gets-committed)

## The flow at a glance

```text
  Idea
   │
   ▼
  Feature Proposal ──────────────► docs/proposals/NNNN-slug.md
   │  (required for new endpoints, compatibility changes,
   │   new dependencies, cross-cutting changes)
   ▼
  Detailed Design (optional) ─────► docs/design/NNNN-slug.md
   │  (required when a ports interface, adapter, state machine,
   │   concurrency, error taxonomy, or migration is touched)
   ▼
  ADR (decisions only) ───────────► docs/adr/NNNN-slug.md
   │  (required when a durable trade-off is locked in)
   ▼
  Implementation Plan ────────────► working notes (not committed)
   │
   ▼
  TDD Implementation ────────────► code + tests + evidence in the pull request
   │
   ▼
  Self-review ───────────────────► run the checklist, fix before asking
   │
   ▼
  Peer Review ───────────────────► docs/process/review-checklist.md
   │
   ▼
  Merge ─────────────────────────► Commits: Conventional Commits + `git commit -s`
```

Not every idea walks every step. A typo fix enters at Merge. A new endpoint
walks the whole path.

## Stage 1: Idea

Anyone can raise an idea in an issue or a discussion. At this stage there is no
artifact. The only question is whether the idea changes a contract. If it might
touch the Docker API surface, the package boundaries, or a dependency, it needs
a proposal. Use the trigger list in
[`docs/proposals/README.md`](../proposals/README.md#when-a-proposal-is-required)
to decide.

## Stage 2: Feature proposal

**Artifact:** `docs/proposals/NNNN-slug.md` from
[`docs/proposals/0000-template.md`](../proposals/0000-template.md).

**Required when:** a new endpoint, a compatibility-surface change, a new
dependency, or a cross-cutting change. See
[the trigger list](../proposals/README.md#when-a-proposal-is-required).

**Owner:** the proposer. **Reviewers:** at least one maintainer of the affected
area, named in the proposal Metadata block.

The proposal starts as `Draft`, moves to `In Review` when the author is ready
for other people's time, and reaches `Accepted` only when every named reviewer
agrees or abstains and no objection is unresolved. The status transitions and
the approval rule live in
[`docs/proposals/README.md`](../proposals/README.md#lifecycle-and-status-transitions).

Do not write production code before `Accepted`. A spike is fine; a merge is not.

## Stage 3: Detailed design (optional)

**Artifact:** `docs/design/NNNN-slug.md` from
[`docs/design/0000-template.md`](../design/0000-template.md).

**Required when:** the proposal adds or changes a `ports` interface, adds an
adapter, changes the domain state machine, touches concurrency or the error
taxonomy, or needs a migration. The full split rule is in
[`docs/design/README.md`](../design/README.md#when-to-split-a-proposal-into-a-design-doc).

**Owner:** the design owner named in Metadata. **Reviewers:** at least one
engineer who will implement from it, plus the maintainer of each touched layer.

The design is where interfaces get argued. Reviewers read the Go signatures, the
state-transition table, the error map, and the test plan before any file is
created. If the design reveals the proposal was wrong, fix the proposal first,
then the design.

## Stage 4: ADR (decisions only)

**Artifact:** `docs/adr/NNNN-slug.md`.

**Required when:** the change locks in a trade-off future readers will question,
such as choosing containerd over CRI, selecting a snapshotter, or accepting a
deliberately unsupported feature. A design can cite several ADRs.

**Owner:** whoever makes the decision. **Reviewers:** same maintainers as the
proposal.

The ADR records context, the decision, the alternatives, and the consequences. It
outlives the proposal. `docs/adr/` is owned by a separate workstream; if the
directory does not exist yet, link the intended path and note the dependency.

## Stage 5: Implementation plan

**Artifact:** working notes kept out of the repository.

The plan decomposes the accepted design's **Milestone checklist** into atomic
tasks, each with a verification command and an evidence record. It is the bridge
between design and code. The mapping rule is in
[`docs/design/README.md`](../design/README.md#how-a-design-maps-to-the-implementation-plan).

**Owner:** the implementer. **Reviewers:** none required; the plan is a working
artifact, not a decision record. It can be revised as reality intrudes, as long
as no verification is dropped.

## Stage 6: TDD implementation

**Artifact:** code, tests, and the evidence recorded in the pull request.

The project's test posture, from the work plan:

- **TDD for pure contracts, translators, and state machines.** Write the failing
  test first. This covers `internal/domain`, `internal/api/container_fields.go`
  classifications, error mapping, and stream framing.
- **Tests-after for runtime adapters and end-to-end integration.** containerd,
  BuildKit, and CNI paths are exercised against fakes first, then the live
  harness.
- **Never weaken an assertion to go green.** The integration suite skips with an
  explicit reason when a prerequisite is missing; it never silently passes.

Every task's evidence artifact records the exact command and its output. The
definition of done is in
[`docs/process/definition-of-done.md`](definition-of-done.md).

## Stage 7: Self-review

Before requesting peer review, the author runs
[`docs/process/review-checklist.md`](review-checklist.md) against their own
change. Fix what you find. Reviewers should not be the first line of defense for
gofmt, a missing race test, or a stale compatibility row.

Minimum self-checks:

```bash
gofmt -l .                 # must print nothing
go vet ./...
go test -race -count=1 ./...
make integration           # when a runtime surface changed
```

## Stage 8: Peer review

**Artifact:** the pull request. **Reviewer:** at least one maintainer of the
affected area; two when the change touches security or the compatibility
surface.

The reviewer walks [`docs/process/review-checklist.md`](review-checklist.md).
Review is about correctness, compatibility, boundaries, and evidence, not style
preferences. A review that finds a real defect cites the checklist section so
the author knows what to fix.

## Stage 9: Merge

**Artifact:** commits on the default branch.

- [Conventional Commits](https://www.conventionalcommits.org/): `feat(api):`,
  `fix(containerd):`, `docs(compatibility):`, `test(integration):`.
- Every commit is signed off under the DCO: `git commit -s`.
- Keep commits atomic and reviewable. Do not mix a compatibility-table update
  into an unrelated refactor. The compatibility row moves in the same commit as
  the handler that changes it.
- Merge only after the [review gates](#review-gates) pass.

See `CONTRIBUTING.md` for the contribution mechanics and the repository's
commit style.

## Who reviews what

| Change touches | Required reviewer |
| --- | --- |
| `internal/api` routes, error mapping, `docs/compatibility.md` | API maintainer |
| `internal/adapters/containerd` | Runtime maintainer |
| `internal/adapters/buildkit` | Image/build maintainer |
| `internal/adapters/cni` | Networking maintainer |
| `internal/domain` state machine or events | Architecture maintainer |
| `internal/config`, `cmd/dockerdless` | Platform maintainer |
| Socket permissions, credentials, `docs/security.md` | Security reviewer (two approvals) |
| `go.mod` dependency changes | A maintainer plus the proposal that justified it |

## Review gates

A change may merge only when all of these hold:

1. **Artifacts exist.** Proposal accepted; design accepted where required; ADR
   filed where a durable decision was made.
2. **Compatibility updated.** Any endpoint, field, or error change has a matching
   row in [`docs/compatibility.md`](../compatibility.md).
3. **Gates green.** `gofmt`, `go vet`, `go test -race ./...`, and `make integration`
   where a runtime surface changed.
4. **Evidence recorded.** The pull request records the exact command and the raw
   result, including any skip and its reason.
5. **Review passed.** [`review-checklist.md`](review-checklist.md) walked by a
   named reviewer.
6. **Commits clean.** Conventional Commits, `Signed-off-by` present, atomic.

## Working artifacts and what gets committed

Plans and evidence are the working state of the project, not the product
surface.

- **The implementation plan** is derived from a design. It is how the work is
  decomposed and tracked, and it stays out of the repository.
- **The evidence record** lives in the pull request. Each task records the exact
  command it ran, the raw result, and any skip reason. Reviews and merges cite
  it. A claim without evidence is not accepted.
- **Working notes** are append-only. They capture what the team learned that is
  not yet in a durable doc, such as a containerd behavior discovered
  mid-implementation, and stay out of the repository.

Only code, tests, docs, and the pull request description are committed. The
durable story lives in `docs/proposals/`, `docs/design/`, `docs/adr/`, and
`docs/compatibility.md`.

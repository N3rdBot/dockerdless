# Feature proposals

A feature proposal is a short, reviewable document that argues for a change
before anyone writes code. It answers "what should we build, why, and what does
it touch." The implementation mechanics live in a [detailed design](../design/README.md)
when the change is large enough to need one.

Start from [`0000-template.md`](0000-template.md). Copy it, never edit it.

## Table of contents

- [Index](#index)
- [When a proposal is required](#when-a-proposal-is-required)
- [When a proposal is not required](#when-a-proposal-is-not-required)
- [Numbering](#numbering)
- [Lifecycle and status transitions](#lifecycle-and-status-transitions)
- [Proposal, design, and ADR: how they relate](#proposal-design-and-adr-how-they-relate)
- [Review and approval](#review-and-approval)

## Index

| Proposal | Title | Status | Date | Summary |
| --- | --- | --- | --- | --- |
| [0001](0001-docker-api-daemon-over-containerd.md) | A Docker Engine API daemon over containerd | Implemented | 2026-09-13 | Serve the Docker API subset testcontainers-go needs over native containerd, BuildKit, and CNI, with every served route and create field explicitly classified and verified live. |

## When a proposal is required

Write a proposal before implementation when a change does any of the following:

- **Adds or removes a Docker API endpoint.** The verified mapping in
  [`docs/compatibility.md`](../compatibility.md#capability-mapping) is a
  contract. Every new endpoint gets a proposal so the compatibility surface is
  argued, not discovered.
- **Changes the compatibility surface.** New request or response fields, a
  changed error status, a new `HostConfig` field classification, or moving a row
  between supported and unsupported all qualify.
- **Adds a dependency.** A new module in `go.mod` (containerd, BuildKit, CNI,
  OTel, a test library) needs a proposal that states the version pin and why the
  existing stack cannot do the job.
- **Crosses a package boundary or a hexagonal boundary.** A new `ports`
  interface, a new adapter package, or a change to the `internal/domain` state
  machine affects the whole architecture. Propose it.
- **Changes operator-visible behavior.** A new config key, socket, log location,
  or telemetry signal is a proposal, because [`docs/operations.md`](../operations.md)
  and [`docs/security.md`](../security.md) must move with it.
- **Changes the security perimeter.** Socket permissions, credential handling,
  or a new host-privileged operation. See [`docs/security.md`](../security.md).

## When a proposal is not required

- Bug fixes that restore documented behavior.
- Tests, refactors, and comments that leave the wire contract and the package
  boundaries unchanged.
- Dependency patch bumps that stay within the pinned major version and change no
  behavior.
- Documentation-only edits.

If you are unsure, open a proposal. A short rejected proposal costs minutes; an
unreviewed compatibility change costs a release.

## Numbering

- Files are named `NNNN-short-slug.md`, for example
  `0007-container-pause.md`.
- `NNNN` is a zero-padded, monotonically increasing integer. Find the highest
  used number in `docs/proposals/`, `docs/design/`, and `docs/adr/`, then take
  the next value shared across all three trees. The number is allocated once and
  **never reused, even if the proposal is rejected or withdrawn.**
- The slug is lowercase, hyphenated, and descriptive. Keep it stable; do not
  rename a file after it has reviewers.
- The template `0000-template.md` is not a real proposal. It never appears in a
  link as a decision.

## Lifecycle and status transitions

The status lives in the proposal's Metadata block. A proposal moves through
these states:

```text
Draft ──► In Review ──► Accepted ──► Implemented
  │            │
  │            └──► Rejected
  │
  └────────────────────► Superseded ◄── (any state)
```

| Status | Meaning | Who sets it |
| --- | --- | --- |
| **Draft** | The author is still writing. Not ready for reviewers. | Author |
| **In Review** | Reviewers are engaged. The author stops rewriting the argument. | Author |
| **Accepted** | Reviewers agree; implementation may start. | Reviewer or maintainer |
| **Rejected** | The change is declined. The rationale lives in the Decision log. | Reviewer or maintainer |
| **Implemented** | The feature shipped and `docs/compatibility.md` reflects it. | Author, at merge |
| **Superseded** | A later proposal replaces this one. Both documents link each other. | Author of the later proposal |

Rules:

- Draft to In Review happens once the template is fully filled and the author
  is ready for other people's time.
- In Review can bounce back to Draft; that is normal. Bump **Updated** each
  time.
- Accepted is the gate to implementation. Do not build ahead of review.
- Implemented is not the same as merged. Mark it once the compatibility table
  says `verified live` and the evidence artifact exists.
- Never delete a proposal. Rejected and Superseded documents are the project's
  memory.

## Proposal, design, and ADR: how they relate

Three documents, three jobs:

| Document | Question it answers | Lives in | Ends with |
| --- | --- | --- | --- |
| Proposal | What are we building, and why? | [`docs/proposals/`](.) | An accepted direction |
| Detailed design | How will it be built inside this codebase? | [`docs/design/`](../design/) | An implementation-ready contract |
| ADR | Which hard trade-off did we lock in, and what did we give up? | `docs/adr/` | One durable decision record |

The proposal is the front door. If a reviewer accepts a proposal, the author
either implements directly (small change) or splits a detailed design (large
change). An ADR is separate: write one whenever the proposal makes a decision
that future readers will question, such as choosing containerd over CRI, or
snapshotter selection, or a deliberately unsupported feature. One proposal can
produce several ADRs. An ADR can outlive the proposal that spawned it.

See [Detailed design: how it relates to a proposal](../design/README.md#how-a-design-relates-to-a-proposal)
for the split rule.

## Review and approval

- **Authors** fill the template and request review.
- **Reviewers** are named in the Metadata block. At least one must be a
  maintainer of the affected area (runtime, images, networking, or API).
- **Approval rule:** a proposal reaches **Accepted** when every named reviewer
  has either approved in the Decision log or explicitly abstained, and no
  reviewer has an unresolved objection. A single unresolved security objection
  blocks acceptance regardless of other approvals.
- **Silence is not approval.** If a reviewer does not respond within a
  reasonable window, the author may replace them by editing the Metadata block
  and re-requesting review.
- **The Decision log is the record.** Every approval, rejection, and
  significant argument goes in the table at the bottom of the proposal, with a
  date and an author.

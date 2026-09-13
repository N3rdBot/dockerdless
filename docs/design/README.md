# Detailed designs

A detailed design document is the implementation contract for an accepted
[proposal](../proposals/README.md). It turns "what and why" into "how, exactly,
inside this codebase": packages, interfaces, the state machine, error mapping,
concurrency, tests, and the milestone sequence.

Start from [`0000-template.md`](0000-template.md). Copy it, never edit it.

## Table of contents

- [How a design relates to a proposal](#how-a-design-relates-to-a-proposal)
- [When to split a proposal into a design doc](#when-to-split-a-proposal-into-a-design-doc)
- [When a design doc is not needed](#when-a-design-doc-is-not-needed)
- [How a design maps to the implementation plan](#how-a-design-maps-to-the-implementation-plan)
- [Numbering and lifecycle](#numbering-and-lifecycle)

## How a design relates to a proposal

The two documents split by question, not by size:

| | Proposal | Detailed design |
| --- | --- | --- |
| Question | What are we building, and why? | How will it be built here? |
| Audience | Reviewers deciding direction | Implementers writing the code |
| Grounding | Docker API, native primitives, alternatives | Actual package layout, Go signatures, tests |
| Answers | Goals, non-goals, compatibility impact | Interfaces, flow, races, error map, milestones |
| Authority | Accepted direction | Implementation-ready contract |
| Lives in | [`docs/proposals/`](../proposals/) | `docs/design/` |

A design doc must link its proposal and must not restate the motivation. If the
design discovers that the proposal was wrong, the author edits the proposal (or
supersedes it), then updates the design. Designs do not silently disagree with
their proposal.

An [ADR](../proposals/README.md#proposal-design-and-adr-how-they-relate) is
different again: it records a durable trade-off, not a plan. A design can cite
several ADRs, and a design decision that future readers will question should
become one.

## When to split a proposal into a design doc

Split when any of these is true:

- The change adds or changes a `ports` interface.
- It adds an adapter or crosses a hexagonal boundary.
- It changes the `internal/domain` state machine (a new state or transition).
- It touches concurrency: new goroutines, shared mutable state, or cancellation.
- It changes the Docker error taxonomy or a status-code mapping.
- It needs a migration, a new config key, or a new telemetry signal.
- The implementation is more than roughly two focused days of work.

A small, additive handler that reuses an existing port and state needs no
design; mark the proposal's design link `Not required: rationale` and move on.

## When a design doc is not needed

- Bug fixes that restore documented behavior.
- Pure refactors with no contract change.
- A one-handler, one-use-case change under a single package, with no new port.

If you waver, look at the split list. "It touches one package and no interface"
is the clean test for skipping a design.

## How a design maps to the implementation plan

The design's **Milestone checklist** is the source for the execution plan. Each
milestone becomes one or more atomic tasks in the implementation plan, and each
task's verification maps to a row in the design's **Test plan**.

```text
Design milestone         ->  Plan task (implementation plan) ->  Evidence (pull request)
M1 port + adapter        ->  task-<N>-add-pause-port        ->  exact command + raw output
M2 app use case          ->  task-<N+1>-pause-usecase       ->  exact command + raw output
M3 handlers + docs       ->  task-<N+2>-pause-handlers      ->  exact command + raw output
M5 live integration      ->  task-<N+3>-pause-integration   ->  exact command + raw output
```

Rules that keep the mapping honest:

- A task is not done until its design row's assertion is green. The pull request
  records the command that produced it.
- The plan may reorder milestones for parallelism, but it may not delete a
  verification. If a milestone can be dropped, update the design first.
- The full end-to-end process, including where plans and evidence fit, lives in
  [`docs/process/collaboration.md`](../process/collaboration.md).

## Numbering and lifecycle

- Files are `NNNN-short-slug.md`, sharing the **same global counter** as
  proposals and ADRs. Never reuse a number.
- A design's status mirrors its proposal: `Draft` while the author writes,
  `In Review` while reviewers check the interfaces and tests, `Accepted` when
  implementation may start, `Implemented` after merge.
- When a design is superseded, keep the file and link both directions. The old
  design is still the record of why the code looked the way it did.

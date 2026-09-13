# Commit conventions

dockerdless uses [Conventional Commits](https://www.conventionalcommits.org/)
with a mandatory DCO sign-off. This document is the authoritative spec: it
defines the header grammar, the allowed types, the scope vocabulary, the body and
footer rules, the breaking-change format, and the atomicity rule. The local
`commit-msg` hook enforces the parts a machine can check; review enforces the
rest.

If this document and the hook ever disagree, that is a bug. Fix one of them in
the same change.

## Table of contents

- [Why a strict format](#why-a-strict-format)
- [Header](#header)
- [Body](#body)
- [Footer](#footer)
- [Breaking changes](#breaking-changes)
- [Atomicity](#atomicity)
- [Examples](#examples)
- [Enforcement](#enforcement)
- [Related documents](#related-documents)

## Why a strict format

A commit message is not decoration. It is the only durable record of a change
that survives a rebase, a squash, and a `git blame` two years from now. The
project pays for this discipline in four concrete ways.

- **Reviewability.** A reviewer reads the subject first. A typed, scoped,
  imperative subject tells them what changed and where before they open the
  diff. A vague subject forces them to reconstruct intent from code.
- **Automated changelog.** The type and scope are machine-readable. The same
  parse that produces a changelog groups `feat` away from `fix` and attributes
  each entry to an area. An unparseable message drops out of the release notes
  silently.
- **Bisectability.** A refactor and a behavior change in one commit make
  `git bisect` lie: the first bad commit looks like cleanup. One logical change
  per commit keeps the search honest.
- **Traceability to proposals and ADRs.** A `feat(api)` that implements a
  proposal cites it with `Refs:`. A `fix` that follows an ADR cites the ADR. The
  commit becomes the link between a decision and the code that carries it.

## Header

The header is one line and has exactly this shape:

```text
<type>[(<scope>)][!]: <description>
```

- `<type>` is one of the allowed types below. Required.
- `(<scope>)` is optional. It is lowercase kebab-case and names the module or
  area affected.
- `!` after the type or after the scope marks a breaking change. See
  [Breaking changes](#breaking-changes).
- `: ` separates the header from the description. The space after the colon is
  required.
- `<description>` is the subject. Its rules follow the type table.

### Allowed types

Every type is from the Conventional Commits set. The meaning column is the only
one that counts; a commit that does not fit a type does not belong in one commit.

| Type | Meaning | Example |
| --- | --- | --- |
| `feat` | A new feature or user-visible capability. | `feat(api): define Docker compatibility contracts` |
| `fix` | A bug fix. | `fix(server): classify every create field and validate mount sources` |
| `docs` | Documentation only. | `docs(deploy): add deployment guide and systemd layout` |
| `style` | Formatting or whitespace with no behavior change. | `style(api): reorder handler methods` (no instance yet in this repo) |
| `refactor` | A behavior-preserving code change. | `refactor(config): split the store into load and watch` (no instance yet in this repo) |
| `perf` | A change that improves performance. | `perf(streams): drop allocations in the frame reader` (no instance yet in this repo) |
| `test` | Tests only, no production behavior. | `test(integration): add real runtime fixture harness` |
| `build` | Build system, module, or dependency changes. | `build(scaffold): establish daemon module and package boundaries` |
| `ci` | Continuous integration configuration. | `ci(git): run the release gate on pull requests` (no instance yet in this repo) |
| `chore` | Maintenance that is none of the other types. | `chore(git): ignore the local agent work folder` |
| `revert` | Reverts a previous commit. | `revert(server): revert structured mount translation` (no instance yet in this repo) |

The examples above marked "no instance yet in this repo" show the required
shape; the repository has not used those types yet, so they are not historical
subjects. Every example in [Examples](#examples) is a real subject from this
repository's history.

A `revert` commit uses the same header shape and adds one body line:

```text
revert(server): revert structured mount translation

This reverts commit 8ae6cf81ca77d41a42796af64c3d31c0db390c15.

Signed-off-by: Ada Lovelace <ada@example.com>
```

### Scope

The scope is optional. When present it must be lowercase kebab-case (`^[a-z0-9]+(-[a-z0-9]+)*$`)
and it must name the module or area the change touches. The hook does not check
membership in a fixed set, but it does check the shape: `API`, `api_v2`, and
`internal/api` are rejected.

Prefer the narrowest area that is still honest. Use a module or package name
when the change lives in one place; use a cross-cutting area name only when the
change really spans it.

Recommended vocabulary for this repository, grouped by area:

- **Backend and runtime:** `api`, `app`, `config`, `domain`, `ports`,
  `adapters`, `streams`, `observability`, `runtime`, `images`, `io`, `server`,
  `state`, `cmd`
- **Delivery and tooling:** `deploy`, `release`, `deps`, `ci`, `git`, `scaffold`
- **Compatibility and test:** `compat`, `integration`
- **Process and decisions:** `docs`, `design`, `adr`, `proposals`, `process`

Pick a scope from this list when it fits. If the touched area is not listed,
name the module or area directly and keep the same shape, for example
`refactor(config):` or `fix(buildkit):`.

### Subject

The text after `: ` is the subject. It follows five rules.

1. **Imperative mood, present tense.** Write the command you are giving the
   codebase: `add`, `end`, `classify`, `wire`. Not `added`, `adds`, `adding`.
   This matches `git`'s own voice and keeps the log scannable.
2. **No trailing period.** A subject is a title, not a sentence.
3. **Lowercase preferred.** Start lowercase unless the first word is a proper
   noun or an identifier (`Docker`, `CNI`, `OpenTelemetry`).
4. **Describe the change, not the file.** `docs(deploy): add deployment guide`
   not `docs(deploy): update deployment.md`. The diff shows which file; the
   subject shows what the change achieves.
5. **One line, bounded length.** Aim for 72 characters or fewer. The hard limit
   is 100; the hook rejects a header longer than 100. A subject between 72 and
   100 is accepted but is a sign the change may need splitting.

The real subject
`fix(runtime): end log follow on exit, release reserved ports once, restore container fidelity`
sits above the recommended 72 but under the hard 100. It passes the hook; it
also packs three fixes into one line, so a reviewer should ask whether it should
have been three commits.

## Body

The body is optional. It starts after one blank line following the header. Leave
that blank line in place even when the body is short; the hook and every parser
rely on it.

Write the body when the change is not self-evident from the subject and diff.
That means:

- a behavior change whose reason is not obvious from the code;
- a fix whose root cause is worth recording;
- a trade-off a future reader will question;
- any breaking change, which must carry a `BREAKING CHANGE:` footer (see below).

The body explains **what** changed and **why**. It does not narrate **how**; the
diff already shows the how. Do not restate the subject, do not paste a
changelog, and do not describe the mechanical steps you took.

Wrap the body at 72 columns, same as the subject recommendation. Blank lines
separate paragraphs. A `-` bullet list is allowed when the change has several
distinct parts.

Good body:

```text
fix(config): retain the last valid snapshot on reload

A failed parse used to publish a zero value, so a typo in the config file
took the daemon down on the next read. The store now validates off to the
side and swaps the pointer only after validation passes; a rejected reload
keeps the previous snapshot and logs the reason.
```

## Footer

Footers go after the body, separated from it by one blank line. Each footer is
one line in `Token: value` form.

### Signed-off-by (required)

Every commit must carry exactly this trailer:

```text
Signed-off-by: Full Name <email@example.com>
```

This is the [Developer Certificate of Origin](https://developercertificate.org/)
sign-off. The name and email must be real and must match the commit author. Do
not invent them.

`git commit -s` appends the trailer automatically from your configured
`user.name` and `user.email`. Use it on every commit:

```bash
git commit -s -m "fix(runtime): release reserved ports once"
```

A pull request with an unsigned commit is returned to the author to amend before
review. The hook rejects a commit whose message lacks the trailer.

### Optional trailers

- `Refs:` links the change to a proposal, design, ADR, or issue, for example
  `Refs: docs/proposals/0001-docker-api-daemon-over-containerd.md` or
  `Refs: #12`.
- `Closes:` marks an issue this commit resolves, for example `Closes: #34`.
- `Co-authored-by:` credits an additional author in the same
  `Full Name <email>` form. One line per co-author.

Order the footers so the machine-readable ones come first, then `Signed-off-by`
last. When a commit is breaking, `BREAKING CHANGE:` comes before the others.

## Breaking changes

A breaking change is any change a client or operator can observe and must react
to: a wire-format change, a removed or renamed endpoint, a status-code change, a
config key that stops being honored, or a package boundary that moves for
downstream consumers.

Mark it twice:

1. `!` after the type, or after the scope when one is present:
   `feat!:` or `feat(api)!:`.
2. A `BREAKING CHANGE:` footer that explains what breaks and what to do about
   it. The `!` alone is not enough; the hook rejects a header with `!` that has
   no `BREAKING CHANGE:` footer.

The footer is the migration note. Say what changed, who it affects, and the
alternate path. Vague is not acceptable: "some things changed" is a rejected
footer in review.

Worked example:

```text
feat(api)!: classify every container create field

The MVP accepted HostConfig fields it silently dropped, so a client could
believe a security policy was applied when it was not. Every wire field is
now classified as handled, rejected, or ignored, and an unhandled field
returns a Docker-shaped 501 instead of succeeding.

BREAKING CHANGE: create requests that set a field outside the documented
policy now fail with 501. Clients must drop the field or wait for the
proposal that adds support. See docs/compatibility.md for the full field
table.

Refs: docs/proposals/0002-create-field-policy.md
Signed-off-by: Ada Lovelace <ada@example.com>
```

## Atomicity

One logical change per commit. A commit must be reviewable on its own and
revertable on its own, with no collateral.

Split when the parts can be reviewed or reverted independently:

- A refactor that enables a fix ships first as `refactor`, then the behavior
  change ships as `fix`. If the fix is reverted, the refactor stays.
- Tests ship in the commit that introduces or changes the behavior they lock.
- A `docs/compatibility.md` row ships in the same commit as the handler that
  changes it, because the doc and the code are one change.
- A dependency bump that a feature needs ships as `build(deps)` first, then the
  feature as `feat`.

Do not combine:

- unrelated scopes in one commit;
- a rename or move with a behavior change;
- generated or vendored files with source changes;
- whitespace or formatting churn with logic changes;
- a `fix` with a `refactor` with a `chore` in a single commit.

A commit that needs "and" in the subject is usually two commits. If a squash
merge collapses several commits, the composed message must follow this spec just
the same.

## Examples

### Good examples

All of these are real subjects from this repository's history. The note explains
what makes each one correct.

1. `feat(api): define Docker compatibility contracts`
   Type matches a new contract surface, `api` names the layer, the subject is
   imperative, lowercase, period-free, and says what the change does.

2. `fix(server): classify every create field and validate mount sources`
   A `fix` with a single clear purpose. Two related code paths, one defect
   class, so it stays atomic.

3. `docs(deploy): add deployment guide and systemd layout`
   `docs` type for a documentation-only change; `deploy` names the area; the
   subject describes the deliverable, not the file.

4. `test(integration): add real runtime fixture harness`
   `test` type because no production behavior changed; `integration` is the
   exact tree touched.

5. `build(scaffold): establish daemon module and package boundaries`
   `build` type for module and layout work; `scaffold` names the area; the
   subject states the outcome.

6. `chore(git): ignore the local agent work folder`
   `chore` for housekeeping; `git` scope; lower-case, imperative, no period.

7. `feat(config): add validated immutable hot-reload snapshots`
   A feature whose subject is specific enough to survive a changelog: it names
   the behavior (validated, immutable, hot reload) rather than "update config".

8. `docs(adr): add ADR template and backfill accepted decisions`
   Two related documentation deliverables in one area, so one atomic `docs`
   commit is right.

9. `feat(server): reject unsupported create fields and document field policy`
   Behavior change plus its documentation in one commit, matching the atomicity
   rule that a compatibility doc ships with the code it describes.

### Bad examples

Each message below is rejected, and the rule it breaks is exact.

| Bad message | Rule violated |
| --- | --- |
| `Added retry logic.` | No type and no scope. Past tense (`Added`). Trailing period. |
| `fix: stuff` | Vague subject. It does not describe the change, so it is useless in a changelog or a bisect. |
| `feat(api)!: change the error envelope` (no footer) | A `!` header must carry a `BREAKING CHANGE:` footer. The hook rejects this. |
| `update(server): tweak login` | `update` is not an allowed type. |
| `feat(API): Add Docker compatibility contracts` | Scope `API` is not lowercase kebab-case. Description starts uppercase; prefer lowercase. |
| `fix(runtime): end log follow on exit.` | Trailing period on the subject. |
| `docs: update docs` | Vague, and it describes the file instead of the change. |
| `chore: misc cleanup` | Vague subject. No reviewer can tell what "misc" means. |
| `feat(api): define contracts and refactor the config store and bump deps` | Not atomic. Three unrelated scopes in one commit, so a revert cannot be surgical. |
| `refactor(api): rename handler and fix the nil dereference` | Mixes a behavior-preserving refactor with a behavior change. Split into `refactor` then `fix`. |
| `feat(api): add image export endpoint that streams a tar archive to the client with progress` | Header exceeds the hard 100-character limit; the hook rejects it. Move detail into the body. |
| `feat(api): define contracts` (no trailer) | Missing the mandatory `Signed-off-by` DCO trailer. |

## Enforcement

The rules are enforced in two places: a local hook and review.

**Local hook.** The repository ships a `commit-msg` hook under `.githooks/`.
Enable it once per clone:

```bash
make hooks
```

That target points git at the checked-in hooks directory:

```bash
git config core.hooksPath .githooks
```

The hook runs on every `git commit` and fails the commit when a mechanically
checkable rule breaks:

- the header does not match `<type>[(<scope>)][!]: <description>`;
- the type is not in the allowed set;
- the scope is present and is not lowercase kebab-case;
- the header is longer than 100 characters;
- the header carries `!` but the body has no `BREAKING CHANGE:` footer;
- the message has no `Signed-off-by:` trailer.

Everything else (imperative mood, lowercase preference, a subject that describes
the change, a body that explains why, atomicity) is checked by a human reviewer
using [the review checklist](review-checklist.md), because a parser cannot judge
meaning.

**Bypass.** The local hook can be skipped for exceptional cases:

```bash
git commit --no-verify -s -m "chore(git): import the generated fixture tree"
```

Reserve the bypass for cases the hook cannot judge well: a rename that git sees
as a full rewrite, a generated file, or a merge or fixup commit. It is not a way
to ship a message that breaks the spec just because the message is hard. Every
bypassed commit is visible in the diff, and review applies the same rules to it,
so the bypass buys latency, never exemption.

**Review enforces the same rules.** A reviewer rejects a commit that fails the
Checklist's [Commits](review-checklist.md#commits) section even when the hook let
it through, and a PR whose history cannot be parsed is asked to squash or amend
before merge.

## Related documents

- [`CONTRIBUTING.md`](../../CONTRIBUTING.md) for the contribution mechanics that
  wrap these commits, including the DCO sign-off and branch naming.
- [`docs/process/review-checklist.md`](review-checklist.md) for the reviewer's
  view, including the `Commits` section that backstops this spec.
- [`docs/process/definition-of-done.md`](definition-of-done.md) for the gates a
  change must pass, including the commit requirements.
- [`docs/process/collaboration.md`](collaboration.md) for where commits land in
  the proposal to merge flow.

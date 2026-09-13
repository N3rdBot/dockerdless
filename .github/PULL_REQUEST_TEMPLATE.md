<!--
Thanks for contributing to dockerdless.
Fill every section. Delete a section only when it genuinely does not apply,
and say why.
-->

## Summary

<!-- What does this change do, and why? One or two short paragraphs. -->

## Linked proposal / design / ADR

<!--
Link the artifact that authorizes this change, if any.
- Feature or compatibility-surface change: docs/proposals/<n>-<slug>.md
- Design-level change: docs/design/<n>-<slug>.md
- Irreversible decision: docs/adr/<n>-<slug>.md
Write "none, small change" only for a contained fix, test, or docs edit.
-->

- Proposal:
- Design:
- ADR:

## Type of change

- [ ] Bug fix (non-breaking)
- [ ] New feature (non-breaking)
- [ ] Breaking change or compatibility-surface change
- [ ] Refactor with no behavior change
- [ ] Documentation only
- [ ] Build, CI, or tooling change

## Docker compatibility impact

- [ ] Adds a new API endpoint
- [ ] Changes the field policy for container create (handled / rejected /
      ignored) in `internal/api/container_fields.go`
- [ ] Updates `docs/compatibility.md`
- [ ] No compatibility impact

<!--
If any box above is checked, summarize the mapping and quote the Docker-shaped
error returned for unsupported input. Unsupported behavior must not fail silently.
-->

## How it was tested

<!--
Paste the exact commands you ran and where the evidence lives.
Examples:
  go test -race -count=1 ./...
  go test -tags=integration -count=1 -v ./integration/...
  make bench
Evidence: the exact command(s) run and their raw output
-->

- Commands:
- Evidence:
- Live backend used (containerd / BuildKit / CNI versions), if any:

## Checklist

- [ ] `make verify` is green (gofmt, `go vet`, `go test -race -count=1 ./...`)
- [ ] `make integration` is green, or a skip reason is documented
- [ ] Every commit carries a `Signed-off-by` trailer (`git commit -s`)
- [ ] Tests cover the new behavior and the failure paths
- [ ] Docs updated where behavior changed
- [ ] No leaked containers, network namespaces, bridges, or temp directories
      after the change runs or fails
- [ ] Unsupported behavior returns Docker-shaped errors and is documented

# ADR-0002: Defer the CRI runtime adapter

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: dockerdless maintainers
- **Related**: [docs/compatibility.md](../compatibility.md), ADR-0001

## Context and problem statement

Containerd ships a CRI plugin, and Kubernetes talks to containerd through it.
It is reasonable to ask why a containerd-backed daemon does not also expose
CRI, or reuse the CRI plugin as its runtime instead of the native containerd
API.

CRI is built for Kubernetes, not for standalone Docker containers. It requires
a pod sandbox: every workload is created inside a sandbox, and the runtime
manages the sandbox, its network namespace, and its lifecycle. It has no image
build or push, no Docker network identity, and no log retrieval endpoint.
Its port-mapping semantics differ as well: `host_port=0` does not mean
"allocate a dynamic host port" the way Docker clients expect.

Serving CRI would therefore mean building a second, differently-shaped API on
top of the same substrate, with its own identity model and its own gaps. That
is real work that does not advance the MVP goal of a deterministic
single-host Docker-compatible test flow.

## Decision

**CRI is deferred. The `enable-cri` flag is inert and reserved.**

- `Config.EnableCRI` defaults to `false` (`internal/config/config.go`).
- Changing `enable-cri` is classified as a startup-only setting and emits a
  `requires restart` warning; it never makes the daemon serve CRI
  (`cmd/dockerdless/reload.go`).
- No CRI gRPC service and no CRI socket are exposed. The compatibility
  document lists CRI as deliberately unsupported.

If CRI is added later, it should be a **separate driving adapter** that
depends on the existing application use cases and runtime port, so the Docker
HTTP API and a CRI gRPC service can coexist over one substrate. It should not
change the ports the containerd adapter already implements.

## Consequences

**Positive**

- The MVP stays focused on the Docker API surface that testcontainers-go
  exercises.
- The `enable-cri` flag documents forward intent without implying support.
- A future CRI adapter has a clean seam: the runtime, image, and network ports
  already exist.

**Negative**

- Kubernetes users cannot point CRI clients at this daemon.
- The reserved flag could be misread as a working feature; the README and
  compatibility document must keep calling it inert.

**Neutral**

- CRI-format **log files** are still produced for container output. That is a
  log-line encoding, not the CRI runtime, and is unrelated to this deferral.

## Alternatives considered

- **Implement CRI now**: large surface with no MVP consumer, and it would
  pull pod-sandbox semantics into the runtime adapter.
- **Remove the flag entirely**: loses the documented forward-compatibility
  signal and changes the config surface for no functional gain.

## References

- `internal/config/config.go`: `EnableCRI`, `DefaultEnableCRI`
- `cmd/dockerdless/reload.go`: `enable-cri` as a startup-only setting
- `internal/ports/ports.go`: the runtime port a future CRI adapter would use
- `docs/compatibility.md`: CRI listed under unsupported features
- Commit `feat(api): define Docker compatibility contracts`
- Commit `feat(config): add validated immutable hot-reload snapshots`

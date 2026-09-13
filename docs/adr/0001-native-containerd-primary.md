# ADR-0001: Native containerd as the primary substrate

- **Status**: Accepted
- **Date**: 2026-09-13
- **Deciders**: dockerdless maintainers
- **Related**: [docs/architecture.md](../architecture.md)

## Context and problem statement

dockerdless must serve the Docker HTTP API on a single Linux host. Docker
clients, and testcontainers-go in particular, expect a daemon over a Unix
socket that can create, start, and stop containers, build and pull images,
wire networks and published ports, and return logs, all with Docker-shaped
payloads. The daemon needs a real container runtime underneath.

Two shortcuts were tempting. First, shell out to `nerdctl`, the containerd
CLI. Second, treat the Kubernetes CRI runtime (containerd's CRI plugin) as the
substrate. Neither survives contact with the requirements.

`nerdctl` is a command-line tool. It has no long-lived HTTP or gRPC daemon
API, so state, streaming I/O, and error translation would have to be scraped
from process output and text. The CRI surface models Kubernetes pods and
sandboxes, not standalone Docker containers; it exposes no image build or
push, no network identity for Docker networks, no log retrieval, and its
`host_port=0` semantics do not match Docker's dynamic port allocation. Using
either as the primary substrate would force a translation layer built on
something that was never meant to be an API.

## Decision

Implement the daemon directly on the **native containerd gRPC API**, with
**CNI** for networking and **BuildKit** for image builds, rather than wrapping
`nerdctl` or treating CRI as the substrate.

Each backend is a driven adapter behind an inward-facing port:

- `internal/adapters/containerd/` implements runtime lifecycle, exec, logs,
  and status against the containerd client.
- `internal/adapters/cni/` implements network create/connect/remove and the
  host-port allocator against CNI plugins.
- `internal/adapters/buildkit/` implements pull, inspect, list, build, and
  remove against BuildKit and the containerd image store.
- `cmd/dockerdless/main.go` is the composition root that constructs every
  adapter and aligns the containerd namespace with the shared BuildKit worker.

## Consequences

**Positive**

- One process talks to structured APIs, so errors, streaming, and state are
  typed rather than parsed from CLI text.
- Containers are first-class Docker resources, not pod sandboxes, so identity
  and lifecycle map cleanly.
- Each backend is swappable behind a port; the application layer never imports
  an adapter.
- CNI and BuildKit are the same primitives a full Docker daemon uses, so
  behavior is closer to Docker than a reimplementation would be.

**Negative**

- The daemon owns adapter code for three backends, which is more surface than
  calling a single CLI.
- It depends on containerd, BuildKit, and CNI plugins being present and
  correctly configured on the host.
- Namespace alignment between containerd and the BuildKit worker must be
  handled explicitly, or built images are invisible to the runtime store.

**Neutral**

- The adapters are exercised by both in-memory fakes and a live integration
  harness, so the substrate choice is testable without the backends.

## Alternatives considered

- **`nerdctl` CLI wrapper**: no daemon API; state and streaming would be
  reconstructed from process output, and concurrent requests would race.
- **CRI-first**: pod-sandbox model, no build or push, no log retrieval, and
  incompatible host-port semantics; see ADR-0002.

## References

- `internal/adapters/containerd/`: runtime lifecycle adapter
- `internal/adapters/cni/`: networking and port allocation adapter
- `internal/adapters/buildkit/`: image and build adapter
- `cmd/dockerdless/main.go`: adapter construction and namespace alignment
- `internal/ports/ports.go`: the contracts each adapter satisfies
- Commit `build(scaffold): establish daemon module and package boundaries`
- Commit `feat(runtime): implement containerd task lifecycle adapter`
- Commit `feat(images): add containerd image and BuildKit adapters`
- Commit `feat(io): add CNI networking ports logs and Docker streams`

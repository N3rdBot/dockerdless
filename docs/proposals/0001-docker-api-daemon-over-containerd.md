# Feature Proposal: A Docker Engine API daemon over containerd

## Metadata

| Field | Value |
| --- | --- |
| **Title** | Docker Engine API daemon over containerd |
| **Status** | Implemented <!-- Draft, In Review, Accepted, Rejected, Implemented, Superseded --> |
| **Authors** | `@N3rdBot` |
| **Reviewers** | `@N3rdBot` |
| **Created** | 2026-09-13 |
| **Updated** | 2026-09-13 |
| **Supersedes** | none |
| **Superseded by** | n/a |
| **Related proposals** | none |
| **Related designs** | `docs/design/0001-mvp-daemon.md` |
| **Related ADRs** | `docs/adr/0001-native-containerd-primary.md`, `docs/adr/0002-defer-cri-runtime-adapter.md`, `docs/adr/0004-docker-identity-and-state-mapping.md`, `docs/adr/0005-disable-ryuk-in-the-mvp.md`, `docs/adr/0006-no-moby-daemon-fork.md`, `docs/adr/0007-explicit-compatibility-surface.md` |
| **Related issues** | none |

## Table of contents

- [Summary](#summary)
- [Motivation](#motivation)
- [Goals and non-goals](#goals-and-non-goals)
- [Background and prior art](#background-and-prior-art)
- [Proposed design (high level)](#proposed-design-high-level)
- [Detailed design](#detailed-design)
- [Docker API and contract impact](#docker-api-and-contract-impact)
- [Native mapping](#native-mapping)
- [Alternatives considered](#alternatives-considered)
- [Compatibility and migration](#compatibility-and-migration)
- [Security considerations](#security-considerations)
- [Observability](#observability)
- [Testing strategy](#testing-strategy)
- [Rollout and milestones](#rollout-and-milestones)
- [Open questions](#open-questions)
- [Decision log](#decision-log)

## Summary

dockerdless serves the Docker Engine API over a Unix socket and backs it with
native containerd, BuildKit, and CNI instead of the Moby daemon. It exists so
that testcontainers-go, which speaks only the Docker API and supports neither
nerdctl nor CRI, can drive non-Docker container environments on a single rootful
Linux host. This proposal records that direction as implemented and states the
compatibility surface it deliberately serves and the surface it deliberately
does not.

## Motivation

The concrete consumer is [testcontainers-go](https://golang.testcontainers.org/).
It creates, starts, inspects, execs into, and removes containers exclusively
through the Docker Engine API reached over `DOCKER_HOST`; its Moby client does
not talk to anything else. The concrete pain is that the native alternatives do
not speak that protocol: nerdctl is a command-line tool with no long-lived HTTP
or gRPC daemon API, and CRI is Kubernetes' runtime interface, built around pod
sandboxes, with no Docker network identity, no image build, and no log
retrieval.

The people who feel this are Go test authors and integration engineers on hosts
that already run containerd, BuildKit, and CNI but not a Docker daemon. Today
their options are to install and run `dockerd`, to give up testcontainers, or to
hand-roll a client against three native APIs. This proposal gives them a fourth:
point `DOCKER_HOST` at dockerdless and keep the test code unchanged.

## Goals and non-goals

**Goals**

- Serve the Docker API subset that testcontainers-go actually exercises: image
  pull, inspect, list, build, and remove; container create, start, stop, remove,
  inspect, and list; logs; exec; networks; and host-port publishing.
- Run containers through containerd, networks and host ports through CNI, and
  image builds through BuildKit, with each backend behind an inward-facing port.
- Be honest about the compatibility surface: every served route is verified
  live, and every unsupported route answers with the documented Docker-shaped
  error instead of a false success.

**Non-goals**

- Full Docker parity. Resource limits, restart policies, privileged containers,
  custom runtimes, and the rest of the Moby daemon behavior are out of scope.
- A CRI implementation. CRI is deferred and the `enable-cri` flag stays inert
  (ADR-0002).
- Rootless mode. The daemon needs root to create network namespaces, bridges,
  and iptables rules.
- Ryuk, the testcontainers reaper. It stays disabled (ADR-0005).
- Swarm, Compose, registry push, and registry login.
- Windows containers. The daemon is Linux-only.

## Background and prior art

- [Docker Engine API](https://docs.docker.com/engine/api/v1.44/) is the wire
  contract. The daemon advertises `1.44` and accepts clients from `1.24` up.
- [containerd](https://containerd.io/) is the container runtime. The daemon
  talks to its gRPC API directly rather than through a CLI.
- [BuildKit](https://github.com/moby/buildkit) builds images. The daemon uses
  the `dockerfile.v0` frontend and the shared containerd image store.
- [CNI](https://www.cni.dev/docs/spec/) provides container networking and host
  port publishing through the `bridge`, `host-local`, `portmap`, `firewall`,
  and `loopback` plugins.
- [nerdctl](https://github.com/containerd/nerdctl) is the closest prior art for
  containerd-backed Docker-like behavior. It is a useful reference for error
  wording and plugin usage, not a code dependency, because it has no daemon API.
- [CRI](https://kubernetes.io/docs/concepts/architecture/cri/) is Kubernetes'
  runtime interface. It is explicitly out of scope; the differences that made it
  the wrong substrate are recorded in ADR-0002.
- ADR-0001 records the choice of native containerd over a nerdctl wrapper or a
  CRI-first design. ADR-0006 records the choice to reuse only Moby's wire types
  rather than fork the daemon.

## Proposed design (high level)

A Docker HTTP-over-Unix-socket layer decodes requests into Moby wire types and
translates them into daemon-neutral application calls. The application layer
orchestrates use cases across inward-facing ports and is the single point where
adapter errors become the canonical `ports.Err*` sentinels that the HTTP layer
maps to Docker status codes. The ports are implemented by three driven adapters:
`internal/adapters/containerd/` for runtime lifecycle, exec, logs, and status;
`internal/adapters/buildkit/` for pull, inspect, list, build, and remove; and
`internal/adapters/cni/` for network create, connect, and remove plus host-port
allocation.

```text
Docker client (testcontainers-go, docker CLI)
    │ HTTP over Unix socket (0660)
    ▼
internal/api       decode Moby types, route, map errors
    ▼
internal/app       use cases, adapter -> ports translation
    ▼
internal/ports     runtime, image, network, controller contracts
    ├── internal/adapters/containerd   containers, tasks, exec, logs
    ├── internal/adapters/buildkit     pull, inspect, build, remove
    └── internal/adapters/cni          networks, host-port publishing
```

The composition root in `cmd/dockerdless/` constructs every adapter, aligns the
containerd namespace with the BuildKit worker, reconciles state from containerd
at startup, and owns shutdown ordering.

## Detailed design

Detailed design: `docs/design/0001-mvp-daemon.md`

Reason to split: the proposal covers a whole daemon rather than one endpoint.
It adds three adapters, several `internal/ports` contracts, an application
layer, a domain state model, and the Docker HTTP boundary, so the interface
contracts and error mapping need a design document reviewers can check before
and against the code.

## Docker API and contract impact

Only a documented subset of the Docker API is served, and the boundary is
explicit rather than implied. Every served route is listed in the capability
mapping, and every unsupported route is listed with the exact error a client
receives; the full tables live in
[`docs/compatibility.md`](../compatibility.md#capability-mapping) and are not
duplicated here.

The create contract gets the strongest treatment. Every field of
`container.Config` and `container.HostConfig`, including the fields promoted
from the embedded `container.Resources`, is classified as **handled**,
**rejected**, or **documented-ignored** in `internal/api/container_fields.go`.
Rejected fields fail before provisioning with a Docker-shaped `501`, malformed
values fail with a `400 NewInvalidParameter`, and the classification is
enforced by `TestContainerCreateFieldPolicyIsExhaustive`, which reflects over
the Moby structs and fails when a field is unclassified or a policy entry is
stale. That test is what stops a future `github.com/moby/moby/api` bump from
silently dropping a new create field. The policy and its rationale are
[ADR-0007](../adr/0007-explicit-compatibility-surface.md); the field tables are
[`docs/compatibility.md#container-create-field-policy`](../compatibility.md#container-create-field-policy).

Route groups served, each verified against a real backend and detailed in
`docs/compatibility.md`:

| Group | Routes |
| --- | --- |
| Daemon | `/_ping`, `/version`, `/info` |
| Images | `POST /images/create`, `GET /images/{name}/json`, `GET /images/json`, `POST /build`, `DELETE /images/{name}` |
| Containers | `POST /containers/create`, `/start`, `/stop`, `DELETE /containers/{id}`, `GET /containers/{id}/json`, `GET /containers/json`, `/logs` |
| Exec | `POST /containers/{id}/exec`, `POST /exec/{id}/start`, `GET /exec/{id}/json` |
| Networks | `GET /networks`, `GET /networks/{id}`, `POST /networks/create`, `POST /networks/{id}/connect`, `DELETE /networks/{id}` |

Everything else returns a Docker-shaped error envelope, never a Go error page
and never an empty success. Events, volumes, Swarm, Compose, registry push and
login, CRI, and rootless mode are listed with their exact errors in
[`docs/compatibility.md#unsupported-features`](../compatibility.md#unsupported-features).

## Native mapping

| Docker operation | containerd | BuildKit | CNI |
| --- | --- | --- | --- |
| Container lifecycle, exec, logs, status | container + task gRPC, CRI log files | none | network namespace lookup |
| Image pull, inspect, list, build, remove | shared image store (pull, config digest) | Dockerfile solve, image export, auth session | none |
| Network create, connect, remove | none | none | conflist write, `bridge` ADD/DEL, netlink bridge delete |
| Host-port publishing | none | none | `portmap` DNAT, concrete port allocation at create |

Docker identity and state cross both containerd metadata and task status, which
are separate services. The daemon keeps an explicit registry and reconciles it
from containerd at startup rather than deriving `running` from metadata alone
(ADR-0004). Image digest and container command are pinned in container labels so
inspect survives a restart.

## Alternatives considered

| Alternative | Why rejected |
| --- | --- |
| Wrap the `nerdctl` CLI | nerdctl has no long-lived HTTP or gRPC daemon API, so state, streaming I/O, and error bodies would have to be scraped from process output and text. Concurrent requests would race, and CLI output is not a contract. Recorded in [ADR-0001](../adr/0001-native-containerd-primary.md). |
| Use CRI as the substrate | CRI is a Kubernetes runtime interface. It requires a pod sandbox for every workload, and it has no image build or push, no Docker network identity, no log retrieval, and `host_port=0` semantics that differ from Docker's dynamic allocation. Recorded in [ADR-0002](../adr/0002-defer-cri-runtime-adapter.md). |
| Fork the Moby daemon | `dockerd` is not designed to be embedded. Forking it drags in libnetwork, the plugin manager, Swarm, and much of the storage stack, which is a multi-year maintenance commitment and destroys substrate replaceability. Recorded in [ADR-0006](../adr/0006-no-moby-daemon-fork.md). |
| Implement it on NATS or a message bus | No Docker client speaks it, so every consumer would need a shim. Adds a broker dependency for no compatibility gain. |
| Serve a false-success stub for unsupported routes | Violates the project rule against routes that report success they did not deliver (ADR-0007) and corrupts client state. |

## Compatibility and migration

This is a new daemon, so migration is client-side and additive: point
`DOCKER_HOST` at the dockerdless socket and pin `DOCKER_API_VERSION=1.44` for
clients that do not negotiate the API version (testcontainers-go among them).
There is no on-disk state to migrate, no Docker data directory, and no config
format inherited from `dockerd`. Existing Docker daemons are untouched. The
compatibility contract is versioned by the advertised API version and by the
pinned `github.com/moby/moby/api` module; an upgrade is a deliberate dependency
bump guarded by the exhaustive field-policy test.

Wire-shape divergences that clients can observe are documented rather than
hidden: `/version` and `/info` report the intentional `0.0.0-dev` version string
and `/info` reports `LoggingDriver: "cri"` because the daemon writes CRI-format
text log files. Multi-network containers, `POST /networks/{id}/disconnect`, and
loopback port publishing are known limitations with documented behavior and
workarounds in
[`docs/compatibility.md`](../compatibility.md#unsupported-features).

## Security considerations

dockerdless is a rootful control plane and it says so: it is not a security
sandbox between its clients and the host. The API socket is the only network
entry point. It is created `0660`, owned by the daemon uid, re-verified after
binding, and never exposed over TCP. Any client that can connect can perform
every supported operation, which matches Docker's own socket trust model.

Containers started through the API run with the privileges their spec requests
under the daemon's rootful containerd, so the ability to start a container is
equivalent to root on the host. Registry credentials arrive per request in the
`X-Registry-Auth` header, are used for a single operation, are never stored, and
are redacted from logs and errors. Published ports are host-exposed services and
are not firewall-scoped beyond what CNI installs. The full trust boundary,
socket lifecycle rules, credential handling, and hardening checklist live in
[`docs/security.md`](../security.md). This proposal adds no new socket, no new
credential path, and no new host-privileged operation beyond the documented
container-control surface.

## Observability

- **Logs:** structured JSON through zap, level-controlled by
  `DOCKERDLESS_LOG_LEVEL`. One access line per HTTP request carries request id,
  method, path, status, response bytes, duration, and trace/span ids; it never
  records request headers or bodies, so credentials cannot leak into logs.
- **Traces:** OpenTelemetry spans cover the HTTP boundary through `otelhttp`
  and the application use cases; W3C trace context arrives from the client or is
  generated per request. OTLP export is opt-in through
  `DOCKERDLESS_OTEL_ENDPOINT` and is fully offline when unset.
- **Metrics:** OTLP metrics are exported through the same opt-in endpoint.
- **Correlation:** log records and spans share the request context, so an access
  log line points at its trace.

## Testing strategy

- **Unit, TDD first:** the application layer runs against in-memory fakes with
  no daemon socket, and the adapters run against narrow seams
  (`internal/adapters/containerd`, `internal/adapters/buildkit`,
  `internal/adapters/cni`) that fake the backend. Tests are table-driven, run
  under `go test -race -count=1 ./...`, and lock error translation to the
  canonical `ports.Err*` sentinels.
- **Integration:** `//go:build integration` tests in `integration/` run against
  real containerd, real BuildKit, and real CNI plugins on an ephemeral socket,
  driven by `go test -tags=integration -count=1 -v ./integration/...`. The suite
  skips with a recorded reason when root, a backend socket, or the plugins are
  missing, and it never silently passes. The image-pull case skips with an
  explicit reason when containerd has no registry egress.
- **testcontainers-go matrix:** a real `testcontainers-go v0.44.0` suite runs
  end to end against the daemon, exercising create, start, wait-for-log, logs,
  exec and `wait.ForExec`, mapped ports, networks, builds, and terminate, with
  `DOCKER_API_VERSION=1.44` and `TESTCONTAINERS_RYUK_DISABLED=true`.
- **Ryuk:** disabled and asserted absent. A runtime compatibility test fails if
  a reaper container appears while the matrix runs (ADR-0005).
- **Evidence:** each compatibility claim is backed by a command and its raw
  output recorded in the pull request; the compatibility table marks a row
  `verified live` only when that evidence exists.

## Rollout and milestones

This proposal is retrospective: the milestones below are the historical path to
`Implemented`, kept at a high level so the shape of the delivery is visible
without recording execution mechanics.

| Milestone | Deliverable | Verification |
| --- | --- | --- |
| M1 | Ports and the three driven adapters | race-clean unit tests with fakes |
| M2 | Application use cases and domain state model | unit tests green, race-clean |
| M3 | Docker HTTP boundary and error mapping | handler tests green |
| M4 | Live integration against real containerd, BuildKit, and CNI | integration suite recorded |
| M5 | testcontainers-go matrix and compatibility documentation | matrix green, `docs/compatibility.md` rows `verified live` |

## Open questions

1. `@N3bot`: Should the loopback port-publishing gap be closed with a userland
   proxy or a route_localnet policy in the CNI adapter, or is the host-address
   contract documented in `docs/compatibility.md` sufficient? The MVP documents
   the host-address requirement and leaves loopback unsatisfied.
2. `@N3bot`: The registry is in-memory and rebuilt from containerd at startup.
   Should container and network state later be persisted, or does startup
   reconciliation remain the only source of truth (ADR-0004)?
3. `@N3bot`: `POST /networks/{id}/connect` requires a running container and only
   one network per container is supported. Is a proper multi-network strategy
   (interface naming, secondary routes) in scope for the next milestone, or does
   it stay documented as unsupported?

## Decision log

| Date | Decision | Rationale | Author |
| --- | --- | --- | --- |
| 2026-09-13 | Serve the Docker API over native containerd, not a nerdctl wrapper or CRI | nerdctl has no daemon API; CRI is built for Kubernetes pods and lacks build, logs, and Docker networks. See ADR-0001 and ADR-0002. | `@N3rdBot` |
| 2026-09-13 | Reuse Moby wire types without forking the daemon | Preserves the wire contract while keeping the substrate replaceable. See ADR-0006. | `@N3rdBot` |
| 2026-09-13 | Classify every create field and route, enforced by a reflective test | Prevents false success on unsupported fields and rot on a Moby bump. See ADR-0007. | `@N3rdBot` |
| 2026-09-13 | Disable Ryuk in the MVP | Offline CI cannot pull the reaper, and a parallel reaper muddies failure attribution. See ADR-0005. | `@N3rdBot` |
| 2026-09-13 | Mark this proposal Implemented at merge | The compatibility table reflects the served surface and the evidence artifact exists. | `@N3rdBot` |

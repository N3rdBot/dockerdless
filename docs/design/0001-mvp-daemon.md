# Detailed Design: MVP daemon over containerd, BuildKit, and CNI

## Metadata

| Field | Value |
| --- | --- |
| **Title** | Docker-compatible MVP daemon over containerd, BuildKit, and CNI |
| **Status** | Implemented |
| **Proposal** | [`docs/proposals/0001-docker-api-daemon-over-containerd.md`](../proposals/0001-docker-api-daemon-over-containerd.md) |
| **Design owner** | `@N3rdBot` |
| **Implementer(s)** | `@N3rdBot` |
| **Reviewers** | `@N3rdBot` |
| **Created** | 2026-09-13 |
| **Updated** | 2026-09-13 |
| **Related ADRs** | [`0001`](../adr/0001-native-containerd-primary.md), [`0003`](../adr/0003-immutable-config-snapshots.md), [`0004`](../adr/0004-docker-identity-and-state-mapping.md), [`0005`](../adr/0005-disable-ryuk-in-the-mvp.md), [`0006`](../adr/0006-no-moby-daemon-fork.md), [`0007`](../adr/0007-explicit-compatibility-surface.md) |
| **Evidence** | recorded in the pull request: exact command + raw output |

This document records the MVP retrospectively. The code shipped first, so every
interface, state, and error claim below was read back from the tree at
`github.com/N3rdBot/dockerdless` on Go 1.27.1. Where the design and the code
disagree, the code wins and this page is wrong.

## Scope and invariants

**In scope**

- A Docker Engine API HTTP daemon speaking over a Unix socket (mode `0660`),
  rooted in a Linux-rootful host.
- Container lifecycle: create, start, stop, remove, inspect, list, logs, exec.
- Image lifecycle: pull, inspect, list, build, remove.
- Network lifecycle: create, list, inspect, connect, remove, plus host-port
  allocation and CNI `portmap`.
- Startup reconciliation of the in-memory state registry against live containerd
  metadata and tasks.
- Structured logging and OpenTelemetry wiring constructed at startup.

**Out of scope**

- CRI, rootless mode, Swarm, Compose, volumes, configs, secrets, and the events
  stream. See [`docs/compatibility.md`](../compatibility.md#unsupported-features)
  for the exact response each one returns.
- Loopback DNAT through a userland proxy. The daemon ships no proxy.
- Multi-network containers, network disconnect, container pause, kill, restart,
  wait, attach, export, archive, resize routes, and registry push/login.

**Invariants**

- The in-memory registry is the single source of truth the HTTP layer renders.
  Containerd is the authority for runtime state; the registry mirrors it and is
  rebuilt from containerd at every startup.
- No adapter imports another adapter. `containerd`, `buildkit`, and `cni` stay
  independent, matching [`docs/architecture.md`](../architecture.md#layers).
- `internal/domain` never imports an infrastructure package.
- Every backend error crosses the application boundary as one of the canonical
  `ports.Err*` sentinels (`internal/ports/ports.go`), never as a containerd,
  BuildKit, or libcni type.
- Host-port allocation happens before CNI `portmap` runs, and a binding with
  `HostPort == 0` is never forwarded to the plugin. See
  `BuildPortMappings` in `internal/adapters/cni/portmap.go`.
- A host-port reservation is released exactly once, even when CNI teardown
  fails. See `internal/adapters/cni/adapter.go`.
- Configuration is published as an immutable snapshot; only `log-level` mutates
  a running daemon. See [`docs/configuration.md`](../configuration.md).

## Component and package layout

The daemon follows a hexagonal layout. The Docker HTTP adapter is the driving
side; containerd, BuildKit, and CNI are driven adapters behind inward ports.

| Package | Responsibility |
| --- | --- |
| `internal/domain` | Docker resource identities, the container state vocabulary, the `Container` aggregate, domain events, the in-memory `Registry`, and the metadata/task reconciliation (`reconcile.go`, `registry.go`, `model.go`, `events.go`). No infrastructure imports. |
| `internal/ports` | Inward-facing contracts and the canonical `Err*` sentinels: `Runtime`, `Image`, `Network`, `IO`, `State`, the `*Controller` extensions, `PortAllocator`, `TaskLocator`, `Logs`, and `ImageConfigReader`. |
| `internal/adapters/containerd` | Runtime adapter over the containerd client. Owns OCI spec translation (`spec.go`), task lifecycle, exec, and errdefs mapping (`errors.go`). |
| `internal/adapters/buildkit` | Image adapter. Implements pull, inspect, list, and remove over the containerd image store, and Dockerfile builds through the BuildKit solver with a Docker JSON progress stream. |
| `internal/adapters/cni` | Network adapter over libcni plus netlink. Owns bridge conflist creation, ADD/DEL against the container netns, the host-port allocator (`portmap.go`), and inspect fallbacks. |
| `internal/api` | Driving adapter. Owns route registration (`routes.go`), the Unix socket lifecycle (`server.go`), JSON request decoding, the Docker error envelope (`errors.go`), and handler-to-service translation. Never imports an adapter package. |
| `internal/app` | Application layer. Composes the ports into use cases (`containers.go`, `images.go`, `networks.go`, `logs.go`, `exec.go`), normalizes adapter results (`adapters.go`), and maps adapter sentinels onto canonical ports errors (`errors.go`). |
| `internal/streams` | Docker stream framing (`stdcopy.go`), HTTP hijack helpers (`hijack.go`), and CRI log parsing with follow/tail/since/until (`logs.go`). |
| `internal/config` | Typed settings, validation, the immutable snapshot store (`store.go`), and the directory watcher (`watch.go`). |
| `internal/observability` | zap logger bootstrap with an atomic level, OpenTelemetry providers and OTLP exporters, and the HTTP correlation middleware. |
| `cmd/dockerdless` | Composition root. Loads configuration, bootstraps observability, connects containerd and BuildKit, aligns the namespace, reconciles startup state, builds the service and router, and owns signal cancellation and shutdown (`main.go`, `reconciliation.go`, `reload.go`). |

```text
cmd/dockerdless                     (composition root)
        │ builds
        ▼
internal/api  (routes, socket, error envelope)
        │  calls use cases
        ▼
internal/app.Service  (registry + use cases)
        │  depends on ports only
        ▼
internal/ports  ◄── interfaces + Err* sentinels
        │
   ┌────┼──────────────┐
   ▼    ▼              ▼
containerd  buildkit   cni     (driven adapters, mutually independent)
```

## Interfaces

The inward ports are declared once in `internal/ports/ports.go`. The sketches
below are the real signatures.

**Lifecycle primitives:**

```go
// internal/ports/ports.go
type Runtime interface {
	Create(context.Context, domain.ContainerSpec) (domain.ContainerID, error)
	Start(context.Context, domain.ContainerID) error
	Stop(context.Context, domain.ContainerID, time.Duration) error
	Remove(context.Context, domain.ContainerID) error
}

type Image interface {
	Pull(context.Context, string) (domain.ImageID, error)
	Remove(context.Context, domain.ImageID) error
}

type Network interface {
	Create(context.Context, string) (domain.NetworkID, error)
	Remove(context.Context, domain.NetworkID) error
}

type IO interface {
	Attach(context.Context, domain.ContainerID) (io.ReadWriteCloser, error)
}

type State interface {
	Get(context.Context, domain.ContainerID) (domain.Container, error)
	Save(context.Context, domain.Container) error
	Remove(context.Context, domain.ContainerID) error
}
```

**Controller extensions the handlers need:**

```go
type RuntimeController interface {
	Runtime
	CreateContainer(context.Context, ContainerCreateSpec) (domain.ContainerID, error)
	Kill(context.Context, domain.ContainerID, syscall.Signal) error
	Wait(context.Context, domain.ContainerID) (ProcessExit, error)
	Status(context.Context, domain.ContainerID) (domain.ContainerState, error)
	Resize(context.Context, domain.ContainerID, uint32, uint32) error
	StartWithIO(context.Context, domain.ContainerID, io.Writer, io.Writer) error
	Exec(context.Context, domain.ContainerID, ExecRequest) (ExecResult, error)
	ExecRecord(string) (domain.ExecRecord, bool)
}

type ImageController interface {
	Image
	PullImage(context.Context, string, PullRequest) (domain.ImageID, error)
	Inspect(context.Context, string) (ImageDetail, error)
	List(context.Context) ([]ImageDetail, error)
	Build(context.Context, BuildRequest, io.Writer) error
}

type NetworkController interface {
	Network
	EnsureDefaultNetwork(context.Context) (domain.NetworkID, error)
	Resolve(context.Context, string) (NetworkDetail, error)
	List(context.Context) ([]NetworkDetail, error)
	CreateNetwork(context.Context, NetworkCreateRequest) (domain.NetworkID, error)
	Connect(context.Context, NetworkConnectRequest) (NetworkAttachmentResult, error)
	DisconnectAll(context.Context, domain.ContainerID) []error
}
```

**Supporting ports:**

```go
type PortAllocator interface {
	// AllocateBindings rolls back every reservation when one binding fails.
	AllocateBindings([]domain.PortBinding) ([]domain.PortBinding, []PortReservation, error)
	Release(...PortReservation)
}

type TaskLocator interface {
	// TaskPID returns the host PID used to build /proc/<pid>/ns/net.
	TaskPID(context.Context, domain.ContainerID) (int, error)
}

type Logs interface {
	Logs(context.Context, domain.ContainerID, LogRequest, io.Writer, io.Writer) error
}

type ImageConfigReader interface {
	ImageConfig(context.Context, string) (ImageConfig, error)
}
```

**How `internal/app.Service` composes them.** `app.New` takes a `Config`
(`internal/app/service.go`) holding `Runtime ports.RuntimeController`,
`Images ports.ImageController`, `Networks ports.NetworkController`,
`Registry *domain.Registry`, `Tasks ports.TaskLocator`,
`Allocator ports.PortAllocator`, plus timeouts, log directory, namespace,
snapshotter, socket paths, and a logger. `New` rejects a nil runtime, image,
network, or registry dependency.

`cmd/dockerdless/main.go` wraps each concrete adapter onto its controller
contract through `app.NewRuntimeAdapter`, `app.NewImageAdapter`, and
`app.NewNetworkAdapter` (`internal/app/adapters.go`). The wrappers exist to
convert adapter-local result types (`containerd.WaitResult`,
`buildkit.ImageDetail`, `cni.Network`) into the port shapes, keeping adapter
types from leaking inward.

Two seams are deliberately unused in this MVP. The `IO` port has no adapter
implementation because no attach route is wired, and `RuntimeController.Resize`
and `.Kill` are implemented by the containerd adapter but have no route. The
`State` port is declared, and the concrete `*domain.Registry` satisfies its
shape, but the app layer stores the concrete registry directly rather than the
interface.

## Data models and state machine

**Docker container states.** `internal/domain/model.go` defines the vocabulary
as `ContainerState` string constants: `unknown`, `created`, `running`, `paused`,
`restarting`, `removing`, `exited`, `dead`, with `ContainerStateStopped` kept as
an alias of `exited`. `IsDockerState` accepts exactly the seven inspect states
and rejects `unknown`.

The `Container` aggregate is the unit the HTTP layer renders. It carries
`ID`, `Name`, `Spec`, `ImageReference`, `ImageDigest`, `Labels`, `State`,
timestamps, `ExitCode`, `OOMKilled`, `Dead`, `Error`, optional `Health`,
`Execs`, `PortBindings`, and `Networks`.

**Metadata versus execution.** The runtime splits into two containerd views:

- **Container metadata** (`containerd.Containers`): the ID, image reference,
  OCI spec, and labels. Docker semantics that the OCI spec cannot express are
  pinned as labels, `io.dockerdless.name`, `io.dockerdless.image-digest`,
  `io.dockerdless.command`, `io.dockerdless.tty`, and `io.dockerdless.mounts`,
  plus the adapter-local `io.dockerdless.terminal` and `io.dockerdless.open-stdin`
  (`internal/adapters/containerd/adapter.go`).
- **Task execution** (`containerd.Tasks`): the running process. `created`,
  `running`, `paused`, `stopped`, and the exit status are read from the task,
  never inferred from metadata alone. `Adapter.Status` reports a container
  without a task as `exited` (`internal/adapters/containerd/adapter.go`).

**Startup reconciliation.** `cmd/dockerdless/main.go` lists metadata and task
status in the active namespace through `loadContainerdSnapshot` and passes both
to `domain.Reconcile` before the API server accepts requests. The rules are
literal in `internal/domain/reconcile.go`:

| Metadata | Task | Reconciled result |
| --- | --- | --- |
| present | absent | `State = exited`, `Dead = false`, added to `Stale` |
| present | `created` | `created` |
| present | `running` | `running` |
| present | `paused` | `paused` |
| present | `restarting` | `restarting` |
| present | `stopped` or `exited` | `exited`, with exit code and finish time copied |
| present | `dead` | `dead`, with `Dead = true` |
| absent | present | added to `Cleanup` |

Task exit data (`ExitCode`, `OOMKilled`, `Error`, `StartedAt`, `FinishedAt`) is
copied whenever a snapshot exists. Duplicate metadata, duplicate task snapshots,
an empty task identity, or an unsupported task status fail reconciliation. When
reconciliation fails, the daemon logs a warning and starts with an empty
registry; it does not refuse to serve.

Reachable transitions in the MVP are `created -> running` on start and
`running -> exited` on stop or task exit. `paused`, `restarting`, `removing`,
and `dead` are part of the vocabulary, surfaced when containerd reports them,
but the MVP exposes no route that drives a container into them.

## Request and data flow

The sequence below covers create, start, inspect, and logs. All paths enter
through `internal/api` and cross the use cases in `internal/app`.

```text
POST /containers/create
  api.containerCreate        decode container.CreateRequest, enforce the field
                             policy, build ports.ContainerCreateRequest
  app.ContainerCreate        requestContext (request-timeout)
    registry.GetByName       reject a duplicate name (409)
    images.Inspect           resolve the image config digest (404 on miss)
    cni.ParseNetworkMode     classify host / none / named
    networks.EnsureDefaultNetwork + Resolve
    allocateBindings         reserve concrete host ports BEFORE CNI
    runtime.CreateContainer  containerd metadata + OCI spec + labels
    registry.Save            persist the Docker identity
  handler                    201 with {Id, Warnings}

POST /containers/{id}/start
  app.ContainerStart
    runtime.Status           already running -> no-op 204
    openLogSink              CRI log file under <socket-dir>/dockerdless-logs/
    runtime.StartWithIO      start the task, attach stdout/stderr writers
    tasks.TaskPID            host PID -> /proc/<pid>/ns/net
    networks ConnectReserved adopt the existing reservations, then CNI ADD with
                             the portMappings capability
    registry.Save            state = running, StartedAt set
  handler                    204

GET /containers/{id}/json
  app.ContainerInspect       resolve id/name/prefix, refresh from runtime.Status,
                             Wait for the exit code when the task is gone,
                             normalize timestamps, save if changed
  handler                    container.InspectResponse

GET /containers/{id}/logs
  app.ContainerLogs          resolve, open the CRI log file, stream it
    cancelFollowWhenExited   poll runtime.Status every 50ms, cancel on exit
    streams.ReadLogs         tail/since/until/follow over CRI lines
  handler                    Content-Type multiplexed (or raw for TTY), framed
                             with streams.NewWriter per stream
```

**Host-port ordering.** `app.ContainerCreate` calls `allocateBindings` before
`runtime.CreateContainer`, and the CNI adapter only runs `portmap` at connect
time. `cni.ConnectReserved` calls `PortAllocator.AdoptBindings`, which verifies
the binding was already reserved and fails with `ErrPortNotReserved` otherwise.
`BuildPortMappings` refuses `HostPort == 0` with `ErrUnallocatedPort`, so a zero
sentinel can never reach the plugin. The concrete port is stable across the
create request, inspect `NetworkSettings.Ports`, and the CNI runtime config.

**Release ordering.** `ContainerCreate` releases its reservations on a runtime
or registry failure. `ContainerRemove` releases them only for a container still
in `created` state, because a started container's reservation is owned by the
CNI attachment and released by `Disconnect`. `Disconnect` always calls
`PortAllocator.Release`, even when the CNI DEL itself fails, so no allocation
leaks.

## Error taxonomy and Docker status mapping

Adapters translate their own failures onto adapter sentinels
(`internal/adapters/containerd/errors.go` maps containerd `errdefs`;
`internal/adapters/buildkit` and `internal/adapters/cni` define sibling sets).
`app.translateError` (`internal/app/errors.go`) folds those onto the canonical
`ports.Err*` sentinels, and `api.mapServiceError` (`internal/api/errors.go`)
maps the sentinels to HTTP status and the Docker envelope.

| Condition | Canonical error | HTTP status | Envelope `message` |
| --- | --- | --- | --- |
| Unknown container id, name, or prefix | `ports.ErrNotFound` | `404` | `No such container: <ref>` |
| Unknown image reference | `ports.ErrNotFound` | `404` | `No such image: <ref>` |
| Unknown network | `ports.ErrNotFound` | `404` | `network <name> not found` |
| Duplicate container name | `ports.ErrConflict` | `409` | Docker's name-in-use message |
| Remove a running container | `ports.ErrConflict` | `409` | `You cannot remove a running container ...` |
| Requested host port already allocated | `ports.ErrConflict` | `409` | the `PortInUseError` message |
| Network already exists or is in use | `ports.ErrConflict` | `409` | the CNI sentinel message |
| Port publishing on a `host`/`none` network | `ports.ErrInvalidArgument` | `400` | conflicting options message |
| Malformed id, unknown mode, missing netns, IPv6 request | `ports.ErrInvalidArgument` | `400` | the adapter detail |
| Zero host port forwarded to CNI | `ports.ErrInvalidArgument` | `400` | `port binding has no allocated host port` |
| Unsupported create field | `ports.ErrNotImplemented` | `501` | `<Type>.<Field> is not supported` |
| BuildKit without a solver | `ports.ErrNotImplemented` | `501` | the build adapter detail |
| No free dynamic host port | `ports.ErrServerError` | `500` | `no free host port found` |
| containerd, BuildKit, or libcni backend failure | `ports.ErrServerError` | `500` | redacted backend message |
| Request deadline exceeded | `ports.ErrServerError` | `500` | `request timed out` |
| Request canceled | `ports.ErrServerError` | `500` | `request canceled` |

The envelope is a `DockerError` whose `Status` and `Kind` are `json:"-"`, so the
wire body is exactly `{"message":"..."}` and nothing else
(`internal/api/errors.go`). `WriteDockerError` sets `Content-Type:
application/json` and writes the mapped status. `mapServiceError` strips the
`dockerdless: <kind>: ` sentinel prefix through `cleanServiceMessage`, and an
error may carry a Docker-facing message through the `DockerMessage()` interface
(`app.dockerError`). A `503` constructor, `NewUnavailable`, exists for exhausted
bounded resources, but no `ports` sentinel currently produces it.

## Concurrency and lifecycle

- **Request contexts.** Every use case wraps the request context with
  `requestContext`, which applies the configured `request-timeout`
  (`internal/app/service.go`). Port methods take that context down to the
  adapter, so a client disconnect cancels the containerd or CNI call. Adapters
  re-apply the containerd namespace onto the incoming context rather than
  swapping it (`namespaceContext`). Use cases use `context.WithoutCancel` only
  for deliberate cleanup, such as rolling back a created container after a
  registry failure.
- **Fixed shutdown.** `cmd/dockerdless/main.go` declares
  `shutdownTimeout = 5 * time.Second` and passes it to
  `api.WithShutdownTimeout`. On a signal the server stops accepting, drains
  in-flight requests within that bound, removes the socket, then
  `runtime.Shutdown` flushes telemetry under the same bound.
- **Race-safe configuration.** `config.Store` holds the active snapshot in an
  `atomic.Pointer[Config]` (`internal/config/store.go`). `Load` decodes and
  validates before a single atomic store, so a bad edit leaves the previous
  snapshot live. Readers call `Current`, which returns a value copy, and never
  touch Viper. `log-level` is applied through zap's `AtomicLevel`
  (`internal/observability/logger.go`, `cmd/dockerdless/reload.go`).
- **Follow termination.** `app.ContainerLogs` derives a cancelable context and,
  when `follow` is set, runs `cancelFollowWhenExited`, which polls
  `runtime.Status` every 50ms and cancels once the state leaves `running` and
  `created`. `streams.ReadLogs` observes cancellation, flushes what it has, and
  returns. The log sink is also closed on refresh when a container exits.
- **Exactly-once port release.** `app.Service` serializes allocator use behind
  `allocMu`. `PortAllocator.AllocateBindings` releases every reservation it made
  when a later binding fails. `Release` deletes map entries, so a repeat release
  is a no-op. `Adapter.Disconnect` releases tracked allocations unconditionally.
  Tests pin this, including `TestContainerRemove_doesNotReleaseCNIOwnedReservationTwice`
  and `TestContainerStart_retryAfterReservedCNIAddFailureKeepsReservation`.
- **Shared state.** `domain.Registry` guards its ID and name indexes with an
  `RWMutex` and returns deep copies through `cloneContainer`, so a handler can
  never mutate stored state. The containerd adapter serializes stdin pipes and
  exec records; the CNI adapter guards its attachment map; the app service
  guards allocator, exec, and log maps.

## Configuration

Configuration is a validated, immutable snapshot loaded from defaults, an
optional YAML file, and `DOCKERDLESS_*` environment variables. The canonical
reference, precedence rules, validation messages, and reload behavior live in
[`docs/configuration.md`](../configuration.md); this section does not restate
them.

The MVP defines thirteen keys: `socket-path`, `log-level`, `otel-service-name`,
`otel-endpoint`, `containerd-namespace`, `containerd-socket`, `cni-config-dir`,
`cni-plugin-dir`, `buildkit-socket`, `default-stop-timeout`, `request-timeout`,
`enable-cri`, and `enable-rootless`. `enable-cri` and `enable-rootless` are
reserved flags that do not change behavior today.

Only `log-level` is hot-reloadable. A valid reload applies it immediately
through the shared atomic level. Every other key is startup-only: a change is
recorded in the snapshot and the daemon logs
`configuration change requires restart` while continuing to use the old value.
An invalid reload is rejected and the previous snapshot stays active.

## Observability

- **Logging.** `observability.Bootstrap` builds the zap logger with an atomic
  level and, when OTLP is configured, tees records through the otelzap bridge
  (`internal/observability/logger.go`, `runtime.go`). Records are JSON. The
  `Sync` path tolerates `EINVAL` and `ENOTTY` so redirected output shuts down
  cleanly.
- **Correlation.** `observability.Middleware` reads or generates
  `X-Request-Id`, echoes it on the response, extracts W3C trace context, and
  starts a server span. The access log line carries `request_id`, `method`,
  `path`, `status`, `response_bytes`, `duration`, `trace_id`, and `span_id`
  (`internal/observability/middleware.go`). Downstream records attach
  `ContextField`, which the otelzap bridge turns into the active span's ids.
- **Telemetry providers.** `observability.BootstrapTelemetry` installs the
  process-wide tracer, meter, and OTLP log providers, with a service-name
  resource, at startup (`internal/observability/telemetry.go`). An empty
  `otel-endpoint` builds the providers with no exporters, so startup performs no
  network work. The MVP registers no custom metric instruments; the traces come
  from the HTTP middleware.

## Test plan

The suite is layered. Unit tests run with in-memory fakes and never touch a
backend. Integration tests are gated behind `//go:build integration` and drive
the real daemon binary against live containerd, BuildKit, and CNI.

| Layer | Test | Location | Assertion |
| --- | --- | --- | --- |
| Unit | `TestContainerCreate_allocatesHostPortsAndPersistsIdentity` | `internal/app/service_test.go` | host ports become concrete before persistence |
| Unit | `TestContainerCreate_publishedPortsWithHostModeRejected` | `internal/app/service_test.go` | port publish on `host` mode returns 400 |
| Unit | `TestContainerStart_joinsNetworkWithNetnsAndConcretePorts` | `internal/app/service_test.go` | CNI is called with a netns and nonzero ports |
| Unit | `TestContainerStart_networkFailureStopsContainer` | `internal/app/service_test.go` | a failed attach stops and does not persist running |
| Unit | `TestContainerRemove_doesNotReleaseCNIOwnedReservationTwice` | `internal/app/service_test.go` | exactly-once release |
| Unit | `TestContainerLogs_streamsCRILinesWithTail` | `internal/app/service_test.go` | tail semantics over CRI lines |
| Unit | `TestTranslateError_mapsAdapterSentinels` | `internal/app/service_test.go` | adapter sentinels fold onto `ports.Err*` |
| Unit | `TestMapErrorMapsContainerdErrdefs` | `internal/adapters/containerd/errors_test.go` | errdefs become adapter sentinels |
| Unit | `TestMapServiceError_mapsCanonicalSentinels` | `internal/api/errors_mapping_test.go` | sentinels become 400/404/409/500/501 |
| Unit | `TestContainerCreateFieldPolicyIsExhaustive` | `internal/api/container_fields_test.go` | every create field is classified |
| Unit | `TestReconcileMetadataWithoutTaskIsExitedAndStale` | `internal/domain/reconcile_test.go` | metadata without a task becomes exited and stale |
| Unit | `TestReconcileCleansRuntimeOnlyTasksAndPreservesPinnedDigest` | `internal/domain/reconcile_test.go` | task without metadata is cleanup |
| Integration | `TestDaemonContainerLifecycle` | `integration/daemon_test.go` | real create/start/inspect/stop/remove |
| Integration | `TestDaemonUnsupportedEndpoints` | `integration/failures_test.go` | unsupported routes return the exact errors |
| Integration | `TestContainerLogsFollowEndsWhenContainerExits` | `integration/logs_follow_test.go` | follow terminates on exit |
| Compat | `TestTestcontainersGoCompatibilityMatrix` | `integration/compat_test.go` | the library drives the daemon end to end |
| Compat | testcontainers `DockerContainer` and `DockerProvider` flows | `integration/compat_images_test.go`, `integration/compat_runtime_test.go` | pull/build/exec/logs/network stay green |

**testcontainers-go matrix.** `integration/compat_test.go` pins
`DOCKER_API_VERSION=1.44`, points `DOCKER_HOST` at the ephemeral daemon socket,
sets `TESTCONTAINERS_HOST_OVERRIDE` to the host's unicast IPv4 address, and
asserts Ryuk is disabled before running (`testcontainers.ReadConfig().RyukDisabled`).
The matrix spawns no reaper container, matching
[`docs/adr/0005-disable-ryuk-in-the-mvp.md`](../adr/0005-disable-ryuk-in-the-mvp.md).
Cleanup uses `t.Cleanup` and `testcontainers.CleanupContainer`.

The integration harness skips with an explicit, actionable reason when the host
lacks root, each backend socket, or any required CNI plugin, and the image-pull
subtest skips when containerd has no registry egress. It never weakens an
assertion to go green.

**Evidence.** Record `go test -race -count=1 ./...`, `go vet ./...`,
`go build ./...`, and `go test -tags=integration -count=1 -v ./integration/...`
in the pull request, with the commit SHA and the host containerd version. No
local path is part of the record.

## Migration and compatibility

This is greenfield. There is no pre-existing daemon state to migrate, and the
registry is intentionally volatile: it is rebuilt from containerd metadata and
tasks at every startup. No config key is added or removed by the runtime path
described here.

The Docker API mapping, the field policy for `container.Config` and
`container.HostConfig`, the published-port contract, and every unsupported
feature with its exact client error are the source of truth in
[`docs/compatibility.md`](../compatibility.md). The compatibility table grows or
shrinks only when that document changes in the same commit as the code.

Two mechanisms keep compatibility honest as the daemon evolves:
`TestContainerCreateFieldPolicyIsExhaustive` reflects over the moby structs and
fails when a field is unclassified, and the unsupported-endpoint test asserts
the exact response for every path the MVP does not serve.

## Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
| --- | --- | --- | --- |
| Loopback DNAT is not delivered by CNI `portmap`, so `127.0.0.1` connections to a published port fail | High in the recorded environment | testcontainers cannot reach mapped ports on loopback | Point testcontainers at the host address with `TESTCONTAINERS_HOST_OVERRIDE`. The constraint is documented in [`docs/compatibility.md`](../compatibility.md#published-ports). |
| Multi-network attach is unsupported | Certain | `Networks[1:]` in testcontainers fails | The CNI adapter attaches a single `eth0` and one default route. One network per container, with the exact error documented in the unsupported table. |
| containerd namespace drifts from the BuildKit worker | Medium | built images are invisible to the runtime store | On startup, when `containerd-namespace` is the built-in `moby` default, `cmd/dockerdless` switches to `default` and logs the alignment. Any other value is used as-is and must match the worker. |
| The registry is volatile and rebuilt at startup | Medium | state is lost across a restart | `domain.Reconcile` reconstructs states, exit codes, and stale/cleanup sets from containerd before the server listens. A reconciliation failure is warned about but does not stop serving. |
| A moby API bump adds a create field the policy does not classify | Medium | a new field silently loses semantics | `TestContainerCreateFieldPolicyIsExhaustive` fails the build until the field is classified. |
| Directory-sourced registry or config watch races with an atomic replace | Low | a reload is missed | The watcher observes the containing directory, so write, create, rename, and symlink-target changes all trigger a reload. |

## Milestone checklist

High-level capability milestones. Each is implemented and covered by the tests
above.

- [x] M1: configuration store, validation, and observability bootstrap
- [x] M2: containerd runtime adapter, OCI spec translation, and error mapping
- [x] M3: BuildKit image adapter (pull, inspect, list, build, remove)
- [x] M4: CNI network adapter with host-port allocation before `portmap`
- [x] M5: Docker HTTP API, stream framing, logs, and exec
- [x] M6: startup reconciliation of metadata against tasks
- [x] M7: testcontainers-go compatibility matrix with Ryuk disabled
- [x] M8: compatibility, architecture, configuration, and design documentation

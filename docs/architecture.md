# dockerdless architecture

dockerdless is a Linux-rootful Docker-compatible daemon built around explicit
Clean Architecture and hexagonal boundaries. The Docker API adapter depends on
application use cases, which depend on inward-facing ports; containerd,
BuildKit, and CNI are replaceable driven adapters behind those ports.

## Layers

- **`cmd/dockerdless`** is the composition root. It loads configuration,
  bootstraps logging and telemetry, constructs every adapter, aligns the
  containerd namespace with the BuildKit worker, registers the HTTP router, and
  owns signal cancellation and shutdown ordering.
- **`internal/api`** is the driving adapter for Docker HTTP requests. It owns
  route registration, the Unix socket lifecycle, and translation of HTTP
  concerns into application calls. It never imports an adapter package.
- **`internal/app`** is the application layer. It orchestrates use cases across
  the ports, translates adapter results into Docker-facing shapes, and is the
  single point where adapter errors become canonical `ports.Err*` sentinels.
- **`internal/domain`** contains Docker resource identities, container state,
  lifecycle specifications, port bindings, and domain events. It has no
  infrastructure dependencies.
- **`internal/ports`** defines inward-facing contracts for runtime, image,
  network, I/O, state, and the controller surfaces the API layer needs.
- **`internal/adapters`** contains driven adapters. `containerd` implements
  runtime operations, `buildkit` implements image operations (pull, inspect,
  build, remove), and `cni` implements network operations and host-port
  allocation. Adapters depend on ports and domain models; adapters never import
  one another.
- **`internal/config`** and **`internal/observability`** are bootstrap
  boundaries for typed settings, zap logging, and OpenTelemetry. The
  observability package is split into `logger.go`, `telemetry.go`,
  `exporters.go`, and `runtime.go` around the same exported API.
- **`internal/streams`** implements Docker stream framing (stdcopy), HTTP
  connection hijacking, TTY resize messages, and CRI log parsing.

## Data flow

```text
Docker client
    │ HTTP over Unix socket (0660)
    ▼
internal/api  (routes, socket, error mapping)
    │
    ▼
internal/app  (use cases, adapter→ports translation)
    │
    ▼
internal/ports
    ┌─────┼──────────┐
    ▼     ▼          ▼
containerd buildkit  cni
    │     │          │
    └─────┴────┬─────┘
         Linux runtime
```

Container logs flow the other way: the containerd adapter starts tasks with
per-container CRI log files under `<socket-dir>/dockerdless-logs/`, and
`internal/streams.ReadLogs` serves them through
`GET /containers/{id}/logs` with follow/tail/since/timestamps semantics.

Requests enter through the API adapter, cross the application use cases, and
invoke ports. Concrete adapters translate those ports to containerd, BuildKit,
and CNI. Domain state and events remain independent of HTTP and infrastructure,
so the same use cases are driven by unit tests with in-memory fakes and by the
live integration harness with real backends.

## Configuration and reload

`internal/config` decodes the environment-backed settings into a validated,
immutable `Config` snapshot. A file watcher exists for atomic snapshot swaps,
but the running daemon does not wire reload in the MVP: restart to apply
changes. The full schema is documented in the
[README configuration reference](../README.md#configuration-reference).

## Compatibility

The verified Docker API → handler → adapter → native primitive →
testcontainers-go mapping, plus every unsupported feature and its exact error,
lives in [compatibility.md](compatibility.md). That document is the source of
truth; this one describes structure only.

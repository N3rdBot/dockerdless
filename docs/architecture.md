# dockerdless architecture

dockerdless is a Linux-rootful Docker-compatible daemon built around explicit
Clean Architecture and hexagonal boundaries.

## Layers

- **`cmd/dockerdless`** is the composition root. It loads configuration,
  bootstraps logging and tracing, registers the HTTP router, and owns signal
  cancellation and shutdown ordering.
- **`internal/api`** is the driving adapter for Docker HTTP requests. It owns
  route registration and translates HTTP concerns into application calls.
- **`internal/domain`** contains Docker resource identities, container state,
  lifecycle specifications, and domain events. It has no infrastructure
  dependencies.
- **`internal/ports`** defines inward-facing contracts for runtime, image,
  network, I/O, and state operations.
- **`internal/adapters`** contains driven adapters. `containerd` implements
  runtime operations, `buildkit` implements image operations, and `cni`
  implements network operations. Adapters depend on ports and domain models;
  adapters never import one another.
- **`internal/config`** and **`internal/observability`** are bootstrap
  boundaries for typed settings, zap logging, and OpenTelemetry.

## Data flow

```text
Docker client
    │ HTTP over Unix socket
    ▼
internal/api ──► application orchestration (future)
                       │
                       ▼
                 internal/ports
                 ┌─────┼─────┐
                 ▼     ▼     ▼
             containerd buildkit cni
                 │     │     │
                 └─────┴─────┘
                  Linux runtime
```

Requests enter through the API adapter, cross application use cases (to be
added in the next task), and invoke ports. Concrete adapters translate those
ports to containerd, BuildKit, and CNI. Domain state and events remain
independent of HTTP and infrastructure so the same use cases can later be
driven by tests, CLI commands, or event consumers.

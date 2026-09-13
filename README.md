# dockerdless

A Linux-rootful Docker-compatible daemon that runs containers through
[containerd](https://containerd.io/), publishes ports through CNI, and builds
images with [BuildKit](https://github.com/moby/buildkit). It serves the Docker
Engine API surface that [testcontainers-go](https://golang.testcontainers.org/)
needs, so existing test suites can point `DOCKER_HOST` at it instead of a
Docker daemon.

It is an MVP, not a Docker replacement. The verified API mapping and the exact
unsupported-feature list live in [docs/compatibility.md](docs/compatibility.md).

## What it is and is not

**Serves:** Docker API `1.44` (minimum `1.24`) over a Unix socket — image
inspect/pull/build/remove, container create/start/stop/remove/inspect/list,
container logs, exec, networks, and port publishing. Verified live against
testcontainers-go `v0.44.0`.

**Does not serve:** CRI, rootless mode, Ryuk (the testcontainers reaper), Swarm,
Compose, registry push/login, events, volumes, multi-network containers, and the
other endpoints listed in
[docs/compatibility.md](docs/compatibility.md#unsupported-features). Every
unsupported route answers with the Docker-shaped error documented there.

## Requirements

- Linux with root (euid 0). Network namespaces, CNI bridges, and iptables rules
  require it. There is no rootless mode.
- containerd listening on `/run/containerd/containerd.sock`.
- BuildKit listening on `/run/buildkit/buildkitd.sock`.
- CNI plugins in `/opt/cni/bin`: `bridge`, `host-local`, `portmap`,
  `firewall`, `loopback`.
- The daemon must be able to write the socket's parent directory.

See [docs/security.md](docs/security.md) for the trust boundary and
[docs/operations.md](docs/operations.md) for the runbook.

## Quick start

```bash
# 1. Build the daemon.
make build

# 2. Run it on a private socket (default is /var/run/dockerdless.sock).
DOCKERDLESS_SOCKET_PATH=/tmp/dockerdless.sock sudo -E ./bin/dockerdless

# 3. In another shell, confirm it is alive.
curl --unix-socket /tmp/dockerdless.sock http://localhost/_ping
# -> OK

# 4. Point any Docker client at it.
export DOCKER_HOST=unix:///tmp/dockerdless.sock
export DOCKER_API_VERSION=1.44   # the daemon does not negotiate for old clients
docker version
```

`DOCKER_API_VERSION=1.44` is required for clients that do not negotiate the API
version (testcontainers-go among them). Unversioned requests negotiate to
`1.44` automatically.

### Use with testcontainers-go

```bash
export DOCKER_HOST=unix:///tmp/dockerdless.sock
export DOCKER_API_VERSION=1.44
export TESTCONTAINERS_RYUK_DISABLED=true
# Published ports are reachable on the host's own addresses, not on 127.0.0.1;
# see the troubleshooting note in docs/operations.md.
export TESTCONTAINERS_HOST_OVERRIDE="$(hostname -I | awk '{print $1}')"
go test ./...
```

Ryuk is intentionally disabled: set `TESTCONTAINERS_RYUK_DISABLED=true` and
clean up with `testcontainers.CleanupContainer` or `t.Cleanup`.

## Configuration reference

Settings may be supplied through environment variables or a YAML config file.
Set `DOCKERDLESS_CONFIG_FILE` to enable file loading and hot reload. There is no
command-line flag except `-help`.

| Environment variable | Type | Default | Meaning |
| --- | --- | --- | --- |
| `DOCKERDLESS_SOCKET_PATH` | absolute path | `/var/run/dockerdless.sock` | Docker API Unix socket. Created `0660`, owned by the daemon uid; see [docs/security.md](docs/security.md). |
| `DOCKERDLESS_LOG_LEVEL` | `debug`\|`info`\|`warn`\|`error`\|`dpanic`\|`panic`\|`fatal` | `info` | Structured (JSON) log level. Output goes to stderr. |
| `DOCKERDLESS_CONFIG_FILE` | absolute path | unset | YAML configuration file. Changes to a valid file are watched atomically. |
| `DOCKERDLESS_OTEL_SERVICE_NAME` | non-empty string | `dockerdless` | `service.name` resource for OpenTelemetry. |
| `DOCKERDLESS_OTEL_ENDPOINT` | `host:port`, `http://…`, or `https://…` | empty (disabled) | OTLP gRPC endpoint for traces, metrics, and logs. Empty means fully offline; exporters are not constructed. `https://` enables TLS. |
| `DOCKERDLESS_CONTAINERD_NAMESPACE` | non-empty string | `moby` | containerd namespace for containers, tasks, and images. When left at the built-in `moby` default the daemon switches to `default` to match the local BuildKit worker (see [docs/operations.md](docs/operations.md#containerd-namespaces)). |
| `DOCKERDLESS_CONTAINERD_SOCKET` | absolute path | `/run/containerd/containerd.sock` | containerd socket. |
| `DOCKERDLESS_CNI_CONFIG_DIR` | absolute path | `/etc/cni/net.d` | Directory where the daemon writes network conflists. |
| `DOCKERDLESS_CNI_PLUGIN_DIR` | absolute path | `/opt/cni/bin` | CNI plugin directory. |
| `DOCKERDLESS_BUILDKIT_SOCKET` | absolute path | `/run/buildkit/buildkitd.sock` | BuildKit daemon socket. |
| `DOCKERDLESS_DEFAULT_STOP_TIMEOUT` | Go duration (`10s`, `1m30s`) | `10s` | Default SIGTERM→SIGKILL escalation window for container stop. |
| `DOCKERDLESS_REQUEST_TIMEOUT` | Go duration | `30s` | Per-request timeout bound. |
| `DOCKERDLESS_ENABLE_CRI` | bool | `false` | Inert forward-compatibility placeholder. The MVP never serves CRI. |
| `DOCKERDLESS_ENABLE_ROOTLESS` | bool | `false` | Inert forward-compatibility placeholder. The MVP never runs rootless. |

The YAML file uses the same kebab-case keys as the table (for example,
`log-level`, `request-timeout`, and `containerd-socket`). A valid `log-level`
change is applied without restart. Invalid edits are logged and the last valid
snapshot remains active. Socket, namespace, CNI, BuildKit, telemetry, feature
flag, and timeout changes are reported as `requires restart`; they do not alter
already-constructed connections or application services. `enable-cri` and
`enable-rootless` are reserved and remain unsupported; enabling either does
not make the daemon serve that mode.

Container log files are written to `dockerdless-logs/` beside the socket
directory (for example `/tmp/dockerdless-logs/<container-id>.log`) and are
served through `GET /containers/{id}/logs`.

## Release gates

Run the full command set before shipping:

```bash
gofmt -l .                                        # must print nothing
go vet ./...                                      # static analysis
go build ./...                                    # compile everything
go test -race -count=1 ./...                      # unit + race detector
go test -tags=integration -count=1 ./integration/...   # real containerd/BuildKit/CNI
go test -run=^$ -bench=. -benchtime=200ms ./internal/streams/... ./internal/adapters/cni/...
```

The same gates are wrapped in the Makefile:

```bash
make verify       # gofmt + go vet + go test -race -count=1 ./...
make integration  # real backend matrix (skips with a recorded reason when unavailable)
make bench        # bounded, deterministic hot-path benchmarks
make release      # verify + bench
```

`make lint` (golangci-lint) is best-effort on this host: the installed binary is
built with go1.26 and cannot load the go1.27.1 module, so `gofmt` + `go vet` are
the authoritative static gates. See
[docs/operations.md](docs/operations.md#static-analysis).

The integration suite skips, with an explicit reason, when the host lacks root,
containerd, BuildKit, or the CNI plugins. It never silently passes.

## Version pins

Pinned in [`go.mod`](go.mod) (Go `1.27.1`):

| Dependency | Version |
| --- | --- |
| `github.com/moby/moby/api` | `v1.56.0` |
| `github.com/moby/moby/client` | `v0.6.0` |
| `github.com/containerd/containerd/v2` | `v2.3.5` |
| `github.com/containernetworking/cni` | `v1.3.1` |
| `github.com/moby/buildkit` | `v0.33.0` |
| `go.uber.org/zap` | `v1.28.0` |
| `github.com/spf13/viper` | `v1.21.0` |
| `go.opentelemetry.io/otel` | `v1.46.0` |
| `github.com/testcontainers/testcontainers-go` | `v0.44.0` |

## Documentation

- [docs/compatibility.md](docs/compatibility.md) — verified Docker API mapping
  and the complete unsupported-feature list.
- [docs/architecture.md](docs/architecture.md) — layers, boundaries, data flow.
- [docs/operations.md](docs/operations.md) — runbook, namespaces,
  troubleshooting, log locations.
- [docs/security.md](docs/security.md) — trust boundary, socket permissions,
  credential handling.

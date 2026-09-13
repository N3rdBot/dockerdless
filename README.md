# dockerdless

A Linux-rootful Docker-compatible daemon that runs containers through
[containerd](https://containerd.io/), publishes ports through CNI, and builds
images with [BuildKit](https://github.com/moby/buildkit). It serves the Docker
Engine API surface that [testcontainers-go](https://golang.testcontainers.org/)
needs, so existing test suites can point `DOCKER_HOST` at it instead of a
Docker daemon.

## MVP scope

- Docker API `1.44` over a Unix socket. Clients that do not negotiate the API
  version must pin it: `DOCKER_API_VERSION=1.44`.
- Verified live with testcontainers-go `v0.44.0`: image inspect/pull/build,
  container create/start/stop/remove, published-port mapping, `wait.ForHTTP`,
  `wait.ForLog`, `wait.ForExec`, container logs, exec, networks, and
  `ContainerList`-based reuse.
- The reaper (Ryuk) is intentionally disabled. Set
  `TESTCONTAINERS_RYUK_DISABLED=true` and clean up with
  `testcontainers.CleanupContainer` or `t.Cleanup`.
- Not supported: CRI, rootless mode, Swarm, Compose, registry push/login,
  events, volumes, multi-network containers. The exact Docker-shaped error for
  each is listed in [docs/compatibility.md](docs/compatibility.md).

## Requirements

- Linux with root (euid 0) — network namespaces, CNI bridges, and iptables
  rules require it.
- containerd at `/run/containerd/containerd.sock`.
- BuildKit at `/run/buildkit/buildkitd.sock`.
- CNI plugins in `/opt/cni/bin`: `bridge`, `host-local`, `portmap`,
  `firewall`, `loopback`.

## Build and run

```bash
make build
DOCKERDLESS_SOCKET_PATH=/tmp/dockerdless.sock ./bin/dockerdless
curl --unix-socket /tmp/dockerdless.sock http://localhost/_ping
```

Useful environment overrides (see `internal/config` for the full schema):
`DOCKERDLESS_SOCKET_PATH`, `DOCKERDLESS_CONTAINERD_SOCKET`,
`DOCKERDLESS_BUILDKIT_SOCKET`, `DOCKERDLESS_CONTAINERD_NAMESPACE`,
`DOCKERDLESS_CNI_CONFIG_DIR`, `DOCKERDLESS_CNI_PLUGIN_DIR`,
`DOCKERDLESS_LOG_LEVEL`. The `enable-cri` and `enable-rootless` settings are
inert forward-compatibility placeholders; this MVP never serves CRI and never
runs rootless.

## Use with testcontainers-go

```bash
export DOCKER_HOST=unix:///tmp/dockerdless.sock
export DOCKER_API_VERSION=1.44
export TESTCONTAINERS_RYUK_DISABLED=true
go test ./...
```

Published ports are reachable on the host's own addresses. Set
`TESTCONTAINERS_HOST_OVERRIDE` to a host address when the daemon runs over a
Unix socket, since the CNI portmap rules do not provide a userland loopback
proxy; see [docs/compatibility.md](docs/compatibility.md#published-ports).

## Verification

```bash
go test -race -count=1 ./...   # unit + race
go vet ./...                   # static checks
go build ./...                 # build
make integration               # real containerd/BuildKit/CNI matrix
```

The integration suite skips, with an explicit reason, when the host lacks root,
containerd, BuildKit, or the CNI plugins. It never silently passes.

See [docs/compatibility.md](docs/compatibility.md) for the verified
Docker API → dockerdless adapter → native primitive → testcontainers-go mapping
and the complete unsupported-feature list.

# dockerdless container deployment

Run the `dockerdless` daemon from a container on a Linux host that already has
containerd, BuildKit, and the CNI plugins installed.

Read this first: **the container is not a sandbox and it is not self-contained.**
`dockerdless` is a host-level control plane. It starts containers through the
host's containerd, joins their network namespaces through host PIDs, and
programs host bridges and iptables rules. The image only packages the daemon
binary and its small runtime deps. The host supplies everything the daemon
drives.

For production, prefer the systemd layout in [`deploy/systemd/`](../systemd/).
Use this container layout for CI, evaluation, and disposable hosts.

## Container versus systemd

| | systemd (`deploy/systemd/`) | container (this directory) |
| --- | --- | --- |
| Isolation | runs directly on the host, journald logs | shares host net/PID namespaces, privileged |
| Boot persistence | yes | depends on the container runtime |
| Best for | production and long-lived hosts | CI, evaluation, throwaway environments |
| Privilege | root with unit hardening | `privileged: true` plus root |
| Packaging | host binary | pinned image, reproducible everywhere |

Both layouts run the same `/usr/local/bin/dockerdless` binary and read the same
`DOCKERDLESS_*` environment variables. The systemd unit and these Compose files
use the same socket path, `/run/dockerdless/dockerdless.sock`.

## What is in the image, and what is not

In the image:

- the statically linked `dockerdless` binary at `/usr/local/bin/dockerdless`,
- `curl` and `ca-certificates` from `debian:stable-slim` (the healthcheck and
  the optional OTLP TLS trust store).

Not in the image, and not meant to be:

- `containerd` and its socket,
- `buildkitd` and its socket,
- CNI plugin binaries (`bridge`, `host-local`, `portmap`, `firewall`,
  `loopback`),
- kernel network namespaces, bridges, or iptables.

These come from the host through the mounts and namespaces described below. Do
not try to bake them into the image; the daemon talks to the host's running
services, not to copies inside its own container.

## Host requirements

- Linux with root. Network namespaces, CNI bridges, and iptables require it, and
  there is no rootless mode.
- `containerd` listening on `/run/containerd/containerd.sock`.
- `buildkitd` listening on `/run/buildkit/buildkitd.sock`.
- CNI plugins in `/opt/cni/bin`.
- A container runtime with Compose v2, or plain `docker`/`podman` with
  `--privileged --network host --pid host`.

The daemon's own requirements are in the root [`README.md`](../../README.md#requirements);
the operational runbook is [`docs/operations.md`](../../docs/operations.md).

## Build

Build from the repository root; the Dockerfile needs the Go module and the
`cmd/` and `internal/` trees.

```bash
docker build -f deploy/docker/Dockerfile -t dockerdless:local .
```

Compose does the same build when you pass `--build`.

## Run

### Docker Compose

```bash
mkdir -p deploy/docker/run
docker compose -f deploy/docker/docker-compose.yaml up -d --build
```

The daemon writes its socket to `deploy/docker/run/dockerdless.sock` on the
host, and container logs to `deploy/docker/run/dockerdless-logs/`. Stop it with
`docker compose -f deploy/docker/docker-compose.yaml down` (see
[Cleanup](#cleanup)).

### Plain `docker run`

The Compose file is a thin wrapper over these flags:

```bash
mkdir -p deploy/docker/run
docker run -d --name dockerdless \
  --network host --pid host --privileged \
  --restart unless-stopped \
  -e DOCKERDLESS_SOCKET_PATH=/run/dockerdless/dockerdless.sock \
  -e DOCKERDLESS_LOG_LEVEL=info \
  -v /run/containerd/containerd.sock:/run/containerd/containerd.sock \
  -v /run/buildkit/buildkitd.sock:/run/buildkit/buildkitd.sock \
  -v /etc/cni/net.d:/etc/cni/net.d \
  -v /opt/cni/bin:/opt/cni/bin:ro \
  -v /var/lib/cni:/var/lib/cni \
  -v "$PWD/deploy/docker/run:/run/dockerdless" \
  dockerdless:local
```

### CI

`docker-compose.ci.yaml` is a standalone variant that binds the socket to a
stable workspace path and polls health quickly, so a CI job can wait for the
daemon and then point a host-side test runner at it.

```bash
mkdir -p deploy/docker/run
docker compose -f deploy/docker/docker-compose.ci.yaml up -d --wait
```

## Host namespaces and mounts

Every entry below is required for a specific reason, grounded in the daemon's
code.

| Namespace / mount | Compose setting | Why it is required |
| --- | --- | --- |
| Host network namespace | `network_mode: host` | CNI creates veth pairs, Linux bridges, and iptables NAT/FORWARD rules for published ports. They must live in the host netns to route host traffic; in a private netns the host cannot see them and publishing silently fails. |
| Host PID namespace | `pid: host` | containerd reports task PIDs as host PIDs, and the daemon joins each task's netns via `/proc/<pid>/ns/net` (`cmd/dockerdless/main.go` `taskLocator`, `internal/adapters/cni`). A private PID namespace cannot see those PIDs, so the join fails. |
| Privileged | `privileged: true` | Creating namespaces and veth pairs, moving interfaces, and writing iptables rules needs `CAP_SYS_ADMIN` and `CAP_NET_ADMIN` with an unconfined device/seccomp profile. `/proc/<pid>/ns/net` access rides on the host PID namespace. |
| containerd socket | `/run/containerd/containerd.sock` | The runtime for every container the daemon starts (`DOCKERDLESS_CONTAINERD_SOCKET`, default `/run/containerd/containerd.sock`). |
| BuildKit socket | `/run/buildkit/buildkitd.sock` | The builder for `docker build` (`DOCKERDLESS_BUILDKIT_SOCKET`, default `/run/buildkit/buildkitd.sock`). |
| CNI config dir | `/etc/cni/net.d` read-write | The daemon writes and reads network conflists here (`DOCKERDLESS_CNI_CONFIG_DIR`, `internal/adapters/cni/adapter.go`). |
| CNI plugin dir | `/opt/cni/bin` read-only | The daemon executes the plugins; it never writes them (`DOCKERDLESS_CNI_PLUGIN_DIR`). |
| CNI IPAM state | `/var/lib/cni` read-write | `host-local` stores allocations in `/var/lib/cni/networks` and libcni caches results under `/var/lib/cni`. Sharing it keeps IP/port allocations consistent across restarts and with other CNI users. |
| Runtime dir | `/run/dockerdless` read-write | Holds the socket (created `0660` root:root) and the `dockerdless-logs/` directory beside it (`cmd/dockerdless/main.go` `logDirFor`). Bind-mount it so host clients reach the same socket inode. |

There is no `ports:` mapping. The API is a Unix socket and the service already
shares the host network, so mapping a TCP port would be meaningless.

## Point a client at the daemon

The socket is a normal Docker API endpoint. On the host:

```bash
export DOCKER_HOST="unix://$PWD/deploy/docker/run/dockerdless.sock"
export DOCKER_API_VERSION=1.44
docker version
```

`DOCKER_API_VERSION=1.44` matters for clients that do not negotiate, including
`testcontainers-go`.

For `testcontainers-go`, disable the reaper and use a host address for published
ports:

```bash
export DOCKER_HOST="unix://$PWD/deploy/docker/run/dockerdless.sock"
export DOCKER_API_VERSION=1.44
export TESTCONTAINERS_RYUK_DISABLED=true
export TESTCONTAINERS_HOST_OVERRIDE="$(hostname -I | awk '{print $1}')"
go test ./...
```

Ryuk is disabled because the daemon does not implement the reaper API. Published
ports answer on the host's non-loopback addresses, not on `127.0.0.1`; both
points are documented in [`docs/operations.md`](../../docs/operations.md)
and [`docs/compatibility.md`](../../docs/compatibility.md).

## Verify

Check the daemon from inside the container:

```bash
docker compose -f deploy/docker/docker-compose.yaml exec dockerdless \
  curl -fsS --unix-socket "$DOCKERDLESS_SOCKET_PATH" http://localhost/_ping
# -> OK
```

Or from the host, against the bind-mounted socket:

```bash
curl --unix-socket "$PWD/deploy/docker/run/dockerdless.sock" http://localhost/_ping   # OK
curl --unix-socket "$PWD/deploy/docker/run/dockerdless.sock" http://localhost/version # JSON
```

`docker compose ps` shows `healthy` once the image healthcheck passes. The
startup log line records the effective socket, namespace, and log directory.

## Caveats

- **Privileged and host-coupled.** With `privileged: true`, host net/PID
  namespaces, and host mounts, this container can see and affect host processes
  and network state. Treat it as host-level software, not an isolated workload.
- **Runs as root.** The daemon needs root for namespaces, bridges, and iptables,
  and there is no rootless mode. The image sets `USER 0:0` on purpose.
- **Not self-contained.** containerd, BuildKit, and the CNI plugins are the
  host's, reached over bind-mounted Unix sockets. The image cannot start on a
  host that lacks them.
- **Socket inode staleness.** The backend sockets are mounted as files. If
  containerd or `buildkitd` restarts, the mount points at the old inode and the
  daemon cannot reconnect. Recreate the container after a backend restart, or
  bind the parent directory (`/run/containerd:/run/containerd`) instead.
- **Socket permissions.** The socket is `0660` root:root. A host client must run
  as root or be in the daemon's group. The daemon refuses to start if it cannot
  make the socket exactly `0660`.
- **Startup-only configuration.** Only `log-level` is hot-reloadable. Socket,
  namespace, CNI, BuildKit, telemetry, timeout, and feature-flag changes log
  `configuration change requires restart`; recreate the container to apply them.
  See [`docs/configuration.md`](../../docs/configuration.md).
- **Persistent side effects.** The daemon does not remove containers, networks,
  images, or `dockerdless-logs/` on stop. The CNI `portmap` plugin also leaves
  per-container `CNI-DN-*` iptables chains behind. Clean up explicitly.
- **Published ports and loopback.** Loopback delivery is a known limitation;
  use a host address. See
  [`docs/operations.md`](../../docs/operations.md#published-ports-do-not-answer-on-127001).

## Cleanup

Stop the daemon, then remove what it left behind. While it is running, clean up
through the API:

```bash
export DOCKER_HOST="unix://$PWD/deploy/docker/run/dockerdless.sock"
export DOCKER_API_VERSION=1.44
docker ps -aq | xargs -r docker rm -f
docker network ls -q | xargs -r docker network rm
docker images -q  | xargs -r docker rmi -f
```

Then stop and remove the container and the runtime directory:

```bash
docker compose -f deploy/docker/docker-compose.yaml down
sudo rm -rf deploy/docker/run
```

The full cleanup procedure, including the leftover `CNI-DN-*` iptables chains,
is in [`docs/operations.md`](../../docs/operations.md#cleanup).

## Related documentation

- [`docs/deployment.md`](../../docs/deployment.md): canonical deployment
  procedure for both layouts.
- [`docs/configuration.md`](../../docs/configuration.md): every `DOCKERDLESS_*`
  setting.
- [`docs/operations.md`](../../docs/operations.md): runbook, namespaces,
  troubleshooting, cleanup.
- [`../systemd/`](../systemd/): the recommended production layout.
- [`../README.md`](../README.md): deployment layouts overview.

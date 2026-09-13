# dockerdless deployment

How to install, run, upgrade, and remove the dockerdless daemon on a rootful
Linux host. This is the operator-facing entry point; runtime behavior (socket
lifecycle, namespace alignment, log locations, and troubleshooting) is covered
in [operations.md](operations.md), and the settings reference lives in
[configuration.md](configuration.md).

Ready-to-install artifacts, including the systemd unit used below, live in
[`deploy/`](../deploy/README.md).

## Table of contents

- [Prerequisites](#prerequisites)
- [Host readiness checklist](#host-readiness-checklist)
- [Install the binary](#install-the-binary)
- [Deploy with systemd (recommended)](#deploy-with-systemd-recommended)
- [Deploy in a container](#deploy-in-a-container)
- [Upgrade](#upgrade)
- [Uninstall](#uninstall)
- [Security posture](#security-posture)
- [Verification](#verification)
- [Troubleshooting](#troubleshooting)
- [Related documentation](#related-documentation)

## Prerequisites

- **Linux with root (euid 0).** Network namespaces, CNI bridges, and iptables
  rules require it. There is no rootless mode.
- **containerd** listening on `/run/containerd/containerd.sock`.
- **BuildKit** listening on `/run/buildkit/buildkitd.sock`.
- **CNI plugins** in `/opt/cni/bin`: `bridge`, `host-local`, `portmap`,
  `firewall`, `loopback`.
- **CNI config directory** `/etc/cni/net.d`, writable by the daemon. The daemon
  writes a conflist there for each Docker network it creates.
- **Go 1.27.1**, only if you build from source. Go 1.27.1 is the version pinned
  in [`go.mod`](../go.mod).

The daemon has exactly one command-line flag, `-help`. There is no `-version`,
no `-config`, and no daemonize flag. Configuration comes from `DOCKERDLESS_*`
environment variables and, optionally, a YAML file named by
`DOCKERDLESS_CONFIG_FILE`.

## Host readiness checklist

Run these before installing. Every check should pass.

```bash
# 1. Root.
id -u                                   # -> 0

# 2. containerd is running and its socket exists.
systemctl status containerd
test -S /run/containerd/containerd.sock && echo "containerd socket ok"

# 3. BuildKit is running and its socket exists. The unit name varies by
#    distribution (buildkit.service or buildkitd.service).
systemctl status buildkit 2>/dev/null || systemctl status buildkitd
test -S /run/buildkit/buildkitd.sock && echo "buildkit socket ok"

# 4. CNI plugins are installed.
for p in bridge host-local portmap firewall loopback; do
  test -x "/opt/cni/bin/$p" || echo "MISSING /opt/cni/bin/$p"
done

# 5. CNI config directory exists and is writable.
test -d /etc/cni/net.d && echo "cni config dir ok"
```

If a check fails, fix the host before continuing. dockerdless is a client of
containerd and BuildKit, not a replacement for either; a missing backend socket
makes startup fail with a connect error.

## Install the binary

Build once from the repository, or compile straight to the system path.

```bash
# From a checkout.
make build                              # -> bin/dockerdless
sudo install -m 0755 bin/dockerdless /usr/local/bin/dockerdless

# Or compile directly.
sudo go build -o /usr/local/bin/dockerdless ./cmd/dockerdless
```

For local development, `go run ./cmd/dockerdless` runs the same foreground
process without installing anything.

## Deploy with systemd (recommended)

The unit in [`deploy/systemd/`](../deploy/systemd/) runs the foreground daemon
with boot persistence, journald logging, restart-on-failure, and as much
hardening as a rootful container control plane allows.

```bash
cd /path/to/dockerdless

# 1. Install the unit, environment file, and tmpfiles entry.
sudo install -d -m 0750 /etc/dockerdless
sudo install -m 0644 deploy/systemd/dockerdless.service  /etc/systemd/system/dockerdless.service
sudo install -m 0600 deploy/systemd/dockerdless.env       /etc/dockerdless/dockerdless.env
sudo install -m 0644 deploy/systemd/dockerdless.tmpfiles  /etc/tmpfiles.d/dockerdless.conf
sudo systemd-tmpfiles --create /etc/tmpfiles.d/dockerdless.conf

# 2. Create the YAML config file. The env file sets DOCKERDLESS_CONFIG_FILE, so
#    this file must exist: the daemon exits non-zero if the path is set but
#    unreadable. Keep at least log-level; the full key list is in
#    docs/configuration.md.
sudo tee /etc/dockerdless/config.yaml >/dev/null <<'YAML'
log-level: info
YAML

# 3. Load the unit and start it.
sudo systemctl daemon-reload
sudo systemctl enable --now dockerdless

# 4. Confirm it is up.
systemctl status dockerdless
journalctl -u dockerdless -f            # JSON logs on stderr -> journald
```

The service runs `/usr/local/bin/dockerdless` with
`EnvironmentFile=-/etc/dockerdless/dockerdless.env`. The unit creates
`/run/dockerdless` (mode 0750) via `RuntimeDirectory=dockerdless`, and the env
file points `DOCKERDLESS_SOCKET_PATH` at `/run/dockerdless/dockerdless.sock`, so
the API socket and the container log directory (`/run/dockerdless/dockerdless-logs/`)
both live under that directory.

The effective settings are:

| Setting | Value in this layout |
| --- | --- |
| Binary | `/usr/local/bin/dockerdless` |
| Socket | `/run/dockerdless/dockerdless.sock` (created `0660`, owned by root) |
| Config file | `/etc/dockerdless/config.yaml` |
| Log level | `info` (JSON on stderr, captured by the journal) |
| Container logs | `/run/dockerdless/dockerdless-logs/<container-id>.log` |

Point clients at the socket:

```bash
export DOCKER_HOST=unix:///run/dockerdless/dockerdless.sock
export DOCKER_API_VERSION=1.44          # required for clients that do not negotiate
docker version
```

### Reloading configuration

There is no `ExecReload` and no `SIGHUP` handler. The daemon watches the YAML
file named by `DOCKERDLESS_CONFIG_FILE` and applies a valid `log-level` change
live. Every other setting logs `configuration change requires restart` and is
ignored until you restart the service. Environment-only settings are read at
startup and are not watched, which is why this layout also sets
`DOCKERDLESS_LOG_LEVEL` in the env file as the startup default.

```bash
# Apply a log-level change without restarting.
sudo sed -i 's/^log-level:.*/log-level: debug/' /etc/dockerdless/config.yaml
journalctl -u dockerdless -f            # look for {"msg":"configuration reloaded","setting":"log-level"}
```

## Deploy in a container

A container deployment of dockerdless is an evaluation and CI path, not the
production recommendation. Because the daemon programs the host network and
connects to host backends, the container must:

- share the host network and PID namespaces (`--network host --pid host`),
- mount the containerd socket (`/run/containerd/containerd.sock`),
- mount the BuildKit socket (`/run/buildkit/buildkitd.sock`),
- mount `/etc/cni/net.d` and `/opt/cni/bin`, and
- run as root with the privileges those operations require.

See [`deploy/docker/`](../deploy/docker/) for the container layout. Even when
packaged as an image, dockerdless is not a normal container: it needs host
network access for CNI and iptables, and it needs to reach the host's
containerd and BuildKit. For production, use the systemd path above; it keeps
the daemon on the host where its backends and networking already live.

## Upgrade

Upgrade in place. A daemon restart does not stop running containers; they are
containerd tasks and keep running while dockerdless is down.

```bash
# 1. Build or fetch the new binary.
make build

# 2. Stop, replace, start.
sudo systemctl stop dockerdless
sudo install -m 0755 bin/dockerdless /usr/local/bin/dockerdless
sudo systemctl start dockerdless
```

If you changed the unit, env, or tmpfiles files, reinstall them and run
`systemctl daemon-reload` between stop and start.

What survives a restart:

- **Socket path and permissions.** The daemon removes the socket file on clean
  stop and rebinds it `0660` at startup. Clients keep using the same
  `DOCKER_HOST` and do not need to change.
- **Running containers.** They are not killed. The daemon reconstructs their
  Docker identity on startup.
- **Container logs.** Files under `/run/dockerdless/dockerdless-logs/` survive a
  service restart. Because `/run` is tmpfs, they do not survive a reboot.

What is rebuilt:

- **The Docker identity registry is volatile.** It is not persisted. Startup
  reconciliation lists container metadata and task status from the configured
  containerd namespace and rebuilds the registry: running tasks restore as
  `running`, stopped tasks as `exited` with their exit code and OOM flag,
  metadata without a task restores as `exited`, and a task without metadata is
  counted as cleanup. The log line `container state reconciliation complete`
  reports `reconciled_count`, `stale_count`, and `cleanup_count`. If containerd
  cannot be listed, the daemon logs a warning and starts with an empty registry
  rather than blocking startup.

See [Startup reconciliation](operations.md#startup-reconciliation) for details.

## Uninstall

Stop and disable the service first, then remove the files it installed, then
clean up the runtime resources dockerdless created.

```bash
# 1. Stop and disable.
sudo systemctl disable --now dockerdless

# 2. Remove the installed files.
sudo rm -f /etc/systemd/system/dockerdless.service
sudo rm -f /etc/tmpfiles.d/dockerdless.conf
sudo rm -f /usr/local/bin/dockerdless
sudo rm -rf /etc/dockerdless
sudo systemctl daemon-reload
```

The daemon does not remove containers, networks, images, or its log directory
on exit. Clean those up explicitly while the daemon is still running, using the
same API it serves:

```bash
export DOCKER_HOST=unix:///run/dockerdless/dockerdless.sock DOCKER_API_VERSION=1.44
docker ps -aq | xargs -r docker rm -f
docker network ls -q | xargs -r docker network rm
docker images -q  | xargs -r docker rmi -f

sudo rm -rf /run/dockerdless/dockerdless-logs
```

The CNI `portmap` plugin leaves per-container `CNI-DN-*` iptables chains behind
on disconnect. They are harmless, but they can be removed with an
`iptables-save` to filter to `iptables-restore` pass that keeps the shared
`CNI-HOSTPORT-*`, `CNI-ADMIN`, and `CNI-FORWARD` chains. This is a known
follow-up, documented in [Cleanup](operations.md#cleanup).

## Security posture

dockerdless is a rootful container control plane. It is not a security sandbox
between its clients and the host. The full trust boundary is in
[security.md](security.md); the reporting process is in
[SECURITY.md](../SECURITY.md).

- **The daemon runs as root.** Network namespaces, CNI bridges, and iptables
  require euid 0, and it connects to the root-owned containerd and BuildKit
  sockets. There is no rootless mode, and the service is not a dedicated user.
- **The API socket is the perimeter.** It is created `0660`, owned by the daemon
  uid (root when run under this unit), and its group is the daemon's primary
  group. Before binding, the daemon refuses a non-socket path, refuses a live
  socket, and refuses a stale socket owned by another uid; it tightens a stale
  socket whose mode is wider than `0660` to `0660` and logs
  `repaired unsafe socket permissions` before replacing it. After binding it
  re-verifies that the socket is exactly `0660` and owned by its uid. There is no
  socket-group option in the MVP.
- **Socket access equals root.** Any client that can connect can create and exec
  containers, build images, and publish ports. There is no per-request
  authentication or authorization beyond Unix socket permissions. This matches
  Docker's own socket trust model.
- **Containers and images are root-equivalent.** A started container runs with
  the privileges its spec requests under the daemon's rootful containerd, and a
  build executes Dockerfile code with root through BuildKit. Do not build or run
  untrusted images on a host you would not hand root.
- **Published ports are host-exposed.** The CNI `portmap` plugin installs host
  DNAT rules; the daemon adds no firewall scoping beyond what CNI installs.

### systemd hardening

The unit enables `ProtectSystem=strict`, `ProtectHome=true`, and
`PrivateTmp=true`, and explicitly allows the two writable paths the daemon
needs:

- `/run/dockerdless` for the API socket and container logs,
- `/etc/cni/net.d` for the CNI conflists it writes.

`ReadWritePaths` uses the concrete paths, so if you change
`DOCKERDLESS_CNI_CONFIG_DIR` or the socket directory, update `ReadWritePaths` to
match or the daemon will fail to write.

Three directives are deliberately **not** set, because a rootful container
control plane cannot use them:

- `NoNewPrivileges` would forbid the privilege transitions CNI plugins and the
  containerd/runc runtime need during namespace, mount, and network setup.
- `PrivateNetwork` would hide the host interfaces, routes, and addresses the
  daemon must program for CNI bridges, veth pairs, iptables DNAT, and registry
  egress.
- `ProtectKernelTunables` would hide the `/proc/sys` networking entries that
  netns, iptables, and CNI setup read and write.

The unit also leaves `ProtectKernelModules` and `PrivateMounts` at their
defaults because the container runtime performs host mounts and may load
modules.

## Verification

With the systemd layout, the socket is `/run/dockerdless/dockerdless.sock`.
Confirm the service is active, the socket exists with the right mode, and the
API answers.

```bash
# Service state.
systemctl is-active dockerdless             # -> active

# Socket ownership and mode.
stat -c '%a %U:%G %F' /run/dockerdless/dockerdless.sock   # -> 660 root:root socket

# API liveness and metadata.
curl --unix-socket /run/dockerdless/dockerdless.sock http://localhost/_ping     # -> OK
curl --unix-socket /run/dockerdless/dockerdless.sock http://localhost/version   # -> JSON
curl --unix-socket /run/dockerdless/dockerdless.sock http://localhost/info      # -> JSON
```

`/_ping` returns `OK`. `/version` reports the advertised API version `1.44`
(minimum `1.24`). `/info` reports container and image counts, the driver, and
host facts. The startup log line records the effective socket, namespace, and
log directory:

```json
{"level":"info","msg":"dockerdless daemon started","socket":"/run/dockerdless/dockerdless.sock","namespace":"default","log_dir":"/run/dockerdless/dockerdless-logs"}
```

The effective namespace is `default` when `DOCKERDLESS_CONTAINERD_NAMESPACE` is
left at the built-in `moby` default, so built images stay visible to the runtime
store. See [containerd namespaces](operations.md#containerd-namespaces).

## Troubleshooting

Start with [Troubleshooting](operations.md#troubleshooting) in operations.md;
it covers socket permission errors, `socket ... is already in use`, the loopback
port-publication limitation, images that are not visible to the runtime, registry
timeouts, and short-lived containers.

Deployment-specific checks:

- **The service is not active, and the journal shows
  `load configuration: read configuration file ...`.** `DOCKERDLESS_CONFIG_FILE`
  is set but the file is missing or unreadable. Create
  `/etc/dockerdless/config.yaml` (at least `log-level: info`) or clear the
  variable from the env file.
- **The service is not active, and the journal shows a connect error for
  containerd or BuildKit.** The backend socket is absent or the service started
  before the backend was ready. Check `systemctl status containerd` and
  `systemctl status buildkit`, and confirm `/run/containerd/containerd.sock`
  and `/run/buildkit/buildkitd.sock` exist.
- **Network creation fails with a permission error after enabling
  `ProtectSystem=strict`.** `/etc/cni/net.d` is not writable. Confirm it exists
  and is listed in `ReadWritePaths` (the unit ships it there), and that you did
  not move `DOCKERDLESS_CNI_CONFIG_DIR` without updating the unit.
- **`stat` reports a socket mode other than `660`.** Something other than the
  daemon owns the path, or a stale socket was widened. The daemon refuses to
  start when it cannot make the socket exactly `0660`; find and stop the owner
  rather than deleting a live socket.

## Related documentation

- [deploy/](../deploy/README.md) and
  [deploy/systemd/](../deploy/systemd/dockerdless.service): install artifacts.
- [configuration.md](configuration.md): every `DOCKERDLESS_*` variable and YAML
  key.
- [operations.md](operations.md): runbook, namespaces, log locations,
  troubleshooting, cleanup.
- [security.md](security.md): trust boundary, socket permissions, credentials.
- [SECURITY.md](../SECURITY.md): how to report a vulnerability privately.
- [compatibility.md](compatibility.md): the supported Docker API surface and the
  unsupported-feature list.

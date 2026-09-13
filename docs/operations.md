# dockerdless operations

Runbook for operating the dockerdless MVP. All commands assume a rootful Linux
host with containerd, BuildKit, and CNI plugins installed (see the
[requirements](../README.md#requirements)).

## Start and stop

```bash
# Build once.
make build

# Start in the foreground. Logs are JSON on stderr.
DOCKERDLESS_SOCKET_PATH=/tmp/dockerdless.sock ./bin/dockerdless

# Start with a non-default backend socket and namespace.
DOCKERDLESS_SOCKET_PATH=/tmp/dockerdless.sock \
DOCKERDLESS_CONTAINERD_SOCKET=/run/containerd/containerd.sock \
DOCKERDLESS_CONTAINERD_NAMESPACE=default \
./bin/dockerdless
```

The daemon installs a SIGINT/SIGTERM handler. On either signal it stops serving,
flushes telemetry and logs, and removes its socket file. Shutdown is bounded by
a fixed 5-second timeout.

```bash
# Graceful stop.
kill -TERM "$(pgrep -f 'bin/dockerdless')"
```

Exit code 0 means the socket was removed and telemetry flushed. The daemon does
not remove containers, networks, or the `dockerdless-logs/` directory on exit;
clean those up explicitly (see [Cleanup](#cleanup)).

There is no daemonize mode. Run it under systemd or in a terminal;
`-help` prints usage. To use a YAML file and enable reload, set
`DOCKERDLESS_CONFIG_FILE=/etc/dockerdless/config.yaml` before starting.

### Configuration reload

The file watcher observes the containing directory, so atomic replacement is
safe. Valid `log-level` edits take effect immediately. Invalid YAML or values
are rejected, logged, and do not replace the last valid snapshot. Changes to
the socket path, containerd/BuildKit sockets or namespace, CNI directories,
OTel settings, `default-stop-timeout`, `request-timeout`, or reserved feature
flags log `configuration change requires restart`; restart the daemon to apply
them. Environment-only configuration is read at startup and is not watched.

For a reload check:

```bash
DOCKERDLESS_CONFIG_FILE=/tmp/dockerdless.yaml ./bin/dockerdless
# edit log-level: info -> debug, then emit a debug-producing operation
# watch stderr for: {"msg":"configuration reloaded","setting":"log-level"}
```

If a valid-looking edit does not apply, inspect stderr for a rejected reload,
confirm the file is readable by the daemon, and ensure the file contains the
same kebab-case keys as the [configuration reference](../README.md#configuration-reference).

### Startup reconciliation

Before the API socket starts serving, dockerdless lists container metadata and
task status from the configured containerd namespace and rebuilds its volatile
Docker identity registry. A task in `running` state restores the container as
`running`; stopped tasks restore `exited` with their exit code and OOM flag.
Metadata with no task is restored as `exited` and counted as stale, while a
task with no metadata is counted as cleanup. The structured log entry
`container state reconciliation complete` includes `reconciled_count`,
`stale_count`, and `cleanup_count`. If containerd cannot be listed or a record
cannot be reconciled, the daemon logs a warning and starts with an empty
registry so a transient backend problem does not block availability.

## Verify it is alive

```bash
curl --unix-socket /tmp/dockerdless.sock http://localhost/_ping          # -> OK
curl --unix-socket /tmp/dockerdless.sock http://localhost/version        # -> JSON
curl --unix-socket /tmp/dockerdless.sock http://localhost/info           # -> JSON
```

The startup log line records the effective socket, namespace, and log
directory:

```json
{"level":"info","msg":"dockerdless daemon started","socket":"/tmp/dockerdless.sock","namespace":"default","log_dir":"/tmp/dockerdless-logs"}
```

## Socket lifecycle and permissions

The Docker API socket is created `0660` and owned by the daemon uid. Before
binding, the daemon applies this gate to an existing path:

1. **Not a socket** — startup fails with `path <path> exists and is not a
   socket`; the file is never touched. Remove or rename the file yourself.
2. **Live socket** — startup fails with `socket <path> is already in use`; the
   other daemon keeps serving. Stop the other daemon first.
3. **Owned by another uid** — startup fails with `refusing to remove socket
   <path> owned by uid <uid>`; a foreign listener's state is never deleted.
4. **Stale socket with permissions wider than `0660`** — the daemon tightens the
   stale socket to `0660`, logs `repaired unsafe socket permissions` with the
   previous mode, then removes it and binds a fresh socket.
5. **Stale socket already at `0660`** — removed and replaced silently.

After binding, the daemon re-verifies that the socket is exactly `0660` and
owned by its uid; a mismatch aborts startup. The parent directory is created
`0755` when missing.

The socket directory must be writable by the daemon. `/var/run` (the default
`/var/run/dockerdless.sock` is a symlink target of `/run`) requires root, which
is already a hard requirement. For test runs, a private directory such as
`/tmp/...` is fine.

## containerd namespaces

The daemon stores containers, tasks, and images in a containerd namespace. The
built-in default is `moby`, but the local BuildKit worker runs in `default`.
To keep built images visible to the runtime store, the composition root aligns
the namespace:

- `DOCKERDLESS_CONTAINERD_NAMESPACE` set to anything other than `moby` is used
  as-is. Make sure BuildKit's worker uses the same namespace.
- `DOCKERDLESS_CONTAINERD_NAMESPACE` unset (or `moby`) switches to `default`
  and logs:

  ```json
  {"level":"info","msg":"aligning containerd namespace with the shared BuildKit worker","configured":"moby","effective":"default"}
  ```

This is a deliberate MVP simplification. Multi-tenant namespace isolation is
not implemented.

## Log locations

| Log | Location | Notes |
| --- | --- | --- |
| Daemon structured logs | stderr (JSON) | `DOCKERDLESS_LOG_LEVEL` controls filtering. |
| Container stdout/stderr | `<socket-dir>/dockerdless-logs/<container-id>.log` | CRI-formatted lines; served through `GET /containers/{id}/logs`. |
| containerd | journald / `/var/log/containerd` (distribution-dependent) | Backend failures surface here. |
| BuildKit | journald / buildkitd's own logging | Build failures surface here. |

The log directory is created lazily when a container starts. A missing log file
without `follow` is an empty stream, not an error.

To inspect a container log directly:

```bash
tail -f /tmp/dockerdless-logs/<container-id>.log
```

## Troubleshooting

### `curl` fails with permission denied on the socket

Confirm the mode and owner:

```bash
stat -c '%a %U:%G %F' /tmp/dockerdless.sock   # -> 660 root:root socket
```

Only the daemon uid and its group can connect. If another user needs access, run
that client as the daemon user (or as root). The daemon refuses to start when it
cannot make the socket exactly `0660`, so a wrong mode means a different process
owns the path.

### Startup fails with `socket ... is already in use`

Another dockerdless (or Docker) instance owns the socket. Find and stop it:

```bash
fuser -v /tmp/dockerdless.sock
```

Do not delete a live socket by hand; stop its owner instead.

### Published ports do not answer on `127.0.0.1`

This is a known, documented limitation. The CNI `portmap` plugin DNATs from
`PREROUTING` and `OUTPUT`, so published ports are reachable on the host's own
non-loopback addresses. Loopback delivery additionally needs a userland proxy or
`route_localnet` handling that this MVP does not implement; in the recorded
verification environment loopback DNAT did not deliver even with
`net.ipv4.conf.*.route_localnet=1`.

Workaround for testcontainers-go: point the library at a host address.

```bash
export TESTCONTAINERS_HOST_OVERRIDE="$(hostname -I | awk '{print $1}')"
```

A plain client can do the same: connect to the host's IP instead of `127.0.0.1`.

### Build succeeds but the image is not visible to the runtime

The BuildKit worker namespace and the daemon namespace differ. Check the
`namespace` field on the startup log line and the
`aligning containerd namespace` record; set
`DOCKERDLESS_CONTAINERD_NAMESPACE` to the worker's namespace (usually `default`)
or configure the BuildKit worker to match the daemon.

### Image pull/build fails with a registry timeout

containerd and buildkitd need their own egress to the registry. An HTTP proxy
exported in the shell does not reach them. Verify with:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://registry-1.docker.io/v2/
```

If the host has no direct egress, use local images or a mirror.

### A container exits immediately and start reports a warning

Short-lived containers can exit before the post-start CNI join completes. The
daemon treats that as a successful start with a logged warning, saves the
container as `exited`, and preserves the exit code. This is expected for
one-shot images such as `hello-world`.

### Static analysis

`make lint` and `make lint-md` **require** the pinned tools installed by
`make tools`; they never fall back to a system `golangci-lint` or
`markdownlint-cli2` and instead fail with `run 'make tools'` when a tool is
missing. `make tools` installs `golangci-lint` v2.13.2 into `.tools/bin` and
`markdownlint-cli2` 0.22.1 into `node_modules`, verifies each binary reports
its pinned version, and never touches `go.mod`/`go.sum`, so run it once per
clone (and after a pin bump). `golangci-lint` v2 then runs as part of the gate
(`make lint`), with the three-group import order enforced by `gci` (using
`localmodule`, derived from `go.mod`) and the architecture boundaries enforced
by `depguard`. `make lint-md` runs the Markdown lint. To deliberately run a
different binary, override the path explicitly, for example in CI:
`make lint GOLANGCI_LINT=golangci-lint` or
`make lint-md MARKDOWNLINT_CLI2=markdownlint-cli2`. `make verify` still runs
`gofmt`, `go vet`, and the race-enabled unit tests.

Do not run a system-installed `staticcheck` directly. It is not pinned by this
repository and a host copy can be too old for the toolchain: a v0.7.0 binary
fails against go1.27.1 with `export data version 4 is greater than maximum
supported version 2`. `staticcheck` is enabled inside `golangci-lint`, so
`make lint` is the authoritative static check; if a newer standalone
`staticcheck` is ever required, pin it the same way as the other tools rather
than relying on PATH.

## Cleanup

The daemon leaves containers, networks, images, and the log directory behind.
Clean up through the API while it is running:

```bash
export DOCKER_HOST=unix:///tmp/dockerdless.sock DOCKER_API_VERSION=1.44
docker ps -aq | xargs -r docker rm -f
docker network ls -q | xargs -r docker network rm
docker images -q  | xargs -r docker rmi -f
```

Then stop the daemon and remove its runtime directory:

```bash
kill -TERM "$(pgrep -f 'bin/dockerdless')"
rm -rf /tmp/dockerdless-logs
```

The CNI `portmap` plugin leaves per-container `CNI-DN-*` iptables chains behind
on disconnect; they are harmless but can be removed with an `iptables-save` →
filter → `iptables-restore` pass that keeps the shared `CNI-HOSTPORT-*`,
`CNI-ADMIN`, and `CNI-FORWARD` chains. This is a known follow-up.

## Release command set

```bash
make tools                                                       # pinned lint tools; required by make lint/lint-md
gofmt -l .                                                       # empty output
go vet ./...
make lint                                                        # golangci-lint v2
make lint-md                                                     # markdownlint-cli2
go build ./...
go test -race -count=1 ./...
go test -tags=integration -count=1 ./integration/...             # needs root + backends
go test -run=^$ -bench=. -benchtime=200ms ./internal/streams/... ./internal/adapters/cni/...
```

Or `make release` for the host-independent subset (`verify` + `bench`).

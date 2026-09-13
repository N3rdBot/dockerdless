# dockerdless Docker API compatibility

This document is the honest, verified MVP mapping between the Docker Engine API
surface, the dockerdless handlers and adapters, the native Linux primitives
behind them, and the testcontainers-go consumers that drive them. Every row
marked **verified live** is exercised by `go test -tags=integration -count=1 -v
./integration/...` against a real daemon on an ephemeral Unix socket, real
containerd (`/run/containerd/containerd.sock`), real BuildKit
(`/run/buildkit/buildkitd.sock`), and real CNI plugins (`/opt/cni/bin`).

Environment of the recorded verification run:

- `github.com/testcontainers/testcontainers-go v0.44.0`, Ryuk **disabled**
  (`TESTCONTAINERS_RYUK_DISABLED=true`), `DOCKER_API_VERSION=1.44` pinned
  because the library's Moby client does not negotiate the API version.
- Published ports are consumed through the host's own IPv4 address via
  `TESTCONTAINERS_HOST_OVERRIDE`; see [Published ports](#published-ports).

## Capability mapping

| Docker API | dockerdless handler → adapter | native primitive | testcontainers-go consumer | Status |
| --- | --- | --- | --- | --- |
| `GET /_ping`, `HEAD /_ping` | `api.pingHandler` | daemon identity (no backend) | `DockerClient.Ping` | verified live |
| `GET /version` | `api.versionHandler` | advertised API `1.44`, minimum `1.24` | `DockerClient.ServerVersion` | verified live |
| `GET /info` | `handlers.info` → `app.Service.Info` | containerd namespace/snapshotter counters, backend socket paths | `DockerClient.Info` (provider startup) | verified live |
| `POST /images/create` | `handlers.imageCreate` → `app.Service.ImagePull` | `buildkit.Adapter.PullImage` → containerd remote pull + unpack | `DockerProvider.PullImage` | implemented; **SKIP** in the recorded run (no direct registry egress from containerd) |
| `GET /images/{name}/json` | `handlers.imageInspect` → `app.Service.ImageInspect` | `buildkit.Adapter.Inspect` (containerd image service) + `app.containerdImageConfigs` (OCI config blob) | `DockerClient.ImageInspect` in every create and build path | verified live |
| `GET /images/json` | `handlers.imageList` → `app.Service.ImageList` | `buildkit.Adapter.List` → containerd image service | no direct library call; asserted through the Moby client | verified live |
| `POST /build` | `handlers.build` → `app.Service.ImageBuild` | `buildkit.Adapter.Build` → BuildKit `dockerfile.v0` solve + image export | `DockerProvider.BuildImage` via `FromDockerfile` | verified live |
| `DELETE /images/{name}` | `handlers.imageRemove` → `app.Service.ImageRemove` | `buildkit.Adapter.Remove` → containerd image service delete | `DockerContainer.Terminate` removes built images | verified live |
| `POST /containers/create` | `handlers.containerCreate` → `app.Service.ContainerCreate` | containerd `CreateContainer` (snapshot + OCI spec), CNI network resolve, host-port allocator | `DockerProvider.CreateContainer` | verified live |
| `POST /containers/{id}/start` | `handlers.containerStart` → `app.Service.ContainerStart` | containerd task start + CNI ADD (`bridge`/`host-local`/`portmap`/`firewall`) | `DockerContainer.Start` | verified live |
| `POST /containers/{id}/stop` | `handlers.containerStop` → `app.Service.ContainerStop` | containerd task SIGTERM → SIGKILL escalation | `DockerContainer.Stop` | verified live |
| `DELETE /containers/{id}` | `handlers.containerRemove` → `app.Service.ContainerRemove` | containerd task/container/snapshot delete + CNI DEL | `DockerContainer.Terminate` | verified live |
| `GET /containers/{id}/json` | `handlers.containerInspect` → `app.Service.ContainerInspect` | in-memory registry + containerd task status + saved port bindings | `DockerContainer.Inspect`/`State`/`MappedPort`, reuse | verified live |
| `GET /containers/json` | `handlers.containerList` → `app.Service.ContainerList` | registry + containerd status refresh | `DockerProvider.findContainerByName` (reuse) | verified live |
| `GET /containers/{id}/logs` | `handlers.containerLogs` → `app.Service.ContainerLogs` | CRI log files written by `StartWithIO` + `streams.ReadLogs` | `DockerContainer.Logs`, `wait.ForLog` | verified live |
| `POST /containers/{id}/exec` | `handlers.execCreate` → `app.Service.ExecCreate` | containerd exec registration | `DockerContainer.Exec`, `wait.ForExec` | verified live |
| `POST /exec/{id}/start` | `handlers.execStart` → `app.Service.ExecStart` | containerd task `Exec` + hijacked stdcopy stream (HTTP 101) | `DockerContainer.Exec`, `wait.ForExec` | verified live |
| `GET /exec/{id}/json` | `handlers.execInspect` → `app.Service.ExecInspect` | in-memory exec record | `DockerContainer.Exec`, `wait.ForExec` | verified live |
| `GET /networks`, `GET /networks/{id}` | `handlers.networkList`/`networkInspect` | CNI conflist parse + netlink bridge inspect | `DockerProvider.ensureDefaultNetwork`, `GetNetwork` | verified live |
| `POST /networks/create` | `handlers.networkCreate` → `app.Service.NetworkCreate` | CNI conflist write + netlink bridge create | `GenericNetwork` (`network.New`) | verified live |
| `POST /networks/{id}/connect` | `handlers.networkConnect` → `app.Service.NetworkConnect` | CNI ADD against the live network namespace | testcontainers `Networks[1:]`; see [multi-network limitation](#unsupported-features) | verified live for one network; extra networks rejected |
| `DELETE /networks/{id}` | `handlers.networkRemove` → `app.Service.NetworkRemove` | CNI DEL + bridge delete | `DockerNetwork.Remove` | verified live |

### Image inspect `Config`

`GET /images/{name}/json` always renders a non-nil `Config` object because
testcontainers-go dereferences `Config.ExposedPorts` on every container create
whose request names no ports. The daemon resolves the image's OCI configuration
by config digest through the containerd image store and maps:

| `Config` field | Source |
| --- | --- |
| `ExposedPorts` | OCI image config `config.ExposedPorts` (e.g. Dockerfile `EXPOSE 8080/tcp`) |
| `Env`, `Cmd`, `Entrypoint`, `WorkingDir`, `User`, `Volumes`, `Labels`, `StopSignal` | OCI image config `config.*` |
| `Image` | not representable in `moby/api/types/image.InspectResponse.Config` (typed as `DockerOCIImageConfig`); omitted, as testcontainers never reads it |

The typed Go response always has a non-nil `ExposedPorts` set. On the wire an
empty set is omitted by the image-config JSON tags, exactly like Docker's
envelope; a client that ranges over the decoded map is safe either way.

## Container create field policy

The daemon rejects unsupported security and lifecycle semantics with HTTP 501
(`{"message":"..."}`). `501 Not Implemented` is used rather than translating
or silently dropping a request: the client explicitly asked for behavior this
MVP cannot honor, and a successful create would falsely imply that the policy
was applied.

### Rejected create fields

These fields are rejected before provisioning whenever their value requests
non-default behavior:

| Docker field | Exact client error | Reason |
| --- | --- | --- |
| `HostConfig.Privileged=true` | `501 HostConfig.Privileged is not supported` | The daemon cannot provide privileged isolation semantics. |
| `HostConfig.CapAdd` non-empty | `501 HostConfig.CapAdd is not supported` | Linux capability changes are not applied. |
| `HostConfig.CapDrop` non-empty | `501 HostConfig.CapDrop is not supported` | Linux capability changes are not applied. |
| `HostConfig.Devices` non-empty | `501 HostConfig.Devices is not supported` | Device mappings are not applied. |
| `HostConfig.ReadonlyRootfs=true` | `501 HostConfig.ReadonlyRootfs is not supported` | Root filesystem mutability cannot be changed. |
| `HostConfig` resource limits non-zero | `501 HostConfig.Resources is not supported` | CPU, memory, cgroup, device, and ulimit settings are not applied. |
| `HostConfig.RestartPolicy` non-default | `501 HostConfig.RestartPolicy is not supported` | The MVP has no restart supervisor. |
| `HostConfig.SecurityOpt` non-empty | `501 HostConfig.SecurityOpt is not supported` | Security labels and profiles are not applied. |

### Ignored create fields

The following fields are accepted and harmlessly ignored because they do not
change the container semantics implemented by this MVP. They are listed here
so their omission is explicit rather than silent:

| Docker field | Compatibility behavior |
| --- | --- |
| `HostConfig.AutoRemove` | Container cleanup remains controlled by the remove endpoint. |
| `HostConfig.Init` | No init process is injected. |
| `HostConfig.LogConfig` | Logs remain CRI text files. |
| `HostConfig.Binds` entries with malformed syntax | The malformed entry is skipped; valid bind entries are honored. |
| `HostConfig.Tmpfs` | Tmpfs mounts are not created. |
| `HostConfig.PublishAllPorts` | Only explicit `PortBindings` are published. |
| `HostConfig.DNS`, `DNSOptions`, `DNSSearch` | Container DNS overrides are not applied. |
| `HostConfig.ExtraHosts` | Extra host entries are not applied. |
| `HostConfig.GroupAdd` | Supplementary groups are not added. |
| `HostConfig.ShmSize` | The default runtime shared-memory configuration is retained. |
| `NetworkingConfig.EndpointsConfig` after the first requested network | Multi-network attachment is unsupported; one network is selected. |

`/version` and `/info` report `0.0.0-dev` as the intentional development
version string. `/info` reports `LoggingDriver: "cri"` because the daemon
writes CRI-format text log files, not Docker `json-file` records.

## Published ports

The port allocator reserves a concrete, nonzero host port at container create
time and CNI `portmap` installs the DNAT rules at start. The published port is
stable and identical in:

- the create request's HostConfig after allocation,
- `GET /containers/{id}/json` `NetworkSettings.Ports`,
- testcontainers `DockerContainer.MappedPort(ctx, "8080/tcp")`.

The integration matrix asserts the equality of those three values and then
performs a real HTTP request through the mapped port.

The CNI `portmap` plugin DNATs from both `PREROUTING` and `OUTPUT`, so
published ports are reachable on the host's own IPv4 addresses. Connections to
`127.0.0.1` additionally require the kernel to let a `127/8` source cross a
routing boundary (`net.ipv4.conf.<if>.route_localnet=1` on the interface that
routes to the container, which the plugin sets for hairpin/localhost SNAT).
This MVP runs no userland proxy, and in the recorded verification environment
loopback DNAT did not deliver even with `route_localnet` set; the matrix
therefore configures testcontainers with `TESTCONTAINERS_HOST_OVERRIDE` set to
the host address. Use the host address (or add a localhost proxy) when running
testcontainers against this daemon.

## Unsupported features

Everything below is deliberately **not** implemented. The error column is the
exact response a Docker client receives; the JSON envelope is always
`{"message": "..."}`.

| Feature | Endpoint(s) | Exact client error | Why |
| --- | --- | --- | --- |
| CRI runtime | no CRI gRPC service | n/a (no socket) | dockerdless serves the Docker HTTP API only; there is no CRI plugin or CRI endpoint. |
| Rootless mode | daemon startup | startup fails (`connect to containerd`, CNI operations require root) | the MVP is Linux-rootful: it needs root to create network namespaces, CNI bridges, and iptables rules. |
| Ryuk reaper | testcontainers reaper container | container cannot be provisioned (image pull unavailable offline) | Ryuk is intentionally disabled; set `TESTCONTAINERS_RYUK_DISABLED=true` and clean up with `t.Cleanup`/`testcontainers.CleanupContainer`. |
| Registry push | `POST /images/{name}/push` | `404 page not found` | pull-only MVP; no registry writer. |
| Registry login | `POST /auth` | `404 page not found` | registry credentials are accepted per pull/build request, never stored. |
| Swarm | `/swarm`, `/services`, `/tasks`, `/nodes` | `404 page not found` | no swarm manager or node agent. |
| Compose | `/volumes`, `/configs`, `/secrets`, `/events` | `404 page not found` | Compose needs volumes/configs/secrets/events, none of which the MVP serves. |
| Events | `GET /events` | `404 page not found` | no event stream. |
| Multi-network containers | testcontainers `Networks[1:]` → `POST /networks/{id}/connect` | before start: `409 Container <id> is not running`; after start: `500 cni: connecting ... plugin type="bridge" failed (add): container veth name ("eth0") ... already exists` | the CNI adapter attaches a single `eth0` and one default route per container; a second network needs an adapter-level interface-name/route strategy. Use one network per container. |
| Network disconnect | `POST /networks/{id}/disconnect` | `404 page not found` | only create/connect/remove are wired. |
| Container kill/restart/pause/wait | `POST /containers/{id}/kill`, `/restart`, `/pause`, `/wait` | `404 page not found` | stop/remove cover the MVP lifecycle. |
| Container attach/export/archive | `POST /containers/{id}/attach`, `/export`, `/archive` | `404 page not found` | logs and exec cover the MVP I/O surface. |
| Image history/tag/load/save/prune | `GET /images/{name}/history`, `POST /images/{name}/tag`, `/images/load`, `/images/{name}/get`, `/images/prune` | `404 page not found` | inspect/list/build/pull/remove cover the MVP image surface. |
| Exec and container resize | `POST /exec/{id}/resize`, `POST /containers/{id}/resize` | `404 page not found` | the runtime adapter can resize a task, but no route is wired. |
| System df/prune | `GET /system/df`, `POST /containers/prune`, `/images/prune`, `/networks/prune`, `/build/prune` | `404 page not found` | no usage or pruning API. |
| Multi-platform image inspect | `GET /images/{name}/json?manifests=1`, `?platform=` | single-platform projection returned; query ignored | the image store projects the local platform only. |

## Reproduce

```bash
go test -race -count=1 ./...
go vet ./...
go build ./...
go test -tags=integration -count=1 -v ./integration/...
```

The integration suite skips, with an explicit reason, when the host lacks root,
containerd, BuildKit, or the CNI plugins, and the image-pull subtest skips when
containerd has no direct registry egress. It never silently passes.

## Version pins

The verification run recorded above was produced against these pins from
[`go.mod`](../go.mod) (Go `1.27.1`):

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

The daemon's trust boundary, socket permissions, and credential handling are
documented in [security.md](security.md); operational procedures live in
[operations.md](operations.md).

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
| `POST /containers/create` | `handlers.containerCreate` → `app.Service.ContainerCreate` | containerd `CreateContainer` (snapshot + OCI spec), bind/tmpfs mount translation, CNI network resolve, host-port allocator | `DockerProvider.CreateContainer` | verified live |
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

Every field of `container.Config` and `container.HostConfig` (including the
fields promoted from the embedded `container.Resources`) is classified as
**handled**, **rejected**, or **ignored**. Rejected fields fail before
provisioning with a Docker-shaped error: HTTP 501
(`{"message":"<Type>.<Field> is not supported"}`) for semantics the MVP cannot
honor, or HTTP 400 `NewInvalidParameter` for a malformed value. The
classification is enforced by `TestContainerCreateFieldPolicyIsExhaustive`,
which reflects over both moby structs and fails when a field is missing from the
policy map, so a future `github.com/moby/moby/api` bump cannot silently drop a
new create field.

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
| `HostConfig.PidMode` non-empty | `501 HostConfig.PidMode is not supported` | PID namespace selection is not applied. |
| `HostConfig.IpcMode` non-empty | `501 HostConfig.IpcMode is not supported` | IPC namespace selection is not applied. |
| `HostConfig.UTSMode` non-empty | `501 HostConfig.UTSMode is not supported` | UTS namespace selection is not applied. |
| `HostConfig.UsernsMode` non-empty | `501 HostConfig.UsernsMode is not supported` | User namespace selection is not applied. |
| `HostConfig.CgroupnsMode` non-empty | `501 HostConfig.CgroupnsMode is not supported` | Cgroup namespace selection is not applied. |
| `HostConfig.Cgroup` non-empty | `501 HostConfig.Cgroup is not supported` | Joining another container's cgroup is not applied. |
| `HostConfig.Sysctls` non-empty | `501 HostConfig.Sysctls is not supported` | Namespace sysctls are not applied. |
| `HostConfig.MaskedPaths` non-empty | `501 HostConfig.MaskedPaths is not supported` | Masked paths are not applied. |
| `HostConfig.ReadonlyPaths` non-empty | `501 HostConfig.ReadonlyPaths is not supported` | Read-only paths are not applied. |
| `HostConfig.Runtime` other than `io.containerd.runc.v2` | `501 HostConfig.Runtime is not supported` | Alternate OCI runtimes are not selectable. |
| `HostConfig.VolumesFrom` non-empty | `501 HostConfig.VolumesFrom is not supported` | Mount inheritance is not applied. |
| `HostConfig.OomScoreAdj` non-zero | `501 HostConfig.OomScoreAdj is not supported` | OOM score adjustment is not applied. |
| `HostConfig.ContainerIDFile` non-empty | `501 HostConfig.ContainerIDFile is not supported` | Writing the container ID to a file is not implemented. |
| `HostConfig.VolumeDriver` non-empty | `501 HostConfig.VolumeDriver is not supported` | Named volume drivers are not implemented. |
| `HostConfig.Annotations` non-empty | `501 HostConfig.Annotations is not supported` | Runtime annotations beyond labels/mounts are not applied. |
| `HostConfig.Links` non-empty | `501 HostConfig.Links is not supported` | Legacy container links are not implemented. |
| `HostConfig.StorageOpt` non-empty | `501 HostConfig.StorageOpt is not supported` | Storage driver options are not applied. |
| `HostConfig.Umask` set | `501 HostConfig.Umask is not supported` | The initial process umask is not applied. |
| `HostConfig.Mounts` type `volume`, `npipe`, `cluster`, or `image` | `501 HostConfig.Mounts type "<type>" is not supported` | Only bind and tmpfs mounts have an OCI translation. |
| `HostConfig.Mounts` type `bind` with an empty or non-absolute `Source` | `400 HostConfig.Mounts source "<source>" must be an absolute path for a bind mount` | A bind mount without an absolute host path cannot be provisioned. |
| `HostConfig.Mounts` type `tmpfs` with a non-empty `Source` | `400 HostConfig.Mounts source "<source>" must be empty for a tmpfs mount` | tmpfs mounts are anonymous; a source is meaningless. |
| `HostConfig.Binds` entry with an empty source/destination or no `:` separator | `400 invalid bind mount specification: "<entry>"` | Malformed binds are rejected instead of being silently skipped. |
| `Config.Domainname` non-empty | `501 Config.Domainname is not supported` | The container NIS domain name is not set. |
| `Config.ExposedPorts` non-empty without a matching `HostConfig.PortBindings` entry | `501 Config.ExposedPorts is not supported` | Exposure-only ports cannot be represented; an exposed port that is also published is honored through the binding. |
| `Config.Healthcheck` set | `501 Config.Healthcheck is not supported` | No healthcheck supervisor is implemented. |
| `Config.Volumes` non-empty | `501 Config.Volumes is not supported` | Anonymous volume creation is not implemented. |
| `Config.NetworkDisabled=true` | `501 Config.NetworkDisabled is not supported` | Use `HostConfig.NetworkMode="none"` instead. |
| `Config.OnBuild` non-empty | `501 Config.OnBuild is not supported` | ONBUILD triggers are image-build metadata, not container runtime config. |
| `Config.StopSignal` non-empty | `501 Config.StopSignal is not supported` | A per-container stop signal is not applied; stop escalates SIGTERM then SIGKILL. |
| `Config.StopTimeout` set | `501 Config.StopTimeout is not supported` | The daemon uses its configured stop timeout, not a per-container one. |

### Ignored create fields

The following fields are accepted and harmlessly ignored because they do not
change the container semantics implemented by this MVP. This list is complete:
`TestContainerCreateFieldPolicyIsExhaustive` fails if any field of
`container.Config` or `container.HostConfig` is absent from the policy map.

| Docker field | Compatibility behavior |
| --- | --- |
| `Config.AttachStdin`, `Config.AttachStdout`, `Config.AttachStderr` | Attach flags are only meaningful for interactive `attach`, which is not routed; log capture is unconditional. |
| `Config.StdinOnce` | Stdin lifecycle is owned by the runtime task. |
| `Config.ArgsEscaped` | Windows-only command escaping; not applicable on Linux. |
| `Config.Shell` | Windows-only shell form; not applicable on Linux. |
| `HostConfig.AutoRemove` | Container cleanup remains controlled by the remove endpoint. |
| `HostConfig.ConsoleSize` | Console size is applied by exec/TTY resize, not at create. |
| `HostConfig.Dns`, `HostConfig.DnsOptions`, `HostConfig.DnsSearch` | Container DNS overrides are not applied. |
| `HostConfig.ExtraHosts` | Extra host entries are not applied. |
| `HostConfig.GroupAdd` | Supplementary groups are not added. |
| `HostConfig.Init` | No init process is injected. |
| `HostConfig.Isolation` | Windows-only isolation technology; not applicable on Linux. |
| `HostConfig.LogConfig` | Logs remain CRI text files; the driver selection is not applied. |
| `HostConfig.PublishAllPorts` | Only explicit `PortBindings` are published. |
| `HostConfig.ShmSize` | The default runtime shared-memory configuration is retained. |
| `HostConfig.Tmpfs` | Tmpfs mounts supplied through the legacy map are not created; use `HostConfig.Mounts` with `type=tmpfs`. |
| `NetworkingConfig.EndpointsConfig` after the first requested network | Multi-network attachment is unsupported; one network is selected. |

`HostConfig.Mounts` with type `bind` or `tmpfs` is translated into OCI mounts
and reported by inspect; it is supported rather than ignored. The end-to-end
claim is proven by `TestStructuredHostConfigMountAppearsInInspect`, which
creates a container with a structured `HostConfig.Mounts` bind and asserts the
mount appears in `GET /containers/{id}/json`.

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
| Registry push | `POST /images/{name}/push` | `404 {"message":"page not found"}` | pull-only MVP; no registry writer. |
| Registry login | `POST /auth` | `404 {"message":"page not found"}` | registry credentials are accepted per pull/build request, never stored. |
| Swarm | `/swarm`, `/services`, `/tasks`, `/nodes` | `404 {"message":"page not found"}` | no swarm manager or node agent. |
| Compose | `/volumes`, `/configs`, `/secrets`, `/events` | `404 {"message":"page not found"}` | Compose needs volumes/configs/secrets/events, none of which the MVP serves. |
| Events | `GET /events` | `404 {"message":"page not found"}` | no event stream. |
| Multi-network containers | testcontainers `Networks[1:]` → `POST /networks/{id}/connect` | before start: `409 Container <id> is not running`; after start: `500 cni: connecting ... plugin type="bridge" failed (add): container veth name ("eth0") ... already exists` | the CNI adapter attaches a single `eth0` and one default route per container; a second network needs an adapter-level interface-name/route strategy. Use one network per container. |
| Network disconnect | `POST /networks/{id}/disconnect` | `404 {"message":"page not found"}` | only create/connect/remove are wired. |
| Container kill/restart/pause/wait | `POST /containers/{id}/kill`, `/restart`, `/pause`, `/wait` | `404 {"message":"page not found"}` | stop/remove cover the MVP lifecycle. |
| Container attach/export/archive | `POST /containers/{id}/attach`, `/export`, `/archive` | `404 {"message":"page not found"}` | logs and exec cover the MVP I/O surface. |
| Image history/tag/load/save/prune | `GET /images/{name}/history`, `POST /images/{name}/tag`, `/images/load`, `/images/{name}/get`, `/images/prune` | `404 {"message":"page not found"}` | inspect/list/build/pull/remove cover the MVP image surface. |
| Exec and container resize | `POST /exec/{id}/resize`, `POST /containers/{id}/resize` | `404 {"message":"page not found"}` | the runtime adapter can resize a task, but no route is wired. |
| System df/prune | `GET /system/df`, `POST /containers/prune`, `/images/prune`, `/networks/prune`, `/build/prune` | `404 {"message":"page not found"}` | no usage or pruning API. |
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

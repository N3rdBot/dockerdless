# dockerdless deployment layouts

This directory holds ready-to-adapt deployment artifacts. The canonical,
step-by-step procedure is [docs/deployment.md](../docs/deployment.md); treat it
as the source of truth and these files as the concrete config to install.

## `systemd/`

Host deployment for the rootful daemon. This is the recommended layout for
production and for any host where dockerdless should start at boot.

| File | Purpose |
| --- | --- |
| `dockerdless.service` | Hardened systemd unit: foreground `Type=simple`, `SIGTERM` stop with a 15s cap (the daemon's own shutdown bound is 5s), `Restart=on-failure`, `RuntimeDirectory=dockerdless`, and `ProtectSystem=strict` with the two writable paths the daemon needs. |
| `dockerdless.env` | `EnvironmentFile` for the unit: socket path under `/run/dockerdless`, YAML config path, log level, and the containerd namespace note. |
| `dockerdless.tmpfiles` | Optional `systemd-tmpfiles` entry that pre-creates the runtime and config directories at boot. The unit's `RuntimeDirectory=` is still authoritative. |

There is no `dockerdless.sysusers` file on purpose. The daemon must run as root
for network namespaces, CNI bridges, and iptables, and there is no rootless
mode, so no dedicated service account exists to create. Adding one would imply a
privilege model the daemon does not have.

Install commands and the host readiness checklist live in
[docs/deployment.md](../docs/deployment.md#deploy-with-systemd-recommended).

## `docker/`

Container-based deployment for evaluation and CI on a host that already has
containerd, BuildKit, and CNI installed. It lives under `deploy/docker/`.

A dockerdless container is not a normal container. Because the daemon programs
the host network and talks to host backends, the container must:

- share the host network and PID namespaces (`--network host --pid host`),
- mount the containerd socket (`/run/containerd/containerd.sock`),
- mount the BuildKit socket (`/run/buildkit/buildkitd.sock`),
- mount the CNI config and plugin directories (`/etc/cni/net.d`,
  `/opt/cni/bin`), and
- run with root and the privileges those operations require.

Given those constraints, the container layout trades isolation for packaging
convenience. Prefer `systemd/` for production; use `docker/` when you want a
reproducible throwaway host for tests.

## Which to choose

- Production host, boot persistence, journald logs, hardening: **`systemd/`**.
- Quick evaluation, CI image, disposable environment: **`docker/`**.

Both layouts run the same `/usr/local/bin/dockerdless` binary and read the same
`DOCKERDLESS_*` environment variables.

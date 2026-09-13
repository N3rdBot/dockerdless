# dockerdless security

This document states the MVP's trust boundary honestly. dockerdless is a
rootful container control plane; it is not a security sandbox between its
clients and the host.

## Trust boundary

```
Docker client (trusted)
    │  Unix socket, 0660, owned by the daemon uid
    ▼
dockerdless daemon (runs as root)
    │
    ├── containerd socket  (/run/containerd/containerd.sock)
    └── BuildKit socket    (/run/buildkit/buildkitd.sock)
```

- **The API socket is the only network entry point.** The daemon binds no TCP
  port and serves no other interface. Everything that can reach the socket can
  create containers, exec into them, build images, and publish ports.
- **The daemon runs as root.** It needs root to create network namespaces, CNI
  bridges, and iptables rules, and it connects to root-owned containerd and
  BuildKit sockets. Rootless mode is not implemented.
- **Containers are root-equivalent workloads.** A container started through the
  API runs with the privileges its spec requests, under the daemon's rootful
  containerd. Treat the ability to start a container as equivalent to root on
  the host, exactly as with Docker.
- **Images are untrusted input.** Pulling, building, and running an image
  executes its code. Builds run through BuildKit with root on the host; a
  Dockerfile can read anything the build context and BuildKit expose. Do not
  build untrusted Dockerfiles on a host you do not intend to expose.

## Socket permissions

The API socket is the security perimeter, so its lifecycle is enforced:

- The socket is created `0660` and owned by the daemon uid; the group is the
  daemon's primary group. Only the daemon user/group (by default, root only)
  can connect.
- After binding, the daemon re-verifies that the socket is exactly `0660` and
  owned by its own uid. A mismatch aborts startup rather than serving an
  exposed socket.
- A pre-existing path that is not a socket is refused untouched.
- A live socket is refused untouched; the daemon never unlinks a socket another
  process is serving.
- A stale socket owned by a different uid is refused untouched.
- A stale socket with permissions wider than `0660` is tightened to `0660` and
  the repair is logged (`repaired unsafe socket permissions`) before the socket
  is removed and replaced.

There is no socket-group option in the MVP; the effective access list is
"daemon uid + daemon gid". If other users must reach the daemon, run their
client as the daemon user. `stat -c '%a %U:%G' <socket>` should always report
`660` and the daemon uid.

The daemon does not authenticate or authorize individual API requests beyond
Unix socket permissions. Any client that can connect can perform every
supported operation. This matches Docker's own socket trust model.

## Backend socket exposure

The daemon holds two privileged backend connections:

| Backend | Socket | Exposure |
| --- | --- | --- |
| containerd | `DOCKERDLESS_CONTAINERD_SOCKET` (`/run/containerd/containerd.sock`) | Full control of containers, tasks, and images in the configured namespace. |
| BuildKit | `DOCKERDLESS_BUILDKIT_SOCKET` (`/run/buildkit/buildkitd.sock`) | Build execution with root on the host. |

Both sockets are root-owned by default and the daemon never proxies them to API
clients. Keep them root-only: any process that can talk to either socket has
privileges equivalent to (or greater than) the dockerdless API itself.

The daemon does not add new exposure to those sockets; it is a client of both.
If containerd or BuildKit is configured to listen on a TCP address, that
exposure is outside this daemon's control and should be secured separately.

## Credentials

Registry credentials arrive per request in the Docker `X-Registry-Auth` header
(base64url JSON). Handling rules:

- **Nothing is stored.** Credentials are decoded for the single pull/build
  request, converted to the adapter's credential type, and used for that
  operation only. There is no credential file, no config persistence, and no
  `POST /auth` endpoint (`POST /auth` returns `404`, documented in
  [compatibility.md](compatibility.md#unsupported-features)).
- **Nothing is written to disk.** The daemon does not save auth configs;
  containerd and BuildKit receive credentials in memory for the request.
- **Logs and errors redact.** Both the transport credential type
  (`internal/ports.RegistryAuth`) and the adapter type
  (`internal/adapters/buildkit.RegistryAuth`) render only presence flags —
  `password_set`, `identity_token_set`, `registry_token_set` — through
  `String`, `GoString`, and zap's `ObjectMarshaler`. The HTTP access log records
  method, path, status, and duration; it never records request headers or
  bodies. Regression tests assert that a credential-bearing request produces
  neither a leaking response body nor a leaking log record.
- **Credentials in the reference URL are not supported.** Registry login
  (`docker login`) is not implemented; only per-request auth is honored.

Residual risk: the API error envelope renders the service error's message. A
backend error that itself embedded a credential would pass through; the
containerd/BuildKit error paths are translated through the redacting adapter
types, and no such path is known. Keep this invariant when adding error
context.

## Network exposure of published ports

Publishing a port creates host DNAT rules through the CNI `portmap` plugin.
Published ports listen on the host's addresses and are reachable by anything
that can reach the host — the daemon does not add firewall scoping beyond what
CNI `firewall`/`portmap` install. Bind published ports to specific host IPs
where the API supports it, and treat published ports as host-exposed services.

## Hardening checklist

- Keep the daemon uid root-only; do not add a shared socket group without a
  deliberate review of every API capability.
- Keep containerd and BuildKit sockets root-only.
- Pin `DOCKER_API_VERSION=1.44` for clients that do not negotiate; do not expose
  the daemon to untrusted clients.
- Do not build or run untrusted images on a host you would not hand root.
- Rotate registry credentials after any suspected daemon compromise: although
  the daemon does not store them, the backends saw them in memory for the
  request.
- Check daemon logs for `repaired unsafe socket permissions`; a repair means
  something else had widened the socket's mode.

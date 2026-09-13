# syntax=docker/dockerfile:1
#
# dockerdless container image.
#
# This image ships ONLY the dockerdless binary. It is NOT self-contained: the
# daemon is a host-level control plane and reaches out to the host's containerd,
# BuildKit, and CNI plugins. A container running this image must share the host
# network and PID namespaces, run privileged, and bind-mount those host paths.
# See deploy/docker/README.md for the exact host requirements and the Compose
# files in this directory for a working invocation.

ARG GO_VERSION=1.27.1

# --- builder ----------------------------------------------------------------
FROM golang:${GO_VERSION} AS builder

WORKDIR /src

# Copy the module files first so `go mod download` is cached independently of
# source edits.
COPY go.mod go.sum ./
RUN go mod download

# Copy only the packages the daemon binary imports. Integration tests, docs,
# and the git history stay out of the image.
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# CGO_ENABLED=0 yields a fully static binary; dockerdless has no cgo
# dependencies and needs no libc at runtime. -trimpath keeps the build
# reproducible across machines. The daemon version is a source constant
# ("0.0.0-dev" in internal/api/handlers_system.go), so no ldflags are needed.
RUN CGO_ENABLED=0 go build -trimpath -o /out/dockerdless ./cmd/dockerdless

# --- runtime ----------------------------------------------------------------
FROM debian:stable-slim AS runtime

# curl backs the HEALTHCHECK below; ca-certificates is the system trust store
# for the optional OTLP exporter when DOCKERDLESS_OTEL_ENDPOINT is an https://
# URL. Neither is used by the core container/BuildKit/CNI path, which is all
# Unix sockets on the local host.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

LABEL org.opencontainers.image.title="dockerdless" \
      org.opencontainers.image.description="Docker-compatible daemon backed by host containerd, BuildKit, and CNI" \
      org.opencontainers.image.source="https://github.com/N3rdBot/dockerdless" \
      org.opencontainers.image.license="MIT"

COPY --from=builder /out/dockerdless /usr/local/bin/dockerdless

# Default socket path inside the container. Compose bind-mounts /run/dockerdless
# from the host so the same socket inode is reachable by host clients. The
# config layer requires an absolute path (internal/config/config.go).
ENV DOCKERDLESS_SOCKET_PATH=/run/dockerdless/dockerdless.sock

# The daemon MUST run as root. It creates network namespaces, veth pairs, and
# iptables rules, and joins containerd task netns through /proc. There is no
# rootless mode (docs/operations.md, README "Requirements"). Setting the user
# explicitly keeps that requirement visible and immune to a future base-image
# default change.
USER 0:0

# The daemon runs in the foreground and handles SIGTERM itself: no init wrapper,
# no daemonize mode (docs/operations.md#start-and-stop).
ENTRYPOINT ["/usr/local/bin/dockerdless"]

# The API is Docker HTTP over a Unix socket with no TCP listener, so the check
# speaks HTTP over the socket. /_ping is the Docker ping route and needs no
# backend, making it a true liveness probe for the daemon process itself. The
# shell form is required so ${DOCKERDLESS_SOCKET_PATH} expands; debian-slim
# provides /bin/sh.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD curl -fsS --unix-socket "${DOCKERDLESS_SOCKET_PATH}" http://localhost/_ping || exit 1

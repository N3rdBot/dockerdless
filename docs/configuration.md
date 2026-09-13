# dockerdless configuration

`dockerdless` reads its bootstrap settings from a YAML file, from environment
variables, or from both. Every setting has a built-in default, so the daemon
starts with no configuration at all. This document is the canonical reference
for the keys, their precedence, validation, and reload behavior.

For runtime behavior such as the socket lifecycle, reconciliation, and cleanup,
see [docs/operations.md](operations.md). For installation and service
integration, see [docs/deployment.md](deployment.md).

## Table of contents

- [How configuration is loaded](#how-configuration-is-loaded)
  - [Precedence](#precedence)
  - [Worked precedence example](#worked-precedence-example)
- [Keys](#keys)
- [Configuration file](#configuration-file)
  - [Watch semantics](#watch-semantics)
  - [Invalid reload behavior](#invalid-reload-behavior)
- [Hot reload vs restart](#hot-reload-vs-restart)
  - [Worked reload example](#worked-reload-example)
- [Validation rules and error shapes](#validation-rules-and-error-shapes)
- [Samples](#samples)
- [Operational notes](#operational-notes)
  - [containerd namespace alignment](#containerd-namespace-alignment)
  - [OTLP is disabled by default](#otlp-is-disabled-by-default)
- [See also](#see-also)

## How configuration is loaded

Configuration is loaded at two points:

1. **Startup.** `cmd/dockerdless` builds a `config.Store`, which starts from the
   built-in defaults, overlays the YAML file when one is set, then overlays the
   environment. The decoded value is validated before it is published as an
   immutable snapshot. A load or validation failure aborts startup.
2. **Reload.** When `DOCKERDLESS_CONFIG_FILE` is set, a directory watcher
   re-reads the file on change, validates it, and publishes a new snapshot only
   when it is valid.

The configuration file path is **not** a configuration key. It is read only
from the environment variable `DOCKERDLESS_CONFIG_FILE`. The value is trimmed;
an empty or whitespace-only value means no file is read and no watcher runs.
There is no command-line flag for it. The only CLI flag is `-help`.

### Precedence

From lowest to highest priority:

1. Built-in defaults.
2. Values in the YAML file named by `DOCKERDLESS_CONFIG_FILE`.
3. Environment variables with the `DOCKERDLESS_` prefix.

Environment variable names are the key upper-cased with `-` replaced by `_`, so
`socket-path` maps to `DOCKERDLESS_SOCKET_PATH`. YAML keys are kebab-case.

One loader detail: an environment variable whose value is an empty string is
treated as unset. Clear the variable rather than exporting it empty when you
want the file or default value to apply.

### Worked precedence example

Start from the default `socket-path` of `/var/run/dockerdless.sock`. A file sets
`socket-path: /run/dockerdless.sock`, and the environment also sets
`DOCKERDLESS_SOCKET_PATH=/tmp/dockerdless.sock`. The daemon binds
`/tmp/dockerdless.sock`, because environment beats file beats default. If the
environment variable is absent, the file wins and the effective socket is
`/run/dockerdless.sock`. If neither is set, the default applies.

## Keys

Every key is optional. The **On reload** column records what happens to a
running daemon when the file changes: `live` means the setting is applied
immediately; `restart` means the new value is stored in the snapshot but the
running daemon logs `configuration change requires restart` and keeps using the
old value until it is restarted.

| Key (YAML) | Env var | Type | Default | Validation | On reload |
| --- | --- | --- | --- | --- | --- |
| `socket-path` | `DOCKERDLESS_SOCKET_PATH` | string, absolute | `/var/run/dockerdless.sock` | non-empty, absolute | restart |
| `log-level` | `DOCKERDLESS_LOG_LEVEL` | string enum | `info` | one of `debug`, `info`, `warn`, `error`, `dpanic`, `panic`, `fatal`, matched case-insensitively | live |
| `otel-service-name` | `DOCKERDLESS_OTEL_SERVICE_NAME` | string | `dockerdless` | non-empty after trimming | restart |
| `otel-endpoint` | `DOCKERDLESS_OTEL_ENDPOINT` | string, may be empty | empty (OTLP disabled) | no leading or trailing whitespace | restart |
| `containerd-namespace` | `DOCKERDLESS_CONTAINERD_NAMESPACE` | string | `moby` | non-empty after trimming | restart |
| `containerd-socket` | `DOCKERDLESS_CONTAINERD_SOCKET` | string, absolute | `/run/containerd/containerd.sock` | non-empty, absolute | restart |
| `cni-config-dir` | `DOCKERDLESS_CNI_CONFIG_DIR` | string, absolute | `/etc/cni/net.d` | non-empty, absolute | restart |
| `cni-plugin-dir` | `DOCKERDLESS_CNI_PLUGIN_DIR` | string, absolute | `/opt/cni/bin` | non-empty, absolute | restart |
| `buildkit-socket` | `DOCKERDLESS_BUILDKIT_SOCKET` | string, absolute | `/run/buildkit/buildkitd.sock` | non-empty, absolute | restart |
| `default-stop-timeout` | `DOCKERDLESS_DEFAULT_STOP_TIMEOUT` | duration | `10s` | greater than zero | restart |
| `request-timeout` | `DOCKERDLESS_REQUEST_TIMEOUT` | duration | `30s` | greater than zero | restart |
| `enable-cri` | `DOCKERDLESS_ENABLE_CRI` | bool | `false` | none (reserved flag) | restart |
| `enable-rootless` | `DOCKERDLESS_ENABLE_ROOTLESS` | bool | `false` | none (reserved flag) | restart |

Type notes:

- **Absolute path** means the value must begin with `/`. `~` and environment
  variables are not expanded.
- **Duration** is a Go duration string such as `10s`, `500ms`, or `1m30s`.
- **bool** accepts the standard Go boolean strings (`true`, `false`, `1`, `0`,
  and their accepted variants).
- `enable-cri` and `enable-rootless` are reserved forward-compatibility flags.
  CRI is not implemented and rootless mode is not implemented, so neither flag
  changes daemon behavior today.

## Configuration file

The file is YAML with kebab-case keys. A complete file that spells out every
key lives at [`deploy/config/dockerdless.yaml`](../deploy/config/dockerdless.yaml);
the smallest working file is
[`deploy/config/dockerdless.minimal.yaml`](../deploy/config/dockerdless.minimal.yaml).

Set the path before starting the daemon:

```bash
DOCKERDLESS_CONFIG_FILE=/etc/dockerdless/dockerdless.yaml ./bin/dockerdless
```

A relative path is made absolute against the process working directory when the
store is created. Prefer an absolute path in production.

### Watch semantics

When `DOCKERDLESS_CONFIG_FILE` is set, the daemon calls `WatchConfig` after the
first load. The watcher observes the **directory that contains** the config
file, not only the file, so an atomic replace (write a temp file, then rename it
over the target) is observed the same way Viper's own `WatchConfig` observes it.
Write, create, and rename events on the config path trigger a reload, and a
change in the file's symlink target is detected too.

If the directory cannot be watched, startup fails with
`watch configuration: <error>`.

Environment-only configuration is read at startup and is never watched. Only
the file named by `DOCKERDLESS_CONFIG_FILE` participates in reloads.

### Invalid reload behavior

A reload that fails to read, decode, or validate is rejected. The store emits
the error, the daemon logs it as
`configuration reload rejected; retaining previous snapshot`, and the last valid
snapshot stays active. The daemon keeps serving with the previous values. On the
next valid edit, the file is reloaded and a new snapshot is published.

A valid edit is published even when it only touches a restart-only key. In that
case the snapshot changes and the daemon logs the restart warning for each
changed setting.

## Hot reload vs restart

Only `log-level` is applied to a running daemon. Everything else is startup-only:
it is loaded and validated at startup, and a later file change is recorded in
the snapshot but not applied until the daemon restarts.

| Setting | Behavior on a valid reload |
| --- | --- |
| `log-level` | Applied immediately. The logger logs `configuration reloaded` with the new value. |
| `default-stop-timeout` | Logs `configuration change requires restart`. |
| `request-timeout` | Logs `configuration change requires restart`. |
| `socket-path` | Logs `configuration change requires restart`. |
| `otel-service-name` | Logs `configuration change requires restart`. |
| `otel-endpoint` | Logs `configuration change requires restart`. |
| `containerd-namespace` | Logs `configuration change requires restart`. |
| `containerd-socket` | Logs `configuration change requires restart`. |
| `cni-config-dir` | Logs `configuration change requires restart`. |
| `cni-plugin-dir` | Logs `configuration change requires restart`. |
| `buildkit-socket` | Logs `configuration change requires restart`. |
| `enable-cri` | Logs `configuration change requires restart`. |
| `enable-rootless` | Logs `configuration change requires restart`. |

Switching the log level live only changes filtering. The new value goes through
`zapcore.ParseLevel` and the shared atomic level on the logger, so it affects
both the stderr core and the OpenTelemetry log tee without rebuilding the
logger. The seven accepted levels are `debug`, `info`, `warn`, `error`,
`dpanic`, `panic`, and `fatal`; `dpanic`, `panic`, and `fatal` are legal values,
and only `debug` through `fatal` level filtering is meaningful.

### Worked reload example

Start the daemon with a file:

```bash
DOCKERDLESS_CONFIG_FILE=/tmp/dockerdless.yaml ./bin/dockerdless
```

Then edit the file in place.

1. Change `log-level: info` to `log-level: debug`. The daemon logs:

   ```json
   {"level":"info","msg":"configuration reloaded","setting":"log-level","value":"debug"}
   ```

2. Change `request-timeout: 30s` to `request-timeout: 45s`. The daemon logs:

   ```json
   {"level":"warn","msg":"configuration change requires restart","setting":"request-timeout"}
   ```

3. Change `log-level` to `verbose`, which is not in the enum. The reload is
   rejected and the daemon keeps the previous snapshot:

   ```json
   {"level":"error","msg":"configuration reload rejected; retaining previous snapshot","error":"reload configuration after WRITE: validate configuration: log level must be one of debug, info, warn, error, dpanic, panic, or fatal"}
   ```

Restore the valid file and the next event publishes again.

## Validation rules and error shapes

`Config.Validate` runs after decode and rejects a configuration before it is
published. The rules are:

- `socket-path` must not be empty and must be absolute.
- `log-level` must be one of the seven accepted levels, matched
  case-insensitively.
- `otel-service-name` must not be empty after trimming.
- `otel-endpoint` may be empty, but must not have leading or trailing
  whitespace.
- `containerd-namespace` must not be empty after trimming.
- `containerd-socket`, `cni-config-dir`, `cni-plugin-dir`, and `buildkit-socket`
  must be absolute.
- `default-stop-timeout` and `request-timeout` must be greater than zero.

Each rule has a fixed message. Everything is wrapped once more by the caller, as
described below the table.

| Condition | Message |
| --- | --- |
| empty `socket-path` | `socket path must not be empty` |
| relative `socket-path` | `socket path must be absolute: "<value>"` |
| unknown `log-level` | `log level must be one of debug, info, warn, error, dpanic, panic, or fatal` |
| empty `otel-service-name` | `OpenTelemetry service name must not be empty` |
| padded `otel-endpoint` | `OpenTelemetry endpoint must not have leading or trailing whitespace` |
| empty `containerd-namespace` | `containerd namespace must not be empty` |
| empty `containerd-socket` | `containerd socket must not be empty` |
| relative `containerd-socket` | `containerd socket must be absolute: "<value>"` |
| empty `cni-config-dir` | `CNI config directory must not be empty` |
| relative `cni-config-dir` | `CNI config directory must be absolute: "<value>"` |
| empty `cni-plugin-dir` | `CNI plugin directory must not be empty` |
| relative `cni-plugin-dir` | `CNI plugin directory must be absolute: "<value>"` |
| empty `buildkit-socket` | `BuildKit socket must not be empty` |
| relative `buildkit-socket` | `BuildKit socket must be absolute: "<value>"` |
| non-positive `default-stop-timeout` | `default stop timeout must be greater than zero` |
| non-positive `request-timeout` | `request timeout must be greater than zero` |

The error is wrapped at each boundary:

- Decode and validation: `validate configuration: <message>`.
- File read or parse: `read configuration file "<path>": <error>`.
- Startup: `load configuration: <error>`, printed to stderr as
  `dockerdless: <error>` with exit code 1.
- Reload: `reload configuration after <event-op>: <error>`, logged under
  `configuration reload rejected; retaining previous snapshot`.
- Watcher setup: `watch configuration: <error>`.

A startup with a non-positive request timeout therefore prints:

```text
dockerdless: load configuration: validate configuration: request timeout must be greater than zero
```

## Samples

`deploy/config/` ships ready-to-use files.

| File | Contents | Use it when |
| --- | --- | --- |
| [`dockerdless.yaml`](../deploy/config/dockerdless.yaml) | Every key, annotated, with production-sensible values. | You want a documented starting point to trim. |
| [`dockerdless.minimal.yaml`](../deploy/config/dockerdless.minimal.yaml) | Only `socket-path`. | You want the smallest working file for a private test socket. |
| [`dockerdless.testcontainers.yaml`](../deploy/config/dockerdless.testcontainers.yaml) | Private socket, `info` logging, `containerd-namespace: default`. | You run the testcontainers-go workflow. |
| [`dockerdless.env.example`](../deploy/config/dockerdless.env.example) | The full sample as `DOCKERDLESS_*` environment variables. | You configure through the environment, not a file. |
| [`deploy/config/README.md`](../deploy/config/README.md) | Index of every sample. | You want a one-screen overview. |

Run with a sample:

```bash
DOCKERDLESS_CONFIG_FILE=deploy/config/dockerdless.yaml ./bin/dockerdless
```

Source the environment example instead of pointing the daemon at a file:

```bash
. deploy/config/dockerdless.env.example
./bin/dockerdless
```

## Operational notes

### containerd namespace alignment

The default `containerd-namespace` is `moby`, but the local BuildKit worker runs
in `default`. On startup, when the configured value equals the built-in `moby`
default, the daemon switches the effective namespace to `default` and logs
`aligning containerd namespace with the shared BuildKit worker` with
`configured=moby` and `effective=default`. Any other value is used as-is, so
make sure the BuildKit worker uses the same namespace. See
[containerd namespaces](operations.md#containerd-namespaces).

### OTLP is disabled by default

`otel-endpoint` defaults to empty, which disables OpenTelemetry export. With an
empty endpoint the SDK providers are constructed without exporters: no network
work happens and startup is never blocked. Set `otel-endpoint` to a `host:port`,
`http://`, or `https://` value to enable OTLP gRPC; `https://` enables TLS. See
the trust boundary in [docs/security.md](security.md).

## See also

- [docs/operations.md](operations.md): socket lifecycle, reconciliation, log
  locations, troubleshooting, and cleanup.
- [docs/deployment.md](deployment.md): installation and service integration.
- [docs/security.md](security.md): the socket and telemetry trust boundary.
- [README.md](../README.md#configuration-reference): the short reference table.


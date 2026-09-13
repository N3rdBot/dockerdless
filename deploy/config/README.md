# dockerdless configuration samples

Ready-to-use configuration for `dockerdless`. Every key, default, and reload
rule is documented in the canonical reference:
[docs/configuration.md](../../docs/configuration.md).

| File | Format | Contents | When to use |
| --- | --- | --- | --- |
| [`dockerdless.yaml`](dockerdless.yaml) | YAML | Every key, annotated, with production-sensible values. | A documented starting point you trim for a real deployment. |
| [`dockerdless.minimal.yaml`](dockerdless.minimal.yaml) | YAML | Only `socket-path`, pointing at a private test socket. | The smallest working file. |
| [`dockerdless.testcontainers.yaml`](dockerdless.testcontainers.yaml) | YAML | Private socket, `info` logging, `containerd-namespace: default`, plus the client-side testcontainers environment in comments. | Running the testcontainers-go workflow. |
| [`dockerdless.env.example`](dockerdless.env.example) | shell environment | The full sample as `DOCKERDLESS_*` variables, with `DOCKERDLESS_CONFIG_FILE` shown commented out. | You configure through the environment instead of a file. |

## Usage

Point the daemon at a YAML sample:

```bash
DOCKERDLESS_CONFIG_FILE=deploy/config/dockerdless.yaml ./bin/dockerdless
```

Source the environment sample instead:

```bash
. deploy/config/dockerdless.env.example
./bin/dockerdless
```

## Notes

- Precedence is environment > config file > built-in defaults.
- Hot reload applies only to `log-level`. Every other key requires a restart.
- The config file path comes from `DOCKERDLESS_CONFIG_FILE`, not from a YAML key.
- All paths must be absolute and all timeouts must be greater than zero, or the
  daemon refuses to start. See
  [validation rules](../../docs/configuration.md#validation-rules-and-error-shapes).

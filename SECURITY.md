# Security Policy

This is the GitHub security policy for the dockerdless project: how to report a
vulnerability privately and what to expect after you do.

For the **technical** trust boundary (socket permissions, what the daemon trusts,
why root is required, and how images are handled), read
[docs/security.md](docs/security.md). That document describes the security model.
This one describes the reporting process.

## Supported versions

dockerdless is an MVP and does not cut release branches yet. Only the latest
commit on `main` is supported. Security fixes land on `main`; there are no
backports to older tags.

| Version | Supported |
| --- | --- |
| `main` (latest) | Yes |
| Older commits, tags, or forks | No |

## Reporting a vulnerability

**Do not open a public issue, discussion, or PR for a vulnerability.**

Use GitHub's private vulnerability reporting: open the repository's **Security**
tab and choose **Report a vulnerability**. That channel is private between you
and the maintainers. If you cannot use it, email the maintainer address listed
on the [@N3rdBot](https://github.com/N3rdBot) GitHub profile with the subject
line `dockerdless security report`.

Please do not disclose the issue publicly until a fix has shipped or a
coordinated disclosure date has been agreed.

## What to include

The more of this you provide, the faster the triage:

- A clear description of the vulnerability and its impact.
- The exact commit or `go.mod` version you tested.
- Step-by-step reproduction, ideally a minimal script or `curl` against the
  Unix socket.
- Any request or payload that triggers it, including the relevant
  `X-Request-Id` response header so logs can be correlated.
- Whether the issue requires the socket already be reachable, and by whom.
- A suggested fix or mitigation, if you have one.
- Your preferred credit name, if you want acknowledgement.

## Response expectations

This is a small project without a paid on-call rotation, so treat these as
good-faith targets, not guarantees:

- **Acknowledgement** within 3 business days.
- **Initial assessment** (severity and whether it is in scope) within 10 business
  days.
- **Fix or mitigation plan** communicated after triage; the timeline depends on
  severity and complexity.

We will keep you updated as the fix progresses and credit you in the release
notes unless you ask us not to.

## Scope

In scope:

- The daemon at `cmd/dockerdless` and the code under `internal/`.
- The Docker-compatible API surface described in
  [docs/compatibility.md](docs/compatibility.md).
- The default configuration and the socket lifecycle described in
  [docs/operations.md](docs/operations.md) and [docs/security.md](docs/security.md).

Out of scope:

- Behavior that is expected for a rootful container control plane. Anyone who can
  reach the socket can start containers, exec into them, and publish ports. Per
  [docs/security.md](docs/security.md), a client that can reach the socket is
  treated as equivalent to root on the host. That is the documented model, not a
  vulnerability.
- Vulnerabilities in upstream dependencies (containerd, BuildKit, CNI plugins,
  the Moby API/client packages). Report those to their own projects. Tell us if
  dockerdless's use of them makes the impact worse.
- Findings that require an attacker to already have root or write access to the
  daemon's own binary or config.
- Denial of service from a trusted local client, which is inherent to a rootful
  local daemon.
- Missing hardening that Docker itself also lacks, unless the repo claims
  otherwise in its docs.

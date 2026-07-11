# Security Policy

## Threat model

docknap has **full read+write access to the Docker socket** (it starts and stops containers). Anyone who can reach docknap's port (default `8000`) effectively has root-equivalent control over the Docker host. docknap does not sandbox or limit the actions it can take on managed containers.

Treat the docknap listen address as equivalent to shell access on the Docker host. Do not expose it on a public network without a TLS-terminating reverse proxy and admin authentication.

## Supported versions

Only the latest stable GitHub release is supported with security updates.

| Version | Supported |
|---------|-----------|
| Latest stable release  | Yes |
| < latest stable release | No |

## Required hardening

1. **Always run docknap behind a TLS-terminating reverse proxy** (Caddy, nginx, Traefik, etc.). HTTP Basic Auth carries base64-encoded credentials and the session cookie carries an unencrypted opaque bearer token; both require TLS between the client and the reverse proxy.
2. **Set `DOCKNAP_ADMIN_USER` and `DOCKNAP_ADMIN_PASS`** in non-trivial environments. docknap will log a warning at startup if these are unset.
3. **Bind docknap's port to a trusted network only** (e.g. a private Docker network, not `0.0.0.0` on a public host).
4. **Restrict the Docker socket** — use a dedicated low-privilege user account or rootless Docker where the engine supports it.
5. **Rotate `DOCKNAP_ADMIN_PASS` periodically.** Generate with `openssl rand -hex 24`.

## Scanner exceptions

`CVE-2026-34040` is temporarily excluded from image scans until 2026-10-01. Trivy attributes this Docker Engine authorization-plugin bypass to the embedded `github.com/docker/docker` Go client. docknap does not include an Engine daemon, run authorization plugins, or expose the affected daemon request-processing path. Review this exception whenever the Docker SDK changes and remove it if the client becomes affected or a compatible corrected module is published.

All other high and critical findings remain release blockers.

## Reporting a vulnerability

Please open a **private security advisory** on GitHub (Repository → Security → Advisories → "New draft security advisory"). Do **not** open a public issue for suspected vulnerabilities.

We will acknowledge within 3 business days and aim to ship a fix within 30 days for critical issues, 90 days for others.

## CVE history

No CVEs have been issued for docknap to date.

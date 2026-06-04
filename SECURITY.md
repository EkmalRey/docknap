# Security Policy

## Threat model

docknap has **full read+write access to the Docker socket** (it starts and stops containers). Anyone who can reach docknap's port (default `8000`) effectively has root-equivalent control over the Docker host. docknap does not sandbox or limit the actions it can take on managed containers.

Treat the docknap listen address as equivalent to shell access on the Docker host. Do not expose it on a public network without a TLS-terminating reverse proxy and admin authentication.

## Supported versions

| Version | Supported |
|---------|-----------|
| latest  | yes       |
| < latest | no       |

## Required hardening

1. **Always run docknap behind a TLS-terminating reverse proxy** (Caddy, nginx, Traefik, etc.). The session cookie and HTTP Basic Auth both send credentials base64-encoded, not encrypted.
2. **Set `DOCKNAP_ADMIN_USER` and `DOCKNAP_ADMIN_PASS`** in non-trivial environments. docknap will log a warning at startup if these are unset.
3. **Bind docknap's port to a trusted network only** (e.g. a private Docker network, not `0.0.0.0` on a public host).
4. **Restrict the Docker socket** — use a dedicated low-privilege user account or rootless Docker where the engine supports it.
5. **Rotate `DOCKNAP_ADMIN_PASS` periodically.** Generate with `openssl rand -hex 24`.

## Reporting a vulnerability

Please open a **private security advisory** on GitHub (Repository → Security → Advisories → "New draft security advisory"). Do **not** open a public issue for suspected vulnerabilities.

We will acknowledge within 3 business days and aim to ship a fix within 30 days for critical issues, 90 days for others.

## CVE history

No CVEs have been issued for docknap to date.

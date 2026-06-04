# docknap

**A lazy-loading reverse proxy for Docker containers.** Put it in front of your rarely-used services and they stay off until someone actually requests them.

[![build](https://img.shields.io/badge/build-passing-brightgreen)](https://github.com/ekmalrey/docknap/actions)
[![release](https://img.shields.io/github/v/release/ekmalrey/docknap)](https://github.com/ekmalrey/docknap/releases)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![image](https://img.shields.io/badge/image-ghcr.io%2Fekmalrey%2Fdocknap-blue)](https://github.com/ekmalrey/docknap/pkgs/container/docknap)

**Admin UI** &mdash; live status, wake, stop, wake-all, stop-all
![Admin UI](docs/screenshots/admin-ui.png)

**Loading page** &mdash; shown while a container boots
![Loading page](docs/screenshots/loading-page.png)

## How it works

- docknap sits in front of your "sleepable" containers
- Containers opt in via Docker labels
- On request, docknap checks if the container is running, starts it if not, waits for the port, then proxies
- After a configurable idle timeout, docknap stops the container
- While the container boots, docknap serves a customizable loading page that polls until the service is ready
- An admin UI lets you see status, wake, and stop services
- Prometheus metrics are exposed for scraping
- Optional HTTP Basic Auth / session-cookie auth protects admin endpoints
- Docker events drive the watch loop; a 10s poll is the safety net

## Quick start

```bash
# 1. Create the network (once)
docker network create docknap_network

# 2. Run docknap
docker run -d --name docknap \
  -p 8000:8000 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --network docknap_network \
  ghcr.io/ekmalrey/docknap:latest
```

Add labels to any container you want to be lazy-loaded, attach it to `docknap_network`, and start it. Now hitting `http://<container-ip>:port` (via a reverse proxy) will trigger a start.

## Container labels

| Label | Required | Default | Description |
|-------|----------|---------|-------------|
| `docknap.enable` | yes | — | Must be `true` |
| `docknap.subdomain` | yes | — | Subdomain to route on (e.g. `myapp` for `myapp.internal`) |
| `docknap.target_port` | yes | — | Port inside the container to proxy to |
| `docknap.idle_timeout` | no | `5m` | How long to wait before stopping the container after the last request |
| `docknap.startup_timeout` | no | `60s` | Max time to wait for the container to become ready |
| `docknap.health_path` | no | — | If set, docknap HTTP-GETs `<ip>:<port><health_path>` (1s timeout, 2xx-3xx = ready) instead of plain TCP dial. Useful for apps that bind the port before they're actually serving. |
| `docknap.title` | no | subdomain | Display name on the loading page |
| `docknap.subtitle` | no | `service is starting up` | Subtitle on the loading page |
| `docknap.icon` | no | `◐` | Icon shown next to the title |
| `docknap.theme` | no | `green` | Color theme: `green`, `blue`, `amber`, `red`, `purple` |
| `docknap.show_logs` | no | `true` | Show the (cosmetic) staged boot log on the loading page |
| `docknap.show_stats` | no | `true` | Show elapsed time and timeout footer |
| `docknap.boot_messages` | no | five fixed lines | Pipe-separated boot messages shown in the loading-page log (cosmetic). |
| `docknap.live_logs` | no | `false` | If `true`, `/_docknap/logs/<sub>` exposes a live SSE stream of the container's stdout+stderr. Off by default — opt in per service. |

## Environment variables

| Var | Default | Description |
|-----|---------|-------------|
| `DOCKNAP_LISTEN` | `:8000` | Address to listen on |
| `DOCKNAP_IDLE_DEFAULT` | `5m` | Default idle timeout |
| `DOCKNAP_START_TIMEOUT` | `60s` | Default startup timeout (overridden by `docknap.startup_timeout` label) |
| `DOCKNAP_WRITE_TIMEOUT` | `60s` | Maximum time the proxy will spend writing a response. Set to `0` to disable (needed for SSE / long polling). |
| `DOCKNAP_NETWORK` | `docknap_network` | Docker network used to resolve container IPs |
| `DOCKNAP_TLD_COUNT` | `1` | Number of trailing labels to treat as TLD when extracting the subdomain. `1` → `myapp.example.com` resolves to `myapp.example`. `2` → `myapp`. |
| `DOCKNAP_LOG_FORMAT` | `text` | `text` or `json` |
| `DOCKNAP_TRUSTED_PROXIES` | (unset) | Comma-separated CIDRs allowed to set `X-Forwarded-Proto` (e.g. `10.0.0.0/8,127.0.0.1/32`). When unset, the header is ignored entirely. |
| `DOCKNAP_ADMIN_HOST` | (unset) | If set, the admin UI is served at the root of this hostname (e.g. `https://docknap.internal/`). Other hostnames are unaffected. |
| `DOCKNAP_ADMIN_USER` | (unset) | If set with `DOCKNAP_ADMIN_PASS`, requires auth for all `/_docknap/*` endpoints except `/_docknap/wait/` (used by the loading page) |
| `DOCKNAP_ADMIN_PASS` | (unset) | Password for admin auth (must be set with `DOCKNAP_ADMIN_USER`) |

## Network setup

docknap needs to reach the managed containers. They must share a Docker network. Create an external network and attach all docknap-managed containers to it:

```bash
docker network create docknap_network
```

Then in each managed container's compose file:

```yaml
services:
  myapp:
    labels:
      - "docknap.enable=true"
      - "docknap.subdomain=myapp"
      - "docknap.target_port=8080"
      - "docknap.idle_timeout=15m"
      - "docknap.startup_timeout=90s"
      - "docknap.health_path=/healthz"
      - "docknap.title=My App"
      - "docknap.subtitle=private service"
      - "docknap.icon=⚡"
      - "docknap.theme=blue"
      - "docknap.show_logs=true"
      - "docknap.show_stats=true"
      - "docknap.boot_messages=booting kernel...|mounting volumes...|starting workers..."
    networks:
      - docknap_network

networks:
  docknap_network:
    external: true
```

docknap itself also joins the `docknap_network` network and mounts the Docker socket:

```yaml
services:
  docknap:
    image: ghcr.io/ekmalrey/docknap:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - DOCKNAP_NETWORK=docknap_network
      - DOCKNAP_ADMIN_USER=admin
      - DOCKNAP_ADMIN_PASS=${DOCKNAP_ADMIN_PASS}
    networks:
      - docknap_network

networks:
  docknap_network:
    external: true
```

docknap needs read+write access to the Docker socket (it starts and stops containers).

## Routing

Point a reverse proxy (e.g. Caddy) at docknap, preserving the `Host` header:

```caddy
docknap.internal *.internal {
    tls internal

    @adguard host adguard.internal
    handle @adguard {
        reverse_proxy host.docker.internal:3080
    }

    handle {
        reverse_proxy docknap:8000 {
            header_up Host {host}
        }
    }
}
```

docknap reads the subdomain from `Host: myapp.internal` and looks up `myapp` in its registry. With `DOCKNAP_TLD_COUNT=2`, `myapp.staging.internal` resolves to `myapp.staging` (dropping the rightmost `N` labels).

If `DOCKNAP_ADMIN_HOST=docknap.internal` is set, the same host also serves the admin UI at the root (`https://docknap.internal/`) — so you can bookmark it as the dashboard entry point. Add the host to your Caddy site block to cover it with the same wildcard cert.

## Loading page

While a container is booting, docknap serves a self-contained HTML page that:

- Polls `/_docknap/wait/<subdomain>` every second (no full-page refresh)
- Shows a progress bar, elapsed time, and a stylized boot sequence
- Auto-reloads once the service is ready
- Shows a "startup timed out" panel with a Retry button if the timeout is hit
- Can show a live stdout/stderr stream instead of cosmetic messages when the container has `docknap.live_logs=true` (see `/_docknap/logs/<subdomain>` directly)

The cosmetic boot log is a fixed set of staged messages, not a live tail of the container's stdout. The page does not expose container names, ports, hostnames, or any internal state.

## Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /` | Proxy (used by reverse proxy) |
| `GET /_docknap`, `GET /_docknap/`, `GET /_docknap/ui` | Admin UI (HTML) |
| `GET /_docknap/auth/login` | Themed login form (no auth required) |
| `POST /_docknap/auth/login` | Submit credentials; sets `docknap_auth` cookie and redirects to `next` (or `/_docknap/`) |
| `POST /_docknap/auth/logout` | Clear the session cookie and redirect to the login page |
| `GET /_docknap/status` | JSON list of registered services and their state |
| `GET /_docknap/config` | JSON snapshot of the parsed config for every registered service (no secrets) |
| `GET /_docknap/wait/<subdomain>` | JSON readiness probe used by the loading page (`{"ready","timed_out","elapsed"}`) |
| `GET /_docknap/logs/<subdomain>` | Server-Sent Events stream of the container's stdout/stderr (only when `docknap.live_logs=true`) |
| `POST /_docknap/wake/<subdomain>` | Manually wake a service without proxying |
| `POST /_docknap/wake_all` | Wake every stopped service |
| `POST /_docknap/stop/<subdomain>` | Manually stop a service |
| `POST /_docknap/stop_all` | Stop every running service |
| `GET /_docknap/metrics` | Prometheus metrics for all services (text format) |
| `GET /_docknap/metrics/<subdomain>` | Prometheus metrics filtered to one service |
| `GET /_docknap/history/<subdomain>` | JSON with current state, event counts, last 100 events, and startup-duration stats |
| `GET /healthz` | Liveness probe (always 200 OK, no auth) |

If `DOCKNAP_ADMIN_HOST` is set, the admin UI is also served at the root of that host (e.g. `https://docknap.internal/`). On other hostnames, the root path is the normal proxy.

The admin UI shows a live table of all registered services with state, uptime, and Wake/Stop buttons. It auto-refreshes every 5 seconds. Header has `wake all` and `stop all` bulk-action buttons and a `logout` button (when auth is enabled).

## Per-service history

`GET /_docknap/history/<subdomain>` returns a JSON object with:

- `subdomain`, `container`, `target_port`, `state`
- `docknap_tracks_started_at` and `uptime_s` (when docknap itself started the container)
- `docker_started_at` (from Docker's state)
- `event_counts` — counters by event type
- `events` — ring buffer of the last 100 events
- `startup_duration_s` — `{count, avg_s, sum_s}` from the startup-time histogram for this service

Event types: `start_requested`, `ready`, `idle_stop`, `stopped` (with `reason: manual|idle|manual_all`), `start_error`, `startup_timeout`, `disappeared`. Each event has a timestamp, type, message, and optional structured fields.

## Admin auth

The admin UI and management endpoints can be locked down with auth. Set both env vars:

```yaml
environment:
  - DOCKNAP_ADMIN_USER=admin
  - DOCKNAP_ADMIN_PASS=your-secret-here
```

When enabled, all `/_docknap/*` endpoints require auth **except `/_docknap/wait/`** (the loading page polls it from the browser) and `/_docknap/auth/*` (the login form itself). docknap serves a themed in-app login page at `/_docknap/auth/login` instead of triggering the browser's native `Authentication Required` dialog. Successful logins set a `docknap_auth` session cookie (256-bit random token, `HttpOnly`, `SameSite=Lax`, `Secure` when the request is HTTPS via `DOCKNAP_TRUSTED_PROXIES` or `r.TLS != nil`, 12 h) that authenticates subsequent requests. Scripts and curl with `-u user:pass` still work via the standard `Authorization: Basic` header. Failed attempts increment `docknap_admin_auth_failures_total`. Login POSTs are rate-limited to 5 per IP per minute (`reason=rate_limited` on overflow).

Passwords are stored as SHA-256 hashes in memory and compared with constant-time equality. The session cookie contains only an opaque token, not the password.

> **Security:** HTTP Basic Auth and the login-cookie flow both send credentials base64-encoded over the wire, not encrypted. **Always run docknap behind a TLS-terminating reverse proxy.** docknap will log a warning at startup if no admin credentials are configured.

## Security

docknap has full read+write access to the Docker socket (it starts and stops containers). Anyone who can reach the docknap port effectively has root-equivalent control over the Docker host. Treat the port as equivalent to shell access.

Recommendations:

- Always run docknap behind a TLS-terminating reverse proxy (Caddy, nginx, Traefik).
- Set `DOCKNAP_ADMIN_USER` and `DOCKNAP_ADMIN_PASS` if the port is exposed beyond localhost. docknap will emit a warning at startup if these are unset.
- Bind docknap's port to a trusted network only (e.g. a private Docker network, not `0.0.0.0` on a public host).
- Set `DOCKNAP_TRUSTED_PROXIES` to the CIDR of the TLS-terminating reverse proxy so docknap trusts `X-Forwarded-Proto` only from it.
- Use a dedicated, low-privilege user account for the Docker socket where the engine supports it.
- Rotate `DOCKNAP_ADMIN_PASS` periodically. Generate with `openssl rand -hex 24`.

See [SECURITY.md](SECURITY.md) for the disclosure process.

## Logging

docknap logs structured events with a level, message, and key/value fields.

Text format (default):

```
2026-06-01T12:00:00.000Z [info] container ready {subdomain=openwebui, container=open-webui, elapsed_ms=10500}
```

JSON format (set `DOCKNAP_LOG_FORMAT=json`):

```json
{"level":"info","subdomain":"openwebui","container":"open-webui","elapsed_ms":10500,"msg":"container ready","ts":"2026-06-01T12:00:00.000Z"}
```

Events include: container start/stop, idle timeouts, startup timeouts, proxy errors, watch events, registration, login attempts (`method=session_cookie` or `method=basic`).

## Metrics

docknap exposes Prometheus metrics at `/_docknap/metrics`:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `docknap_proxy_requests_total` | counter | `subdomain`, `status` | Proxied requests by HTTP status |
| `docknap_proxy_duration_seconds` | histogram | `subdomain` | Request duration through docknap |
| `docknap_container_starts_total` | counter | `subdomain` | Container starts triggered by docknap |
| `docknap_container_stops_total` | counter | `subdomain`, `reason` | Container stops |
| `docknap_idle_timeouts_total` | counter | `subdomain` | Idle timeouts that stopped a container |
| `docknap_startup_failures_total` | counter | `subdomain`, `reason` | Startup failures (`startup_timeout`, `start_error`, `rate_limited`) |
| `docknap_start_duration_seconds` | histogram | `subdomain` | Time from wake to ready port |
| `docknap_container_state` | gauge | `subdomain`, `state` | Current container state (1 for active state) |
| `docknap_registered_containers` | gauge | — | Number of registered containers |
| `docknap_admin_auth_failures_total` | counter | `path`, `reason` | Admin auth failures (`missing`, `malformed`, `invalid`, `rate_limited`) |

## Development

```bash
make test       # go test -race ./...
make cover      # coverage report
make build      # go build with version from git describe
make docker     # docker build with VERSION build-arg
make integration # full wake/stop/idle cycle against nginx:alpine
```

## Install

### Docker (recommended)

```bash
docker pull ghcr.io/ekmalrey/docknap:latest
```

### From source

```bash
go install github.com/ekmalrey/docknap@latest
```

## Build & run locally

```bash
git clone https://github.com/ekmalrey/docknap
cd docknap
docker build -t docknap .
docker run -d --name docknap -p 8000:8000 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --network docknap_network docknap
```

## Examples

See [`examples/nginx/`](examples/nginx/) for a minimal demo.

## Screenshots

See the screenshots at the top of this README.

## License

MIT

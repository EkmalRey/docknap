# Changelog

All notable changes to docknap will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-06-04

### Added
- **CSRF protection** for all state-changing admin endpoints (`/_docknap/wake/`, `/_docknap/stop/`, `/_docknap/wake_all`, `/_docknap/stop_all`, `/_docknap/auth/logout`). A `docknap_csrf` cookie (`HttpOnly=false`) is set at login in addition to the session cookie. The admin UI sends the token as an `X-CSRF-Token` header on POSTs and the logout form embeds it as a hidden field. The check is skipped for `Authorization: Basic` requests (since the header is already an effective CSRF defense).
- **`docknap.disable_idle` label** — when `true`, docknap never auto-stops the container on idle. Useful for long-running services you want to keep up.
- **`docknap.strategy=pause` label** — when set, docknap calls `ContainerPause` / `ContainerUnpause` instead of `ContainerStop` / `ContainerStart`. The container stays on the network, so `docknap.health_path` is required (a plain TCP dial would falsely report "ready" against a frozen cgroup). Wakes become sub-second.
- **Webhooks** via `DOCKNAP_WEBHOOK_URL` and `DOCKNAP_WEBHOOK_EVENTS`. Lifecycle events (`start_requested`, `ready`, `idle_stop`, `stopped`, `paused`, `start_error`, `startup_timeout`, `disappeared`) are queued and POSTed to the configured URL as JSON. Best-effort, with a 3s per-request timeout. Filter with `DOCKNAP_WEBHOOK_EVENTS=ready,stopped`.
- **`/_docknap/version`** — JSON `{"version", "go_version"}` for ops/CI checks. No auth.
- **`/_docknap/readyz`** — readiness probe. 200 when the docker events stream is healthy, 503 when the polling fallback is in use. Use for k8s/Compose `readinessProbe`.
- **`/_docknap/debug/pprof/`** — Go pprof endpoints (heap, goroutine, allocs, profile, trace, ...). Auth-gated. Use for production debugging.
- **`/healthz` start-period** bumped from 5s to 10s to match the cold-start discover pass.

### Changed
- **`getContainerIP` no longer falls through to other networks.** Previously, if a container wasn't attached to `DOCKNAP_NETWORK` it would silently return the IP of a different network. Now it returns an explicit error, so a misconfigured container surfaces as "service unavailable" instead of being routed to the wrong IP.
- **HTTP health probe (`docknap.health_path`) does not follow redirects.** A slow redirect chain can no longer blow the 1s readiness budget.
- **`refreshStateGauges` is a pure read** of the watch-loop's state cache. Prometheus scrapes no longer trigger per-service `ContainerInspect` calls.
- **Login rate limiter keys on the real client IP** when behind a trusted proxy (reads `X-Forwarded-For`). Without a trusted proxy, the header is ignored to prevent spoofing.
- **Image now runs as a non-root user** (`docknap` uid). The `docknap` user has no shell and no home.
- **`handleWakeAll` and `handleStopAll` use bounded concurrency** (8 in flight) and `s.rootCtx` (no per-call 5s timeout) so a slow start doesn't fail the whole bulk action.
- **Test helpers and `golangci-lint`** added to CI.
- **Removed dead code:** `netSplitHostPort`, `requestIsHTTPSRaw`, `_ = e` in `subscribeDockerEvents`, `var _ = fmt.Sprintf` in main.

### Fixed
- **`docknap_container_stops_total` is no longer double-counted** for manual and `manual_all` stops. The endpoint handler used to increment the counter and then call `stopContainerWithReason`, which also incremented. Idle stops were unaffected.
- **`splitNonEmpty` no longer allocates per rune.** Uses `strings.Split` + filter.

## [0.2.0] - 2026-06-04

### Changed
- **HTTP health probe** via `docknap.health_path` label. If set, docknap issues a 1-second HTTP GET to `<ip>:<port><health_path>` and considers the service ready only on a 2xx/3xx response. Default behavior (plain TCP dial) is unchanged. Lets apps that bind a port before they're actually serving (e.g. Python, JVM) finish initializing before the proxy forwards traffic.
- **Write timeout is now bounded.** A new `DOCKNAP_WRITE_TIMEOUT` env var (default `60s`) replaces the previous `WriteTimeout: 0`. Slow clients can no longer hold proxy connections open indefinitely and exhaust file descriptors. Set `DOCKNAP_WRITE_TIMEOUT=0` to restore unbounded writes (e.g. for SSE / long polling).
- **Watch loop now subscribes to Docker events** (`start`, `die`, `stop`, `destroy`, `create`, `rename`) for the configured network. Changes are debounced at 500 ms and a full resync runs every 10 s as a safety net. The pure-poll watcher is preserved as a fallback if the events stream errors out. Label changes on running containers are picked up immediately rather than waiting for a restart.
- **Wake endpoint is POST-only** (`GET /_docknap/wake/<sub>` now returns 405). Stops browser pre-fetch, link previews, and access logs from inadvertently waking stopped services.
- **Per-service state is cached** in `s.states` (running/exited/created/etc.) and updated by the watch loop. `handleStatus` and the `docknap_container_state` gauge no longer re-inspect every container on each call. The `docknap_container_state` gauge is now refreshed only when state actually changes; metrics scrapes read the cached value.
- **Container IP is cached** for 30 s in `s.ipCache`. Eliminates the per-request `ContainerInspect` call from the proxy hot path on a busy service.
- **`handleProxy` inspects the port under the startLock** so that a stale `Running` reading (from a container that just exited on idle) cannot cause the proxy to skip the loading page and then `getTargetURL` to fail. A second `checkPort` after the lock is acquired catches the race.
- **Subdomain extraction** now takes a `tldCount` argument: with `tldCount=N`, the subdomain is everything except the rightmost `N` dot-separated parts. This enables multi-level subdomains like `myapp.staging.internal → myapp.staging` (with `DOCKNAP_TLD_COUNT=2`). Default behavior with `DOCKNAP_TLD_COUNT=1` is "everything before the rightmost label". This is a small semantic change from v0.1.x, which always returned the first part.
- **Loading-page boot messages are configurable** via `docknap.boot_messages` (pipe-separated). When unset, docknap uses the original five fixed lines.
- **HTML templates moved to `templates/*.html`** and parsed with `html/template` at startup via `embed.FS`. The fragile `{PLACEHOLDER}` `strings.NewReplacer` pattern is gone; templates use proper Go template syntax with auto-escaping.
- **Code is split into single-responsibility files** (`config.go`, `registry.go`, `docker.go`, `proxy.go`, `timers.go`, `templates.go`, `sessions.go`, `ratelimit.go`, `cidr.go`, `logs.go`, `handlers_*.go`). `main.go` is now ~190 lines (just startup, signal handling, shutdown).

### Added
- **`/_docknap/config`** — JSON snapshot of the current parsed configuration for every registered service (no secrets, no runtime state). Use for gitops, diffing, and dashboards. Requires admin auth.
- **`/_docknap/wake_all`** and **`/_docknap/stop_all`** — POST endpoints that wake every stopped service or stop every running service. Surfaced in the admin UI as buttons. Requires admin auth.
- **`docknap.live_logs=true`** label — when set, `/_docknap/logs/<sub>` becomes a Server-Sent Events stream of the container's stdout+stderr. Useful for diagnosing startup failures without `docker logs`. Off by default (opt-in per-service).
- **`DOCKNAP_WRITE_TIMEOUT`** env var (default `60s`).
- **`DOCKNAP_TLD_COUNT`** env var (default `1`).
- **`DOCKNAP_TRUSTED_PROXIES`** env var — comma-separated CIDRs that are allowed to set `X-Forwarded-Proto`. When unset, docknap ignores the header entirely. When set, only requests from the listed CIDRs can trigger HTTPS-only cookie behavior.
- **Per-IP login rate limiter** — 5 failed POSTs per IP per minute before the login form returns `error=rate_limited`. Counter resets when the window passes. Logged and counted in `docknap_admin_auth_failures_total{reason="rate_limited"}`.
- **Opaque session tokens** — the `docknap_auth` cookie value is now a 256-bit random token, not `base64(user:password)`. Tokens are stored in an in-memory map keyed by token with a 12-hour TTL, revoked on logout, and GC'd every 5 minutes. The old `Authorization: Basic` flow is still supported for scripts/curl.
- **Broadcast-on-ready** — when the inner port-ready goroutine observes a port is open, it broadcasts on a per-subdomain channel. All concurrent waiters on `/_docknap/wait/<sub>` (loading page, wake endpoints) wake from the same event instead of each running their own ticker.
- **`startup_stats` field in `/_docknap/history/<sub>`** — count / sum / avg of `docknap_start_duration_seconds` observations for the service, derived from the existing histogram.
- **`/healthz`** is unchanged but explicitly documented to not require auth.
- **Logging includes which auth path was used** — `method=session_cookie` on successful logins; `method=basic` is implied by Authorization-header successes. Makes audit trails unambiguous.
- **Logging shows version** at startup and on the admin UI footer (`v{VERSION}`).
- **`SECURITY.md`** with threat model, supported-versions table, and disclosure process.
- **`Makefile`** with `make build`, `make test`, `make cover`, `make docker`, `make integration`. `VERSION` is set from `git describe --tags --always --dirty`.
- **Integration test harness** at `tests/integration/run.sh` + `docker-compose.yml` that exercises the wake / stop / idle cycle against a real `nginx:alpine` container. Run with `make integration` (requires Docker, `curl`, `jq`).
- **CI coverage step** — `go test -coverprofile=coverage.txt -covermode=atomic` runs on every push; the artifact is uploaded for inspection. (Not gated on a threshold; that comes once we have one.)
- **GitHub release workflow** now passes `VERSION` as a Docker build-arg so the `main.version` variable baked into the binary matches the tag.

### Fixed
- **`proxy.ErrorHandler` no longer corrupts partial responses** when the upstream disconnects mid-stream. The handler now checks `statusRecorder.headersSent()` and aborts the connection instead of writing a full HTML page on top of partial body bytes.
- **Shutdown iterates idle timers under a read lock**, closing a `go test -race`-detectable race against the watch goroutine's last write. The `watch()` goroutine is now driven by `rootCtx.Done()` only and exits before timers are stopped.
- **Background start-ready goroutine is bound to `rootCtx`** (not `context.Background()`), so a `SIGTERM` mid-start cancels the ticker without leaking.
- **`requestIsHTTPS` honors `DOCKNAP_TRUSTED_PROXIES`** before trusting `X-Forwarded-Proto`. Untrusted clients can no longer trick docknap into setting `Secure` on a cookie delivered over plain HTTP.
- **`handleStatus` no longer crashes on a missing container** — falls back to `state="unknown"` instead of returning 500.
- **`handleWait` and `handleProxy` no longer race on `bootStarts`/`startedAt`** — both functions take/release the relevant map mutations under `s.mu`.
- **`discover` records `container.StartedAt`** for already-running containers, so `uptime_s` and `started_at` are populated from the very first request.

### Security
- **Session cookie no longer contains the password.** See "Opaque session tokens" above.
- **Per-IP login rate limiting** to slow credential stuffing.
- **Trusted-proxy CIDR list** to avoid `X-Forwarded-Proto` spoofing.

## [0.1.5] - 2026-06-04

### Changed
- Replaced the browser's default HTTP Basic Auth dialog on admin endpoints with a themed in-app login page at `/_docknap/auth/login`. The page matches the existing docknap visual language (monospace, dark green/cyan, scanline overlay, blinking cursor) and exposes a `user@docknap` / `password` form that POSTs to the same endpoint. The browser will no longer pop the native `Authentication Required` dialog. All `/_docknap/*` endpoints still require auth except `/_docknap/wait/`.
- A successful login now sets a `docknap_auth` session cookie (`HttpOnly`, `SameSite=Lax`, `Secure` when the request is HTTPS — `r.TLS != nil` or `X-Forwarded-Proto: https`, `Max-Age=12h`). Subsequent admin requests are accepted with the cookie or, as before, with an `Authorization: Basic` header — so curl/scripts that use `-u user:pass` keep working unchanged.
- The admin UI header now has a `logout` button that POSTs to `/_docknap/auth/logout` and clears the cookie. Without it the cookie would persist for 12h since the browser has no UI to clear an HTTP-Basic-style session.
- `?next=/path` is preserved across the login flow so a user hitting `/_docknap/status` directly is bounced through login and back. `next` is restricted to relative same-origin paths to prevent open redirects.

### Tests
- New `auth_test.go` covering: `parseBasicAuth` (valid / wrong prefix / bad base64 / no colon), `safeRedirect` (open-redirect vectors), `verifyCredentials`, `checkRequestAuth` (header vs cookie, both, neither), `requireAuth` (disabled / valid header / no creds -> themed login / bad header -> login with error), `handleLogin` (GET unauthenticated / GET already-authenticated redirect / error query / bad method / POST valid / POST invalid / POST missing fields / POST open-redirect sanitization), `handleLogout` (POST clears cookie / GET rejected), and `requestIsHTTPS` (X-Forwarded-Proto).

## [0.1.4] - 2026-06-02

### Fixed
- `/_docknap/status` now returns services in stable alphabetical order by subdomain. Previously Go's randomized map iteration produced a different ordering on every call, which made the admin UI rows jump around on each 2s refresh.

### Changed
- Admin UI auto-refresh interval: 2s → 5s. Status changes (start/stop/idle-timeout) are infrequent in a homelab; 5s halves the polling load without losing visibility.

## [0.1.3] - 2026-06-01

### Fixed
- `discover()` and `watch()` now arm an idle timer for containers that are already running at the time docknap notices them. Previously, if a container was running when docknap started (or when the watch loop first saw it), the container was tracked in `startedAt` but no idle timer was ever set up — the container would never stop on idle. Found in the wild: openwebui and glance running for 16+ minutes past their 10-minute idle timeout. New helper `armIdleTimer()` sets the timer only if none exists (idempotent). `resetIdleTimer()` (used by the proxy hot path) is unchanged.

### Changed
- `http.Server` now sets `ReadHeaderTimeout: 10s` and `MaxHeaderBytes: 1 MiB` to mitigate slowloris / oversized-header DoS. `WriteTimeout: 0` is preserved for streaming proxy responses.

### Tests
- `TestArmIdleTimerCreatesTimer` and `TestArmIdleTimerIsIdempotent` in `armIdleTimer_test.go`.

## [0.1.2] - 2026-06-01

### Fixed
- `handleWait` now reports a sensible `elapsed` when the container was already running before the first poll. Previously, if the inner start-goroutine had cleared `bootStarts[sub]` (port opened fast), `elapsed` was computed from the zero `time.Time`, producing a value of "thousands of years" in the JSON response. Now falls back to `startedAt[sub]` when `bootStarts[sub]` is missing. The `timed_out` flag was unaffected (already gated on `!portOpen`).

## [0.1.1] - 2026-06-01

### Added
- `DOCKNAP_ADMIN_HOST` env var: when set, the admin UI is served at the root of that host (e.g. `https://docknap.internal/`). Other hostnames continue to behave as proxies.
- `HEALTHCHECK` in the Docker image (polls `/_docknap/status`).
- Resource limits in `docker-compose.example.yml` (128 MiB / 0.5 CPU / 256 PIDs).
- Security section in the README; startup warning when admin auth is disabled.
- Graceful shutdown on `SIGINT`/`SIGTERM` (10s drain, then `srv.Shutdown`; idle timers stopped; watch goroutine cancelled).
- Container-start singleflight (per-container mutex) to eliminate the duplicate-start race between `handleProxy` and the loading-page poll.
- ContainerList filter by `DOCKNAP_NETWORK` instead of unfiltered `All: true` scan of the host.
- `extractSubdomain` returns empty for IP-based hosts (no more first-octet "subdomain" match).
- Tests for `statusRecorder` (default 200 on bare `Write`, no double-WriteHeader regression).
- Tests run with `-race` in CI.

### Changed
- `Dockerfile`: bumped `alpine` 3.20 → 3.22, added `wget` for the healthcheck.
- `watch()` now respects context cancellation and cleans up the per-container start lock on disappear.
- `stopContainerWithReason` uses a cancellable context derived from the root context (no more `context.Background()`).
- `statusRecorder.Write` now explicitly sets `status=200` when called before `WriteHeader` (was correct by accident before).
- README: loading-page boot log is now described as cosmetic (not a live stdout tail).
- README: admin path docs now match the actual routes (`/_docknap`, `/_docknap/`, `/_docknap/ui`); the `/` path only works on the admin host.

### Fixed
- `log.Fatal` on `ListenAndServe` no longer prevents in-flight requests from draining on shutdown.
- `Watch()` goroutine leak on process exit.

## [0.1.0] - 2026-06-01

### Added
- Lazy-loading reverse proxy for Docker containers
- Container opt-in via `docknap.*` labels (`enable`, `subdomain`, `target_port`, `idle_timeout`, `startup_timeout`, `title`, `subtitle`, `icon`, `theme`, `show_logs`, `show_stats`)
- Automatic discovery via Docker labels and a watch loop that re-syncs every 10 seconds
- Per-request startup: sleeper checks the container's port, starts it if not running, then proxies
- Idle timeout that stops the container after the configured duration of inactivity
- Customizable loading page with 5 themes (green/blue/amber/red/purple), progress bar, staged boot messages, and retry button on startup timeout
- Admin UI at `/_docknap`, `/_docknap/`, `/_docknap/ui` with live service table, Wake/Stop buttons, and 2-second auto-refresh
- Prometheus metrics at `/_docknap/metrics` with per-service filtering at `/_docknap/metrics/<sub>`
- Per-service history endpoint at `/_docknap/history/<sub>` with state, event counts, and a 100-event ring buffer

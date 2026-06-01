# Changelog

All notable changes to docknap will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
- Structured logging in text or JSON mode (configurable via `DOCKNAP_LOG_FORMAT`)
- HTTP Basic Auth on admin endpoints via `DOCKNAP_ADMIN_USER` / `DOCKNAP_ADMIN_PASS` env vars (constant-time compare, SHA-256 in memory)
- Multi-arch Docker image (linux/amd64, linux/arm64)
- CI: build and test on push; multi-arch image push to GHCR on tag

### Notes
- Project was previously called "sleeper" — renamed to "docknap" in this release
- Network `sleeper_network` is now `docknap_network`
- All env vars, labels, endpoints, and metric names have been renamed

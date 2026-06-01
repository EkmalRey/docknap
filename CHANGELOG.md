# Changelog

All notable changes to docknap will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-06-01

### Added
- Lazy-loading reverse proxy for Docker containers
- Container opt-in via `docknap.*` labels (`enable`, `subdomain`, `target_port`, `idle_timeout`, `startup_timeout`, `title`, `subtitle`, `icon`, `theme`, `show_logs`, `show_stats`)
- Automatic discovery via Docker labels and a watch loop that re-syncs every 10 seconds
- Per-request startup: sleeper checks the container's port, starts it if not running, then proxies
- Idle timeout that stops the container after the configured duration of inactivity
- Customizable loading page with 5 themes (green/blue/amber/red/purple), progress bar, live boot log, and retry button on startup timeout
- Admin UI at `/_docknap` (also `/`, `/ui`) with live service table, Wake/Stop buttons, and 2-second auto-refresh
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

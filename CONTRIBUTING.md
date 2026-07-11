# Contributing to docknap

Thanks for your interest in contributing! docknap is a small, focused project and contributions are welcome.

## Code of conduct

Be respectful. Assume good faith. This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md).

## Filing issues

Use the [issue templates](https://github.com/EkmalRey/docknap/issues/new/choose). For bugs, include:

- docknap version (output of `curl -s http://docknap:8000/_docknap/version | jq .version`)
- Docker version (`docker --version`)
- Your `docker-compose.yml` (or equivalent)
- Relevant logs (`docker logs docknap`)
- The exact `docknap.*` labels on the affected container

## Submitting pull requests

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-change`
3. Make your changes
4. Run `go vet ./...`, `golangci-lint run`, `go test -race ./...` — all must pass
5. Keep commits focused; squash noise commits before requesting review
6. Push your branch and open a PR

## Development setup

Requirements: Go 1.25+, Docker, Make (optional).

```bash
git clone https://github.com/EkmalRey/docknap
cd docknap
docker network create docknap_network
docker compose -f docker-compose.example.yml up -d
```

The example compose starts docknap on port 8000 and a demo nginx container with idle timeout of 10 minutes.

## Testing

```bash
go test -race ./...          # unit tests (logger, metrics, label parser, subdomain, CSRF, middleware)
golangci-lint run            # static analysis (staticcheck, gosec, errcheck, ...)
tests/integration/run.sh    # end-to-end: real Docker, lazy-start + idle-stop
```

Aim for new unit tests when adding non-trivial behavior. The integration script
needs a working Docker daemon and exercises the full lazy-start/idle-stop flow.

## Code style

- Run `gofmt -s -w .` before committing
- Run `go vet ./...` — it must be clean
- Prefer small, focused changes
- Do not add code comments unless behavior is non-obvious

## Commit messages

Short and descriptive. Use the imperative mood:

```
add startup timeout retry button
fix metrics label round-trip for missing subdomain
```

If your change affects user-facing behavior (labels, env vars, endpoints, metrics), mention it in the commit body. Such changes may warrant a CHANGELOG entry and a minor version bump.

## Release process

Maintainers tag releases with `vX.Y.Z` (semver). Pushing the tag triggers the release workflow which:

1. Runs a `verify` job (vet + `go test -race` + lint) that gates the build
2. Builds multi-arch images (linux/amd64, linux/arm64)
3. Pushes to `ghcr.io/ekmalrey/docknap:X.Y.Z` and `:latest` (lowercase per GHCR / Go module rules)
4. Creates a GitHub release with auto-generated notes

If you're a maintainer, ensure the CHANGELOG is updated before tagging.

## Project structure

| File | Purpose |
|------|---------|
| `main.go` | Entry point, server wiring, signal handling |
| `docker.go` | Docker event subscription, poll fallback, container sync/reconcile |
| `registry.go` | In-memory service registry and config state |
| `timers.go` | Idle-stop timers and stop/start orchestration |
| `config.go` | Label and env parsing/validation |
| `proxy.go` | Reverse proxy + WebSocket/streaming upgrade handling |
| `auth.go` | Basic auth, session cookies, CSRF protection |
| `sessions.go` | Login session store |
| `handlers_*.go` | HTTP handlers (actions, admin UI, status) |
| `logs.go` | Container log tailer + SSE stream |
| `metrics.go` | Prometheus registry (counters, gauges, histograms) |
| `notifier.go` / `webhooks.go` | Start/stop notifications and webhooks |
| `templates.go` + `templates/` | Admin UI + loading page HTML |
| `middleware.go` | Security/recovery middleware |
| `logger.go` | Structured text/JSON logger |
| `cidr.go` / `ratelimit.go` / `subdomain.go` | Helpers (trusted proxies, rate limiting, subdomain extraction) |
| `tests/integration/` | End-to-end Docker integration test |
| `*_test.go` | Unit tests |

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).

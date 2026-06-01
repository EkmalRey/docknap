# Contributing to docknap

Thanks for your interest in contributing! docknap is a small, focused project and contributions are welcome.

## Code of conduct

Be respectful. Assume good faith. This project follows the spirit of the [Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/).

## Filing issues

Use the [issue templates](https://github.com/EkmalRey/docknap/issues/new/choose). For bugs, include:

- docknap version (`docknap_version` field in `/_docknap/status` output)
- Docker version (`docker --version`)
- Your `docker-compose.yml` (or equivalent)
- Relevant logs (`docker logs docknap`)
- The exact `docknap.*` labels on the affected container

## Submitting pull requests

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-change`
3. Make your changes
4. Run `go vet ./...` and `go test ./...` — both must pass
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
go test ./...
```

The test suite covers the logger, metrics registry, label parser, subdomain extraction, and theme/theme-fallback logic. Aim for new tests when adding non-trivial behavior.

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

1. Runs `go vet` and `go test`
2. Builds multi-arch images (linux/amd64, linux/arm64)
3. Pushes to `ghcr.io/ekmalrey/docknap:X.Y.Z` and `:latest` (lowercase per GHCR / Go module rules)
4. Creates a GitHub release with auto-generated notes

If you're a maintainer, ensure the CHANGELOG is updated before tagging.

## Project structure

| File | Purpose |
|------|---------|
| `main.go` | HTTP server, label parsing, lifecycle management |
| `admin.go` | Admin UI HTML + JavaScript |
| `auth.go` | HTTP Basic Auth middleware |
| `logger.go` | Structured text/JSON logger |
| `metrics.go` | Prometheus registry (counters, gauges, histograms) |
| `*_test.go` | Unit tests |

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).

# Public Release Hardening Design

## Goal

Prepare docknap for public GitHub release as an honest beta-quality v0.x project by eliminating verified actionable runtime, security, resource, test, CI, release, supply-chain, and community-health gaps.

This work does not claim perfection or eliminate Docker's inherent trust model. It must leave unavoidable upstream and operational risks explicit, narrow, and verifiable.

## Release Posture

Docknap will present itself as beta v0.x software. Documentation must avoid claims such as flawless, fully audited, production-proven, or safe for hostile multi-tenant environments.

The changelog remains under `Unreleased` until an actual matching SemVer tag is published. Release automation accepts only tags matching `vMAJOR.MINOR.PATCH`, verifies that the matching changelog version exists, and must not silently publish malformed tags.

## Authentication Decision

Docknap will continue to start when admin credentials are absent. This is an explicit product requirement.

When authentication is disabled:

- Startup emits a prominent warning describing Docker-host impact.
- Recommended deployment examples do not expose the backend admin listener directly to public networks.
- Documentation states that authenticated admin endpoints and public service wake routes are separate trust boundaries.
- Documentation states that public service requests can wake labeled containers even when admin authentication is enabled.
- Sensitive service hosts should be protected by the external TLS reverse proxy.

No opt-out environment variable or fail-closed startup gate will be introduced.

## Runtime Hardening

### Docker Log Frames

Docker multiplexed log frame lengths are untrusted input. The parser will reject frames above a fixed, documented ceiling before allocating memory. Tests cover a valid frame at the ceiling and a rejected frame above it.

### Readiness Ownership

Each subdomain may have at most one readiness worker for a boot attempt. Concurrent start or wait requests must share the existing boot ownership rather than launching duplicate polling goroutines. Cancellation clears active ownership without recording a timeout; deadline expiration records one terminal timeout; only an explicit retry starts a new attempt.

### Bulk Actions

Start-all and stop-all use a fixed worker pool of eight workers rather than one goroutine per service blocked behind a semaphore. This bounds goroutine and closure retention by worker count while preserving existing concurrency and responses.

### Rate Limiting

Rate-limit buckets are pruned opportunistically during normal access. Per-limiter background ticker goroutines are removed, eliminating lifecycle leaks without adding shutdown APIs.

### Docker Reconciliation Input

Container reconciliation checks that Docker returned at least one usable container name before indexing the name list. Malformed entries are skipped with a warning rather than panicking the watcher.

### Metrics Cardinality

Authentication failure metrics must not label series with arbitrary request paths. They use a bounded route category or fixed endpoint label. Service lifecycle metrics must not retain unbounded retired service series; removal either deletes relevant series or uses bounded labels where deletion is unsupported.

The implementation will prefer small deletion methods on the existing metric primitives over replacing the metrics subsystem.

### HTTP Connection Reuse

Health checks and webhook delivery drain bounded response bodies before closing them so normal small responses permit connection reuse. Draining remains bounded to prevent untrusted peers from forcing unlimited reads.

### Trusted Proxy Configuration

A non-empty malformed `DOCKNAP_TRUSTED_PROXIES` entry is a startup configuration error. Partial acceptance is not allowed because it can silently alter secure-cookie and client-address behavior. Valid parsed ranges may be logged at startup without exposing secrets.

### Proxy Performance

No reverse-proxy cache will be introduced without evidence. A warm-proxy allocation benchmark will establish the current cost. Caching is implemented only if the benchmark demonstrates a material bottleneck and can be invalidated by existing Docker identity changes without new speculative architecture.

## Tests And Benchmarks

Focused tests will cover:

- Oversized and boundary-sized Docker log frames.
- Concurrent starts producing one readiness worker.
- Startup cancellation, terminal timeout, and explicit retry.
- Fixed-worker bulk start and stop behavior.
- Malformed Docker container entries.
- Bounded metric cardinality and service-series retirement.
- Rate-limiter pruning without goroutine leaks.
- Health and webhook HTTP connection reuse.
- Strict trusted-proxy parsing.
- Existing timer replacement ownership and log-tailer ownership boundaries.

Tests should invoke real handlers and methods where practical rather than duplicating production arithmetic. Existing tests that can pass despite downstream panic or never invoke the named handler will be corrected.

Add only three benchmark families:

- Warm proxy requests with allocation reporting.
- Metrics scrape at 1, 1,000, and 10,000 series.
- Container reconciliation at 1, 1,000, and 10,000 containers.

Benchmarks are smoke-run in CI or verification without brittle performance thresholds. Optimization follows measured evidence, not arbitrary targets.

The integration harness will retain wake, proxy, and idle-stop coverage and add one concurrent cold-start scenario proving eventual proxy success and single-start ownership. Broader Docker daemon failure simulation is deferred unless it can be tested deterministically without a second infrastructure stack.

## Lint And Static Analysis

All actionable `golangci-lint` findings will be fixed. Writes and closes are checked when failure affects correctness or resource ownership; explicitly irrelevant response-write failures may be ignored with narrow, local justification.

Each `gosec G203` finding will be reviewed against actual template provenance. Safe, repository-owned templates may receive a line-local suppression explaining why the value is trusted. Untrusted values must use contextual escaping instead. Global linter disablement is prohibited.

`go vet`, race tests, module verification, and `govulncheck` remain release gates.

## Docker And Deployment Hardening

The production image continues to run as the existing non-root `docknap` user. Recommended Compose configuration will avoid forcing root when a Docker group GID or rootless Docker setup is available, while clearly documenting that Docker group access remains root-equivalent.

Verified-compatible container restrictions will be added to examples:

- `cap_drop: [ALL]`
- `security_opt: [no-new-privileges:true]`
- Read-only root filesystem and temporary writable mounts only if integration verification passes.

Dockerfile base images will be pinned by verified digest while retaining readable version tags. Dependabot will update those digests. Production deployment examples will recommend a versioned docknap image rather than `latest`; `latest` may remain a convenience tag with explicit non-production wording.

Documentation will include practical rootless Docker and restricted socket-proxy guidance. A socket proxy must expose only operations docknap uses: container list, inspect, events, logs, stats, start, stop, pause, and unpause. The repository will not claim that Docker group membership or a read-only mount provides meaningful least privilege.

## Supply Chain And CI

### CI

CI keeps full-SHA action pins and read-only default permissions. It runs formatting, vet, unit and race tests, lint, module verification, `govulncheck`, Compose validation, Docker build, image scanning, and integration tests.

A pinned image scanner checks the built image and fails on fixable high or critical vulnerabilities. Unfixed upstream findings are reported and governed by the security policy rather than hidden through broad ignores.

### Release Permissions

Workflow-level permissions default to `contents: read`. Verification jobs remain read-only. Publishing jobs receive only the permissions they require. OIDC `id-token: write` is granted only to the attestation/signing step or job; package and release writes are separated where practical.

### Release Ordering

The release pipeline:

1. Validates the SemVer tag and changelog entry.
2. Runs all verification gates.
3. Builds and pushes the immutable versioned multi-architecture image.
4. Produces explicit SBOM and maximum BuildKit provenance.
5. Records and signs or attests the immutable image digest using GitHub OIDC-supported tooling.
6. Creates the matching GitHub release with verification instructions and digest.
7. Promotes the same verified digest to `latest` only after the versioned release succeeds.

The design uses standard GitHub artifact attestations or keyless Cosign support rather than a custom signing system. All third-party actions remain pinned to full commit SHAs.

Standalone binary archives, checksums, and archive signing are out of scope because the project currently distributes a container image. If binaries are added later, they require `SHA256SUMS`, SBOM, provenance, and signatures.

## Dependency Risk

Dependabot configuration will be committed and cover Go modules, GitHub Actions, the root Dockerfile, and maintained nested Docker/Compose examples.

`govulncheck` findings will be evaluated for call-path reachability. If a reachable issue has an available fixed dependency, it blocks release. If no fixed upstream version exists, the repository documents the affected dependency, reachability, mitigations, and update tracking. Fabricated or unsafe dependency replacements are prohibited.

Base-image digest pinning is paired with scheduled Dependabot updates and CI scanning so immutability does not freeze known vulnerabilities indefinitely.

## Public Repository Health

The repository will include and maintain:

- MIT `LICENSE`.
- `SECURITY.md` naming only the latest stable GitHub release as supported and directing private reports to GitHub Security Advisories.
- Contributor Covenant 2.1 in `CODE_OF_CONDUCT.md` with a real private enforcement contact.
- `CONTRIBUTING.md` linked to the Code of Conduct and using the public version endpoint for diagnostics.
- Issue chooser configuration directing vulnerabilities to private reporting.
- Bug, feature, question, and pull-request templates.
- A real GitHub Actions workflow badge rather than a hard-coded passing badge.
- A clean Keep a Changelog-style `CHANGELOG.md` with one `Unreleased` section and no unreleased version presented as shipped.
- Consistent canonical GitHub links and lowercase Go module/GHCR identifiers.

`CODEOWNERS`, funding configuration, and a separate support file are omitted unless they correspond to real maintainers, funding destinations, or support processes. The README and issue chooser are sufficient for current support routing.

Repository topics and wiki settings are GitHub repository metadata rather than source changes. Recommended topics are `docker`, `reverse-proxy`, `homelab`, `go`, and `lazy-loading`; the wiki should remain disabled unless it will be maintained.

## Acceptance Criteria

Public-release hardening is complete only when fresh verification confirms:

- `gofmt` reports no files.
- `git diff --check` passes.
- `go test -count=1 ./...` passes.
- `go test -race -count=1 ./...` passes.
- `go vet ./...` passes.
- `go mod verify` passes.
- `golangci-lint` passes with no unexplained suppressions.
- `govulncheck ./...` has no reachable fixable vulnerability; any unfixable upstream result is documented.
- Compose files validate.
- The production image builds for the supported architecture used locally.
- The image scanner has no fixable high or critical result.
- Full integration, including concurrent cold start, passes.
- Benchmark smoke runs complete.
- Release workflow syntax, permissions, tag validation, attestation, and ordering are reviewed.
- An independent final code and security review finds no unresolved critical or high actionable issue.

## Residual Risks

The final public documentation must retain these facts:

- Writable Docker API access is privileged and compromise can lead to host takeover.
- Docker group membership is effectively root-equivalent.
- Docknap does not terminate TLS; external proxy configuration controls TLS policy, HSTS, certificates, and forwarded-header integrity.
- Public service routes can wake containers by design.
- Upstream vulnerabilities without a fixed release cannot be locally eliminated safely.
- Passing tests and scanners reduce risk but do not prove absence of defects.

These are accepted platform and operational boundaries, not hidden implementation debt.

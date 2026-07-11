# Public Release Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close every actionable gap in the approved public-release hardening design.

**Architecture:** Preserve the single-package Go application. Fix bounds and ownership at existing state boundaries, add focused regressions and benchmark smoke checks, and use standard GitHub and Docker supply-chain features.

**Tech Stack:** Go 1.25, Docker SDK and Compose, GitHub Actions, GHCR, BuildKit SBOM/provenance, artifact attestations, golangci-lint, govulncheck, Trivy.

---

### Task 1: Runtime Bounds And Ownership

**Files:** `logs.go`, `docker.go`, `registry.go`, `logs_test.go`, `docker_test.go`

- [ ] Add failing tests for oversized Docker log frames, duplicate readiness workers, and nameless Docker containers.
- [ ] Run each focused test in `golang:1.25` and verify the expected failure.
- [ ] Reject log frames above 1 MiB before allocation.
- [ ] Make boot ownership explicit and launch only one readiness worker per attempt.
- [ ] Skip malformed nameless Docker entries with a warning.
- [ ] Run focused and race tests green.

### Task 2: Bounded Work And Lifecycle

**Files:** `handlers_actions.go`, `ratelimit.go`, relevant tests

- [ ] Add failing tests proving bulk operations use at most eight workers and rate-limit access prunes expired buckets.
- [ ] Replace goroutine-per-service fan-out with eight fixed workers.
- [ ] Remove rate-limiter ticker goroutines and prune opportunistically under the existing lock.
- [ ] Run focused and race tests green.

### Task 3: Metrics And HTTP Reuse

**Files:** `metrics.go`, `auth.go`, `docker.go`, `webhooks.go`, `registry.go`, relevant tests

- [ ] Add failing tests for bounded auth metric cardinality, retired service series, and HTTP connection reuse.
- [ ] Replace arbitrary auth path labels with bounded categories.
- [ ] Add minimal metric-series deletion and use it when services disappear.
- [ ] Drain at most 64 KiB before closing health and webhook responses.
- [ ] Run focused and race tests green.

### Task 4: Strict Proxy Configuration

**Files:** `cidr.go`, `main.go`, `handlers_test.go`

- [ ] Add failing table tests for valid, empty, whitespace, and malformed trusted-proxy lists.
- [ ] Change parsing to return an error for every malformed non-empty entry.
- [ ] Propagate failure through startup configuration without changing unauthenticated startup behavior.
- [ ] Run focused and full tests green.

### Task 5: Test Quality And Benchmarks

**Files:** `csrf_test.go`, `handlers_test.go`, `armIdleTimer_test.go`, `bench_test.go`

- [ ] Replace tests that duplicate arithmetic or tolerate downstream panic with handler-level assertions.
- [ ] Correct idle-timer idempotence testing while the original timer remains active.
- [ ] Add warm proxy, metrics scale, and reconciliation scale benchmark families.
- [ ] Run `go test -run '^$' -bench . -benchtime=1x ./...`.
- [ ] Optimize warm proxy construction only if measurements justify it without new invalidation architecture.

### Task 6: Lint And Static Analysis

**Files:** exact files reported by `.golangci.yml`

- [ ] Run golangci-lint v2.4.0 and capture exact findings.
- [ ] Fix behaviorally relevant unchecked writes and closes.
- [ ] Escape untrusted template values or add line-local justified `#nosec G203` only for repository-owned templates.
- [ ] Add no global suppressions.
- [ ] Run lint, tests, race, and vet green.

### Task 7: Docker Deployment Hardening

**Files:** `Dockerfile`, Compose files, `.github/dependabot.yml`, `README.md`, `SECURITY.md`

- [ ] Resolve and pin verified base-image digests while retaining readable tags.
- [ ] Recommend versioned docknap images instead of `latest` for production.
- [ ] Add `cap_drop: [ALL]`, `no-new-privileges`, and read-only filesystem only where integration proves compatibility.
- [ ] Remove forced root from the recommended example; document Docker GID, rootless Docker, and socket-proxy options.
- [ ] Cover maintained nested Docker files in Dependabot.
- [ ] Document public wake routes and Docker socket residual risk.
- [ ] Validate Compose and integration green.

### Task 8: CI And Release Supply Chain

**Files:** `.github/workflows/build.yml`, `.github/workflows/release.yml`

- [ ] Add formatting, module verification, Compose validation, image build, and pinned image scanning to CI.
- [ ] Validate release tags against strict SemVer and require a matching changelog section.
- [ ] Scope permissions per job with read-only defaults.
- [ ] Publish versioned image first with explicit SBOM and maximum provenance.
- [ ] Attest the immutable digest using GitHub OIDC-supported tooling.
- [ ] Create the GitHub release with digest and verification instructions before promoting that digest to `latest`.
- [ ] Keep every third-party action pinned to a verified full SHA.
- [ ] Validate workflow syntax and inspect effective permissions/order.

### Task 9: Public Repository Health

**Files:** `README.md`, `CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `.github/ISSUE_TEMPLATE/config.yml`, issue templates

- [ ] Replace the hard-coded build badge with the workflow badge.
- [ ] Clean duplicate changelog sections and keep unreleased work under `Unreleased`.
- [ ] Add Contributor Covenant 2.1 with a private enforcement contact.
- [ ] Link contribution policy to it and use `/_docknap/version` for diagnostics.
- [ ] Add issue chooser security-advisory routing.
- [ ] Clarify latest stable release support, beta posture, support routes, and residual risks.
- [ ] Keep CODEOWNERS, funding, and separate support files absent unless backed by real processes.

### Task 10: Final Acceptance

**Files:** repository-wide verification only

- [ ] Run gofmt, diff check, unit tests, race tests, vet, module verification, lint, govulncheck, Compose validation, image build/scan, integration, and benchmark smoke checks.
- [ ] Classify any vulnerability finding as fixable, unreachable, or upstream-unfixed with evidence.
- [ ] Request independent correctness and security review; resolve every actionable critical/high finding.
- [ ] Inspect final status and diff without reverting unrelated changes.
- [ ] Report exact residual operational/upstream risks and do not claim flawlessness.

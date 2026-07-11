# Docknap Complete Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the verified lifecycle, concurrency, security, configuration, CI, and cleanup defects while preserving docknap's single-binary homelab architecture.

**Architecture:** Keep existing files and APIs, fixing ownership and state transitions at their shared roots. Docker list results provide stable identity; timers and log tailers use atomic ownership; browser mutations require exact origin; configuration rejects explicit invalid values.

**Tech Stack:** Go 1.25, Docker Engine SDK, `net/http`, `html/template`, GitHub Actions, Docker Compose.

---

### Task 1: Docker Reconciliation and Idle Arming

**Files:**
- Modify: `docker.go`
- Modify: `registry.go`
- Test: `docker_test.go`

- [ ] Add focused fake-Docker tests proving unchanged running containers are not repeatedly inspected, changed IDs invalidate runtime caches, initial running services receive idle timers, recently started services receive deferred timers, and duplicate subdomains register neither service.
- [ ] Run the focused tests in a Go 1.25 Docker builder and confirm they fail against current behavior.
- [ ] Persist known Docker start timestamps, skip inspection when state and ID are unchanged, schedule remaining warm-up before idle arming, and reject duplicate subdomains.
- [ ] Run focused tests and all unit tests.

### Task 2: Startup Timeout State

**Files:**
- Modify: `docker.go`
- Modify: `handlers_actions.go`
- Test: `handlers_test.go`

- [ ] Add tests proving root cancellation emits no timeout, deadline expiration emits one timeout, timed-out polling does not silently launch another attempt, and an explicit retry clears timeout state.
- [ ] Run focused tests and confirm failure.
- [ ] Record timeout only for `context.DeadlineExceeded`; treat timeout state as terminal for wait polling until explicit retry/start transition resets it.
- [ ] Run focused and full unit tests.

### Task 3: Atomic Idle Timers

**Files:**
- Modify: `timers.go`
- Test: `armIdleTimer_test.go`

- [ ] Add a race-oriented unit test where the old callback reaches ownership checking while a reset occurs.
- [ ] Confirm the test fails or exposes the stale callback path.
- [ ] Atomically verify and remove the exact timer under `s.mu` before performing stop work; cancel timers when `DisableIdle` becomes true.
- [ ] Run timer tests and full tests.

### Task 4: Log Tailer Ownership

**Files:**
- Modify: `logs.go`
- Test: `logs_test.go`

- [ ] Add tests for stale-owner cleanup, live-only reconnect, and atomic subscriber-cap admission.
- [ ] Confirm focused failures.
- [ ] Replace resettable integer generations with unique ownership tokens, preserve first-tail versus reconnect behavior, and combine cap checking with subscription insertion.
- [ ] Run focused and full tests.

### Task 5: Same-Origin CSRF and CSP

**Files:**
- Modify: `auth.go`
- Modify: `middleware.go`
- Modify: `templates/loading.html`
- Test: `auth_test.go`
- Test: `middleware_test.go`

- [ ] Add table tests rejecting sibling subdomains, scheme changes, port changes, and `Sec-Fetch-Site: same-site` for Basic-auth mutations while allowing same-origin and non-browser API requests.
- [ ] Add tests proving nonce generation failure does not render a predictable nonce and loading HTML contains no inline event handlers.
- [ ] Run focused tests and confirm failure.
- [ ] Compare full normalized origins, require same-origin fetch metadata, return HTTP 500 when nonce generation fails, and attach Retry with `addEventListener`.
- [ ] Run focused and full tests.

### Task 6: Strict Configuration

**Files:**
- Modify: `config.go`
- Modify: `main.go`
- Test: `parseLabels_test.go`

- [ ] Replace permissive tests with table tests rejecting invalid explicit durations, booleans, themes, strategies, and pause-without-health-path.
- [ ] Add process-environment parser tests proving malformed explicit values return errors while missing values retain defaults.
- [ ] Run focused tests and confirm current failures.
- [ ] Implement strict label parsing and error-returning environment parsing used by startup.
- [ ] Run focused and full tests.

### Task 7: Integration and CI Reproducibility

**Files:**
- Modify: `tests/integration/run.sh`
- Modify: `.github/workflows/build.yml`
- Modify: `.github/workflows/release.yml`
- Modify: `.github/dependabot.yml`
- Modify: `.dockerignore`
- Modify: `.gitignore`

- [ ] Change integration setup to stop the demo before the first request and wait until Docker reports it stopped.
- [ ] Run the real integration harness and require wake, proxy, and docknap-driven idle stop to pass.
- [ ] Pin Actions to full commit SHAs and `govulncheck` to a fixed version; retain release verification parity.
- [ ] Ignore `coverage.html` and all generated build outputs in Docker context.
- [ ] Validate both workflow files and Compose configuration.

### Task 8: Dead Readiness Cleanup

**Files:**
- Modify: `registry.go`
- Modify: `handlers_actions.go`
- Modify: `handlers_test.go`

- [ ] Remove `ReadyChans`, `serviceStateCopy`, `subscribeReady`, `broadcastReady`, and tests that only exercise the unused subsystem.
- [ ] Run full tests and verify no production caller remains.

### Task 9: Final Verification

**Files:**
- Modify: `CHANGELOG.md`

- [ ] Update the Unreleased changelog with externally relevant hardening changes.
- [ ] Run `gofmt`, `git diff --check`, `go vet`, unit tests, module verification, coverage, Docker build, Compose validation, and real integration.
- [ ] Run race tests and golangci-lint in suitable toolchain containers; report exact limitations if infrastructure prevents either.
- [ ] Inspect final diff for unrelated changes, secrets, stale audit comments, and unnecessary abstractions.

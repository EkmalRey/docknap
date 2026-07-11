# Docknap Complete Hardening Design

## Goal

Close the verified runtime, concurrency, security, integration, configuration, reproducibility, and cleanup findings while preserving docknap's small single-binary architecture and homelab-focused operation.

## Scope

### Lifecycle and reconciliation

- Use Docker container ID and persisted start timestamp as the service identity.
- Inspect only newly discovered containers, running-state transitions, or changed container IDs.
- Invalidate IP and startup state when container identity changes.
- Arm idle timers for running services discovered at startup without relying on repeated inspections.
- For recently started services, defer idle arming until the startup window expires unless readiness is observed first.
- Reject duplicate subdomain registrations instead of selecting a winner.
- Distinguish startup timeout from process cancellation.
- Require an explicit retry transition after startup timeout rather than silently starting another attempt from readiness polling.

### Timer and log concurrency

- Have an idle callback atomically claim and remove its exact timer before performing stop work.
- Use non-repeating log-tailer ownership tokens so stale goroutines cannot remove replacements.
- Replay log history only on the first connection; reconnect live-only.
- Admit log subscribers atomically with the configured cap.

### Security

- Require same-origin browser requests for state-changing Basic-auth operations.
- Compare normalized scheme, host, and effective port using trusted proxy HTTPS handling.
- Preserve token validation for cookie-authenticated mutations.
- Remove inline event-handler attributes so nonce CSP does not break controls.
- Fail a request closed if a secure CSP nonce cannot be generated.
- Keep JSON encoding and text-only DOM insertion for label-derived boot messages.

### Configuration

Reject enabled containers with:

- Ports outside 1 through 65535.
- Missing or invalid positive idle/startup durations.
- Unknown strategies or themes.
- `pause` strategy without a health path.
- Invalid explicit boolean label values.

Logs identify the container and invalid label. Process-level invalid environment values fail startup rather than silently falling back. Missing values retain documented defaults.

### CI and reproducibility

- Make the Docker integration test explicitly establish a stopped target before testing wake behavior.
- Verify real idle shutdown without manually stopping during the assertion.
- Run unit/race, vet, lint, vulnerability, and integration checks before releases.
- Pin GitHub Actions and installed CI tools to immutable versions or commit revisions.
- Keep Dependabot configured to update pins.
- Exclude tests, generated binaries, and coverage output from Docker context; ignore generated coverage HTML.

### Cleanup

- Remove the test-only ready-channel subsystem and its tests because no production waiter consumes it.
- Remove remaining dead helpers and unused state discovered while touching these paths.
- Avoid new dependencies or architectural layers.

## Error Handling

- Docker reconciliation failures leave the previous valid registry intact and mark readiness degraded.
- Invalid container configuration skips only that container and logs the exact reason.
- Duplicate subdomains register neither conflicting service.
- Nonce generation failure returns HTTP 500 without rendering executable HTML.
- Explicit startup retries reset timeout state before launching one readiness worker.

## Testing

Add focused regression coverage for:

- Initial running-service timer arming and delayed arming.
- No repeated inspect for unchanged running containers.
- Container-ID cache invalidation.
- Duplicate subdomain rejection.
- Startup cancellation versus timeout and explicit retry.
- Atomic timer ownership at reset boundaries.
- Log-tailer generation replacement, reconnect tail mode, and subscriber cap.
- Full-origin CSRF checks including sibling subdomains, scheme, and port differences.
- CSP nonce presence and absence of inline event handlers.
- Strict label and environment validation.

Final verification runs formatting, vet, unit tests, race tests where the toolchain supports CGO, lint, module verification, Compose validation, Docker image build, and the real Docker integration harness.

## Non-Goals

- Replacing the Docker SDK.
- Splitting the single package into speculative internal packages.
- Adding persistence, distributed coordination, Kubernetes-specific behavior, or a new UI.
- Redesigning existing templates beyond CSP-required event wiring.

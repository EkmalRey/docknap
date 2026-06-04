#!/usr/bin/env bash
# docknap integration test
# Spins up docknap + a demo nginx container, then exercises:
#   1. Initial stop: requesting the subdomain serves the loading page.
#   2. Wake: after waiting past startup_timeout, the demo returns its index.
#   3. Idle stop: setting a short idle_timeout and waiting stops the container.
#
# Requires: docker, docker compose, curl, jq, and a working /var/run/docker.sock.

set -euo pipefail

cd "$(dirname "$0")/../.."

PROJECT=docknap-it
NETWORK="${PROJECT}_network"
COMPOSE_FILE="tests/integration/docker-compose.yml"
DOCKNAP_URL="http://127.0.0.1:8000"

cleanup() {
    docker compose -p "$PROJECT" -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
    docker network rm "$NETWORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cleanup
docker network create "$NETWORK" >/dev/null
docker compose -p "$PROJECT" -f "$COMPOSE_FILE" up -d >/dev/null

# Wait for docknap to come up
for _ in $(seq 1 30); do
    if curl -fsS "$DOCKNAP_URL/healthz" >/dev/null; then break; fi
    sleep 1
done

echo "1) initial request should serve loading page (demo not running)"
status=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: demo.internal" "$DOCKNAP_URL/")
if [[ "$status" != "503" ]]; then
    echo "FAIL: expected 503 while container is starting, got $status"
    exit 1
fi

echo "2) wait for demo to come up and serve the index"
ok=0
for _ in $(seq 1 30); do
    if curl -fsS -H "Host: demo.internal" "$DOCKNAP_URL/" | grep -q "Welcome to nginx"; then
        ok=1
        break
    fi
    sleep 1
done
if [[ "$ok" -ne 1 ]]; then
    echo "FAIL: demo never became ready"
    docker compose -p "$PROJECT" -f "$COMPOSE_FILE" logs
    exit 1
fi

echo "3) idle timeout should stop the container"
# Set very short idle timeout via a second compose run
docker compose -p "$PROJECT" -f "$COMPOSE_FILE" stop demo >/dev/null 2>&1 || true
ok=0
for _ in $(seq 1 60); do
    state=$(curl -fsS "$DOCKNAP_URL/_docknap/status" | jq -r '.services[0].state')
    if [[ "$state" == "exited" || "$state" == "stopped" ]]; then
        ok=1
        break
    fi
    sleep 1
done
if [[ "$ok" -ne 1 ]]; then
    echo "FAIL: container did not stop on idle within 60s (state=$state)"
    exit 1
fi

echo "PASS"

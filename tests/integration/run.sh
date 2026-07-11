#!/usr/bin/env bash
# docknap integration test
# Spins up docknap + a demo nginx container, then exercises:
#   1. Initial stop: requesting the subdomain serves the loading page.
#   2. Wake: after waiting past startup_timeout, the demo returns its index.
#   3. Idle stop: setting a short idle_timeout and waiting stops the container.
#
# Requires: docker, docker compose, curl, and a working /var/run/docker.sock.

set -euo pipefail

cd "$(dirname "$0")/../.."

PROJECT=docknap-it
NETWORK="${PROJECT}_network"
COMPOSE_FILE="tests/integration/docker-compose.yml"
DOCKNAP_URL="http://127.0.0.1:18000"

cleanup() {
    docker compose -p "$PROJECT" -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
    docker network rm "$NETWORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cleanup
docker network create "$NETWORK" >/dev/null
docker compose -p "$PROJECT" -f "$COMPOSE_FILE" up -d >/dev/null
docker compose -p "$PROJECT" -f "$COMPOSE_FILE" stop demo >/dev/null
demo_id=$(docker compose -p "$PROJECT" -f "$COMPOSE_FILE" ps -aq demo)
for _ in $(seq 1 30); do
    state=$(docker inspect -f '{{.State.Status}}' "$demo_id")
    [[ "$state" == "exited" ]] && break
    sleep 1
done
[[ "$state" == "exited" ]] || { echo "FAIL: demo did not stop before first request"; exit 1; }

# Wait for docknap to come up and reconcile the stopped demo.
ok=0
for _ in $(seq 1 30); do
    if curl -fsS "$DOCKNAP_URL/healthz" >/dev/null &&
       curl -fsS "$DOCKNAP_URL/_docknap/status" | grep -q '"state":"exited"'; then
        ok=1
        break
    fi
    sleep 1
done
[[ "$ok" -eq 1 ]] || { echo "FAIL: docknap did not reconcile demo"; exit 1; }

echo "1) initial request should serve loading page (demo not running)"
body=$(curl -sS -H "Host: demo.internal" "$DOCKNAP_URL/")
if [[ "$body" != *"Starting"* ]]; then
    echo "FAIL: expected loading page while container is starting"
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

echo "3) idle timeout should stop the container (docknap does it, no manual stop)"
# Do NOT touch the container here; just wait for docknap's own idle timer to
# stop it. The status poll makes no proxy request, so the idle timer is not reset.
ok=0
for _ in $(seq 1 60); do
    status=$(curl -fsS "$DOCKNAP_URL/_docknap/status")
    if [[ "$status" == *'"state":"exited"'* || "$status" == *'"state":"stopped"'* ]]; then
        ok=1
        break
    fi
    sleep 1
done
if [[ "$ok" -ne 1 ]]; then
    echo "FAIL: container did not stop on idle within 60s (status=$status)"
    exit 1
fi

echo "PASS"

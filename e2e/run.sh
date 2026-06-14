#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE="docker compose -f $SCRIPT_DIR/compose.yml"
NETWORK=e2e_default
TMP=$(mktemp -d)

cleanup() {
    echo ""
    echo "==> teardown"
    $COMPOSE down -v --remove-orphans 2>/dev/null || true
    rm -rf "$TMP"
}
trap cleanup EXIT

echo "==> clean previous run"
$COMPOSE down -v --remove-orphans 2>/dev/null || true

echo "==> build and start"
$COMPOSE up --build -d

echo "==> wait for pgsafe to finish"
$COMPOSE wait pgsafe

echo "==> pull backup from MinIO"
docker run --rm \
    --network "$NETWORK" \
    -v "$TMP:/out" \
    --entrypoint /bin/sh minio/mc -c \
    'mc alias set s http://minio:9000 minioadmin minioadmin --quiet && mc mirror s/pgsafe-test /out'

echo "==> verify"
DUMP=$(find "$TMP" -name "*.dump" | head -1)
[ -n "$DUMP" ] || { echo "FAIL: no .dump file found in bucket"; exit 1; }
echo "   file: $(basename "$DUMP")"

MANIFEST=$(docker run --rm \
    -v "$TMP:/backup" \
    postgres:18-alpine \
    pg_restore --list "/backup/$(basename "$DUMP")")

for table in users orders; do
    if echo "$MANIFEST" | grep -q "TABLE DATA public $table"; then
        echo "   OK  table '$table' present"
    else
        echo "FAIL: table '$table' missing from backup"
        exit 1
    fi
done

echo "==> verify table contents"
DATA=$(docker run --rm \
    -v "$TMP:/backup" \
    postgres:18-alpine \
    pg_restore --data-only --inserts "/backup/$(basename "$DUMP")")

check() {
    local label="$1" value="$2"
    if echo "$DATA" | grep -qF "$value"; then
        echo "   OK  $label"
    else
        echo "FAIL: $label ('$value' not found in backup data)"
        exit 1
    fi
}

# users rows
check "user Alice"   "alice@example.com"
check "user Bob"     "bob@example.com"
check "user Charlie" "charlie@example.com"

# orders rows
check "order 99.99"  "99.99"
check "order 14.50"  "14.50"
check "order 250.00" "250.00"
check "order 7.99"   "7.99"

check "status completed" "completed"
check "status pending"   "pending"
check "status cancelled" "cancelled"

echo ""
echo "==> PASS"

#!/usr/bin/env bash
# verify.sh — validate pgsafe backup files produced by a single run.
#
# Usage:
#   verify.sh --backup-dir DIR \
#             --format      custom|plain \
#             --compression none|gzip|bzip2|xz \
#             --encryption  true|false \
#             [--cipher-key KEY] \
#             --expect-conns CONN[,CONN,...] \
#             --testdb-conn  CONN
#
# --expect-conns  Every listed connection must have ≥1 backup file.
# --testdb-conn   The connection whose testdb backup is content-verified.
set -euo pipefail

BACKUP_DIR=""
FORMAT="custom"
COMPRESSION="none"
ENCRYPTION="false"
CIPHER_KEY=""
EXPECT_CONNS=""
TESTDB_CONN=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --backup-dir)   BACKUP_DIR="$2";   shift 2 ;;
        --format)       FORMAT="$2";       shift 2 ;;
        --compression)  COMPRESSION="$2";  shift 2 ;;
        --encryption)   ENCRYPTION="$2";   shift 2 ;;
        --cipher-key)   CIPHER_KEY="$2";   shift 2 ;;
        --expect-conns) EXPECT_CONNS="$2"; shift 2 ;;
        --testdb-conn)  TESTDB_CONN="$2";  shift 2 ;;
        *) echo "unknown argument: $1" >&2; exit 1 ;;
    esac
done

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

ok()   { echo "   OK  $*"; }
fail() { echo "  FAIL $*" >&2; exit 1; }

# ── decode: copy, decrypt, decompress → returns path of decoded file ──────────

decode() {
    local src="$1"
    local name; name=$(basename "$src")
    cp "$src" "$TMP/$name"
    local f="$TMP/$name"

    # Decryption is undone first — encryption is the outermost layer in the pipeline.
    if [[ "$f" == *.enc ]]; then
        local plain="${f%.enc}"
        openssl enc -d -aes-256-cbc -pbkdf2 -iter 100000 \
            -pass "pass:$CIPHER_KEY" -in "$f" -out "$plain"
        rm "$f"; f="$plain"
    fi

    case "$f" in
        *.gz)  gunzip  "$f"; f="${f%.gz}"  ;;
        *.bz2) bunzip2 "$f"; f="${f%.bz2}" ;;
        *.xz)  xz -d  "$f"; f="${f%.xz}"  ;;
    esac

    echo "$f"
}

# ── content checks ────────────────────────────────────────────────────────────

verify_custom() {
    local file="$1"
    local dir; dir=$(dirname "$file")
    local bname; bname=$(basename "$file")

    local manifest
    manifest=$(docker run --rm -v "$dir:/b" postgres:18-alpine \
        pg_restore --list "/b/$bname")

    echo "$manifest" | grep -q "TABLE DATA public users"  || fail "users table missing in $bname"
    echo "$manifest" | grep -q "TABLE DATA public orders" || fail "orders table missing in $bname"
    ok "schema (users, orders present)"

    local data
    data=$(docker run --rm -v "$dir:/b" postgres:18-alpine \
        pg_restore --data-only --inserts "/b/$bname")

    echo "$data" | grep -qF "alice@example.com"   || fail "user Alice missing"
    echo "$data" | grep -qF "bob@example.com"     || fail "user Bob missing"
    echo "$data" | grep -qF "charlie@example.com" || fail "user Charlie missing"
    echo "$data" | grep -qF "99.99"               || fail "order 99.99 missing"
    echo "$data" | grep -qF "14.50"               || fail "order 14.50 missing"
    echo "$data" | grep -qF "250.00"              || fail "order 250.00 missing"
    echo "$data" | grep -qF "7.99"                || fail "order 7.99 missing"
    echo "$data" | grep -qF "completed"           || fail "status 'completed' missing"
    echo "$data" | grep -qF "pending"             || fail "status 'pending' missing"
    echo "$data" | grep -qF "cancelled"           || fail "status 'cancelled' missing"
    ok "data (3 users, 4 orders, all statuses)"
}

verify_plain() {
    local file="$1"
    local bname; bname=$(basename "$file")

    # Plain-format dumps are SQL text: CREATE TABLE + COPY ... FROM stdin blocks.
    grep -q "users"  "$file" || fail "users table missing in $bname"
    grep -q "orders" "$file" || fail "orders table missing in $bname"
    ok "schema (users, orders present)"

    grep -qF "alice@example.com"   "$file" || fail "user Alice missing"
    grep -qF "bob@example.com"     "$file" || fail "user Bob missing"
    grep -qF "charlie@example.com" "$file" || fail "user Charlie missing"
    grep -qF "99.99"               "$file" || fail "order 99.99 missing"
    grep -qF "14.50"               "$file" || fail "order 14.50 missing"
    grep -qF "250.00"              "$file" || fail "order 250.00 missing"
    grep -qF "7.99"                "$file" || fail "order 7.99 missing"
    grep -qF "completed"           "$file" || fail "status 'completed' missing"
    grep -qF "pending"             "$file" || fail "status 'pending' missing"
    grep -qF "cancelled"           "$file" || fail "status 'cancelled' missing"
    ok "data (3 users, 4 orders, all statuses)"
}

# ── main ──────────────────────────────────────────────────────────────────────

echo "==> backup files in $BACKUP_DIR:"
ls -1 "$BACKUP_DIR/"

# 1. Every expected connection must have at least one backup file.
IFS=',' read -ra CONNS <<< "$EXPECT_CONNS"
for conn in "${CONNS[@]}"; do
    shopt -s nullglob
    files=( "$BACKUP_DIR"/${conn}_* )
    shopt -u nullglob
    [[ ${#files[@]} -gt 0 ]] || fail "no backup files for connection '$conn'"
    ok "connection $conn → ${#files[@]} file(s)"
done

# 2. Decode and content-verify the testdb backup from the nominated connection.
echo ""
echo "==> decoding and verifying testdb (conn=$TESTDB_CONN)"

shopt -s nullglob
testdb_files=( "$BACKUP_DIR"/${TESTDB_CONN}_testdb_* )
shopt -u nullglob
[[ ${#testdb_files[@]} -gt 0 ]] || fail "no testdb backup for connection '$TESTDB_CONN'"

decoded=$(decode "${testdb_files[0]}")
echo "   decoded: $(basename "$decoded")"

case "$decoded" in
    *.dump) verify_custom "$decoded" ;;
    *.sql)  verify_plain  "$decoded" ;;
    *) fail "unrecognised decoded file: $decoded" ;;
esac

echo ""
echo "==> PASS"

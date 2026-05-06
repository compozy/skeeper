#!/bin/bash
# full-audit.sh — Run the complete concurrency audit from STATIC-AUDIT.md
# Usage: ./full-audit.sh [path]
# Defaults to current directory

set -euo pipefail

TARGET="${1:-.}"

if [[ ! -d "$TARGET" ]]; then
    echo "ERROR: Target directory not found: $TARGET" >&2
    exit 1
fi

OUTDIR="$(mktemp -d "${TMPDIR:-/tmp}/concurrency-audit.XXXXXX")"

find_first_match() {
    find "$TARGET" "$@" -print -quit 2>/dev/null || true
}

echo "=== Concurrency Audit: $TARGET ==="
echo "Output: $OUTDIR"
echo ""

# Detect language
RUST="$(find_first_match -name '*.rs' -not -path '*/target/*')"
GO="$(find_first_match -name '*.go' -not -path '*/vendor/*')"
PY="$(find_first_match -name '*.py' -not -path '*/__pycache__/*')"
TS="$(find_first_match -name '*.ts' -not -path '*/node_modules/*')"

# Class 2: await-holding-lock (Rust)
if [ -n "$RUST" ]; then
    echo "=== Class 2: Rust await-holding-lock (clippy) ==="
    if command -v cargo >/dev/null 2>&1 && (cd "$TARGET" && cargo locate-project >/dev/null 2>&1); then
        (
            cd "$TARGET"
            cargo +nightly clippy -- -W clippy::await_holding_lock
        ) 2>&1 | tee "$OUTDIR/class2_clippy.log" || true
    else
        echo "Skipping cargo clippy (no Cargo.toml found from $TARGET or cargo unavailable)"
    fi
    echo ""

    echo "=== Class 2: block_on in async ==="
    rg -n --type rust 'block_on' "$TARGET" | tee "$OUTDIR/class2_block_on.log" || true
    echo ""

    echo "=== Class 5: OnceLock/Lazy on init paths ==="
    rg -n --type rust 'OnceLock|OnceCell|Lazy::new|lazy_static|thread_local!' "$TARGET" | tee "$OUTDIR/class5_init.log" || true
    echo ""

    echo "=== Class 8: std::sync::Mutex (poisoning) ==="
    rg -n --type rust 'std::sync::Mutex|std::sync::RwLock' "$TARGET" | tee "$OUTDIR/class8_poison.log" || true
    echo ""

    echo "=== Class 6: unsafe impl Send/Sync ==="
    rg -n --type rust 'unsafe impl (Send|Sync) for' "$TARGET" | tee "$OUTDIR/class6_unsafe_send.log" || true
    echo ""
fi

# Class 4: SQLite (any language)
echo "=== Class 4: Connection opens without busy_timeout ==="
rg -n -e 'Connection::open' -e 'sqlite3\.connect' -e 'new Database' -e 'SqlitePool::connect' "$TARGET" 2>/dev/null | tee "$OUTDIR/class4_opens.log" || true
echo ""

echo "=== Class 4: PRAGMA audit ==="
rg -n -o 'PRAGMA\s+\w+' "$TARGET" 2>/dev/null | sort -u | tee "$OUTDIR/class4_pragmas.log" || true
echo ""

# Class 7: file reservations
echo "=== Class 7: flock / file reservation ==="
rg -n -e 'file_reservation_paths' -e 'flock' -e 'advisory_lock' "$TARGET" 2>/dev/null | tee "$OUTDIR/class7_locks.log" || true
echo ""

# Go-specific
if [ -n "$GO" ]; then
    echo "=== Go: race detector configured? ==="
    rg -rn '\-race' "$TARGET" 2>/dev/null | tee "$OUTDIR/go_race.log" || true

    echo "=== Go: goroutine spawns ==="
    rg -n '\bgo [a-zA-Z_]' "$TARGET" --type go 2>/dev/null | tee "$OUTDIR/go_spawns.log" || true
    echo ""
fi

# Python-specific
if [ -n "$PY" ]; then
    echo "=== Python: threading.Lock in async code ==="
    rg -n 'threading\.Lock' "$TARGET" --type py 2>/dev/null | tee "$OUTDIR/py_threading_lock.log" || true

    echo "=== Python: sync I/O in async functions ==="
    rg -n 'async def' "$TARGET" --type py -A 20 2>/dev/null | rg -e 'requests\.' -e 'open(' -e 'sqlite3\.' | head -20 | tee "$OUTDIR/py_sync_in_async.log" || true
    echo ""
fi

# TypeScript/Node-specific
if [ -n "$TS" ]; then
    echo "=== Node: sync I/O ==="
    rg -n -e 'readFileSync' -e 'writeFileSync' -e 'execSync' "$TARGET" --type ts 2>/dev/null | tee "$OUTDIR/ts_sync_io.log" || true

    echo "=== Node: missing await ==="
    rg -n '\.then\(' "$TARGET" --type ts 2>/dev/null | rg -v -e '\.catch' -e 'await' | head -20 | tee "$OUTDIR/ts_missing_await.log" || true
    echo ""
fi

# Summary
echo "=== SUMMARY ==="
echo ""
shopt -s nullglob
for f in "$OUTDIR"/*.log; do
    name=$(basename "$f" .log)
    count=$(wc -l < "$f" 2>/dev/null || echo 0)
    if [ "$count" -gt 0 ]; then
        echo "  $name: $count hits"
    fi
done
shopt -u nullglob

echo ""
echo "Review each file in $OUTDIR/"
echo "Disposition each hit as BUG / SAFE / UNKNOWN"
echo "See STATIC-AUDIT.md for detailed recipes per class"

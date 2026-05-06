# Static-Audit Recipes

<!-- TOC: Class 1 Classic Mutex | Class 2 Async await | Class 3 Livelock | Class 4 Database | Class 5 LD_PRELOAD | Class 6 Data Races TOCTOU | Class 7 Multi-Process Swarm | Class 8 Poisoning | Class 9 Memory Ordering | Running Full Audit -->

## Table of Contents

- [Class 1 — Classic Mutex Deadlock](#class-1--classic-mutex-deadlock)
- [Class 2 — Async / `.await` Deadlocks](#class-2--async--await-deadlocks)
- [Class 3 — Livelock / Retry Storms](#class-3--livelock--retry-storms)
- [Class 4 — Database Concurrency (SQLite)](#class-4--database-concurrency-sqlite)
- [Class 5 — Runtime-Init / Reentrant Hazards (LD_PRELOAD)](#class-5--runtime-init--reentrant-hazards-ld_preload)
- [Class 6 — Data Races & TOCTOU](#class-6--data-races--toctou)
- [Class 7 — Multi-Process / Swarm Races](#class-7--multi-process--swarm-races)
- [Class 8 — Poisoning & Partial State](#class-8--poisoning--partial-state)
- [Class 9 — Memory Ordering & Lost Wakeups](#class-9--memory-ordering--lost-wakeups)
- [Running the Full Audit](#running-the-full-audit)

Grep-based and ast-grep-based searches that find the nine classes of concurrency hazard without running the program. Use before a runtime repro is available, or after a fix to enforce the [Fourth Instance](THE-FOURTH-INSTANCE.md) rule.

> **Prefer `ast-grep` when structure matters** (e.g., "mutex guard variable alive across `.await`"). **Prefer `ripgrep` when text is enough** (e.g., "find every `Connection::open` that doesn't set `busy_timeout`"). See AGENTS.md §"ast-grep vs ripgrep".

## Class 1 — Classic Mutex Deadlock

### Two-lock acquisition sites (manual review)

```bash
# List all sites where a lock is acquired
rg -n --type rust '\.(lock|read|write)\(\)' > /tmp/lock_sites.txt

# Find functions that acquire more than one lock
ast-grep run -l Rust -p '{
  let $A = $X.$L1();
  $$$
  let $B = $Y.$L2();
  $$$
}' --json | jq '.[] | {file, lines}'

# Review every hit: do ALL call sites acquire the pair in the same order?
```

### RwLock upgrade hazard

```bash
# Thread takes read, then write, on the SAME rwlock → instant self-deadlock
ast-grep run -l Rust -p '
let $R = $L.read()$$$;
$$$
let $W = $L.write()$$$;
'
```

### parking_lot deadlock_detection feature

```toml
# Cargo.toml (debug profile)
[dependencies]
parking_lot = { version = "0.12", features = ["deadlock_detection"] }
```

```rust
// Startup hook (debug builds only)
#[cfg(debug_assertions)]
std::thread::spawn(|| {
    loop {
        std::thread::sleep(std::time::Duration::from_secs(10));
        let deadlocks = parking_lot::deadlock::check_deadlock();
        if deadlocks.is_empty() { continue; }
        eprintln!("DEADLOCK DETECTED ({} cycles)", deadlocks.len());
        for (i, threads) in deadlocks.iter().enumerate() {
            eprintln!("Deadlock #{}", i);
            for t in threads {
                eprintln!("Thread Id {:#?}", t.thread_id());
                eprintln!("{:#?}", t.backtrace());
            }
        }
        std::process::abort();
    }
});
```

## Class 2 — Async / `.await` Deadlocks

### `std::sync::Mutex` held across `.await` (high-ROI check)

```bash
# Find async fns that use std::sync::Mutex at all (flag + review)
ast-grep run -l Rust -p 'async fn $F($$$) { $$$ }' --json \
  | jq -r '.[] | .file' \
  | xargs rg -l 'std::sync::Mutex\b|std::sync::RwLock\b'

# Find explicit `.lock()` guard variables, then grep the same function for `.await`
rg -n --type rust -U 'let\s+(\w+)\s*=\s*\w+\.lock\(\).*\n(?:.*\n){0,30}.*\.await' .

# clippy (nightly) has a lint
cargo +nightly clippy -- -W clippy::await_holding_lock
```

### `block_on` inside async context

```bash
# Every block_on() call site. Each is suspicious until reviewed.
rg -n --type rust '\b(block_on|tokio::runtime::Handle::block_on)\b' .

# Cross-check: is the enclosing fn `async`, or is it a non-async entry?
```

### `rusqlite`/sync-DB call from async

```bash
# Find async fns that reference rusqlite::Connection directly (not in spawn_blocking)
ast-grep run -l Rust -p '
async fn $F($$$) { $$$ rusqlite::Connection $$$ }
'
```

### Unbounded channels (back-pressure hazard)

```bash
rg -n --type rust '\bunbounded_channel\b|mpsc::unbounded\(' .
```

### `Arc<Mutex<T>>` as a `Send` workaround

```bash
rg -n --type rust 'Arc<Mutex<' . > /tmp/arc_mutex.txt
# Review each: is the state actually shared? Could a channel replace it?
```

## Class 3 — Livelock / Retry Storms

### Retry without backoff

```bash
# Common culprit: `loop { if do().is_ok() { break; } }` with no sleep
ast-grep run -l Rust -p 'loop { if $X.$Y().is_ok() { break; } }'

# Look for tight retry on SQLITE_BUSY
rg -n --type rust 'rusqlite::Error::SqliteFailure.*BUSY|ErrorCode::DatabaseBusy' . -A 5
```

### Accept loop without backoff

```bash
rg -n 'accept4|accept\(' --type rust . -A 3 | rg -B 2 'EAGAIN|WouldBlock'
```

## Class 4 — Database Concurrency (SQLite)

### Missing `busy_timeout` on connection open

```bash
# Every connection open site
rg -n 'Connection::open|OpenFlags::SQLITE_OPEN_READ_WRITE|sqlx::SqlitePool::connect' --type rust .

# Centralize with a wrapper:
rg -n 'fn\s+(open|new|connect).*Connection' --type rust .
# → every such fn should call a single `configure_connection(&conn)` helper
#   that sets ALL pragmas.
```

### PRAGMA audit

```bash
# Find every PRAGMA set in the codebase, sorted + deduplicated
rg -n 'PRAGMA\s+\w+' --type rust -o | sort -u
```

Expected baseline (from session evidence):
```
PRAGMA journal_mode     = WAL
PRAGMA synchronous      = NORMAL
PRAGMA busy_timeout     = 5000
PRAGMA foreign_keys     = ON
PRAGMA temp_store       = MEMORY
PRAGMA mmap_size        = 268435456
PRAGMA wal_autocheckpoint = 1000
```

### Long transactions

```bash
# BEGIN without IMMEDIATE (possible upgrade deadlock)
rg -n --type rust -U 'BEGIN\s*;' .
rg -n --type rust '\.transaction\(\)' .
```

## Class 5 — Runtime-Init / Reentrant Hazards (LD_PRELOAD)

```bash
# In any LD_PRELOAD-targeted crate, every blocking primitive is suspect
for sym in OnceLock OnceCell 'Lazy::new' lazy_static 'thread_local!' 'Mutex::new' 'RwLock::new' 'parking_lot::Mutex::new' 'parking_lot::RwLock::new'; do
  echo "=== $sym ==="
  rg -n --type rust "$sym" crates/glibc-rs-abi/ crates/glibc-rs-membrane/
done

# Also: anything that calls the allocator on an init path
rg -n --type rust 'Box::new|vec!\[|String::from|format!|\.to_string\(\)' crates/glibc-rs-abi/

# And std::env::var (reentrant into strlen)
rg -n --type rust 'std::env::var' crates/glibc-rs-abi/ crates/glibc-rs-membrane/
```

### Verify LD_PRELOAD-safe fast path

Every exported ABI function should begin with:

```rust
#[no_mangle]
pub extern "C" fn strlen(s: *const c_char) -> usize {
    if !LIBRARY_READY.load(Ordering::Acquire) {
        return raw_byte_loop_strlen(s);
    }
    // … full policy-checked path …
}
```

Audit:

```bash
ast-grep run -l Rust -p '
#[no_mangle]
pub extern "C" fn $F($$$) -> $R { $$$ }
' --json | jq -r '.[] | .file + ":" + (.range.start.line|tostring)'
# → every hit should start with a LIBRARY_READY check
```

## Class 6 — Data Races & TOCTOU

### TSAN

```bash
# Rust nightly
RUSTFLAGS="-Zsanitizer=thread" \
  cargo +nightly test --target x86_64-unknown-linux-gnu \
  --test-threads=16 2>&1 | tee /tmp/tsan.log

# Filter real races
rg '^WARNING: ThreadSanitizer' /tmp/tsan.log -A 30
```

### Go

```bash
go test -race ./...
```

### Check-then-act gaps

```bash
# if ... { do() } patterns on shared state without a lock around BOTH
ast-grep run -l Rust -p 'if $X.$Y() { $X.$Z(); }'
# Manually review each for TOCTOU race (especially on atomics / files)
```

## Class 7 — Multi-Process / Swarm Races

### Every `file_reservation_paths` call

```bash
rg -n 'file_reservation_paths' .
# Review each: is the pattern scoped tightly? Is there a TTL? Is there an explicit release?
```

### `flock` usage

```bash
rg -n 'flock\(|fcntl::Flock|advisory_lock' .
```

### Missing heartbeat on long-lived reservations

Cross-reference reservation TTL with expected job runtime. If `ttl > job_runtime` isn't guaranteed, add heartbeat or shorten TTL.

## Class 8 — Poisoning & Partial State

```bash
# All std::sync::Mutex usages (Rust default, poisons)
rg -n 'std::sync::Mutex\b|std::sync::RwLock\b' --type rust .

# parking_lot::Mutex does NOT poison — often safer
# Migration check:
rg -n '\.lock\(\)\.unwrap\(\)' --type rust .
# → each is a potential panic site that propagates poisoning
```

## Class 9 — Memory Ordering & Lost Wakeups

```bash
# Relaxed ordering on pointer publication (should be Release/Acquire)
ast-grep run -l Rust -p '$X.store($Y, Ordering::Relaxed)'
ast-grep run -l Rust -p '$X.load(Ordering::Relaxed)'

# Condvar without predicate loop
ast-grep run -l Rust -p '$CV.wait($G)'
# Review: is it `while !predicate { cv.wait(g) }`?

# tokio::sync::Notify: notify_one() before notified().await creates the subscription
rg -n 'notify_one\(\)|notify_waiters\(\)' --type rust .
```

---

## Running the Full Audit

```bash
# Save as scripts/full-audit.sh
#!/bin/bash
set -euo pipefail

echo "=== Class 2: await-holding-lock (clippy) ==="
cargo +nightly clippy -- -W clippy::await_holding_lock 2>&1 | tee /tmp/audit_class2.log || true

echo "=== Class 4: SQLite PRAGMA + busy_timeout ==="
rg -n 'Connection::open|OpenFlags' --type rust | tee /tmp/audit_class4_opens.log
rg -n 'PRAGMA\s+\w+' --type rust -o | sort -u | tee /tmp/audit_class4_pragmas.log

echo "=== Class 5: LD_PRELOAD init hazards (if applicable) ==="
rg -n 'OnceLock|OnceCell|Lazy::new|lazy_static|thread_local!' --type rust | tee /tmp/audit_class5.log || true

echo "=== Class 6: TSAN ==="
RUSTFLAGS="-Zsanitizer=thread" cargo +nightly test --target x86_64-unknown-linux-gnu 2>&1 | tee /tmp/audit_class6.log || true

echo "=== Class 7: file reservations ==="
rg -n 'file_reservation_paths|flock' | tee /tmp/audit_class7.log || true

echo "=== Class 8: std::sync::Mutex (poisoning) ==="
rg -n 'std::sync::Mutex|std::sync::RwLock' --type rust | tee /tmp/audit_class8.log || true

echo "=== Summary ==="
wc -l /tmp/audit_class*.log
```

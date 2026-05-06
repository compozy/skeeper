# Database Concurrency (SQLite-Heavy)

Distilled from the `frankensqlite`, `jeffreys-skills-md`, `mcp-agent-mail-rust`, and codex 2026-02-07 session clusters. SQLite is the overwhelming majority of "database is locked" incidents in this repo; Postgres/MySQL get their own briefer treatment at the bottom.

## The Baseline PRAGMAs (Copy This Exactly)

Every `Connection::open` must flow through a single helper that sets these. Do not scatter PRAGMA calls across the codebase.

```rust
pub fn configure_connection(conn: &rusqlite::Connection) -> rusqlite::Result<()> {
    conn.pragma_update(None, "journal_mode",        &"WAL")?;
    conn.pragma_update(None, "synchronous",         &"NORMAL")?;
    conn.pragma_update(None, "busy_timeout",        &5000)?;   // ms
    conn.pragma_update(None, "foreign_keys",        &"ON")?;
    conn.pragma_update(None, "temp_store",          &"MEMORY")?;
    conn.pragma_update(None, "mmap_size",           &268_435_456i64)?; // 256 MiB
    conn.pragma_update(None, "wal_autocheckpoint",  &1000)?;    // pages
    Ok(())
}
```

**Why each:**

- `journal_mode = WAL` — readers and one writer can proceed concurrently. This is the default you want.
- `synchronous = NORMAL` — durability at transaction boundaries, not every page write. Safe with WAL, 2–5× faster than `FULL`.
- `busy_timeout = 5000` — **the single most important PRAGMA.** Without it, SQLite returns `SQLITE_BUSY` instantly on any lock contention. With it, SQLite retries internally for 5s before giving up. Every "database is locked" incident we've seen started with `busy_timeout = 0` (the default).
- `foreign_keys = ON` — unrelated to concurrency but catch it while you're there.
- `temp_store = MEMORY` — keeps temporary tables off the disk + avoids one more lock path.
- `mmap_size = 256MiB` — lets SQLite read without copying; mostly a performance win, slightly better concurrency.
- `wal_autocheckpoint = 1000` — default is 1000 pages (~4 MiB). Enough to keep the WAL from growing, small enough to not stall writers.

## The Single-Writer Rule

**Multiple processes writing to the same SQLite file is the root cause of most "database is locked" incidents in this repo.** SQLite serializes writes internally, but the loser of the race gets `SQLITE_BUSY` if `busy_timeout` is zero, and without `busy_timeout` set the default driver behavior varies wildly.

**The fix is architectural**: one writer process (a coordinator or actor), any number of reader processes. Writers submit work via IPC (channel, queue, socket, message bus). Readers use a connection pool.

If you must allow multiple writer processes:

1. `busy_timeout` on every connection.
2. Application-level retry with exp-backoff + jitter on `SQLITE_BUSY` **on top of** `busy_timeout`.
3. `BEGIN IMMEDIATE` (not `BEGIN`) for any transaction that will eventually write — this takes the write lock up-front, preventing the "upgrade to write lock after readers have already taken the write-lock candidate" hazard.

## `BEGIN IMMEDIATE` Pattern

```rust
fn do_write(conn: &mut rusqlite::Connection) -> rusqlite::Result<()> {
    let tx = conn.transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
    tx.execute("UPDATE foo SET n = n + 1 WHERE id = ?", [42])?;
    tx.commit()
}
```

A plain `BEGIN` (`DEFERRED`) starts as a reader. When you first execute an `UPDATE`, SQLite tries to upgrade to a writer. If another writer is already in progress, the upgrade fails with `SQLITE_BUSY` and **cannot** be retried — the transaction is now dead. `BEGIN IMMEDIATE` takes the writer up-front, so the retry loop works.

## Retry Wrapper

```rust
use std::time::Duration;
use rand::Rng;

pub fn with_busy_retry<F, T>(mut f: F) -> rusqlite::Result<T>
where
    F: FnMut() -> rusqlite::Result<T>,
{
    let mut attempts = 0;
    loop {
        match f() {
            Ok(v) => return Ok(v),
            Err(e) if is_busy(&e) && attempts < 6 => {
                attempts += 1;
                let base = 20u64 << attempts;          // 40, 80, 160, 320, 640, 1280 ms
                let jitter = rand::thread_rng().gen_range(0..base / 2);
                std::thread::sleep(Duration::from_millis(base + jitter));
            }
            Err(e) => return Err(e),
        }
    }
}

fn is_busy(e: &rusqlite::Error) -> bool {
    matches!(
        e,
        rusqlite::Error::SqliteFailure(
            libsqlite3_sys::Error {
                code: libsqlite3_sys::ErrorCode::DatabaseBusy, ..
            }, _
        )
    )
}
```

## Async + rusqlite: Always `spawn_blocking`

`rusqlite::Connection` is synchronous and `!Send` (wait, it's `Send` but not safe to use across `.await` because every call can block for `busy_timeout` ms). Using it from an async handler directly blocks the tokio worker thread. Fix:

```rust
pub async fn insert_user(pool: Arc<ConnectionPool>, user: User) -> Result<()> {
    tokio::task::spawn_blocking(move || {
        let conn = pool.get()?;
        conn.execute("INSERT INTO users ...", params![user.name])?;
        Ok::<_, Error>(())
    })
    .await?
}
```

Or use `sqlx` or `tokio-rusqlite`, which do this for you.

**Bounding:** `spawn_blocking` dispatches to a separate thread pool. If you spawn unboundedly, the pool fills up and new blocking tasks wait forever. Wrap with a semaphore:

```rust
let permit = db_semaphore.clone().acquire_owned().await?;
tokio::task::spawn_blocking(move || {
    let _permit = permit;  // dropped when the closure returns
    // ... sync DB work ...
})
```

## WAL Checkpoint Starvation

A long-running reader transaction keeps the WAL alive — SQLite cannot reset the WAL file until all readers have moved past the last checkpoint. Writes continue to append, WAL file grows unbounded, and on process restart SQLite replays the whole WAL.

**Rules:**

1. Reader transactions must be short (< 100 ms). If a reader needs to do work, copy the data out and release the txn.
2. On a schedule (or after bulk writes), run `PRAGMA wal_checkpoint(TRUNCATE)` to force a clean reset. `TRUNCATE` mode truncates the WAL file after checkpointing; `FULL` and `RESTART` do more work and block readers longer.
3. Monitor WAL file size. If it's growing past ~100 MiB, something is holding a reader txn open.

## Connection Pool Semantics

A connection pool does not magically fix concurrency. Each connection in the pool has its own lock state. A long-running txn on connection #3 does not block connections #1 and #2 — *unless* the operation takes the write lock, in which case all other writers wait regardless of which connection they come from.

**Rules:**

1. Bound the pool size to the actual parallelism you need.
2. Time out connection acquires (don't wait forever).
3. Never `await` while holding a connection out of the pool.
4. One dedicated writer connection + many readers is often simpler than a true pool.

## Anti-Patterns Observed

From jeffreys-skills-md a5c1ec13:607:

> [Bash — Check if database is locked]
> [Bash — Kill locking process and sync]
> [Bash — Check DB lock holders]

This is the **diagnosis flow of last resort**. If you're using `lsof` to find and `kill` a process holding a lock, you've already lost — the damage is architectural (multi-writer without coordination, no `busy_timeout`, no retry). The fix is not a better kill script; it's rearchitecting so the contention doesn't happen.

From 572c0464:242:

> The lock is getting poisoned. Let me check if project.rs also needs the shared lock.

Shared test fixtures (`env_lock`, `cwd_lock`) with `std::sync::Mutex` + a panic = cascade failure. Every subsequent test gets `PoisonError`. Fix options:

1. **`parking_lot::Mutex`** — does not poison; returns the guard even if the previous holder panicked. Callers must handle partial state explicitly.
2. **Transactional updates** — build the new state in a local variable, then swap at the end of the critical section. A panic mid-build leaves shared state intact.
3. **`catch_unwind` around the critical section** — converts panics to errors before the lock drops.

## PostgreSQL / MySQL (Briefer)

- **Connection-per-request is fine** (these DBs have proper multi-writer concurrency).
- **Deadlocks still happen** via row-level locks taken in different orders. The DB detects them and kills one transaction — retry with exp-backoff.
- **Long-running transactions on Postgres** bloat MVCC. Monitor `pg_stat_activity` for idle-in-transaction.
- **Serializable isolation** turns many race conditions into deadlocks. Handle `SerializationFailure` like you'd handle `SQLITE_BUSY`.

## Validation

- [ ] Every `Connection::open` goes through `configure_connection()`.
- [ ] `busy_timeout >= 5000` on every connection.
- [ ] Every writer txn uses `BEGIN IMMEDIATE`.
- [ ] Every rusqlite call from an async handler is inside `spawn_blocking`.
- [ ] `spawn_blocking` calls are bounded by a `Semaphore`.
- [ ] WAL file size is bounded (monitored or checkpointed on schedule).
- [ ] Shared test fixtures use `parking_lot::Mutex` or transactional updates.
- [ ] Static audit [STATIC-AUDIT.md §Class 4](STATIC-AUDIT.md#class-4--database-concurrency-sqlite) shows no hits.

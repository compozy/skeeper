# Rust Concurrency Cookbook

<!-- TOC: Primitives | Tokio | std vs parking_lot vs tokio::sync | Crossbeam | Rayon | Dashmap | Sqlx & Rusqlite | Axum/Tower/Hyper | arc-swap | OnceLock | Loom & Miri | Tokio-Console | Diagnosis | Creative Patterns | Anti-Patterns | Code Recipes -->

## Table of Contents

- [The Landscape](#the-landscape)
- [Primitive Selection Matrix](#primitive-selection-matrix)
- [Tokio in Depth](#tokio-in-depth)
- [std::sync vs parking_lot vs tokio::sync](#stdsync-vs-parking_lot-vs-tokiosync)
- [Channels Deep Dive](#channels-deep-dive)
- [Crossbeam](#crossbeam)
- [Rayon](#rayon)
- [Dashmap and Concurrent Maps](#dashmap-and-concurrent-maps)
- [SQLx and rusqlite](#sqlx-and-rusqlite)
- [Axum / Tower / Hyper Patterns](#axum--tower--hyper-patterns)
- [arc-swap and RCU-style](#arc-swap-and-rcu-style)
- [OnceLock, OnceCell, Lazy](#oncelock-oncecell-lazy)
- [Loom and Miri](#loom-and-miri)
- [tokio-console](#tokio-console)
- [Send / Sync Bounds](#send--sync-bounds)
- [Pin and Futures](#pin-and-futures)
- [Diagnosis Recipes](#diagnosis-recipes)
- [Creative Patterns (Actor, Watch, Epoch)](#creative-patterns-actor-watch-epoch)
- [Rust-Specific Anti-Patterns](#rust-specific-anti-patterns)
- [Code Recipe Library (30+)](#code-recipe-library-30)
- [Audit Commands](#audit-commands)

---

## The Landscape

Rust has the best concurrency story of any mainstream language for two reasons:

1. **The compiler enforces memory safety across threads.** Data races are a compile error, not a runtime heisenbug. If your code compiles and uses only safe APIs, the only classes of concurrency bug you can have are: deadlock, livelock, starvation, priority inversion, lost wakeup, cross-process races, and logic errors.
2. **The ecosystem has genuine choice.** You can pick sync or async, std or parking_lot, tokio or smol or embassy, channels or shared state, and the choices are real — not wrappers over the same thing. This is a strength (you can right-size) and a trap (wrong choice = deadlock).

The nine classes from the main [SKILL.md](../SKILL.md) all show up in Rust, but the *density* of incidents is concentrated in Classes 2 (async), 4 (DB), 5 (runtime-init), and 1 (classic deadlock with `parking_lot`).

## Primitive Selection Matrix

The single most important decision. Get this wrong and every subsequent line of code is defensive against the choice.

| You need… | In **sync** code | In **async** code |
|-----------|------------------|-------------------|
| Mutual exclusion, short hold | `parking_lot::Mutex` | `std::sync::Mutex` (drop guard before `.await`!) |
| Mutual exclusion, hold across await | — | `tokio::sync::Mutex` |
| Reader/writer | `parking_lot::RwLock` | `tokio::sync::RwLock` (or snapshot + `arc-swap`) |
| One-shot init | `std::sync::OnceLock` | same (init in a spawn_blocking if init blocks) |
| Atomic counter/flag | `std::sync::atomic::*` | same |
| Condition variable | `parking_lot::Condvar` | `tokio::sync::Notify` or `watch` channel |
| MPSC channel | `std::sync::mpsc` or `crossbeam::channel` | `tokio::sync::mpsc` |
| SPSC channel, high perf | `rtrb` / `ringbuf` | `rtrb` / `ringbuf` |
| MPMC channel | `crossbeam::channel` | `async_channel` or `flume` |
| Broadcast (fanout) | — | `tokio::sync::broadcast` |
| Single-producer latest-value | — | `tokio::sync::watch` |
| Concurrent hashmap | `dashmap::DashMap` or `parking_lot::RwLock<HashMap>` | same |
| Semaphore | `std::sync::Mutex<u32>` + Condvar, or `parking_lot::Mutex<u32>` | `tokio::sync::Semaphore` |
| Thread-safe Arc swap | `arc-swap::ArcSwap` | same |
| CPU-parallel work | `rayon` | `rayon::ThreadPool` inside `spawn_blocking` |
| Work-stealing executor | `rayon` | `tokio` |

**Rules of thumb:**

- **In async code, start with `std::sync::Mutex`, not `tokio::sync::Mutex`**. `std::sync::Mutex` is faster; you just can't hold the guard across `.await`. If you find yourself needing to hold across `.await`, think twice: can you drop the guard first? If not, use `tokio::sync::Mutex`.
- **Default to channels, not shared state.** An actor with an `mpsc::Receiver` inbox has zero lock-ordering bugs.
- **`dashmap` is not a drop-in replacement for `Mutex<HashMap>`.** It uses striped locking; iterators hold locks; getting + inserting is two separate lock acquisitions. See the Dashmap section.
- **`parking_lot` does not poison, `std::sync` does.** Choose based on how you handle panics in critical sections.

## Tokio in Depth

Tokio is the de facto async runtime. The runtime model is:

- **N worker threads** (default = `num_cpus()`) — each runs an event loop that polls futures
- **A blocking-pool** (default = 512 threads) — for `spawn_blocking` work
- **A scheduler** — work-stealing across workers, with a per-worker task queue

**The cardinal rule:** a worker thread cannot be blocked. If a worker stops polling its tasks, every task on that worker is dead until the worker is free again.

**Things that block a worker:**
- `std::sync::Mutex::lock()` when contended
- `std::thread::sleep`
- Synchronous I/O (`std::fs::*`, `std::net::*`, `rusqlite::*`, `reqwest::blocking::*`)
- CPU-heavy computation (long loops without `yield_now`)
- `block_on` from inside an async context
- Native C library calls (via FFI) that do I/O

**The fix for every one of these** is `tokio::task::spawn_blocking`:

```rust
let result = tokio::task::spawn_blocking(move || {
    // Sync code runs on a blocking-pool thread, not a worker
    expensive_sync_work()
}).await?;
```

### Task starvation detection

One task monopolizing a worker is visually indistinguishable from a deadlock from the outside. Detect it:

```bash
# Enable tokio-console in Cargo.toml:
# tokio = { version = "1", features = ["full", "tracing"] }
# console-subscriber = "0.2"

# In main.rs:
console_subscriber::init();

# Then run and in another terminal:
tokio-console
```

Look for tasks with `busy > 100ms` — those are blocking their worker. `tokio-console` will tell you which task.

Without tokio-console, use gdb (see `DIAGNOSIS.md`):

```bash
gdb --batch -ex "thread apply all bt 5" -p $PID 2>&1 | grep -A5 'tokio-runtime'
```

If any worker is NOT in `epoll_wait`, that worker is busy. Look at what.

### The spawn_blocking semaphore pattern

Unbounded `spawn_blocking` exhausts the blocking pool:

```rust
// BUG: unbounded
for item in items {
    tokio::task::spawn_blocking(move || heavy_work(item));
}
```

Fix with `Arc<Semaphore>`:

```rust
use std::sync::Arc;
use tokio::sync::Semaphore;

let sem = Arc::new(Semaphore::new(16));  // max 16 concurrent
let mut handles = vec![];
for item in items {
    let permit = sem.clone().acquire_owned().await?;
    handles.push(tokio::task::spawn_blocking(move || {
        let _p = permit;  // dropped at end of closure
        heavy_work(item)
    }));
}
for h in handles { h.await??; }
```

### Task cancellation and drop safety

When a `JoinHandle` is dropped without being awaited, tokio lets the task keep running — that's often what you want, but not always. `tokio::select!` cancels the *losing* branch: if you `select!` over `fut_a` and `fut_b`, whichever completes first, the other is dropped mid-execution. Any `Drop` impl fires; any `.await` point becomes the cancellation point.

This makes **every `.await` a potential drop point**. Design your code so that being dropped mid-await leaves shared state consistent:

```rust
// BAD: if dropped between these two steps, DB state is torn
async fn transfer(db: &Db, from: Id, to: Id, amt: u64) {
    db.debit(from, amt).await;   // dropped here → half-applied
    db.credit(to, amt).await;
}

// GOOD: one transaction, drop-safe
async fn transfer(db: &Db, from: Id, to: Id, amt: u64) -> Result<()> {
    let mut tx = db.begin().await?;
    tx.debit(from, amt).await?;
    tx.credit(to, amt).await?;
    tx.commit().await        // if dropped before commit, tx auto-rollback
}
```

Structured concurrency crates like `tokio::task::JoinSet` + `CancellationToken` make this more explicit.

### The Tokio thread model cheatsheet

```
┌──────────────────────────────────────────────────────┐
│ Tokio runtime                                        │
├──────────────────────────────────────────────────────┤
│ Worker threads (N = num_cpus)                        │
│ ┌────────────────────┐                               │
│ │ tokio-runtime-wN   │ ← NEVER block these           │
│ │   ▸ poll task A    │                               │
│ │   ▸ poll task B    │                               │
│ │   ▸ epoll_wait     │ ← idle (healthy)              │
│ └────────────────────┘                               │
│                                                      │
│ Blocking pool (default 512 threads, grows on demand) │
│ ┌────────────────────┐                               │
│ │ tokio-runtime-bN   │ ← OK to block                 │
│ │   ▸ spawn_blocking │                               │
│ └────────────────────┘                               │
└──────────────────────────────────────────────────────┘
```

Inspection:

```bash
ps -Lp $PID -o tid,pcpu,comm --no-headers | sort -k2 -nr | head
# tokio-runtime-w  = workers (should be low CPU OR in epoll)
# tokio-runtime-b  = blocking pool (may be high CPU, that's OK)
# signal-hook      = signal handling thread
# main             = the main thread
```

## std::sync vs parking_lot vs tokio::sync

**Cheat sheet:**

| Property | `std::sync::Mutex` | `parking_lot::Mutex` | `tokio::sync::Mutex` |
|----------|--------------------|-----------------------|----------------------|
| Speed (uncontended) | ~60ns | ~20ns | ~150ns |
| Speed (contended) | slower | faster | context switch |
| Poisons on panic | Yes | **No** | No |
| Can hold across `.await` | **No** | **No** | Yes |
| Fairness | Unfair | Configurable | FIFO (ordered) |
| Size | ~40 bytes | ~8 bytes | ~48 bytes |
| `try_lock_for(duration)` | No | **Yes** | No (use `timeout`) |
| Uses kernel futex | Yes | No (on Linux) | Only via tokio runtime |

**When to pick which:**

- **`parking_lot::Mutex`** — default for sync code. Faster, smaller, no poison, `try_lock_for`.
- **`std::sync::Mutex`** — when you *want* poison semantics (to fail-fast on panic in critical section) or when you need zero dependencies.
- **`tokio::sync::Mutex`** — ONLY when you must hold a guard across `.await` AND can't restructure. It's slower and every `.lock()` is a task yield point.

**Common mistake:** using `tokio::sync::Mutex` everywhere because "it's async". It's not a better default; it's a fallback.

## Channels Deep Dive

Rust has too many channels. Here's the decision tree:

```
Need a channel?
│
├─ Sync code (no async)?
│  ├─ Single sender, single receiver, max perf → rtrb, ringbuf
│  ├─ Multi-producer → crossbeam::channel
│  └─ Just stdlib → std::sync::mpsc
│
├─ Async code?
│  ├─ Task-to-task, low volume → tokio::sync::mpsc
│  ├─ High-volume fanout → tokio::sync::broadcast (each receiver sees all)
│  ├─ Single-writer, "latest value" semantics → tokio::sync::watch
│  ├─ One-shot reply → tokio::sync::oneshot
│  ├─ MPMC (many senders, many receivers) → async-channel or flume
│  └─ Sync-to-async bridge → flume (both sides work)
```

### Bounded vs unbounded

**Always bound.** Unbounded is a memory leak. The bound is a design decision:

- **Small bound (1–16)** — tight backpressure; senders slow down when consumer is slow. Use when you care more about latency than throughput.
- **Medium bound (100–10_000)** — typical web handler fan-out; burst tolerance.
- **Large bound (100k+)** — you're building a queue, not a channel. Consider a real queue (Redis, SQS, NATS).

### The `Sender::send().await` backpressure trap

```rust
// BUG: all workers are blocked sending into a full channel
// whose consumer is itself waiting for a reply via a DIFFERENT channel
// that is also full. Both channels are full. Deadlock.
```

Channel cycles are the async equivalent of AB-BA deadlock. Fix: break the cycle (one direction is a oneshot reply, or use `try_send` with drop-oldest).

### Broadcast lagged receiver

`tokio::sync::broadcast` drops messages for slow receivers and returns `RecvError::Lagged`. Handle it:

```rust
loop {
    match rx.recv().await {
        Ok(msg) => handle(msg),
        Err(broadcast::error::RecvError::Lagged(n)) => {
            tracing::warn!("receiver lagged, dropped {n} messages");
            // resync from a source of truth if needed
        }
        Err(broadcast::error::RecvError::Closed) => break,
    }
}
```

### The `watch` channel for state distribution

When many readers need "the current value, whatever it is":

```rust
let (tx, mut rx) = tokio::sync::watch::channel(State::default());

// Writer updates:
tx.send_modify(|state| state.counter += 1);

// Readers subscribe, always see the latest:
loop {
    rx.changed().await?;
    let current = rx.borrow().clone();
    // ...
}
```

This is the idiomatic Rust equivalent of `rcu_read_lock` / `rcu_read_unlock`. No mutex.

## Crossbeam

`crossbeam` is the performance-critical concurrency library for *synchronous* Rust. Key components:

- `crossbeam::channel` — the high-perf alternative to `std::sync::mpsc`. Supports MPMC, `select!`, timeouts.
- `crossbeam::epoch` — epoch-based memory reclamation for lock-free data structures.
- `crossbeam::queue::ArrayQueue` / `SegQueue` — lock-free queues.
- `crossbeam::deque::Worker<T>` — work-stealing deque (this is what `rayon` uses under the hood).
- `crossbeam::utils::CachePadded<T>` — pad a value to a cache line to avoid false sharing.

### `crossbeam::select!` vs `tokio::select!`

```rust
use crossbeam::channel::select;

select! {
    recv(rx_a) -> msg => handle_a(msg?),
    recv(rx_b) -> msg => handle_b(msg?),
    default(Duration::from_millis(100)) => timeout(),
}
```

vs. async:

```rust
tokio::select! {
    msg = rx_a.recv() => handle_a(msg?),
    msg = rx_b.recv() => handle_b(msg?),
    _ = tokio::time::sleep(Duration::from_millis(100)) => timeout(),
}
```

**Priority in `select!`:** both macros pick a ready branch non-deterministically (roughly uniform random). If you need priority, check branches manually in `poll_*` style or use `biased;` in `tokio::select!`:

```rust
tokio::select! {
    biased;
    msg = high_priority.recv() => ...,
    msg = normal.recv()       => ...,
}
```

### Epoch-based reclamation

When you have a lock-free data structure that uses raw pointers, you need a way to safely reclaim memory that another thread might still be reading. Epoch-based reclamation tracks "which epoch each reader is in" and only frees when no reader is in an older epoch.

```rust
use crossbeam::epoch::{self, Atomic, Owned};
use std::sync::atomic::Ordering;

let ptr: Atomic<MyData> = Atomic::new(MyData::new());

// Reader
let guard = epoch::pin();
let shared = ptr.load(Ordering::Acquire, &guard);
if let Some(data) = unsafe { shared.as_ref() } {
    // Use data — guaranteed valid while `guard` lives
}

// Writer (replace the pointer, defer free)
let new = Owned::new(MyData::new());
let old = ptr.swap(new, Ordering::AcqRel, &guard);
unsafe { guard.defer_destroy(old); }  // freed when safe
```

Use when: you need a lock-free hashmap, stack, or queue and the cost of reference counting is too high.

## Rayon

Rayon is CPU-parallel iterators built on work-stealing. It's for the problem "I have a `Vec<T>` and I want to `.map` it across all cores."

```rust
use rayon::prelude::*;

let result: Vec<_> = items
    .par_iter()
    .map(|x| expensive_compute(x))
    .collect();
```

**Common pitfalls:**

1. **Using rayon from a tokio worker thread** — rayon's work-stealing blocks the calling thread until the parallel op completes, which blocks a tokio worker. Wrap in `spawn_blocking`:

   ```rust
   let result = tokio::task::spawn_blocking(|| {
       items.par_iter().map(expensive).collect()
   }).await?;
   ```

2. **Recursive `par_iter` blowing the stack** — `par_iter` inside a `par_iter` each consume a rayon thread; deep recursion can deadlock if the pool is exhausted.

3. **Shared mutable state** — rayon closures must be `Send`. Use `Mutex<T>` or (better) `fold` + `reduce`:

   ```rust
   let sum: i64 = items.par_iter()
       .map(|x| x.value as i64)
       .sum();
   ```

4. **`par_iter_mut()` can deadlock** — if you call back into rayon from inside the closure on the same pool.

**Custom thread pools:**

```rust
let pool = rayon::ThreadPoolBuilder::new()
    .num_threads(4)
    .thread_name(|i| format!("heavy-pool-{}", i))
    .build()?;

let result = pool.install(|| {
    items.par_iter().map(heavy_work).collect::<Vec<_>>()
});
```

Dedicated pools avoid interactions with the default pool (which tokio's `spawn_blocking` might also touch).

## Dashmap and Concurrent Maps

`dashmap::DashMap` is a "concurrent hashmap" — but the semantics are subtler than you'd think. It uses **striped locking**: internally there are N (default 4 * num_cpus) `RwLock<HashMap>` shards. `get` takes a read lock on one shard; `insert` takes a write lock on one shard.

**Deadlock hazard #1: holding a guard across a second access**

```rust
let map: DashMap<i32, String> = DashMap::new();
// ...
let guard = map.get(&1).unwrap();       // holds read lock on shard
map.insert(1, "new".to_string());       // tries write lock on SAME shard → deadlock
// guard dropped too late
```

Fix: clone the value out before the second access, or restructure:

```rust
let value = map.get(&1).map(|r| r.clone());
map.insert(1, "new".to_string());
```

**Deadlock hazard #2: iteration + mutation**

```rust
for entry in map.iter() {         // holds read locks on ALL shards
    if entry.value().is_old() {
        map.remove(entry.key());  // write lock → deadlock
    }
}
```

Fix: collect keys first, then mutate:

```rust
let old_keys: Vec<_> = map.iter()
    .filter(|e| e.value().is_old())
    .map(|e| *e.key())
    .collect();  // read locks released
for k in old_keys {
    map.remove(&k);
}
```

**When to use `parking_lot::RwLock<HashMap>` instead:**

- When you do bulk operations and want one lock for the whole map
- When you want to serialize multiple operations together
- When you want to use `HashMap`'s full API without adapter surface area

## SQLx and rusqlite

See also [DATABASE.md](DATABASE.md) for the cross-cutting concurrency rules.

### `rusqlite` — synchronous, careful in async

```rust
// CORRECT: wrap sync rusqlite in spawn_blocking
use std::sync::Arc;
use tokio::sync::Semaphore;
use r2d2::Pool;
use r2d2_sqlite::SqliteConnectionManager;

#[derive(Clone)]
pub struct Db {
    pool: Pool<SqliteConnectionManager>,
    write_sem: Arc<Semaphore>,  // serialize writes
}

impl Db {
    pub async fn insert_user(&self, name: &str) -> anyhow::Result<i64> {
        let _permit = self.write_sem.clone().acquire_owned().await?;
        let pool = self.pool.clone();
        let name = name.to_string();
        tokio::task::spawn_blocking(move || {
            let conn = pool.get()?;
            configure_connection(&conn)?;  // every connection gets PRAGMAs
            conn.execute("INSERT INTO users (name) VALUES (?1)", [name])?;
            Ok(conn.last_insert_rowid())
        }).await?
    }
}

fn configure_connection(conn: &rusqlite::Connection) -> rusqlite::Result<()> {
    conn.pragma_update(None, "journal_mode",       &"WAL")?;
    conn.pragma_update(None, "synchronous",        &"NORMAL")?;
    conn.pragma_update(None, "busy_timeout",       &5000)?;
    conn.pragma_update(None, "foreign_keys",       &"ON")?;
    conn.pragma_update(None, "wal_autocheckpoint", &1000)?;
    Ok(())
}
```

### `sqlx` — native async, still has traps

```rust
use sqlx::sqlite::{SqlitePool, SqliteConnectOptions};
use std::str::FromStr;

let opts = SqliteConnectOptions::from_str("sqlite:./db.sqlite")?
    .create_if_missing(true)
    .journal_mode(sqlx::sqlite::SqliteJournalMode::Wal)
    .synchronous(sqlx::sqlite::SqliteSynchronous::Normal)
    .busy_timeout(Duration::from_secs(5))
    .foreign_keys(true);

let pool = SqlitePool::connect_with(opts).await?;
```

**Traps:**

1. **Interactive transactions** (`pool.begin().await?`) hold a connection out of the pool. Long transactions = pool exhaustion.
2. **`.fetch_all()` vs `.fetch()` streaming** — `.fetch` streams rows and holds the connection until the stream is dropped. If you `.await` something else in the middle, the connection is held across await.
3. **SQLite pool size > 1 is risky** — multiple writer connections fight for the WAL lock. Pool size = 1 for writes, separate pool for reads is safer.

## Axum / Tower / Hyper Patterns

### The `State<T>` extractor

```rust
#[derive(Clone)]
struct AppState {
    db: Db,
    cache: Arc<parking_lot::RwLock<Cache>>,
}

async fn handler(State(state): State<AppState>) -> impl IntoResponse {
    let cached = state.cache.read().get(&key).cloned();
    // ...
}
```

**Rules:**
- `AppState` must be `Clone + Send + Sync + 'static`.
- Clone is cheap (Arc inside) — Axum clones it per request.
- **Never hold a write lock across `.await` in a handler.** (The default Rust concurrency rule.)

### Tower middleware ordering

Tower services compose: `Router::new().layer(A).layer(B).layer(C)` wraps the service as `C(B(A(service)))`. Think of layers as decorators.

**Deadlock hazard:** a middleware that acquires a lock on request and releases on response, wrapping a handler that acquires the same lock. Classic AB-BA if another request is in progress.

Fix: use `tower::limit::ConcurrencyLimit` for connection limiting instead of ad-hoc locks.

### `Service::poll_ready`

A Tower `Service` has two phases: `poll_ready` (is it ready for another request?) and `call` (handle this request). The pattern is:

```rust
// CALLER:
service.ready().await?;
let resp = service.call(req).await?;
```

A common mistake is calling `call` without `ready` — the service may be at capacity (semaphore exhausted, rate-limited, backoff). Always `ready().await` first.

## arc-swap and RCU-style

`arc_swap::ArcSwap<T>` is "atomic swap of an `Arc<T>`". Readers get a cheap `Guard<Arc<T>>` with no locks; writers replace the whole value.

```rust
use arc_swap::ArcSwap;
use std::sync::Arc;

struct Config { /* ... */ }

let config = ArcSwap::from_pointee(Config::load());

// Reader (hot path, no locks)
let current = config.load();
// use &**current
drop(current);

// Writer (cold path, atomic replace)
let new_config = Config::load_from_env();
config.store(Arc::new(new_config));
```

**Use when:** reads vastly outnumber writes, and writers can afford to build a whole new value. Typical: config reload, feature flags, routing tables, prepared statement caches.

**Don't use when:** you need to update *part* of the state cheaply. The whole `T` is replaced every write.

This is essentially a minimal RCU (read-copy-update) for Rust, and it's wait-free for readers.

## OnceLock, OnceCell, Lazy

Rust's one-time initialization primitives. Use them for static data that's computed on first access.

```rust
use std::sync::OnceLock;

static CONFIG: OnceLock<Config> = OnceLock::new();

pub fn config() -> &'static Config {
    CONFIG.get_or_init(|| Config::load_from_env())
}
```

**Gotchas:**

1. **Panic inside `get_or_init` closure:** on Rust ≥1.70, returns `PoisonError` next call. Pre-1.70, the OnceLock was permanently deadlocked. Upgrade Rust.
2. **Reentrant call:** if `get_or_init`'s closure calls `config()` again (same OnceLock), it deadlocks. Never do substantial work in the closure — just load and return.
3. **In LD_PRELOAD or `.init_array`:** forbidden. See [LD-PRELOAD.md](LD-PRELOAD.md).
4. **Fallible init:** `OnceLock<Result<T, E>>` is the idiomatic pattern:
   ```rust
   static DB: OnceLock<Result<Db, String>> = OnceLock::new();
   fn db() -> Result<&'static Db, &'static String> {
       DB.get_or_init(|| Db::connect().map_err(|e| e.to_string())).as_ref()
   }
   ```

## Loom and Miri

**Loom** is a permutation-testing model checker. It exhaustively explores all possible orderings of operations in a small test:

```rust
#[cfg(loom)]
use loom::sync::{Arc, Mutex};
#[cfg(not(loom))]
use std::sync::{Arc, Mutex};

#[cfg(loom)]
#[test]
fn test_counter() {
    loom::model(|| {
        let counter = Arc::new(Mutex::new(0));
        let t1 = loom::thread::spawn({
            let c = counter.clone();
            move || *c.lock().unwrap() += 1
        });
        let t2 = loom::thread::spawn({
            let c = counter.clone();
            move || *c.lock().unwrap() += 1
        });
        t1.join().unwrap();
        t2.join().unwrap();
        assert_eq!(*counter.lock().unwrap(), 2);
    });
}
```

Run: `RUSTFLAGS="--cfg loom" cargo test --release --lib`

**Use for:** designing a lock-free primitive, verifying your atomic-order choices, testing that a small fan-out/fan-in is correct.

**Don't use for:** whole applications (exponential blow-up). Loom is for the core 20 lines of concurrency code.

**Miri** runs code on the Rust abstract machine; catches undefined behavior, data races, use-after-free, uninitialized reads, memory ordering violations:

```bash
rustup +nightly component add miri
cargo +nightly miri test
```

Slow; use on unit tests of the primitive, not the full suite.

## tokio-console

The observability tool for tokio. Enable in `Cargo.toml`:

```toml
[dependencies]
tokio = { version = "1", features = ["full", "tracing"] }
console-subscriber = "0.2"
```

In `main`:

```rust
fn main() {
    console_subscriber::init();
    tokio::runtime::Runtime::new().unwrap().block_on(async {
        // app
    });
}
```

Run your program, then in another terminal:

```bash
tokio-console
```

Screens:
- **Tasks** — every task with `busy`, `idle`, `polls`, `total`, `scheduled`. `busy > 100ms` = suspect.
- **Resources** — every mutex, semaphore, channel. Shows waiters.
- **Details** — per-task: where it was spawned, last awoke, current state.

This is how you diagnose Class 2 (async deadlock) without gdb.

## Send / Sync Bounds

Every deadlock discussion in Rust eventually hits `Send`/`Sync`. The rules:

- `Send` = safe to move between threads (`tokio::spawn` requires `Send + 'static`)
- `Sync` = safe to share (`&T`) between threads
- Auto-derived for types whose fields are `Send`/`Sync`

**When you can't make a type Send**, the usual mistake is wrapping in `Arc<Mutex>`:

```rust
// Rc is !Send
let data = Rc::new(RefCell::new(vec![]));
tokio::spawn(async move {
    data.borrow_mut().push(1);  // compile error: Rc is !Send
});

// BAD: paper over with Arc<Mutex>
let data = Arc::new(Mutex::new(vec![]));
tokio::spawn(async move {
    data.lock().unwrap().push(1);  // works, but introduces lock contention
});

// GOOD: refactor to owned data
let mut data = vec![];
tokio::spawn(async move {
    data.push(1);  // no shared state
});

// GOOD: message passing
let (tx, mut rx) = tokio::sync::mpsc::channel::<i32>(16);
tokio::spawn(async move {
    let mut data = vec![];
    while let Some(x) = rx.recv().await { data.push(x); }
});
// senders: tx.send(1).await?;
```

**`unsafe impl Send for T`** is a hand-written promise to the compiler. Every one is a potential UB. Review each.

## Pin and Futures

Most users never touch `Pin` directly, but it shows up in three places:

1. **`select!` branches must be `Unpin`** or pinned with `tokio::pin!`:
   ```rust
   let fut = some_async_fn();
   tokio::pin!(fut);
   tokio::select! {
       result = &mut fut => ...,
       _ = shutdown.recv() => ...,
   }
   ```
2. **Self-referential state machines** (generated by `async fn`) are `!Unpin` and must be pinned before polling.
3. **Boxed futures** (`Box<dyn Future>`) must be `Pin<Box<dyn Future>>`.

`Pin` is not a concurrency primitive, but misunderstanding it can look like a deadlock (futures that were never polled).

## Diagnosis Recipes

### R1 — Tokio handler hangs under load

```bash
# 1. Confirm the process is alive
ps -p $PID

# 2. What are the workers doing?
gdb --batch -ex "set pagination off" \
  -ex "thread apply all bt 3" -p $PID 2>&1 > /tmp/bt.txt
grep -A5 'tokio-runtime' /tmp/bt.txt

# 3. Launch tokio-console (if enabled)
tokio-console

# 4. If no console, check for std::sync::Mutex held in async contexts
cargo +nightly clippy -- -W clippy::await_holding_lock
```

### R2 — "Why is my rayon job stuck?"

```bash
# rayon uses its own thread pool; look for rayon-* threads in gdb
gdb --batch -ex "thread apply all bt 3" -p $PID 2>&1 | grep -A5 'rayon-'

# Check for nested rayon calls (can deadlock the pool)
rg -n --type rust 'par_iter|par_bridge' src/
```

### R3 — `dashmap` deadlock

```bash
# gdb trace to find the lock on the striped shard
gdb --batch -ex "thread apply all bt full" -p $PID 2>&1 | \
  grep -B5 'dashmap::RawTable::lock\|parking_lot::RawRwLock'
```

Almost always an iterator held while mutating.

### R4 — OnceLock deadlock

```bash
# Trace: where is the thread blocked?
gdb --batch -ex "thread apply all bt" -p $PID 2>&1 | grep -B5 -A3 'OnceLock'
```

Usually means the closure is calling `get_or_init` again (recursive), or panicked on an old Rust version (upgrade to ≥1.70).

## Creative Patterns (Actor, Watch, Epoch)

### The Actor Pattern

```rust
use tokio::sync::{mpsc, oneshot};

enum Command {
    Get { key: String, reply: oneshot::Sender<Option<String>> },
    Set { key: String, value: String, reply: oneshot::Sender<()> },
}

pub struct CacheHandle {
    tx: mpsc::Sender<Command>,
}

impl CacheHandle {
    pub async fn get(&self, key: String) -> Option<String> {
        let (reply, rx) = oneshot::channel();
        self.tx.send(Command::Get { key, reply }).await.ok()?;
        rx.await.ok().flatten()
    }
    pub async fn set(&self, key: String, value: String) {
        let (reply, rx) = oneshot::channel();
        let _ = self.tx.send(Command::Set { key, value, reply }).await;
        let _ = rx.await;
    }
}

fn spawn_cache() -> CacheHandle {
    let (tx, mut rx) = mpsc::channel(32);
    tokio::spawn(async move {
        let mut state: HashMap<String, String> = HashMap::new();
        while let Some(cmd) = rx.recv().await {
            match cmd {
                Command::Get { key, reply } => {
                    let _ = reply.send(state.get(&key).cloned());
                }
                Command::Set { key, value, reply } => {
                    state.insert(key, value);
                    let _ = reply.send(());
                }
            }
        }
    });
    CacheHandle { tx }
}
```

Benefits:
- State is **private to one task**. No `Mutex`, no lock ordering, no `Send`/`Sync` games on the state itself.
- Callers interact via a typed command enum.
- The actor serializes access by construction.

This is the single most important pattern for medium-sized Rust applications. Use it instead of `Arc<Mutex<State>>` whenever possible.

### The Watch Channel for Coalescing Updates

When N readers need "the latest value of X", and you don't care if some readers miss intermediate values:

```rust
let (tx, rx) = tokio::sync::watch::channel(Config::default());

// Writer (can update as fast as it wants)
tokio::spawn(async move {
    loop {
        tokio::time::sleep(Duration::from_secs(60)).await;
        tx.send(Config::reload().await).ok();
    }
});

// Each reader:
tokio::spawn({
    let mut rx = rx.clone();
    async move {
        loop {
            let config = rx.borrow().clone();
            do_work_with(&config).await;
            rx.changed().await.ok();  // block until next update
        }
    }
});
```

### Epoch-Based GC with crossbeam

See the Crossbeam section. Use when you've written a lock-free data structure and need safe memory reclamation.

### Single-Writer Log

Append-only logs (e.g., for event sourcing) map naturally to single-writer:

```rust
struct EventLog {
    tx: mpsc::Sender<Event>,
}

impl EventLog {
    pub async fn append(&self, e: Event) -> Result<()> {
        self.tx.send(e).await.map_err(|_| anyhow!("log closed"))
    }
}

fn spawn_logger(path: PathBuf) -> EventLog {
    let (tx, mut rx) = mpsc::channel(1024);
    tokio::spawn(async move {
        let file = tokio::fs::OpenOptions::new()
            .append(true).create(true).open(&path).await.unwrap();
        let mut writer = tokio::io::BufWriter::new(file);
        while let Some(e) = rx.recv().await {
            let bytes = serde_json::to_vec(&e).unwrap();
            writer.write_all(&bytes).await.unwrap();
            writer.write_all(b"\n").await.unwrap();
            writer.flush().await.unwrap();
        }
    });
    EventLog { tx }
}
```

## Rust-Specific Anti-Patterns

- **`Arc<Mutex<HashMap<_, _>>>` instead of `DashMap` or actor** — unless you've measured, the actor + mpsc is almost always a better fit.
- **Holding a `MutexGuard` across `.await`** — the compiler may not catch this in generics; clippy does.
- **`tokio::sync::Mutex` everywhere** — slower than `std::sync::Mutex` and every `.lock().await` is a yield point.
- **`block_in_place` inside handlers** — blocks a worker; use `spawn_blocking`.
- **Spawning from inside a hot path without a bound** — spawn rate outpaces completion, tasks pile up.
- **`unwrap()` on `lock()`** — propagates poison without handling. Use `.unwrap_or_else(|e| e.into_inner())` or `parking_lot`.
- **`try_lock` in a retry loop without backoff** — livelock.
- **`Send` workaround via `unsafe impl Send for T`** — if you don't control every field, you're lying to the compiler.
- **Recursive `rayon::par_iter`** — can deadlock the work-stealing pool.
- **Using `std::sync::mpsc` in async code** — it blocks the worker. Use `tokio::sync::mpsc`.

## Code Recipe Library (30+)

### 1. Guard drop before await

```rust
let value = {
    let g = state.lock();
    g.extract()  // compute what you need
};  // guard dropped here
do_io(value).await;
```

### 2. `tokio::sync::Mutex` only when unavoidable

```rust
let mut guard = shared.lock().await;  // tokio::sync::Mutex
guard.field = compute();
do_async_work().await;  // safe, tokio-aware
```

### 3. Bounded `spawn_blocking`

```rust
let permit = db_sem.clone().acquire_owned().await?;
tokio::task::spawn_blocking(move || {
    let _p = permit;
    rusqlite_call()
}).await?
```

### 4. Actor handle pattern (see Creative Patterns)

### 5. Watch channel for latest-value

```rust
let (tx, rx) = watch::channel(initial);
// writer: tx.send_modify(|s| s.update());
// reader: loop { rx.changed().await?; let v = rx.borrow().clone(); }
```

### 6. `arc-swap` for config reload

```rust
static CONFIG: OnceLock<ArcSwap<Config>> = OnceLock::new();
// hot path: let c = CONFIG.get().unwrap().load();
// reload:  CONFIG.get().unwrap().store(Arc::new(new_config));
```

### 7. Bounded retry with exp backoff + jitter

```rust
use std::time::Duration;
async fn with_retry<F, Fut, T, E>(mut f: F) -> Result<T, E>
where F: FnMut() -> Fut, Fut: Future<Output = Result<T, E>>, E: IsRetryable,
{
    let mut attempts = 0u32;
    loop {
        match f().await {
            Ok(v) => return Ok(v),
            Err(e) if e.is_retryable() && attempts < 6 => {
                attempts += 1;
                let base = 20u64 << attempts;
                let jitter = rand::random::<u64>() % (base / 2);
                tokio::time::sleep(Duration::from_millis(base + jitter)).await;
            }
            Err(e) => return Err(e),
        }
    }
}
```

### 8. SQLite writer actor

See the SQLx/rusqlite section above.

### 9. Ranked mutex for debug-build deadlock detection

```rust
pub struct RankedMutex<T> {
    rank: u32,
    inner: parking_lot::Mutex<T>,
}
thread_local! {
    static LOCK_STACK: std::cell::RefCell<Vec<u32>> = Default::default();
}
impl<T> RankedMutex<T> {
    pub fn lock(&self) -> parking_lot::MutexGuard<'_, T> {
        #[cfg(debug_assertions)]
        LOCK_STACK.with(|s| {
            let stack = s.borrow();
            if let Some(&top) = stack.last() {
                assert!(top < self.rank,
                    "lock order violation: held rank {top}, acquiring {}", self.rank);
            }
            drop(stack);
            s.borrow_mut().push(self.rank);
        });
        self.inner.lock()
    }
}
```

### 10. Parking-lot deadlock detector (startup hook)

```rust
#[cfg(debug_assertions)]
fn start_deadlock_detector() {
    std::thread::spawn(|| loop {
        std::thread::sleep(std::time::Duration::from_secs(10));
        let deadlocks = parking_lot::deadlock::check_deadlock();
        if deadlocks.is_empty() { continue; }
        eprintln!("!!! DEADLOCK DETECTED ({}) !!!", deadlocks.len());
        for (i, ts) in deadlocks.iter().enumerate() {
            eprintln!("Deadlock #{i}");
            for t in ts {
                eprintln!("  Thread {:?}: {:#?}", t.thread_id(), t.backtrace());
            }
        }
        std::process::abort();
    });
}
```

### 11. `CancellationToken` for graceful shutdown

```rust
use tokio_util::sync::CancellationToken;
let token = CancellationToken::new();
let child = token.child_token();

tokio::spawn(async move {
    loop {
        tokio::select! {
            _ = child.cancelled() => { tracing::info!("shutting down"); break; }
            msg = rx.recv() => handle(msg).await,
        }
    }
});

// Later:
token.cancel();
```

### 12. `JoinSet` for fan-out with bounded concurrency

```rust
use tokio::task::JoinSet;

let mut set = JoinSet::new();
for url in urls {
    if set.len() >= 16 {
        set.join_next().await;  // wait for one to finish
    }
    set.spawn(fetch(url));
}
while let Some(result) = set.join_next().await {
    handle(result?);
}
```

### 13. `Notify` subscription before event

```rust
use tokio::sync::Notify;
let notify = Arc::new(Notify::new());

let notified = notify.notified();  // subscribe FIRST
tokio::pin!(notified);

if !ready.load(Ordering::Acquire) {
    notified.await;  // now safe — won't miss a notification
}
```

### 14. Cancel-safe select over multiple tasks

```rust
tokio::select! {
    biased;  // check in order
    _ = shutdown.cancelled() => break,
    msg = rx.recv() => handle(msg),
    _ = heartbeat_interval.tick() => send_heartbeat().await,
}
```

### 15. Fairness-safe semaphore for rate limiting

```rust
use tokio::sync::Semaphore;
let limiter = Arc::new(Semaphore::new(100));  // max 100 concurrent
for req in requests {
    let permit = limiter.clone().acquire_owned().await?;
    tokio::spawn(async move {
        let _p = permit;  // released on drop
        handle(req).await
    });
}
```

### 16. Timeout wrapping every async call

```rust
use tokio::time::{timeout, Duration};
let result = timeout(Duration::from_secs(30), operation()).await??;
// First ? = timeout Result; second ? = inner Result
```

Every `.await` in a handler should be inside a `timeout(...)` or have an ancestor `.await` that is. Hangs become log lines, not stalls.

### 17. Shutdown via watch channel

```rust
let (tx, rx) = watch::channel(false);
// Main: tokio::signal::ctrl_c().await?; tx.send(true).ok();
// Each task:
let mut rx = rx.clone();
tokio::select! {
    _ = rx.changed() => cleanup(),
    _ = work() => {}
}
```

### 18. `oneshot::Sender` for reply channel

```rust
let (tx, rx) = oneshot::channel();
send_command(Command::Query { reply: tx });
let result = rx.await?;  // blocks until command is processed
```

### 19. `futures::stream::buffer_unordered` for bounded concurrent fan-out

```rust
use futures::{stream, StreamExt};

let results: Vec<_> = stream::iter(urls)
    .map(fetch)
    .buffer_unordered(16)  // up to 16 concurrent
    .collect()
    .await;
```

### 20. BEGIN IMMEDIATE for writer transactions (SQLite)

```rust
let tx = conn.transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)?;
tx.execute("UPDATE ...", params)?;
tx.commit()?;
```

### 21. `yield_now` in long-running tasks

```rust
for (i, item) in items.iter().enumerate() {
    process(item);
    if i % 1000 == 0 {
        tokio::task::yield_now().await;  // let other tasks run
    }
}
```

### 22. Lock-free counter with atomic

```rust
use std::sync::atomic::{AtomicU64, Ordering};
static COUNTER: AtomicU64 = AtomicU64::new(0);
COUNTER.fetch_add(1, Ordering::Relaxed);  // Relaxed OK for counters
```

### 23. Lock-free flag with acq/rel

```rust
use std::sync::atomic::{AtomicBool, Ordering};
static READY: AtomicBool = AtomicBool::new(false);
// producer:
// (initialize data)
READY.store(true, Ordering::Release);
// consumer:
if READY.load(Ordering::Acquire) {
    // safe to read data
}
```

### 24. `Arc::strong_count` for reference-count debugging

```rust
let arc = Arc::new(big_data);
let clone = arc.clone();
assert_eq!(Arc::strong_count(&arc), 2);
```

### 25. `Weak` for cycle-breaking

```rust
struct Parent { children: Vec<Arc<Child>> }
struct Child  { parent: Weak<Parent> }  // Weak breaks the cycle
```

### 26. `Mutex::new(())` as a marker for critical sections

```rust
let serial = parking_lot::Mutex::new(());
// at site A:
let _guard = serial.lock();
do_something();
// at site B:
let _guard = serial.lock();
do_other_thing();
// A and B are now mutually exclusive
```

### 27. `fork_join` pattern with `rayon::join`

```rust
use rayon::join;
let (a, b) = join(
    || compute_left(&data),
    || compute_right(&data),
);
```

### 28. Per-thread state with thread_local

```rust
use std::cell::RefCell;
thread_local! {
    static STATS: RefCell<Stats> = RefCell::new(Stats::default());
}
STATS.with(|s| s.borrow_mut().count += 1);
```

### 29. `Event` from `event-listener` crate for sync/async bridging

```rust
use event_listener::Event;
let event = Event::new();

// async side:
let listener = event.listen();  // subscribe first
if !ready() { listener.await; }

// sync side:
event.notify(usize::MAX);  // wakes everyone
```

### 30. `Mutex<Option<T>>` for "take-once" patterns

```rust
let slot = parking_lot::Mutex::new(Some(expensive_resource));
// Consumer:
if let Some(resource) = slot.lock().take() {
    use_it(resource);
}  // next call sees None
```

## Audit Commands

```bash
# Class 2: await-holding-lock
cargo +nightly clippy -- -W clippy::await_holding_lock

# Class 5: blocking primitives in LD_PRELOAD crate
rg -n --type rust 'OnceLock|OnceCell|Lazy::new|lazy_static|thread_local!' crates/preload/

# Class 4: Connection::open without configure_connection call
rg -n --type rust 'Connection::open' | grep -v 'configure_connection'

# Class 6: unsafe impl Send/Sync
rg -n 'unsafe impl (Send|Sync) for' src/

# Class 2: block_on in async contexts
rg -n --type rust -U 'async fn[^{]*\{[^}]*block_on' src/

# Class 3: retry loops without backoff
rg -n --type rust -U 'loop \{[^}]*is_err\(\)[^}]*\}' src/ | head

# TSAN full run
RUSTFLAGS="-Zsanitizer=thread" cargo +nightly test \
    --target x86_64-unknown-linux-gnu -- --test-threads=16 2>&1 | tee /tmp/tsan.log
rg '^WARNING: ThreadSanitizer' /tmp/tsan.log -A 30

# Loom (on primitive-level tests only)
RUSTFLAGS="--cfg loom" cargo test --release --lib loom_tests

# Miri (slow but thorough)
cargo +nightly miri test
```

## See Also

- [SKILL.md](../SKILL.md) — the 9-class taxonomy and triage table
- [DATABASE.md](DATABASE.md) — SQLite/sqlx/rusqlite concurrency in depth
- [LD-PRELOAD.md](LD-PRELOAD.md) — the runtime-init hazard (full glibc-rust narrative)
- [ASYNC.md](ASYNC.md) — cross-language async patterns (this file is Rust-specific)
- [LOCK-FREE.md](LOCK-FREE.md) — CAS, hazard pointers, epoch-based reclamation
- [FORMAL-METHODS.md](FORMAL-METHODS.md) — loom, miri, TLA+, shuttle
- [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) — actor, STM, structured concurrency
- [THE-FOURTH-INSTANCE.md](THE-FOURTH-INSTANCE.md) — find ALL instances, not just the first

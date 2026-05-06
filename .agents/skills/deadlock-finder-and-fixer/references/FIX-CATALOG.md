# Fix Catalog

Canonical replacements for broken concurrency patterns. Each entry: the smell, the fix, the Rust snippet, when it applies, and what NOT to do.

## Index

1. [OnceLock → atomic state machine (Class 5)](#1-oncelock--atomic-state-machine)
2. [`std::sync::Mutex` across await → drop guard or `tokio::sync::Mutex` (Class 2)](#2-stdsyncmutex-across-await--drop-guard-or-tokiomutex)
3. [Deep call holding two locks → total lock order (Class 1)](#3-deep-call-holding-two-locks--total-lock-order)
4. [Retry on BUSY → exponential backoff + jitter (Class 3)](#4-retry-on-busy--exponential-backoff--jitter)
5. [Connection-per-request SQLite → single writer + read pool (Class 4)](#5-connection-per-request-sqlite--single-writer--read-pool)
6. [Shared `Mutex<Vec<Work>>` → `mpsc::channel` + actor (Class 2/6)](#6-shared-mutexvecwork--mpsc--actor)
7. [`lazy_static` in LD_PRELOAD → `const` or `LIBRARY_READY` pattern (Class 5)](#7-lazy_static-in-ld_preload--const-or-library_ready)
8. [`std::Mutex` + panic → `parking_lot::Mutex` + transactional update (Class 8)](#8-stdmutex--panic--parking_lotmutex--transactional-update)
9. [`flock` only in-process → `flock` + advisory lease (Class 7)](#9-flock-only-in-process--flock--advisory-lease)
10. [`block_on` inside async → `spawn_blocking` (Class 2)](#10-block_on-inside-async--spawn_blocking)
11. [Unbounded `spawn_blocking` → `Semaphore`-bounded (Class 2)](#11-unbounded-spawn_blocking--semaphore-bounded)
12. [`Arc<Mutex<T>>` as `Send` workaround → ownership or channel (Class 2)](#12-arcmutext-as-send-workaround--ownership-or-channel)
13. [Condvar on edge → condvar on predicate loop (Class 9)](#13-condvar-on-edge--condvar-on-predicate-loop)
14. [Publishing `Arc<T>` with `Relaxed` → `Release`/`Acquire` (Class 9)](#14-publishing-arct-with-relaxed--releaseacquire)

---

## 1. `OnceLock` → atomic state machine

**Smell.** `OnceLock`, `Lazy`, `lazy_static`, `OnceCell` on an initialization path that may be called from a code path that can recurse back into the same module (LD_PRELOAD, signal handler, fork handler, panic handler).

**Fix.** Replace with `AtomicU8` (or larger) state machine + raw atomic pointer for the stored value. Initialization is wait-free for race losers.

```rust
use std::sync::atomic::{AtomicU8, AtomicPtr, Ordering};
use std::ptr;

const UNINIT: u8 = 0;
const INITIALIZING: u8 = 1;
const READY: u8 = 2;

static STATE: AtomicU8 = AtomicU8::new(UNINIT);
static VALUE: AtomicPtr<MyType> = AtomicPtr::new(ptr::null_mut());

fn get() -> Option<&'static MyType> {
    if STATE.load(Ordering::Acquire) == READY {
        // safe because VALUE was published with Release before STATE==READY
        let p = VALUE.load(Ordering::Acquire);
        if !p.is_null() {
            return Some(unsafe { &*p });
        }
    }
    None
}

fn initialize() {
    if STATE.compare_exchange(UNINIT, INITIALIZING, Ordering::Acquire, Ordering::Acquire).is_err() {
        return;  // someone else is handling it
    }
    let value = Box::leak(Box::new(MyType::build()));  // NO allocations on interposed paths
    VALUE.store(value as *mut _, Ordering::Release);
    STATE.store(READY, Ordering::Release);
}
```

**Important:** `Box::new` inside `initialize()` still allocates. For the LD_PRELOAD case specifically, you must use the `LIBRARY_READY` pattern (see item 7 and [LD-PRELOAD.md](LD-PRELOAD.md)).

---

## 2. `std::sync::Mutex` across await → drop guard or `tokio::Mutex`

**Smell.**

```rust
let g = state.lock().unwrap();
some_io().await;       // ← bug
```

**Fix (preferred — cheaper).**

```rust
let value = {
    let g = state.lock().unwrap();
    g.clone()          // or extract what you need
};
some_io(value).await;
```

**Fix (only if you must hold across await).**

```rust
let g = state.lock().await;    // tokio::sync::Mutex
some_io().await;               // ok, tokio's wait is async-aware
```

**Detection:** `cargo +nightly clippy -- -W clippy::await_holding_lock`.

---

## 3. Deep call holding two locks → total lock order

**Smell.** Different call paths acquire the same pair of locks in different orders.

**Fix.** Assign a numeric rank to every lock in the system. Assert (debug builds) that locks are only acquired in ascending rank. `parking_lot`'s deadlock detector can verify this at runtime.

```rust
use std::sync::atomic::{AtomicU32, Ordering};

thread_local! {
    static LOCK_STACK: std::cell::RefCell<Vec<u32>> = Default::default();
}

pub struct RankedMutex<T> {
    rank: u32,
    inner: parking_lot::Mutex<T>,
}

impl<T> RankedMutex<T> {
    pub fn lock(&self) -> parking_lot::MutexGuard<'_, T> {
        #[cfg(debug_assertions)]
        LOCK_STACK.with(|s| {
            let stack = s.borrow();
            if let Some(&top) = stack.last() {
                assert!(top < self.rank, "lock order violation: held rank {top}, acquiring rank {}", self.rank);
            }
            drop(stack);
            s.borrow_mut().push(self.rank);
        });
        self.inner.lock()
    }
}
```

---

## 4. Retry on BUSY → exponential backoff + jitter

**Smell.** `while let Err(BUSY) = f() {}` or the same with `continue`.

**Fix.** Every retry loop has exp backoff + jitter + bounded attempts.

```rust
use std::time::Duration;

fn with_retry<F, T, E>(mut f: F) -> Result<T, E>
where F: FnMut() -> Result<T, E>, E: IsRetryable,
{
    let mut attempts = 0;
    loop {
        match f() {
            Ok(v) => return Ok(v),
            Err(e) if e.is_retryable() && attempts < 6 => {
                attempts += 1;
                let base = 20u64 << attempts;
                let jitter = rand::random::<u64>() % (base / 2);
                std::thread::sleep(Duration::from_millis(base + jitter));
            }
            Err(e) => return Err(e),
        }
    }
}
```

---

## 5. Connection-per-request SQLite → single writer + read pool

**Smell.** Every request opens a new `rusqlite::Connection` and writes directly.

**Fix.** One writer task/process owns a single connection; everyone else submits requests:

```rust
enum WriteRequest {
    InsertUser { name: String, reply: oneshot::Sender<Result<i64>> },
    UpdateProfile { id: i64, data: Profile, reply: oneshot::Sender<Result<()>> },
}

async fn writer_actor(mut rx: mpsc::Receiver<WriteRequest>, conn: rusqlite::Connection) {
    while let Some(req) = rx.recv().await {
        match req {
            WriteRequest::InsertUser { name, reply } => {
                let r = conn.execute("INSERT INTO users (name) VALUES (?1)", params![name])
                            .map(|_| conn.last_insert_rowid());
                let _ = reply.send(r);
            }
            WriteRequest::UpdateProfile { id, data, reply } => {
                let r = do_update(&conn, id, &data);
                let _ = reply.send(r);
            }
        }
    }
}
```

Readers use a separate pool (e.g., `r2d2_sqlite`) so they can parallelize freely.

---

## 6. Shared `Mutex<Vec<Work>>` → mpsc + actor

**Smell.**

```rust
let queue = Arc::new(Mutex::new(Vec::<Work>::new()));
// Producers: queue.lock().unwrap().push(w);
// Consumers: queue.lock().unwrap().pop();
```

**Fix.** Channel.

```rust
let (tx, mut rx) = tokio::sync::mpsc::channel::<Work>(256);
// Producers: tx.send(w).await?;
// One consumer: while let Some(w) = rx.recv().await { process(w).await; }
```

No lock on the hot path. Backpressure is a natural consequence of the bounded channel size.

---

## 7. `lazy_static` in LD_PRELOAD → `const` or `LIBRARY_READY`

**Smell.** Any `lazy_static!`, `OnceLock`, or `Lazy::new` in a crate compiled into an `LD_PRELOAD` library.

**Fix (simple case).** `const fn` initialization:

```rust
// Before
lazy_static! { static ref FOO: String = String::from("bar"); }

// After (if possible)
const FOO: &str = "bar";
```

**Fix (can't constify).** `LIBRARY_READY` fast-path pattern. See [LD-PRELOAD.md](LD-PRELOAD.md) for the full recipe.

---

## 8. `std::Mutex` + panic → `parking_lot::Mutex` + transactional update

**Smell.** `.lock().unwrap()` with any chance of panic inside the critical section.

**Fix.** `parking_lot::Mutex` doesn't poison. Panic recovery is your problem, but at least the mutex is still usable.

```rust
use parking_lot::Mutex;

let m = Mutex::new(State::new());
{
    let mut g = m.lock();
    // Compute new state in a local first
    let next = compute_new_state(&*g);     // may panic — if so, *g unchanged
    *g = next;                              // atomic swap
}
```

---

## 9. `flock` only in-process → `flock` + advisory lease

**Smell.** Using `flock` as if it were a cross-process mutex, without TTL, heartbeat, or fallback.

**Fix.** Layer: advisory lease (TTL + explicit release) on top of `flock` for best-effort exclusion:

```rust
use fs2::FileExt;
let f = std::fs::File::create("/var/lock/resource.lock")?;
if f.try_lock_exclusive().is_err() {
    return Err("resource busy");
}
// Also: record lease in agent-mail or a file with TTL
write_lease(&lease_path, std::time::Instant::now() + Duration::from_secs(300))?;
// ... do the work ...
remove_lease(&lease_path)?;
f.unlock()?;
```

---

## 10. `block_on` inside async → `spawn_blocking`

**Smell.**

```rust
async fn handler() {
    let result = tokio::runtime::Handle::current().block_on(async { inner().await });
}
```

**Fix.**

```rust
async fn handler() {
    let result = inner().await;
}
```

Or, if inner is sync:

```rust
async fn handler() {
    let result = tokio::task::spawn_blocking(|| inner_sync()).await?;
}
```

---

## 11. Unbounded `spawn_blocking` → `Semaphore`-bounded

**Smell.**

```rust
for x in items {
    tokio::task::spawn_blocking(move || heavy(x));
}
```

**Fix.**

```rust
let sem = Arc::new(tokio::sync::Semaphore::new(16));
for x in items {
    let permit = sem.clone().acquire_owned().await?;
    tokio::task::spawn_blocking(move || {
        let _permit = permit;
        heavy(x)
    });
}
```

---

## 12. `Arc<Mutex<T>>` as `Send` workaround → ownership or channel

**Smell.** `Arc<Mutex<T>>` where the data is only *used* by one task at a time (sequentially), not truly shared.

**Fix.** Move ownership or use a channel. See `ASYNC.md` Pattern 4.

---

## 13. Condvar on edge → condvar on predicate loop

**Smell.**

```rust
// If the predicate changed RIGHT BEFORE the wait, you miss the wake.
if !ready { cv.wait(g); }
```

**Fix.**

```rust
while !ready {
    cv.wait(g);
}
```

The predicate must be checked under the lock. The wait releases the lock atomically.

---

## 14. Publishing `Arc<T>` with `Relaxed` → `Release`/`Acquire`

**Smell.**

```rust
ptr.store(new_value, Ordering::Relaxed);        // writer
let v = ptr.load(Ordering::Relaxed);            // reader
```

The reader may see the pointer before the object it points to is fully initialized. Undefined behavior.

**Fix.**

```rust
ptr.store(new_value, Ordering::Release);
let v = ptr.load(Ordering::Acquire);
```

`Release` on the writer side ensures all prior stores to the object are visible *before* the pointer store is observed. `Acquire` on the reader side ensures reads of the object come from *after* the pointer store.

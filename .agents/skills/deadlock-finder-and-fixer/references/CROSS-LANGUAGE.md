# Cross-Language Concurrency Pattern Matrix

The same concurrency problem manifests differently in every language. This matrix maps patterns across languages so you can translate solutions.

---

## Primitive Equivalents

| Concept | Rust (asupersync) | Rust (tokio) | Go | Python | TypeScript/Node | C/C++ |
|---------|-------------------|--------------|-----|---------|----------------|-------|
| **Mutex** | `Mutex::lock(cx)` | `parking_lot::Mutex` | `sync.Mutex` | `threading.Lock` | `AsyncMutex` (manual) | `pthread_mutex_t` |
| **RwLock** | `RwLock::read/write(cx)` | `parking_lot::RwLock` | `sync.RWMutex` | `threading.RLock` (partial) | — | `pthread_rwlock_t` |
| **Channel (MPSC)** | `mpsc::channel` (two-phase) | `tokio::sync::mpsc` | `chan` | `asyncio.Queue` | `postMessage` | pipe/socket |
| **Channel (broadcast)** | `broadcast::channel` | `tokio::sync::broadcast` | — (manual) | — | `BroadcastChannel` | — |
| **Watch (latest value)** | `watch::channel` | `tokio::sync::watch` | `atomic.Value` | — | — | `atomic<T>` |
| **Oneshot** | `oneshot::channel` | `tokio::sync::oneshot` | `chan` (cap 1) | `asyncio.Future` | `Promise` | — |
| **Semaphore** | `Semaphore::acquire(cx)` | `tokio::sync::Semaphore` | `semaphore.Weighted` | `asyncio.Semaphore` | `p-limit` | `sem_t` |
| **Condition var** | `Notify` | `tokio::sync::Notify` | `sync.Cond` | `threading.Condition` | — | `pthread_cond_t` |
| **Once init** | `OnceCell` (cancel-safe) | `std::sync::OnceLock` | `sync.Once` | — | — | `pthread_once_t` |
| **Atomic** | `std::sync::atomic` | same | `sync/atomic` | — (GIL) | `Atomics` (SharedArrayBuffer) | `std::atomic<T>` |
| **Scope / nursery** | `Scope` (native) | `JoinSet` (manual) | `errgroup` (partial) | `trio.open_nursery` | — | — |
| **Actor** | `GenServer` | mpsc+oneshot | goroutine+chan | asyncio.Queue | worker+postMessage | — |
| **Spawn** | `cx.spawn` (region-owned) | `tokio::spawn` | `go func()` | `asyncio.create_task` | `new Worker` | `pthread_create` |
| **Cancel** | `CancellationToken` (Cx) | `CancellationToken` | `context.WithCancel` | `asyncio.Task.cancel` | `AbortController` | `pthread_cancel` |
| **Timeout** | `timeout` combinator | `tokio::time::timeout` | `context.WithTimeout` | `asyncio.wait_for` | `AbortSignal.timeout` | `pthread_cond_timedwait` |
| **Race detector** | TSAN + loom + DPOR | TSAN + loom | `go test -race` | `asyncio debug=True` | — | TSAN / Helgrind |
| **Thread dump** | `TaskInspector` | `gdb thread apply all bt` | `runtime.Stack` | `py-spy dump` | `node --inspect` | `gdb -p PID` |

---

## The Same Bug, Different Languages

### "Guard held across yield" (Class 2)

| Language | How It Looks | Fix |
|----------|-------------|-----|
| **Rust** | `let g = mutex.lock(); foo.await; drop(g);` | Drop guard before `.await` |
| **Python** | `with lock: await something()` | `async with asyncio.Lock()` |
| **Go** | `mu.Lock(); <-ch; mu.Unlock()` | Unlock before channel op |
| **Node** | N/A (single-threaded) | N/A |
| **C++** | N/A (no coroutines in typical use) | N/A (but C++20 coroutines can hit this) |

### "Fire and forget" (Class 2 — orphan task)

| Language | How It Looks | Fix |
|----------|-------------|-----|
| **Rust (asupersync)** | Impossible — `cx.spawn` requires owning Scope | Already fixed by design |
| **Rust (tokio)** | `tokio::spawn(async { ... });` (JoinHandle dropped) | Use `JoinSet` or hold handle |
| **Go** | `go func() { ... }()` (no tracking) | Use `errgroup` + `context` |
| **Python** | `asyncio.create_task(work())` (no reference) | Hold ref + `add_done_callback` |
| **Node** | `doWork()` (missing await) | `await doWork()` or `void doWork().catch(...)` |

### "Database is locked" (Class 4)

| Language | ORM | Fix |
|----------|-----|-----|
| **Rust** | rusqlite / sqlx | `spawn_blocking` + `PRAGMA busy_timeout=5000` |
| **Go** | database/sql | `SetMaxOpenConns` + `context.WithTimeout` |
| **Python** | sqlite3 / SQLAlchemy | Thread-local connections + `timeout=30.0` |
| **Node** | better-sqlite3 / Prisma | worker_threads (better-sqlite3) / batch $transaction (Prisma) |

### "Retry storm" (Class 3)

| Language | Fix Pattern |
|----------|-------------|
| **Rust (asupersync)** | `retry(cx, RetryPolicy::exponential(...), \|\| op(cx))` |
| **Rust (tokio)** | Manual: `loop { match op().await { ... } tokio::time::sleep(backoff).await; }` |
| **Go** | Manual: `for i := 0; i < max; i++ { time.Sleep(backoff) }` |
| **Python** | `tenacity.retry(wait=wait_exponential())` |
| **Node** | `p-retry` or manual with `setTimeout` |

---

## Detection Tool Equivalents

| Tool Category | Rust | Go | Python | Node | C/C++ |
|--------------|------|-----|---------|------|-------|
| **Data race** | TSAN (`-Zsanitizer=thread`) | `go test -race` | — (GIL hides most) | — | TSAN / Helgrind |
| **Deadlock** | parking_lot deadlock_detection | runtime auto-detect ("all goroutines asleep") | — (manual) | — (manual) | Helgrind / lockdep |
| **Exhaustive** | loom / asupersync DPOR | — | — | — | — |
| **Thread dump** | gdb + tokio-console | pprof / `runtime.Stack` | py-spy / faulthandler | `--inspect` / clinic.js | gdb |
| **Memory ordering** | miri | — | — | — | TSAN |
| **Async debug** | tokio-console / asupersync TaskInspector | pprof goroutine | `PYTHONASYNCIODEBUG=1` | `--inspect` | — |

---

## Concurrency Model Comparison

| Language | Model | Parallelism | Concurrency | GC |
|----------|-------|-------------|-------------|-----|
| **Rust** | Ownership + Send/Sync | Real (threads) | async (tokio/asupersync) | None (RAII) |
| **Go** | CSP (goroutines + channels) | Real (M:N scheduler) | Built-in | Tracing GC |
| **Python** | GIL + asyncio | Fake (threads) / Real (multiprocessing) | asyncio / trio | Refcount + GC |
| **Node** | Event loop + workers | Single thread / workers | Promises / async-await | V8 GC |
| **C/C++** | Manual (pthreads / std::thread) | Real (threads) | Manual (epoll/io_uring) | None (manual) |
| **Java** | JMM + virtual threads | Real (threads) | CompletableFuture / virtual threads | GC |

---

## The Universal Rules (All Languages)

1. **Prefer message passing over shared state.** Actor / channel / queue.
2. **Single writer, many readers.** Eliminates write-write conflicts.
3. **Lock ordering must be total.** Any deviation = latent deadlock.
4. **Every wait must have a timeout.** Hangs become log lines, not stalls.
5. **Every concurrent primitive needs a test.** Not just the happy path.
6. **The Fourth Instance rule applies everywhere.** Find ALL of them.
7. **Structured concurrency > fire-and-forget.** Scope-owned > orphan tasks.
8. **Backpressure > unbounded growth.** Bound every queue, channel, pool.
9. **Retry with jitter > retry without.** Synchronized retries = livelock.
10. **Evidence > intuition.** Capture state before diagnosing.

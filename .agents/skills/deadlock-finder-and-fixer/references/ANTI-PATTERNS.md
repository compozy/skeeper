# Anti-Patterns

Consolidated from every concurrency incident in the repo. Each entry is something we've seen go wrong — not theoretical, observed.

## Across the board

- **Fixing the first instance and assuming you're done.** See [THE-FOURTH-INSTANCE.md](THE-FOURTH-INSTANCE.md). Find ALL of them.
- **Guessing the root cause without capturing state.** "It's probably a deadlock" is not diagnosis. Snapshot → prove → fix.
- **Adding a timeout as the "fix" for a deadlock.** The timeout converts a hang into an error. The deadlock is still there, just intermittent and harder to find.
- **Adding more locks to fix a lock bug.** Usually makes it worse. Step back; consider whether the state should be shared at all.
- **"Catch the panic and retry."** Panics in critical sections leave partial state. Retry on partial state corrupts further. Fix the panic or use transactional updates.
- **Thread-safety by Arc<Mutex>.** Not always the answer. Consider ownership refactoring or channels first.

## Class 1 — Classic deadlock

- **Two call sites that acquire the same pair of locks in different orders.** This is the textbook AB-BA. Find by audit, fix by total ordering.
- **Holding a lock across a callback or foreign-function call.** You don't know what the callback does. It might acquire another lock. It might re-enter yours. Release first, then call.
- **`RwLock::read()` → `RwLock::write()` on the same instance in the same thread.** Guaranteed self-deadlock. Always separate or recheck under write.
- **`.lock().unwrap()` in many places.** Poisoning cascades. Use `parking_lot::Mutex` or handle the PoisonError explicitly.

## Class 2 — Async / await

- **`std::sync::Mutex` held across `.await`.** The most common async bug. Drop the guard.
- **`block_on` inside an async context.** Recursive runtime entry. Use `spawn_blocking` or restructure.
- **Unbounded `spawn_blocking`.** Thread pool exhaustion. Bound with `Semaphore`.
- **`Arc<Mutex<T>>` to make a type `Send`.** Usually papering over an ownership problem. Refactor instead.
- **Unbounded channels** (`mpsc::unbounded_channel`). Memory leak waiting to happen. Bound + handle backpressure.
- **`tokio::spawn(async move { ... })` from sync context without a runtime.** Panics with "no reactor running".
- **Forgetting `pin_mut!` or `tokio::pin!`** on self-referential futures. Tokio lint catches some, not all.

## Class 3 — Livelock / retry storms

- **Retry loop without backoff.** Tight loop on `BUSY` or `EAGAIN` is a livelock factory. Exp backoff + jitter.
- **Accept loop without throttling.** On `EAGAIN`, poll or sleep — don't immediately retry.
- **All N workers retrying identical work.** Retry with different offsets to avoid thundering herd.
- **Edge-triggered notifications.** Use level-triggered (store the state, not the transition).

## Class 4 — Database concurrency (SQLite)

- **`Connection::open` without `busy_timeout`.** Default is 0 = fail immediately. Set to ≥1000ms. Always.
- **Multiple writer processes.** Rearchitect to single-writer + IPC.
- **Plain `BEGIN` then `UPDATE`.** The upgrade from reader to writer can fail with no retry path. Use `BEGIN IMMEDIATE`.
- **`rusqlite` from an async handler without `spawn_blocking`.** Blocks the tokio worker thread.
- **Long-running reader transactions.** Keep WAL alive; prevent checkpoint; DB grows unbounded.
- **Scattering `PRAGMA` calls.** Centralize in one helper called on every connection open.
- **Relying on OS-level file locks.** SQLite manages its own locks; don't add `flock` on the db file.

## Class 5 — Runtime-init / reentrant hazards

- **`OnceLock` / `Lazy` / `lazy_static` in LD_PRELOAD-reachable code.** Will deadlock on reentry.
- **`std::env::var` from a reentrant code path.** `getenv` walks environ via `strlen`; if `strlen` is interposed, you loop.
- **Allocating (`Box::new`, `vec!`, `format!`) in an LD_PRELOAD init function.** The allocator may be interposed too.
- **Missing `Acquire`/`Release` on `LIBRARY_READY` flag.** `Relaxed` races.
- **Assuming `.init_array` runs before every interposed call.** It runs on the thread that loads the library; other threads may see the flag still false briefly.

## Class 6 — Data races / TOCTOU

- **Ignoring TSAN warnings.** "Probably a false positive" — almost always a real race.
- **`unsafe impl Send for X` without reading every use site.** Every hand-rolled Send is a compile-time lie to the type checker.
- **Check then act without locking both.** `if cache.contains(k) { return cache.get(k) }` — another thread can remove `k` between the two calls.
- **Double-checked locking without correct memory ordering.** Use an atomic flag with `Acquire`/`Release`.

## Class 7 — Multi-process / swarm

- **Broad file reservation patterns** (`*`, `**/*`, absolute paths). Reserve precisely.
- **TTL longer than the operation.** Hung agents hold leases forever.
- **Reservations without explicit release.** Rely on release on success + TTL as fallback.
- **Killing the wezterm-mux-server.** Destroys every agent session. **Never.** See system-performance-remediation.
- **Cross-process `Mutex`.** Not a thing. Use flock, leases, or IPC.
- **Assuming `flock` works on NFS.** On old NFS, it silently doesn't. Use POSIX `fcntl` or avoid.

## Class 8 — Poisoning

- **`Mutex<State>` where State has complex invariants.** A panic mid-update poisons. Use transactional updates or `parking_lot`.
- **`.lock().unwrap()` as the default.** Propagates panics. Prefer `.lock().map_err(|e| e.into_inner())` if using std Mutex, or switch to `parking_lot`.
- **Shared test fixtures** (`env_lock`, `cwd_lock`) with `std::sync::Mutex`. One test panic → all subsequent tests fail. Switch to `parking_lot` or isolate tests with dedicated env/cwd per test.

## Class 9 — Memory ordering / lost wakeups

- **Condvar without a predicate loop.** `if !ready { cv.wait(g); }` → lost wakeup. Use `while`.
- **`Notify::notify_one()` before `notified().await`** has been called. The notification is dropped. Subscribe first.
- **Publishing a pointer with `Ordering::Relaxed`.** Reader may see the pointer before the pointee is initialized.
- **Assuming `AtomicBool::load(Relaxed)` gives you sequenced reads.** It doesn't. Use `Acquire` for flags that gate data.

## Meta anti-patterns

- **Copying code from Stack Overflow without understanding the memory model.** Especially for atomics.
- **"It works on my machine."** Concurrency bugs are heisenbugs. If you can't reproduce, ship it to prod and it'll reproduce for you — expensively.
- **Skipping the regression test** because "I'm sure I fixed it". The reproducer is the test. Write it.
- **Disabling TSAN/loom** because they're slow. They catch bugs humans never will.
- **Ignoring a `parking_lot::deadlock::check_deadlock()` warning** in logs as "noise". Every warning is a proof of a bug.

---
name: deadlock-finder-and-fixer
description: >-
  Find and fix concurrency bugs - deadlocks, races, livelocks,
  await-holding-lock, database locks, LD_PRELOAD init, swarm races.
  Use when processes hang, tests flake, or auditing concurrency.
---

<!-- TOC: Taxonomy | Triage | Static Audit | Diagnosis | Fix Catalog | Prevention | Validation | References -->

# Deadlock Finder and Fixer

> **Core Insight.** Concurrency bugs do not come from one missing lock — they come from **one lock acquired in the wrong place, at the wrong time, held across the wrong operation, by a thread that didn't know it was holding it.** Find every instance of the hazard, not just the one that fired.

> **The Universal Rule.** When you think you found the deadlock and fixed the three instances you could see, **there is almost always a fourth**. This is the single most common failure mode across every concurrency debugging session in this repo's history. Keep searching until you can prove exhaustively — by code audit — that no hazard remains. See [THE FOURTH INSTANCE](references/THE-FOURTH-INSTANCE.md).

> **The False-Positive Rule.** When you think you found a concurrency bug via static pattern-matching, **verify the actual code paths before reporting it.** Grep-based audits produce pattern matches, not proofs. The most common false positives come from: (1) not checking whether Rust's ownership model (`&mut self`) already prevents the concurrent access, (2) recommending backoff for spin loops protecting nanosecond critical sections, (3) flagging `OnceLock`/`Lazy` in code that is never called from a loader or signal handler, (4) not recognizing correct condvar double-check patterns, and (5) calling `Ordering::Relaxed` a bug when the synchronization comes from a different mechanism (borrow checker, mutex gate, happens-before from thread creation). **Every finding must survive: "Can I construct a concrete interleaving of real threads that reaches this state?"** If you cannot, it is not a bug — it is a pattern match.

---

## Quick Start: Something Is Hung

```bash
# 1. Is it CPU-alive or CPU-dead?
ps -Lp $PID -o tid,pcpu,pmem,comm --no-headers | head -20

# 2. Snapshot all thread states (pick ONE, in order of availability):
gdb --batch -ex "set pagination off" -ex "thread apply all bt full" -p $PID 2>&1 | tee /tmp/bt.txt
#   OR (if ptrace blocked / LD_PRELOAD hazard):
strace -k -f -p $PID 2>&1 | head -200
#   OR (sample /proc):
for i in 1 2 3; do cat /proc/$PID/task/*/stack 2>/dev/null | sort -u; sleep 1; done

# 3. Classify (pick the matching row from the Symptom Triage Table below).
# 4. Jump to the matching section in this skill or in gdb-for-debugging.
```

**Diagnosis depth is in [gdb-for-debugging](../gdb-for-debugging/SKILL.md)** — which already contains the Lock Graph Construction algorithm, mutex ownership inspector, async runtime analysis, and TSAN/rr workflow. **This skill is the complement**: it covers *taxonomy*, *static-audit discovery*, *fix catalog*, and *prevention by design* — the parts that don't need a running process.

---

## Symptom Triage Table

| Observed Symptom | Likely Bug Class | Jump To |
|------------------|------------------|---------|
| Process 0% CPU, won't respond, threads in `futex_wait` / `__lll_lock_wait` | **Classic deadlock (AB-BA or self)** | [Class 1](#class-1--classic-mutex-deadlock) |
| Async tasks pending but all tokio workers in `epoll_wait` | **Mutex held across `.await`** or **channel cycle** | [Class 2](#class-2--async--await-deadlocks) |
| 100% CPU, futex spam, no progress | **Livelock / retry storm / broken condvar** | [Class 3](#class-3--livelock--retry-storms) |
| `database is locked`, `SQLITE_BUSY`, timeouts | **SQLite WAL contention / long transaction / writer fight** | [Class 4](#class-4--database-concurrency) |
| Hang during library load, `strlen` or `malloc` call hangs | **LD_PRELOAD / runtime-init reentrancy** | [Class 5](#class-5--runtime-init--reentrant-hazards) |
| Test flakes under load, passes under `--test-threads=1` | **Data race** (TSAN) or **TOCTOU** | [Class 6](#class-6--data-races--toctou) |
| Agent swarm stalls; two agents editing same file | **Advisory-lease race or missing reservation** | [Class 7](#class-7--multi-process--swarm-races) |
| tmux pane hung, mux unresponsive | **External process holding a shared lock / fd** | [Class 7](#class-7--multi-process--swarm-races) |
| Task starvation: one worker CPU-pegged, others idle | **Blocking call on async runtime thread** | [Class 2](#class-2--async--await-deadlocks) |
| Poisoned `std::sync::Mutex` after panic | **Cascading panic-in-critical-section** | [Class 8](#class-8--poisoning--partial-state) |
| Lost updates, wrong counter values, weird retries | **Lost wakeup / missed notification / incorrect memory ordering** | [Class 9](#class-9--memory-ordering--lost-wakeups) |

---

## The Nine Classes (Taxonomy)

### Class 1 — Classic Mutex Deadlock

**Definition:** Two or more threads each hold a lock the other needs; circular wait in the lock-wait graph.

**Canonical forms:**
- **AB-BA:** T1 holds A, wants B; T2 holds B, wants A.
- **Self-deadlock:** Single thread re-enters the same non-recursive mutex (e.g., callback path re-enters lock holder).
- **Reader-upgrade:** Thread holds `RwLock::read`, then asks for `RwLock::write` in the same thread → guaranteed hang.
- **Condvar wakeup loop:** Thread in `pthread_cond_wait` on `M`; its waker needs to acquire `M` to signal but can't.

**How to spot at rest (static audit):** search for any function that acquires two distinct mutexes, verify all call paths acquire them in the *same order* everywhere. Any deviation is a latent deadlock. See [STATIC-AUDIT.md](references/STATIC-AUDIT.md) for ast-grep recipes.

**How to spot at runtime:** see gdb-for-debugging §"Lock Graph Construction & Deadlock Proof". The algorithm: identify all threads in `__lll_lock_wait`, read the `__owner` field on each contested `pthread_mutex_t` to build the wait-for graph, find a cycle.

**Rust-specific false positives to avoid:**
- **`&mut self` IS synchronization.** If the function that transitions state X requires `&mut self`, no concurrent `&self` readers can exist. The borrow checker enforces this at compile time. An `AtomicBool` that is only set by a `&mut self` function and read by `&self` functions is safe with `Relaxed` ordering — the exclusive borrow is the barrier. Before flagging an atomic ordering issue, **check the function signatures** of all writers AND readers.
- **Consistent lock ordering across all sites is not a "risk" — it's a proof of safety.** If every nested acquisition follows A→B ordering everywhere in the codebase, there is no AB-BA deadlock. Report this as "CLEAN" not "LOW risk." The absence of inconsistency is the correctness proof.
- **Double-checked locking with explicit scope drops is safe.** `{ let r = lock.read(); ... } let w = lock.write();` — the read lock is dropped before the write lock is acquired. This is NOT a reader-upgrade deadlock. Check whether the first guard is dropped (via scope exit, explicit `drop()`, or `let` rebinding) before the second acquisition.

**Fix catalog:**
- **Total lock order.** Assign every mutex a global rank; assert in debug builds that locks are only acquired in ascending order. `parking_lot::deadlock` detector can enforce this at runtime.
- **Lock coalescing.** Replace two separate mutexes with one covering both pieces of state if they're always used together.
- **Critical-section shrinking.** Copy needed data out, release the lock, then do the work without it.
- **Don't hold a lock across a call you don't own.** Never call user callbacks, foreign functions, or allocator hooks while holding a lock — they may re-enter.

### Class 2 — Async / `.await` Deadlocks

**Definition:** The logical task graph has a cycle, or a task that holds a non-`.await`-aware lock yields to the runtime and is never re-polled because the next task needs the same lock.

**Canonical forms:**
- **`std::sync::Mutex` held across `.await`.** The guard crosses the yield point; the task is parked with the lock still held; another task needs the lock and blocks the worker thread.
- **`block_on` inside an async runtime.** Runtime thread enters a synchronous wait; the thing it's waiting for needs the runtime to make progress.
- **`spawn_blocking` missing** (or misused for sync I/O from async context).
- **Channel cycle.** Task A sends to a bounded channel whose reader is Task B; Task B sends to a bounded channel whose reader is Task A; both blocked on full/empty.
- **JoinHandle cycle.** Task A `.await`s B's handle; B `.await`s A's.
- **Task starvation.** A long-running task on a tokio-runtime-worker monopolizes the thread; other tasks cannot be polled. Looks like a deadlock to the end user.

**Signature at rest:** `grep` the codebase for `let guard = lock.lock(); ... .await` and `std::sync::Mutex` inside `async fn`. Use the recipes in [STATIC-AUDIT.md](references/STATIC-AUDIT.md) — this is the highest-ROI static check you can run on an async Rust codebase.

**Signature at runtime:** workers idle in `epoll_wait`, but requests pending. See gdb-for-debugging §"Diagnosing Async Deadlocks".

**Fix catalog:**
- **Drop the guard before `.await`.** Explicitly: `let data = { let g = lock.lock(); g.clone() }; do_io(data).await;`.
- **Use `tokio::sync::Mutex`** only when you *must* hold the lock across `.await`. It is slower — prefer dropping the guard.
- **`spawn_blocking` for synchronous I/O** from an async context (synchronous SQLite, `std::fs::read`, CPU-heavy work, C library calls).
- **Replace shared state with channels.** Actors own their state; requests arrive via `mpsc`; replies via `oneshot`. No shared mutex, no lock-order bugs.
- **Bound channels carefully.** Unbounded is a memory leak; bounded risks backpressure deadlock if the sender can't make progress without the receiver running. When in doubt, prefer `try_send` + drop-oldest policy.
- **Never `block_on` inside an async context.** If you must bridge, use `Handle::current().spawn_blocking(...)` or restructure to avoid the bridge.

### Class 3 — Livelock / Retry Storms

**Definition:** Threads make visible activity (`futex_wake` + `futex_wait`, high CPU, log noise) but **no forward progress**. Often mistaken for a deadlock.

**Canonical forms:**
- **Retry-on-BUSY without backoff.** N workers hit a contended resource, all retry immediately, none wins.
- **Condvar ping-pong.** Two threads repeatedly wake each other on edges that are no longer true.
- **Accept-loop without backoff.** `accept4` returns `EAGAIN`, immediately retried; no poll, no sleep.
- **ABA in a lock-free structure.** CAS succeeds on a value that was swapped out and back in; the operation corrupts state; other threads undo and retry.

**Signature:** 100% CPU, `strace` shows a tight loop of the same syscall, logs show retry messages stacked.

**Fix catalog:**
- **Exponential backoff with jitter — but only for unbounded or long waits.** Match the backoff strategy to the expected wait duration:
  - **Nanosecond critical sections** (seqlocks, CAS on a single atomic, flat-combining slot checks): `std::hint::spin_loop()` is correct. `yield_now()` costs 1-10 microseconds (context switch) — orders of magnitude more expensive than the expected wait. A bounded spin with `spin_loop()` and a high retry cap (e.g., 1M) is the standard pattern. **Do NOT recommend yield/sleep for sub-microsecond waits.**
  - **Microsecond critical sections** (lock-protected batch operations, flat-combining batches): Spin for ~1024 iterations, then `yield_now()`, then try to take the lock yourself. This is the correct pattern for work that takes 1-100 microseconds.
  - **Millisecond+ operations** (database transactions, network calls, file I/O): Full exponential backoff with jitter. `sleep(Duration)` is appropriate here.
  - **The test:** estimate the expected contention duration. If it's < 1 microsecond, spin. If it's 1-100 microseconds, spin-then-yield. If it's > 100 microseconds, backoff with sleep.
- **Single-writer pattern.** Serialize writes to a contested resource through one owner; readers proceed in parallel.
- **Fairness / queue-based locks.** `parking_lot` is unfair by default; switch to `fair()` if starvation is observed.
- **Convert edge-triggered notifications to level-triggered.** Store the *desired state*, not the *transition*; the consumer can always recompute.

### Class 4 — Database Concurrency (SQLite-heavy)

**The recurring pain points across our projects:**

1. **`SQLITE_BUSY` / "database is locked".** Multiple connections want the write lock simultaneously. The loser fails.
2. **WAL checkpoint starvation.** Long-running readers prevent the WAL from being reset; writes continue to append; DB size explodes.
3. **Connection-per-request with shared file.** Every connection sees its own lock state; a long transaction in one blocks every other. Connection pool can hide the problem or make it worse.
4. **Async blocking on sync driver.** `rusqlite::Connection` is synchronous; using it from an async handler without `spawn_blocking` blocks the runtime thread.
5. **PRAGMA left at defaults.** No `busy_timeout`, no `journal_mode=WAL`, no `synchronous=NORMAL`. Every writer serializes with exclusive locks and no retry.
6. **Transaction escalation.** `BEGIN` followed by a read followed by a write upgrades the lock; another writer that's already in a write transaction now deadlocks.

**Fix catalog:**
- **Set PRAGMAs on every connection open:**
  ```sql
  PRAGMA journal_mode = WAL;
  PRAGMA synchronous = NORMAL;
  PRAGMA busy_timeout = 5000;     -- ms; SQLite will retry internally
  PRAGMA foreign_keys = ON;
  PRAGMA temp_store = MEMORY;
  PRAGMA mmap_size = 268435456;
  ```
- **One writer connection, many reader connections.** Serialize all writes through a single `Mutex<Connection>` or a single actor task. Readers can use a pool.
- **`BEGIN IMMEDIATE` for transactions that will write.** Acquires the write lock up-front; prevents deferred-to-immediate upgrade deadlocks.
- **Outer retry on `SQLITE_BUSY` with exponential backoff + jitter**, on top of the internal `busy_timeout`.
- **Checkpoint explicitly.** `PRAGMA wal_checkpoint(TRUNCATE)` on a schedule or after bulk writes so WAL doesn't grow unbounded.
- **From async: always wrap sync SQLite calls in `spawn_blocking`.** Or use `sqlx`/`tokio-rusqlite` which do it for you.

See [DATABASE.md](references/DATABASE.md) for the full WAL semantics reference, PRAGMA matrix, retry-with-backoff Rust template, and project-sourced incident reports.

### Class 5 — Runtime-Init / Reentrant Hazards

**Definition:** Code that runs during early process/library initialization acquires a lock, and something on the init path re-enters the same lock (or a lock held by the loader itself).

**The canonical case from `glibc_rust`:** `libglibc_rs_abi.so` exports `strlen`. When loaded via `LD_PRELOAD`, the dynamic loader calls `strlen` during symbol resolution. `strlen` calls into the membrane crate, which touches a `OnceLock` holding global policy. `OnceLock::get_or_init` takes a lock. The allocator inside `get_or_init` also goes through the same libc and re-enters the ABI. **Reentrant lock on a non-recursive primitive → infinite hang.**

**The broader rule:** *Any* function that may be called before `main` — or by a library interposition — **cannot** use `OnceLock`, `std::sync::Mutex`, `lazy_static`, `RwLock`, or the allocator. All of these can block.

**Scope check — when does this class actually apply?**
This class applies ONLY to code that can be invoked by the dynamic linker, a signal handler, or before `main()`. Specifically:
- Libraries loaded via `LD_PRELOAD` that export symbols the loader calls during resolution (e.g., `malloc`, `strlen`, `pthread_*`)
- `#[no_mangle] pub extern "C"` functions in shared libraries that could be `dlopen`'d and called from arbitrary contexts
- Signal handlers and `atexit` callbacks

**This class does NOT apply to:**
- Normal application code with `OnceLock`/`LazyLock` for lazy initialization — this is standard Rust and is safe
- FFI libraries where the caller explicitly calls an init function (e.g., `sqlite3_open`) — these are user-initiated, not loader-initiated
- `thread_local!` in application code — safe unless used in signal handlers

**Before flagging an OnceLock/LazyLock as a Class 5 hazard, ask:** "Can the dynamic linker or a signal handler reach this code path?" If the answer is "only if a user explicitly calls our API function first," it is NOT a Class 5 hazard. **Trace the actual call chain** from the `#[no_mangle]` export to the `OnceLock` — if the init closure doesn't re-enter the same lock or call functions that the loader might also call, it is safe.

**Static-audit signature:**
```bash
ast-grep run -l Rust -p '$X::get_or_init($$$)'
rg -n 'OnceLock|OnceCell|Lazy::new|lazy_static!|thread_local!' crates/<preload_lib>/
```
Every hit is a potential hazard in an LD_PRELOAD context — but **verify the call chain before reporting**.

**Fix catalog:**
- **Atomic state machine instead of `OnceLock`.** Encode `{UNINIT=0, INIT_IN_PROGRESS=1, INIT_DONE=2}` in an `AtomicU8`; race losers spin-wait briefly (rare path) or fall back to a null-safe default.
- **Compile-time constant initialization** wherever possible (`const fn`, `static`).
- **Deferred initialization.** Don't initialize on first call; initialize lazily only on paths that are safe (after `main`).
- **Signal-safety:** forbid allocation and locks in any code path that might run from a signal handler. This is the same class of hazard.
- **Test via `LD_PRELOAD` the binary against a small program that calls every exported function; any hang means reentrant init.**

See [LD-PRELOAD.md](references/LD-PRELOAD.md) for the full incident + fix narrative from glibc-rust/frankenlibc sessions.

### Class 6 — Data Races & TOCTOU

**Definition:** Unsynchronized concurrent access to the same memory; one of the accesses is a write. In a language with a defined memory model (Rust, Go, Java, C11+), this is undefined behavior.

**TOCTOU (time-of-check-to-time-of-use):** Check a condition, then act on it, assuming it's still true. It isn't.

**Discovery:** TSAN is ground truth. `RUSTFLAGS="-Zsanitizer=thread" cargo +nightly build ...` then run the test suite with high concurrency. For Go: `go test -race`. For C/C++: `-fsanitize=thread`.

**TOCTOU false positives in Rust (common pattern):**
An `AtomicBool` used as a fast-path guard — `if flag.load(Relaxed) { lock.lock(); check_again(); }` — is NOT a TOCTOU bug if:
1. The slow path (after the lock) re-checks the condition, and
2. A stale `true` reading just causes a harmless extra lock acquisition that finds nothing, and
3. A stale `false` reading is safe because the only false→true transition requires `&mut self` (exclusive access, which prevents concurrent readers from existing during the transition)

This "optimistic flag + pessimistic lock" pattern is a deliberate optimization, not a race. The atomic is a performance hint, not a correctness mechanism — the Mutex is the real synchronization. **Do not flag this as TOCTOU unless you can show that a stale value causes incorrect behavior, not just unnecessary work.**

**Fix catalog:**
- **Wrap shared state in `Mutex` / `RwLock` / `Atomic`.** The compiler enforces this in Rust; listen to it.
- **Replace shared state with channels / message passing.**
- **For counters: `AtomicUsize` with `Ordering::Relaxed` only if you've read the memory-ordering rules; otherwise `SeqCst`. Err on the side of stronger.**
- **For TOCTOU: eliminate the gap.** Use atomic `compare_exchange`, transactional updates, or hold the lock across check + action.

See gdb-for-debugging §"Race Condition Methodology" for the reproduce → detect → localize → fix → verify loop.

### Class 7 — Multi-Process / Swarm Races

**Definition:** Multiple processes (or agents) contend for a shared resource — a file, a database, a tmux session, a git working tree — without in-process synchronization.

**Our typical forms:**
- **Two agents editing the same file.** Agent A writes, Agent B writes; one overwrites the other's work.
- **Advisory lease expiry.** An agent takes a file reservation with TTL=3600, runs longer, lease expires, another agent takes it; original finishes and writes over the new work.
- **Cross-process SQLite.** Solved by Class 4 techniques *plus* `PRAGMA locking_mode=NORMAL` (not `EXCLUSIVE`).
- **tmux mux server wedged.** A child process holds an fd the mux needs; the mux blocks on I/O; every pane hangs.

**Fix catalog:**
- **Use MCP Agent Mail file reservations.** See [agent-mail](../agent-mail/SKILL.md). `file_reservation_paths` with an appropriate TTL + a `reason` tying back to the bead/task. Release explicitly; don't rely on TTL.
- **`flock(2)` for filesystem-only coordination.** Advisory, cooperative. Every consumer must call it.
- **Single-writer process for the DB.** Other processes submit work via a queue or RPC.
- **Monitor mux health.** `wezterm-mux-server` is sacred — protect it explicitly (see system-performance-remediation).

### Class 8 — Poisoning & Partial State

**Definition:** A thread panics while holding a `Mutex`. Rust's `std::sync::Mutex` poisons the mutex; subsequent `.lock()` calls return `Err(PoisonError)`. If the panic left shared state partially updated, *every* caller must now decide: trust or discard.

**Fix catalog:**
- **`parking_lot::Mutex` does not poison.** It's faster and simpler, but callers must handle partial state explicitly.
- **Treat every critical section as a transaction.** Build the new state in a local, then swap atomically at the end. A panic mid-build leaves the shared state untouched.
- **Catch-unwind at the task boundary.** If a task panics, let the runtime notice and restart it; don't let the panic propagate through shared state.

### Class 9 — Memory Ordering & Lost Wakeups

**Definition:** Correct locks, incorrect assumptions about visibility or ordering. The observed behavior seems to violate program order — because it does, on the CPU's reordered view.

**Canonical forms:**
- **Wake-before-wait.** Thread A stores a flag, signals condvar; Thread B hasn't entered wait yet; signal is lost.
- **Lost notification on an edge.** Use of `Notify::notify_one` before `notified().await` — the notification is dropped.
- **Wrong memory ordering.** `Ordering::Relaxed` on a pointer publication — reader sees a garbage object because the initializer store hasn't become visible.

**Fix catalog:**
- **Always pair a condvar with a predicate.** `while !ready { cv.wait(lock) }`. Never `if` — unless you use the **double-checked gate pattern** (see below).
- **Store the predicate before signaling.** Signal *after* the condition is true.
- **Prefer tokio `Notify` with `notified()` set up *before* the event can happen** (see Tokio `Notify` docs — the `notified()` future must be polled at least once to subscribe).
- **Use `Ordering::Release` for the producer store and `Ordering::Acquire` for the consumer load** when publishing a pointer / building an atomic state machine. Never `Relaxed` for data publication unless the synchronization comes from a different mechanism (see below).

**Correct patterns that look wrong (do NOT flag these as bugs):**

- **The double-checked gate pattern.** This is a CORRECT condvar protocol that does not use `while`:
  ```rust
  // Waiter:
  if predicate_changed() { return true; }     // Fast check (no lock)
  let gate = gate_lock.lock();                 // Acquire gate
  if predicate_changed() { return true; }     // Re-check under lock
  cv.wait_for(&mut gate, timeout);            // Atomically release + wait
  predicate_changed()                          // Check after wake

  // Notifier:
  let _gate = gate_lock.lock();               // Acquire SAME gate
  update_predicate();                          // Mutate state
  cv.notify_one();                            // Signal
  ```
  This is safe because: (1) the gate lock serializes the predicate check and the condvar.wait, (2) the notifier holds the same gate lock while updating the predicate AND signaling, (3) `condvar.wait` atomically releases the lock and enters the wait state — there is zero gap for a lost notification. The `if` (not `while`) is fine here because the post-wake check on the predicate handles spurious wakeups. **Do NOT flag this as a lost-wakeup bug.**

- **`Ordering::Relaxed` with non-atomic synchronization.** `Relaxed` is safe when the synchronization comes from a mechanism other than the atomic itself:
  - An `AtomicBool` flag set by `fn set(&mut self)` and read by `fn check(&self)`: the `&mut self` borrow IS the barrier. `Relaxed` is correct.
  - An atomic counter incremented under a Mutex and read outside it: the Mutex provides the happens-before. `Relaxed` load is correct because the only possible stale value leads to a harmless extra lock check.
  - Metrics counters (`fetch_add` for stats): `Relaxed` is correct because approximate counts are acceptable.
  - **The test:** Ask "what happens if the reader sees a stale value?" If the answer is "it does slightly more work but produces a correct result," `Relaxed` is fine.

---

## The Discovery Playbook

When a bug has been reported:

1. **Capture state before touching anything.** gdb `thread apply all bt full` → file. Once the process dies, the evidence is gone.
2. **Classify using the Symptom Triage Table.** Jump to the matching class above.
3. **Build the lock-wait graph** (gdb-for-debugging §Lock Graph). Prove the deadlock exists before you "fix" it — guessing is how you get four more of them.
4. **Fix the root cause, not the symptom.** A timeout on a deadlocked `.lock()` is a smoke alarm, not a fire extinguisher.
5. **Audit for the other three.** Every deadlock you find is a sample from a distribution. Run the static-audit recipes to find the rest. See [THE FOURTH INSTANCE](references/THE-FOURTH-INSTANCE.md).
6. **Add a regression test.** Stress tests with `--test-threads=N` and `loom` (Rust) or `go test -race`. Fuzz the scheduler with `rr --chaos` if you have it.

When doing a preemptive audit (no bug reported yet):

1. **Run the static audit recipes** ([STATIC-AUDIT.md](references/STATIC-AUDIT.md)) — ast-grep + ripgrep across the codebase for every pattern in the nine classes.
2. **Validate every finding before reporting** (see "Finding Validation Checklist" below).
3. **Enable `parking_lot` deadlock detection** in debug builds; run the test suite. Any detection is a proof of deadlock.
4. **Run TSAN** on the test suite.
5. **Run `loom`** (if Rust) on the core concurrency primitives of the project.
6. **Review every `unsafe impl Send/Sync`.** Each one is a hand-written promise the compiler couldn't check.

### Finding Validation Checklist

**Before reporting any static-audit finding, apply these filters. A finding that fails any filter is a false positive.**

1. **Construct a concrete interleaving.** Name the threads (T1, T2), list the exact operations in order, and show the state at each step. If you cannot construct an interleaving that reaches the bad state, it is not a bug. "This looks like it could be a problem" is not a finding.

2. **Check Rust ownership constraints.** If the state-mutating function requires `&mut self` and the reading functions take `&self`, concurrent access is prevented by the compiler. This is true even for atomics — `&mut self` IS synchronization. Check the function signatures of ALL writers.

3. **Trace the actual call chain.** For reentrancy (Class 5) and callback (Class 1) hazards: trace from the alleged re-entry point back to the lock acquisition. If the call chain does not actually re-enter, it is not a hazard. Do not flag patterns — flag paths.

4. **Measure the critical section duration.** For spin-loop concerns (Class 3): estimate the wall-clock time of the operation being waited on. Sub-microsecond operations (single atomic store, CAS on one slot, seqlock write) are correctly handled by `spin_loop()`. Recommending `yield_now()` or `sleep()` for nanosecond waits is an anti-optimization that harms performance by 100-1000x.

5. **Check what happens with a stale value.** For `Relaxed` ordering concerns (Class 9): determine the consequence of reading a stale value. If the stale value causes a harmless extra check (e.g., acquiring a lock and finding nothing), or produces an approximately-correct metric, it is not a bug. `Relaxed` paired with an external synchronization mechanism (Mutex gate, `&mut self` borrow, thread::spawn happens-before) is correct.

6. **Recognize correct condvar patterns.** The double-checked gate pattern (fast check → lock → re-check → condvar.wait, with notifier holding the same lock during state change + notify) is a standard correct protocol. Do not flag it as a lost-wakeup bug merely because it uses `if` instead of `while`. The post-wake predicate check handles spurious wakeups.

7. **Severity requires exploitability.** A pattern that matches a known hazard shape but cannot be triggered due to architectural constraints (e.g., a `Relaxed` load where the only store is behind `&mut self`) should be reported as "architecturally safe, fragile if refactored" — NOT as a bug. Reserve CRITICAL for findings where you can demonstrate a concrete failure scenario.

---

## Static Audit Recipes (High-ROI Greps)

See [STATIC-AUDIT.md](references/STATIC-AUDIT.md) for the full catalog. Highlights:

```bash
# Rust: guard held across await (manual inspection required)
rg -n --type rust -U 'let\s+\w+\s*=\s*.*\.(lock|read|write)\(\).*\n[^}]*\.await' .

# Rust: std::sync::Mutex inside async fn (smell)
ast-grep run -l Rust -p 'async fn $F($$$) { $$$ std::sync::Mutex $$$ }'

# Rust: block_on inside anywhere (double-check: may be inside a sync bridge)
rg -n --type rust 'block_on' .

# Rust: OnceLock / Lazy in LD_PRELOAD libs (Class 5)
rg -n --type rust 'OnceLock|OnceCell|Lazy::new|lazy_static!|thread_local!' crates/<preload>/

# Two different lock orderings in the same code (Class 1)
rg -n --type rust 'let\s+\w+\s*=\s*self\.\w+\.lock\(\)' . | sort -u

# SQLite: missing busy_timeout (Class 4)
rg -n 'Connection::open|open_in_memory' . | rg -v 'busy_timeout'

# Rust: unbounded channel (Class 2 back-pressure risk)
rg -n 'unbounded_channel|mpsc::unbounded' --type rust .

# Missing fairness on rwlock (Class 3)
rg -n 'RwLock::new' --type rust .  # review each for writer-starvation risk
```

---

## Fix Catalog (Canonical Replacements)

See [FIX-CATALOG.md](references/FIX-CATALOG.md). Summary:

| Broken Pattern | Replace With | Why |
|----------------|--------------|-----|
| `OnceLock` on LD_PRELOAD path | `AtomicU8` state machine | No allocator, no reentrancy |
| `std::sync::Mutex` held across `.await` | Scoped guard dropped before `.await` | Task yield with lock is a bug |
| Deep call holding two locks | Total lock order + assertion | Eliminate cycle possibility |
| Retry-on-BUSY tight loop | Exponential backoff + jitter | Break livelock |
| Connection-per-request SQLite | Single writer, read pool | Prevent lock escalation storms |
| Shared `Mutex<Vec<Work>>` | `mpsc::channel` + actor | No lock for producers |
| `lazy_static` in `LD_PRELOAD` | `const` / compile-time init | No lock needed |
| `std::Mutex` + panic risk | `parking_lot::Mutex` + transaction-style updates | No poisoning, clearer semantics |
| `flock` only in-process | `flock` + app-level lease + TTL | Multi-process coordination |

### NOT Broken (Common False Positives)

| Pattern That Looks Wrong | Why It's Actually Fine | How to Verify |
|--------------------------|----------------------|---------------|
| `AtomicBool::load(Relaxed)` as fast-path guard | The Mutex behind the guard is the real sync; stale `true` → harmless lock; stale `false` impossible if writer requires `&mut self` | Check: does stale value cause *incorrect* behavior or just *unnecessary work*? Check writer function signature. |
| SeqLock reader spin with `spin_loop()`, no yield | Write duration is nanoseconds; `yield_now()` costs microseconds; spin is 100-1000x faster than yielding | Estimate write critical section duration. If < 1 microsecond, spin is correct. |
| `OnceLock<Mutex<T>>` in a library init function | Safe unless the init closure re-enters the same OnceLock, or the function is called by the dynamic linker | Trace the call chain from `#[no_mangle]` export through the init closure. Does it re-enter? |
| Condvar with `if` instead of `while` (gate pattern) | Double-checked gate: fast check → lock → re-check → wait. Post-wake check handles spurious wakeups. Gate lock prevents notification between re-check and wait. | Verify: (1) notifier holds same gate lock, (2) predicate updated before notify, (3) waiter checks predicate after wake |
| CAS spin loop holding a read lock | If the CAS target is a single atomic slot, contention is sub-nanosecond; the read lock prevents Vec reallocation during CAS — this is intentional | Check: how many iterations does the CAS loop typically run? If 1-2, the read lock duration is negligible. |
| Nested locks with consistent ordering across all sites | If EVERY nested acquisition follows A→B order, there is no AB-BA deadlock. This is a proof of safety, not a risk. | Enumerate ALL acquisition sites for both locks. Any inconsistency is a real bug; total consistency is a clean bill. |

---

## Prevention by Design

1. **Prefer message passing over shared state.** An actor with a private mutable state + mpsc inbox has zero lock-ordering bugs, by construction.
2. **Single writer, many readers.** Most of our DB and state-sharing incidents would not have happened if writes were serialized through one owner.
3. **Ranks for locks.** Assign a total order; assert ascending acquisition. Loom or parking_lot can check in tests.
4. **Bound every queue / channel.** Unbounded is a leak; explicit bounds force you to reason about backpressure.
5. **Time-bound every wait.** `try_lock_for(Duration)` over `.lock()`; `timeout(Duration, fut).await` over bare `.await`. Every hang becomes a log line, not a stall.
6. **Encapsulate; don't share handles to shared state.** Give callers a method that does the operation, not a guard.
7. **Fix the worst class first.** By volume of pain across our incidents, the ranking is: **Class 4 (DB) > Class 2 (async) > Class 5 (runtime init) > Class 7 (swarm) > Class 1 (classic)**. Look at your own codebase and adjust.

---

## Validation

Before you declare a concurrency fix done:

- [ ] **Reproduce reliably**, or accept that you can't and run the fix past stress + TSAN + loom.
- [ ] **Add a test** that would have caught it: `#[test]` with `--test-threads=N`, or `loom::model`, or a stress harness with N=100× the old workload.
- [ ] **Run the static audit** ([STATIC-AUDIT.md](references/STATIC-AUDIT.md)) and find every other instance of the same hazard.
- [ ] **Document the fix in commit/bead** so future-you knows what you changed and why.
- [ ] **parking_lot deadlock_detection on** in debug builds; run tests; no detections.
- [ ] **TSAN clean** on the test suite (Rust/C/Go).
- [ ] **`loom::model`** passes for the critical primitive if Rust.

---

## References

### Core

| Topic | Reference |
|-------|-----------|
| **The Fourth Instance** (find ALL hazards, not just one) | [THE-FOURTH-INSTANCE.md](references/THE-FOURTH-INSTANCE.md) |
| **Static-audit recipes** (ast-grep + ripgrep, all languages) | [STATIC-AUDIT.md](references/STATIC-AUDIT.md) |
| **Fix catalog** (14+ canonical replacements) | [FIX-CATALOG.md](references/FIX-CATALOG.md) |
| **Diagnosis techniques** (pointers to gdb-for-debugging) | [DIAGNOSIS.md](references/DIAGNOSIS.md) |
| **Anti-patterns** (what NOT to do, all classes) | [ANTI-PATTERNS.md](references/ANTI-PATTERNS.md) |
| **Incident narratives** (8+ real project stories) | [INCIDENTS.md](references/INCIDENTS.md) |
| **Validation tooling** (TSAN, loom, miri, parking_lot, rr) | [VALIDATION.md](references/VALIDATION.md) |

### Language Cookbooks

| Language | Reference |
|----------|-----------|
| **Rust (asupersync)** — PRIMARY: Cx, Scope, obligations, lab/DPOR, structured concurrency | [ASUPERSYNC.md](references/ASUPERSYNC.md) |
| **Rust (tokio/std ecosystem)** — tokio, parking_lot, crossbeam, rayon, dashmap, sqlx | [RUST.md](references/RUST.md) |
| **Go** — goroutines, channels, sync, context, errgroup, pprof, race detector | [GO.md](references/GO.md) |
| **Python** — GIL, asyncio, threading, multiprocessing, trio/anyio, py-spy | [PYTHON.md](references/PYTHON.md) |
| **TypeScript / Node.js** — event loop, promises, worker_threads, React, Next.js, Prisma | [TYPESCRIPT.md](references/TYPESCRIPT.md) |

### Domain Deep-Dives

| Topic | Reference |
|-------|-----------|
| **Database concurrency** (SQLite WAL, PRAGMAs, retries) | [DATABASE.md](references/DATABASE.md) |
| **LD_PRELOAD / reentrant init** (glibc-rust incident) | [LD-PRELOAD.md](references/LD-PRELOAD.md) |
| **Async / await** (cross-language async patterns) | [ASYNC.md](references/ASYNC.md) |
| **Multi-process / swarm** (agent-mail, flock, leases) | [SWARM.md](references/SWARM.md) |
| **Distributed concurrency** (Redlock, pg_advisory, etcd, CRDTs, saga, outbox) | [DISTRIBUTED.md](references/DISTRIBUTED.md) |
| **Creative patterns** (actor, STM, CSP, structured concurrency, single-writer, "do nothing") | [CREATIVE-PATTERNS.md](references/CREATIVE-PATTERNS.md) |
| **Lock-free** (CAS, ABA, epoch reclamation, seqlocks, flat combiner, HTM) | [LOCK-FREE.md](references/LOCK-FREE.md) |
| **Formal methods** (loom, DPOR, TLA+, miri, linearizability, evidence ledgers) | [FORMAL-METHODS.md](references/FORMAL-METHODS.md) |
| **Resilience patterns** (circuit breaker, bulkhead, singleflight, backpressure, hedge, quorum) | [RESILIENCE-PATTERNS.md](references/RESILIENCE-PATTERNS.md) |
| **Concurrency operators** (composable diagnostic moves with triggers + failure modes + prompts) | [CONCURRENCY-OPERATORS.md](references/CONCURRENCY-OPERATORS.md) |
| **C/C++ systems** (pthread, memory model, signal safety, fork hazards, io_uring, epoll) | [C-CPP.md](references/C-CPP.md) |
| **Database advanced** (Postgres advisory, SKIP LOCKED, SSI, MVCC, Prisma/Drizzle, Redis) | [DATABASE-ADVANCED.md](references/DATABASE-ADVANCED.md) |
| **Cookbook index** (dispatch by language, topic, or bug class) | [COOKBOOK-INDEX.md](references/COOKBOOK-INDEX.md) |
| **Cross-language matrix** (primitive equivalents, same-bug-different-language, detection tools) | [CROSS-LANGUAGE.md](references/CROSS-LANGUAGE.md) |

### Companion Skills

| Skill | Use When |
|-------|----------|
| [`/cs/gdb-for-debugging/`](../gdb-for-debugging/SKILL.md) | Lock-graph construction, async runtime debugging, TSAN, rr |
| [`/cs/asupersync-mega-skill/`](../asupersync-mega-skill/SKILL.md) | Full asupersync runtime, migration, all reference files |
| [`/cs/agent-mail/`](../agent-mail/SKILL.md) | Advisory file reservations, multi-agent coordination |
| [`/cs/system-performance-remediation/`](../system-performance-remediation/SKILL.md) | Process triage, kill hierarchy, mux protection |

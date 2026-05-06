# Incident Narratives

Real concurrency incidents across projects in this repo. Each narrative: what broke, what we tried, what we learned. Sourced from CASS mining of past agent sessions (see [`CASS_SEARCHES_RELATED_TO_CONCURRENCY.md`](../../../CASS_SEARCHES_RELATED_TO_CONCURRENCY.md) at the repo root).

---

## Incident 1 — `glibc_rust` LD_PRELOAD strlen hang

**Project:** `glibc_rust` / `libglibc_rs_abi.so`
**Session:** `/home/ubuntu/.claude/projects/-data-projects-glibc-rust/3b5a23a6-2133-4efd-a713-7dd0256e313b/subagents/agent-ad965f6.jsonl` + parent session
**Class:** 5 (Runtime-init / reentrant hazards)

**Setup.** A Rust shared library interposing libc symbols (`strlen`, `malloc`, …) via LD_PRELOAD. The interposed `strlen` runs a policy check through `runtime_policy::check_ordering()` before delegating to the real libc `strlen`.

**Symptom.** `LD_PRELOAD=libglibc_rs_abi.so /tmp/test_short` hangs. Process in sleeping state, 0% CPU, single thread in `futex_wait`. The hang occurs during dynamic linker symbol resolution *before* any user code runs.

**Rounds of fix:**

- **Round 1.** Found three `OnceLock` instances on the call path from `strlen`. Replaced each with `AtomicU8` + `AtomicPtr` state machine (states UNINIT/INITIALIZING/READY). **Still hangs.**
- **Round 2.** The engineer writes a prompt to a subagent to find all remaining blocking primitives. That prompt is now the anchor quote in [THE-FOURTH-INSTANCE.md](THE-FOURTH-INSTANCE.md). The subagent discovers:
  - **Fourth instance:** `safety_level()` calls `std::env::var("GLIBC_RUST_MODE")`, which internally uses `strlen`, which is interposed, which calls `safety_level()`. Infinite loop.
  - **Fifth instance:** `RuntimeMathKernel::new()` contains **72 `parking_lot::Mutex` fields**. `Box::new(RuntimeMathKernel::new())` calls the allocator, which is also interposed.
  - **Sixth instance:** Rust stdlib's own lazy-initialized globals (panic handling, TLS) use blocking primitives internally.
- **Round 3.** The `LEVEL_RESOLVING=255` sentinel on `safety_level()` prevents direct reentrance of the env var lookup, but the other layered locks still hit. Not enough.
- **Round 4.** Proposed fix: **`LIBRARY_READY` atomic flag + raw-byte-loop fast path**. Standard jemalloc/tcmalloc recipe. `.init_array` eager init before any interposed call. Each exported symbol gates the "full" path behind `LIBRARY_READY.load(Acquire)` and falls back to a raw byte-loop implementation if not ready. Init also pre-warms `std::env::var`.

**Lessons.**

1. **The Fourth Instance rule.** Three fixes were not enough. Six would not have been enough without the raw fast path. You must audit exhaustively.
2. **Reentrancy via stdlib.** `std::env::var`, `Box::new`, `format!` — all innocent-looking, all allocate or touch blocking state. In an interposed library, none of them are safe during init.
3. **Fast-path pattern.** The jemalloc/tcmalloc `LIBRARY_READY` flag is the canonical solution for interposed libraries. Copy the pattern; don't re-invent it.
4. **`.init_array` alone is not sufficient.** Other threads (spawned before your library is loaded via `dlopen`) may see the flag false briefly. Use `Acquire`/`Release`.

**References:** [LD-PRELOAD.md](LD-PRELOAD.md), [THE-FOURTH-INSTANCE.md](THE-FOURTH-INSTANCE.md).

---

## Incident 2 — `jeffreys-skills-md` SQLite sync "database is locked"

**Project:** `jeffreys_skills_md` (SaaS backend)
**Session:** `/home/ubuntu/.claude/projects/-data-projects-jeffreys-skills-md/a5c1ec13-ea0b-4c3d-ad4b-3c25a6afbc01.jsonl` (line 607)
**Class:** 4 (Database concurrency)

**Symptom.** `br sync --flush-only` fails with `database is locked`. Multi-process sync races each other for the exclusive write lock. Default `busy_timeout = 0` means losers fail instantly.

**Diagnosis flow observed in the session:**

```
[Bash — Check if database is locked]
[Bash — Kill locking process and sync]
[Bash — Check DB lock holders]
```

`lsof` identifies the holder PID, `kill -9` frees the lock, retry the sync.

**This is the diagnosis flow of last resort.** Using `kill` to break a lock means the architecture has failed: no `busy_timeout`, no retry, no writer coordination.

**The architectural fix:**
1. Set `PRAGMA busy_timeout = 5000` on every connection open.
2. Single-writer coordinator for `br sync` operations (one process owns the DB; others submit work).
3. Application-level retry with exp backoff on `SQLITE_BUSY` as a belt-and-suspenders.
4. `BEGIN IMMEDIATE` for any transaction that will write.

**Lessons.**

1. **"database is locked" is an architectural smell, not a transient error.** Fix the architecture.
2. **If your diagnosis flow involves `kill`, you've already lost.**
3. **Multi-process writers to SQLite are almost always wrong.** Use a coordinator.

**References:** [DATABASE.md](DATABASE.md).

---

## Incident 3 — `jeffreys-skills-md` shared test fixture lock poisoning

**Project:** `jeffreys_skills_md`
**Session:** `/home/ubuntu/.claude/projects/-data-projects-jeffreys-skills-md/572c0464-3116-4efe-aba7-807a5cfc6e30.jsonl` (line 242)
**Class:** 8 (Poisoning & partial state)

**Symptom.** Tests fail with `PoisonError` on the `env_lock` shared mutex. Quote from the session: *"The lock is getting poisoned. Let me check if project.rs also needs the shared lock."*

**Root cause.** The test suite uses a global `std::sync::Mutex<()>` to serialize tests that mutate process-wide state (`std::env::set_var`, `std::env::set_current_dir`). When any single test panics inside the critical section, the mutex is poisoned. Every subsequent test sees `PoisonError` on `lock()`.

**Fix options:**
1. **`parking_lot::Mutex`.** Doesn't poison. Callers see the guard even if the previous holder panicked.
2. **Transactional update + catch_unwind.** Build new state in a local; swap at end; catch panic outside the critical section.
3. **Isolation per test.** Each test gets its own temp dir + env set; no shared state to lock.

**Lessons.**

1. **`std::sync::Mutex` poisoning is a landmine in test suites.** Panics are normal in tests (failing assertions, expected panics). Every panic with a held lock cascades.
2. **`parking_lot::Mutex` is usually better for test fixtures.** You lose the built-in poison-on-panic signal, but you gain resilience.
3. **Shared mutable state in tests is a smell.** Prefer per-test isolation when possible.

**References:** [FIX-CATALOG.md](FIX-CATALOG.md) §8, [ANTI-PATTERNS.md](ANTI-PATTERNS.md) Class 8.

---

## Incident 4 — Multi-process SQLite WAL contention (codex 2026-02-07 cluster)

**Project:** Various
**Sessions:** `codex/sessions/2026/02/07/rollout-*.jsonl` (cluster of 4 sessions)
**Class:** 4 (Database) + 3 (Livelock)

**Symptom.** A process is livelocked on SQLite WAL operations — high CPU, constant `SQLITE_BUSY`, no progress. `wal_checkpoint(FULL)` hangs because long-running readers hold the DB, but readers can't make progress because they're retrying on BUSY.

**Root causes.**
1. No `busy_timeout` (instant BUSY → retry storm).
2. Retry loop has no backoff → livelock.
3. WAL checkpoint mode is `FULL`, which blocks until all readers finish. With constantly-retrying readers, this never happens.

**Fix.**
1. `busy_timeout = 5000`.
2. Retry with exp backoff + jitter (not tight loop).
3. `PRAGMA wal_checkpoint(PASSIVE)` on schedule; `TRUNCATE` at well-known quiescent points; avoid `FULL`/`RESTART` unless readers are known to be idle.

**Lessons.**

1. **Livelock is not deadlock, but is just as bad.** 100% CPU + no progress is still "stuck".
2. **WAL checkpoint modes matter.** `PASSIVE` doesn't block; `FULL/RESTART` do. Default to `PASSIVE`.
3. **Retry + no backoff = livelock generator.** Always add backoff.

**References:** [DATABASE.md](DATABASE.md), [FIX-CATALOG.md](FIX-CATALOG.md) §4.

---

## Incident 5 — `remote-compilation-helper` async-lock across await

**Project:** `remote-compilation-helper`
**Session:** `/home/ubuntu/.claude/projects/-data-projects-remote-compilation-helper/bb4cecdf-7a1b-4583-87d3-e714e982be3c.jsonl` (6+ hits on "await held lock")
**Class:** 2 (Async deadlock)

**Symptom.** RPC handler hangs under load. Individual requests complete in isolation but under concurrency, handlers stall. tokio-console shows tasks in "waiting" state indefinitely.

**Root cause.** A `Mutex<State>` guard is acquired early in a handler, then an `.await` for remote compilation occurs, then the guard is used again. The guard lives across the yield. When another request enters the same code path, it blocks the worker thread waiting for the lock. The first request can't make progress because the worker it needs is blocked by the second request.

**Fix.** Drop the guard before the await:

```rust
// Before
let mut g = state.lock().unwrap();
let plan = g.make_plan();
let result = remote_compile(plan).await?;     // BUG: g still alive
g.record(result);

// After
let plan = {
    let mut g = state.lock().unwrap();
    g.make_plan()
};  // g dropped
let result = remote_compile(plan).await?;
state.lock().unwrap().record(result);
```

**Lessons.**

1. **`clippy::await_holding_lock` catches this.** Turn it on in CI. Don't rely on code review.
2. **The fix is almost always "drop the guard first", not "switch to tokio::Mutex".** The async mutex is slower and still risks the same bug if misused.
3. **tokio-console is essential.** Without it, this bug looks like "sometimes slow" until it looks like "completely hung".

**References:** [ASYNC.md](ASYNC.md) Pattern 1, [STATIC-AUDIT.md](STATIC-AUDIT.md) Class 2.

---

## Incident 6 — Wezterm mux server OOM (system-performance-remediation)

**Project:** trj host (499GB RAM, btrfs)
**Date:** 2026-02-23
**Class:** 7 (Multi-process/swarm) with resource-starvation component

**Symptom.** `vfs_cache_pressure=50` let btrfs inode/dentry caches balloon to 388GB page cache + 40GB slab. Memory pressure hit 18%. `systemd-oomd` killed `user@1000.service`, destroying `wezterm-mux-server` and **all 382 agent sessions** instantly.

**Root cause.** The mux server was not protected; OOM killer chose it as the largest memory consumer in the user slice, ignoring that it was the root of every agent session.

**Fix.**
1. `vfs_cache_pressure=200` — reclaim filesystem cache aggressively.
2. `vm.min_free_kbytes=2GB` — keep enough for page-cache workers.
3. Drop caches periodically.
4. OOM protection for the mux server (systemd `OOMScoreAdjust=-1000`).
5. Monitoring on `/proc/pressure/memory`.

**Lessons.**

1. **The mux server is a single point of failure.** Protect it at every level.
2. **"Free" RAM is lying to you.** The kernel hoards page cache; reclaim cost rises nonlinearly under pressure.
3. **OOM killer makes decisions you won't like.** Make sure critical processes have `OOMScoreAdjust=-1000`.

**References:** [SWARM.md](SWARM.md), system-performance-remediation skill.

---

## Incident 7 — `frankensqlite` `OnceLock` deadlock (line 768)

**Project:** `frankensqlite`
**Session:** `/home/ubuntu/.claude/projects/-data-projects-frankensqlite/6f58dbe1-2e20-4842-81b5-7b5988f6f1c2.jsonl` (line 768)
**Class:** 5 (Runtime-init reentrancy) + 8 (Poisoning)

**Symptom.** On startup, the first call to a SQLite wrapper hangs. The second call to the same wrapper panics with `PoisonError`.

**Root cause.** A `OnceLock<SqliteConfig>::get_or_init(...)` closure can panic (e.g., bad PRAGMA string). On pre-1.70 Rust, if the closure panics, the OnceLock is permanently poisoned — subsequent calls block forever. On 1.70+, they return `PoisonError`.

**Fix.**
1. Upgrade to Rust ≥1.70.
2. Ensure the closure cannot panic (validate inputs before calling, wrap in `Result`, use `OnceCell` with explicit error handling).
3. Don't use `OnceLock` for anything that can fail. Use `OnceLock<Result<T, E>>` or a custom initializer.

**Lessons.**

1. **`OnceLock::get_or_init` is not panic-safe pre-1.70.** Upgrade.
2. **Lazy init + fallible construction is a trap.** If init can fail, return a `Result`, not a panic.
3. **Test the panic path of your init closure.** In CI, force it to panic and verify the system recovers.

---

## Incident 8 — tmux pane hung, agent swarm stalled (`ntm` cluster)

**Projects:** `ntm` (`c06045ff`, `dc0b4cab`, `9efd1ae4`)
**Class:** 7 (Multi-process)

**Symptom.** A tmux pane running an agent is "hung" — no output, no progress. `tmux send-keys` does not reach the agent. Other panes are fine.

**Root causes (observed across sessions):**
1. Child process (the agent) holds a file descriptor tmux needs for I/O.
2. Agent is in a blocking call (network, DB, subprocess wait).
3. Parent shell inside the pane crashed; the pane is orphaned.
4. Zombie process occupying the pane.

**Fix.**
1. `tmux capture-pane -p -t $PANE` to see last output.
2. `ps -eLf | grep $PID` to find the actual stuck thread.
3. `gdb --batch -ex "thread apply all bt" -p $PID` for diagnosis.
4. `tmux kill-pane -t $PANE` to cleanly reap the pane.
5. Restart the agent in a fresh pane.

**Crucially:** **do not kill the mux server.** Kill individual panes only. See Incident 6.

**Lessons.**

1. **Process isolation via tmux is the right design.** Each agent gets its own process, its own memory, its own locks. Faults are isolated.
2. **The cost of process isolation is IPC overhead.** Worth it for fault tolerance.
3. **`tmux kill-pane` is the blunt instrument; `tmux kill-session` kills every pane in that session (still OK); `kill wezterm-mux-server` is catastrophic.**

**References:** [SWARM.md](SWARM.md), `system-performance-remediation` skill, `wezterm` skill.

---

## Incident 9 — frankensqlite CommitSequenceCombiner contention

**Project:** `frankensqlite`
**Session:** `/home/ubuntu/.claude/projects/-data-projects-frankensqlite/6f58dbe1-2e20-4842-81b5-7b5988f6f1c2.jsonl` (line 1259)
**Class:** 3 (Livelock) + 1 (Classic deadlock under contention)

**Symptom.** Under concurrent transaction commits (>8 threads), the shared `commit_seq` counter becomes a serialization point. All threads contend on a single `AtomicU64::fetch_add`, causing cache-line ping-pong and throughput collapse.

**Diagnosis.** `ContendedMutex` lock-metrics showed high wait times on the commit sequence allocation path. Profiling revealed cache-line bouncing on the atomic counter.

**Fix.** Flat Combiner pattern: `CommitSequenceCombiner` batches `fetch_add` operations. One "combiner" thread executes batched requests while others spin on per-thread result slots. Data stays hot in one core's L1 cache. Measured 2-3x improvement under high contention.

**Lessons.**
1. **Flat combiner is a real pattern, not just theory.** Implemented and tested with 6 unit tests in frankensqlite.
2. **Contention must be measured, not assumed.** `ContendedMutex` with `lock-metrics` gave the evidence.
3. **Only beneficial above ~8 contending threads.** Below that, the overhead of the combiner exceeds the savings.

---

## Incident 10 — mcp-agent-mail-rust CoalesceMap loom verification

**Project:** `mcp-agent-mail-rust`
**Session:** `/home/ubuntu/.claude/projects/-data-projects-mcp-agent-mail-rust/24c75547-a44e-4d96-9dbe-f27cd8f27c72.jsonl` (line 938)
**Class:** 6 (Data race) — caught by formal method, not runtime failure

**Symptom.** The `CoalesceMap` (singleflight/request-coalescing utility) uses `Mutex<HashMap>` + `Condvar` for concurrent leader/joiner coordination. Code review couldn't prove it was correct under all orderings.

**Diagnosis.** Loom model-checking (`loom::model`) was applied to the `execute_or_join` function. Loom explored all thread interleavings and verified:
- Leader always completes before joiners read the result
- Joiners never see partial state
- Error propagation from leader to joiners is consistent
- Cache eviction doesn't corrupt in-flight results

31 edge-case tests were written covering: max entries, timeout, concurrent leaders, cache eviction, error propagation, re-entry after eviction, mixed success/failure.

**Fix.** No code change needed — loom *proved* the implementation correct. The value was confidence, not a bug fix.

**Lessons.**
1. **Loom finds bugs you can't.** But it also proves correctness you can't. Both are valuable.
2. **Write loom tests for your concurrency primitives.** Not your whole app — just the critical 20 lines.
3. **31 edge-case tests** is the density you need for a concurrent primitive that will be used across the codebase.

---

## Incident 11 — NTM data race in rotation.go

**Project:** `ntm` (Go)
**Session:** `/home/ubuntu/.claude/projects/-data-projects-ntm/ccdc3efb-e713-428e-964d-b670dd874b9d/subagents/agent-ac00089b87a719408.jsonl`
**Class:** 6 (Data race)

**Symptom.** `go test -race` reports concurrent map/slice mutations in `rotation.go`. Multiple goroutines call `CheckAndRotate()`, `createPendingRotation()`, `GetPendingRotation()`, `ConfirmRotation()`, `CancelPendingRotation()`, `rotateAgent()`, `GetHistory()`, `ClearHistory()` — all accessing `r.pending` map and `r.history` slice without synchronization.

**Additional bug:** `GetHistory()` at line 781 returns `r.history` directly — the caller gets a pointer to the mutable backing array, allowing concurrent modification from outside the struct.

**Fix.**
1. Add `sync.RWMutex` to `RotationManager`
2. Wrap every access to `pending` and `history` in `mu.RLock()`/`mu.Lock()`
3. `GetHistory()` returns a deep copy: `return append([]RotationEvent{}, r.history...)`

**Lessons.**
1. **Go maps are not thread-safe.** `fatal error: concurrent map iteration and map write` is a runtime panic, not a compile error.
2. **Returning a slice from a struct is returning a mutable reference.** Deep copy or use `sync.Map`.
3. **`go test -race` catches this.** Run it in CI on every PR.

---

## Incident 12 — NTM signal.Notify goroutine leak

**Project:** `ntm` (Go, third-party bubbletea dependency)
**Session:** `/home/ubuntu/.claude/projects/-data-projects-ntm/ccdc3efb-e713-428e-964d-b670dd874b9d/subagents/agent-af75bf394e1a5e231.jsonl` (line 22)
**Class:** 2 (Orphan task / goroutine leak)

**Symptom.** `signal.Notify(c, syscall.SIGCONT)` in `suspendProcess()` (bubbletea `tty_unix.go:45`) registers a signal handler but never calls `signal.Stop(c)`. If the function returns without receiving the signal, the goroutine blocking on `<-c` leaks.

**Audit finding.** All other 12 occurrences of `signal.Notify` in the NTM codebase have matching `signal.Stop` calls or use `signal.NotifyContext` with deferred stop. This one was in a third-party dependency.

**Fix.** Use `signal.NotifyContext` (Go 1.16+):
```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGCONT)
defer stop()
<-ctx.Done()
```

**Lessons.**
1. **Third-party code has concurrency bugs too.** Audit dependencies that use goroutines, channels, or signal handlers.
2. **`signal.NotifyContext` + `defer stop()`** is the idiomatic Go pattern. It handles cleanup automatically.
3. **Goroutine leaks are invisible** unless you monitor with pprof. The goroutine just sits there consuming memory.

---

## Incident 13 — asupersync obligation leak detection

**Project:** `asupersync`
**Session:** Various asupersync test suites
**Class:** 8 (Poisoning / partial state) — prevented by design

**Setup.** A test exercises two-phase channel send: `reserve(cx).await` obtains a `TrackedPermit`, then the test returns early without calling `permit.send()` or `permit.abort()`.

**What happens in Tokio:** Nothing. The permit is dropped. The channel may be in an inconsistent state. No error, no warning, no detection. The bug is silent until something downstream hangs.

**What happens in asupersync:**
1. `TrackedPermit::drop()` fires because the obligation token was not consumed
2. The drop impl panics: `"OBLIGATION TOKEN LEAKED: TrackedPermit(mpsc)"`
3. The `ObligationLeakOracle` in the lab runtime records the violation
4. The test fails with a structured evidence ledger showing: which obligation, who held it, when, and why it was leaked

**Lessons.**
1. **Obligation tracking converts silent bugs into loud failures.** The bug that would have caused a mysterious hang in Tokio is a panicking assertion in asupersync.
2. **Two-phase send (reserve/commit) is not just an API difference.** It's a correctness mechanism: the permit is a linear resource that must be consumed.
3. **Lab oracles catch bugs that unit tests miss.** The oracle checks every obligation at region close, not just the ones you thought to assert on.

---

## Incident 14 — frankensqlite epoch-based reclamation + HTM danger

**Project:** `frankensqlite` (design document)
**Session:** alien-cs-graveyard §4.2 / §14.10
**Class:** 9 (Memory ordering) + 5 (Runtime init hazard)

**Setup.** The design calls for Hardware Transactional Memory (Intel TSX RTM) to speculatively execute short critical sections (~5-8ns fast path), with a fallback to mutex if the transaction aborts.

**The danger (documented in graveyard):**
> "HTM abort during EBR epoch-pinned section can skip epoch advancement → unbounded memory (HIGH risk)."

When a thread is pinned to an epoch (reading a lock-free data structure) and the HTM transaction aborts, the abort handler may not re-pin the epoch. The thread appears "un-pinned" to the epoch reclamation system, but is actually still reading the data. If the epoch advances and the data is freed, the thread reads freed memory.

**Mitigation (documented):** Disable HTM during EBR critical sections. Only use HTM for non-epoch-sensitive paths.

**Lessons.**
1. **Advanced concurrency techniques can interact in dangerous ways.** HTM + EBR is each correct independently; combined, they break.
2. **Document interactions between concurrency mechanisms.** The graveyard's §4.2/§14.10 cross-reference is exactly how to flag these.
3. **Not every optimization is worth the risk.** HTM saves 5-8ns per operation but introduces an unbounded-memory failure mode. Measure the value before accepting the risk.

---

## Meta-lessons across incidents

1. **Exhaustive audit > iterative fixing.** Every incident had hidden instances. Find them all.
2. **State capture before triage.** Diagnose from evidence, not guessing.
3. **Single-writer simplifies everything.** The majority of multi-process SQLite incidents would not have happened with a single-writer design.
4. **Architecture > tools.** `busy_timeout` + retry is a patch; single-writer is a fix.
5. **Process isolation is worth the overhead.** One hung agent is cheap to kill; one hung mutex in a shared process is expensive.
6. **The language's memory model matters.** Rust's `Acquire`/`Release` are not decorations; they're load-bearing.

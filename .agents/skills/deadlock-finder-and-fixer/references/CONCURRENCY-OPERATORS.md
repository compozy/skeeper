# Concurrency Debugging Operators

Operators are reusable cognitive moves for finding and fixing concurrency bugs. Each operator has explicit triggers, failure modes, and a copy-paste prompt module. They compose into diagnostic chains.

Adapted from the [operationalizing-expertise](../../operationalizing-expertise/SKILL.md) methodology.

---

## Operator Index

| Symbol | Name | Use When |
|--------|------|----------|
| **T** | Trace-All-Locks | Process hung; need to find every blocking primitive on a call path |
| **G** | Graph-Build | Two or more threads blocked; need to prove a deadlock cycle |
| **E** | Exhaust-Search | Found one instance of a hazard; need to find ALL others |
| **C** | Classify-Symptom | Something is stuck; don't know which of the 9 classes |
| **S** | Snapshot-State | Process misbehaving; need evidence before it disappears |
| **R** | Replace-Primitive | Identified the wrong primitive; need the canonical replacement |
| **D** | Deterministic-Repro | Bug is timing-dependent; need to make it reproducible |
| **A** | Audit-Static | No bug reported yet; proactive hazard search |
| **V** | Validate-Fix | Fix applied; need to verify it's correct and complete |
| **P** | Prevent-by-Design | Keep hitting the same class of bug; need an architectural change |

---

## T — Trace-All-Locks

**Definition:** Systematically enumerate every blocking primitive on a call path from entry point to the observed hang.

**When-to-Use Triggers:**
- A function hangs but you don't know why
- You fixed one lock but the hang persists (the "fourth instance")
- Migrating code and need to find all sync points
- Reviewing an LD_PRELOAD library's init path

**Failure Modes:**
- Stopping at the first blocking primitive found → miss the fourth instance
- Not following into transitive dependencies (stdlib, allocator, env vars)
- Confusing "holds a lock" with "waits for a lock" → wrong direction in the graph
- Missing `Box::new()` / `format!()` / `String::from()` as implicit lock sites (allocator)

**Prompt Module:**
```text
[OPERATOR: T Trace-All-Locks]
1) Identify the entry point (the function that hangs).
2) Trace EVERY function call on the path from entry to the hang point.
3) For each function, check: does it acquire any of these?
   - Mutex, RwLock, OnceLock, OnceCell, Lazy, lazy_static, thread_local
   - Semaphore, Condvar, Barrier
   - Channel send/recv (bounded = potential block)
   - Allocator (Box::new, vec!, format!, String::from)
   - File I/O (flock, SQLite, advisory locks)
   - std::env::var (may call strlen which may be interposed)
4) Record: {function_name, file:line, primitive_type, blocking_behavior}
5) Repeat for EVERY branch, not just the happy path.
6) Output: complete table of all blocking sites on the call path.

Output (required): blocking-primitive table with file:line for each
Anchors: THE-FOURTH-INSTANCE.md, LD-PRELOAD.md
```

**Canonical tag:** `trace-all-locks`
**Quote-bank anchors:** Cluster A anchor quote (glibc-rust LD_PRELOAD)

---

## G — Graph-Build

**Definition:** Construct the lock-wait graph from a thread dump to prove or disprove a deadlock cycle.

**When-to-Use Triggers:**
- Two or more threads are blocked in `futex_wait` / `__lll_lock_wait`
- gdb backtrace shows multiple threads contending
- Process is 0% CPU but unresponsive

**Failure Modes:**
- Building the graph from code analysis instead of live state → may not reflect actual contention
- Not reading the `__owner` field on contested mutexes → can't identify who holds the lock
- Confusing async task deadlock (all workers in epoll_wait) with thread deadlock (workers in futex)
- Assuming one cycle is the only cycle (there may be multiple independent deadlocks)

**Prompt Module:**
```text
[OPERATOR: G Graph-Build]
1) Capture full state: gdb --batch -ex "thread apply all bt full" -p $PID > /tmp/bt.txt
2) Find all threads in __lll_lock_wait or futex_wait:
   grep -B15 '__lll_lock_wait|futex_wait_queue' /tmp/bt.txt | grep -E 'Thread|mutex='
3) For each blocked thread, extract the mutex address being waited on.
4) For each contested mutex, read the owner:
   gdb --batch -ex "print ((pthread_mutex_t*)0xADDR)->__data.__owner" -p $PID
5) Build directed graph: T_blocked → M_waited → T_holder
6) Check for cycles. If cycle found → DEADLOCK PROVEN.
7) If no cycle → not a classic deadlock; consider async deadlock, livelock, or starvation.

Output (required): wait-for graph (nodes = threads + mutexes, edges = waits/holds)
Anchors: gdb-for-debugging §"Lock Graph Construction", DIAGNOSIS.md
```

**Canonical tag:** `graph-build`

---

## E — Exhaust-Search

**Definition:** Given one instance of a concurrency hazard, find every other instance in the codebase. The [Fourth Instance](THE-FOURTH-INSTANCE.md) operator.

**When-to-Use Triggers:**
- Just fixed a deadlock and want to make sure there are no more
- Found one `std::sync::Mutex` held across `.await` — are there others?
- Discovered a missing `busy_timeout` on one `Connection::open` — any others?
- Fixed one `OnceLock` on a reentrant path — are there more?

**Failure Modes:**
- Searching only the file where the bug was found (must search the entire repo)
- Using too-narrow search terms (miss variants like `RwLock::write` vs `Mutex::lock`)
- Declaring done without documenting the search and disposition of each hit
- Not adding a CI-level regression check to prevent reintroduction

**Prompt Module:**
```text
[OPERATOR: E Exhaust-Search]
1) Name the hazard precisely. One sentence. Example: "std::sync::Mutex guard held across .await in async fn"
2) Write the search query:
   - ast-grep for structural matches
   - ripgrep for textual matches
   - Be over-inclusive on purpose (false positives are cheap)
3) Run the search across the ENTIRE repo/monorepo. Not just the file where the bug was.
4) For each hit, disposition as:
   - BUG: same hazard, needs fix
   - SAFE: not the same hazard, document why
   - UNKNOWN: investigate further
5) Fix all BUG hits.
6) Add a regression check (CI grep, ast-grep rule, clippy lint, loom test).
7) Record: {search_query, total_hits, bug_count, safe_count, unknown_count}

Output (required): audit table with disposition per hit + regression rule added
Anchors: THE-FOURTH-INSTANCE.md, STATIC-AUDIT.md
```

**Canonical tag:** `exhaust-search`

---

## C — Classify-Symptom

**Definition:** Given an observed misbehavior, determine which of the nine concurrency bug classes it belongs to, then jump to the right section.

**When-to-Use Triggers:**
- Something is stuck and you don't know where to start
- A test flakes under load
- Error messages mention locks, timeouts, or database contention
- Performance degrades under concurrency

**Failure Modes:**
- Jumping to "it's a deadlock" without capturing evidence first
- Confusing livelock (100% CPU, no progress) with deadlock (0% CPU, no progress)
- Confusing async deadlock (workers idle in epoll_wait) with thread deadlock (workers in futex)
- Missing the distinction between "hung" and "slow" (starvation vs deadlock)

**Prompt Module:**
```text
[OPERATOR: C Classify-Symptom]
1) Capture evidence: S Snapshot-State first (never skip this).
2) Check CPU usage of the process:
   - 0% CPU + unresponsive → Class 1 (deadlock) or Class 2 (async deadlock)
   - 100% CPU + no progress → Class 3 (livelock) or task starvation
   - Normal CPU + wrong results → Class 6 (data race) or Class 9 (memory ordering)
   - "database is locked" → Class 4
   - Hang during library load → Class 5
   - Test flakes under --test-threads=N → Class 6
   - Two agents editing same file → Class 7
   - PoisonError → Class 8
3) Cross-reference with the Symptom Triage Table in SKILL.md.
4) Jump to the matching class section.

Output (required): class number + confidence + evidence that supports classification
Anchors: SKILL.md §"Symptom Triage Table"
```

**Canonical tag:** `classify-symptom`

---

## S — Snapshot-State

**Definition:** Capture the complete state of a misbehaving process before touching anything. Evidence disappears when the process dies.

**When-to-Use Triggers:**
- ANY process is hanging, spinning, or misbehaving
- Before attaching a debugger
- Before killing and restarting
- Before "trying a fix" on a live system

**Failure Modes:**
- Killing the process before capturing state → evidence gone
- Capturing only a partial snapshot (backtrace but not /proc maps, lsof, strace)
- Saving to a location that gets cleaned up

**Prompt Module:**
```text
[OPERATOR: S Snapshot-State]
1) Create evidence directory: mkdir -p /tmp/hang-$(date +%s) && cd $_
2) Capture all of these IN PARALLEL (process may die any moment):
   - ps -Lp $PID -o tid,pcpu,pmem,stat,comm --no-headers > threads.txt
   - gdb --batch -ex "set pagination off" -ex "thread apply all bt full" -p $PID > gdb.txt 2>&1
   - timeout 5s strace -k -f -p $PID -o strace.txt &
   - cat /proc/$PID/maps > maps.txt
   - cat /proc/$PID/status > status.txt
   - for tid in $(ls /proc/$PID/task); do cat /proc/$PID/task/$tid/stack > task_$tid.txt; done
   - lsof -p $PID > lsof.txt
3) ONLY THEN proceed to diagnosis.

Output (required): path to evidence directory with all captured files
Anchors: DIAGNOSIS.md §"Artifact Collection for Post-Mortem"
```

**Canonical tag:** `snapshot-state`

---

## R — Replace-Primitive

**Definition:** Given an identified wrong-primitive bug, apply the canonical replacement from the fix catalog.

**When-to-Use Triggers:**
- Diagnosis identified `std::sync::Mutex` across `.await` → need to replace
- Found `OnceLock` on LD_PRELOAD path → need atomic state machine
- Found `rusqlite` in async handler without `spawn_blocking` → need wrapper
- Found multi-writer SQLite → need single-writer architecture

**Failure Modes:**
- Applying the replacement only at the one call site you found → must E Exhaust-Search first
- Choosing `tokio::sync::Mutex` as the default replacement → it's slower; prefer dropping guard
- Replacing shared state with channels without understanding the ownership model → may introduce new bugs

**Prompt Module:**
```text
[OPERATOR: R Replace-Primitive]
1) Look up the broken pattern in FIX-CATALOG.md.
2) Verify the replacement is appropriate for this specific context.
3) Apply the replacement at THIS call site.
4) Run E Exhaust-Search to find ALL other instances.
5) Apply the replacement at every other instance.
6) Run the test suite + relevant validation (TSAN, loom, lab tests).

Output (required): diff of all replacements + test results
Anchors: FIX-CATALOG.md, THE-FOURTH-INSTANCE.md
```

**Canonical tag:** `replace-primitive`

---

## D — Deterministic-Repro

**Definition:** Convert a timing-dependent concurrency bug into a deterministic, replayable test case.

**When-to-Use Triggers:**
- Bug only reproduces "sometimes" or "under load"
- Need to verify a fix without relying on timing
- Writing a regression test for a concurrency bug

**Failure Modes:**
- Writing a stress test that "usually" catches it → not deterministic; will eventually be skipped
- Using `sleep()` to widen the race window → fragile; depends on system load
- Not considering that the fix itself might introduce a new timing dependency

**Prompt Module:**
```text
[OPERATOR: D Deterministic-Repro]
1) Choose the right tool for the language:
   - Rust (asupersync): LabRuntime + DPOR (best: explores ALL interleavings)
   - Rust (tokio): loom::model for primitives; rr --chaos for whole-app
   - Go: go test -race + high goroutine count
   - Python: asyncio debug=True + PYTHONASYNCIODEBUG=1
   - C/C++: rr record --chaos + TSAN
2) Write the minimal test that exercises the concurrent paths:
   - Spawn N tasks/threads/goroutines
   - Use barriers or Notify to force contention
   - Add assertions on shared state
3) Run the test:
   - With DPOR/loom: explores all orderings automatically
   - With rr --chaos: run 100+ times with random scheduling
   - With race detector: verify clean output
4) If the bug reproduces → you have a regression test. Keep it.
5) If it doesn't → increase N, add delays, or narrow the suspect code.

Output (required): deterministic test that catches the bug on every run
Anchors: VALIDATION.md, ASUPERSYNC.md §"Lab Runtime and DPOR"
```

**Canonical tag:** `deterministic-repro`

---

## A — Audit-Static

**Definition:** Proactively search a codebase for concurrency hazards before any bug is reported.

**When-to-Use Triggers:**
- Starting work on a new codebase
- Before a release / code freeze
- After a migration (e.g., Tokio → asupersync)
- Periodic hygiene (quarterly audit)

**Failure Modes:**
- Running only clippy → misses Classes 1, 3, 4, 5, 7, 8, 9
- Skipping the full STATIC-AUDIT.md script → partial coverage
- Finding many hits but not dispositioning each → audit theater

**Prompt Module:**
```text
[OPERATOR: A Audit-Static]
1) Run the full audit script from STATIC-AUDIT.md §"Running the Full Audit".
2) For EACH class (1-9), review the hits:
   - Disposition each as BUG / SAFE / UNKNOWN
   - For BUG: create a tracking issue (bead) with priority
   - For UNKNOWN: investigate further before proceeding
3) Run TSAN / go test -race / asyncio debug for runtime coverage.
4) Run loom / DPOR / lab tests on core primitives.
5) Record: {class, total_hits, bug_count, safe_count, unknown_count}
6) Add CI checks for the most common hazards.

Output (required): per-class audit summary + tracking issues for all BUGs
Anchors: STATIC-AUDIT.md, VALIDATION.md
```

**Canonical tag:** `audit-static`

---

## V — Validate-Fix

**Definition:** After applying a fix, verify that it's correct, complete, and won't regress.

**When-to-Use Triggers:**
- Just applied a concurrency fix
- About to merge a PR that touches concurrent code
- Verifying a migration (Tokio → asupersync)

**Failure Modes:**
- Running only the existing test suite → tests may not cover the concurrent path
- Declaring done after "tests pass" → no new regression test added
- Not running the exhaust search → other instances of the same bug remain

**Prompt Module:**
```text
[OPERATOR: V Validate-Fix]
1) Verify the fix compiles and passes existing tests.
2) Write a NEW regression test that would have caught the original bug:
   - Use D Deterministic-Repro methodology
   - The test must FAIL without the fix and PASS with it
3) Run E Exhaust-Search for other instances of the same hazard.
4) Run the validation suite from VALIDATION.md:
   - [ ] clippy::await_holding_lock (if async Rust)
   - [ ] TSAN / go test -race (data races)
   - [ ] loom / DPOR / lab tests (primitives)
   - [ ] parking_lot deadlock_detection (mutex deadlocks)
   - [ ] Stress test (100× iterations, high concurrency)
5) Document the fix in the commit message: what, why, and what the regression test covers.

Output (required): regression test + exhaust-search results + validation report
Anchors: VALIDATION.md, THE-FOURTH-INSTANCE.md
```

**Canonical tag:** `validate-fix`

---

## P — Prevent-by-Design

**Definition:** When a class of concurrency bug keeps recurring, restructure the architecture to make the bug class impossible.

**When-to-Use Triggers:**
- Third time fixing the same type of bug in the same codebase
- Migrating to a new runtime (opportunity to redesign)
- Greenfield design (get it right from the start)

**Failure Modes:**
- Over-engineering (STM for a simple counter)
- Under-engineering (keeping `Arc<Mutex>` when an actor would eliminate the class)
- Choosing a pattern that's correct but nobody on the team understands → maintenance burden

**Prompt Module:**
```text
[OPERATOR: P Prevent-by-Design]
1) Identify the recurring bug class.
2) Consult CREATIVE-PATTERNS.md for architectural alternatives:
   - Actor model (eliminates shared state)
   - Structured concurrency (eliminates orphan tasks)
   - Single-writer (eliminates write-write conflicts)
   - Two-phase channels (eliminates half-sent messages)
   - Advisory leases (eliminates cross-process deadlock)
3) Choose the simplest pattern that eliminates the bug class entirely.
4) Implement the architectural change.
5) Verify with D Deterministic-Repro that the bug class is gone.
6) Remove the old code path (don't leave it as a fallback).

Output (required): architectural decision record + proof the bug class is eliminated
Anchors: CREATIVE-PATTERNS.md, ASUPERSYNC.md §"Primitive Chooser"
```

**Canonical tag:** `prevent-by-design`

---

## Operator Composition Chains

### Standard Diagnostic Chain (bug reported)

```
S → C → G → T → R → E → V
Snapshot → Classify → Graph → Trace → Replace → Exhaust → Validate
```

### Proactive Audit Chain (no bug reported)

```
A → E → D → V
Audit → Exhaust → Deterministic-test → Validate
```

### Architecture Improvement Chain (recurring bugs)

```
C → P → D → V
Classify → Prevent → Deterministic-test → Validate
```

### Emergency Response Chain (production hang)

```
S → C → (G or T) → R → V
Snapshot → Classify → (Graph or Trace) → Replace → Validate
```

### Migration Validation Chain (Tokio → asupersync)

```
A → E → R → D → V
Audit → Exhaust → Replace → Deterministic-test → Validate
```

---

## Using Operators in Agent Prompts

Copy the prompt module for the operator you need and paste it as the task instruction:

```
I need to diagnose a hung process. Apply these operators in sequence:

[OPERATOR: S Snapshot-State]
...

Then:

[OPERATOR: C Classify-Symptom]
...

Then based on the classification, apply the appropriate diagnostic operator.
```

Operators are designed to be **composable** and **agent-safe** — each produces a well-defined output that feeds the next operator in the chain.

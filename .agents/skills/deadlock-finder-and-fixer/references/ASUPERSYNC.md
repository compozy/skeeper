# Asupersync Concurrency Cookbook

<!-- TOC: Why Asupersync Eliminates Concurrency Bugs | Cx and Scope | Checkpoint | Two-Phase Channels | Obligations | Lock Ordering | Structured Concurrency | Lab Runtime and DPOR | Supervision | Error Taxonomy | Primitive Chooser | Migration from Tokio | Diagnosis | Anti-Patterns | Code Recipes -->

Asupersync is the **primary async runtime** for our Rust projects. It is not a Tokio wrapper — it is a complete replacement with stronger guarantees around structured concurrency, obligation tracking, deterministic testing, and capability security. This cookbook covers how asupersync's design prevents or surfaces the nine classes of concurrency bugs from the main [SKILL.md](../SKILL.md).

> **Core Insight.** Most of what this skill teaches about Tokio concurrency bugs — await-holding-lock, lost wakeups, fire-and-forget tasks, channel backpressure deadlocks — are **structurally prevented** in asupersync by design. The runtime makes these bugs either compile errors, obligation-leak detections, or deterministic test failures. You don't need discipline; you need to use the primitives correctly.

---

## Why Asupersync Eliminates Whole Classes of Concurrency Bugs

| Bug Class (from SKILL.md) | Tokio: How It Bites | Asupersync: How It's Prevented |
|---------------------------|---------------------|-------------------------------|
| **Class 1: Classic Deadlock** | Lock ordering violations caught only at runtime (parking_lot detector) | Canonical lock order E→D→B→A→C enforced by `ShardGuard` + 23 dedicated tests; `ContendedMutex` with lock-metrics for hot-path evidence |
| **Class 2: Await-Holding-Lock** | `clippy::await_holding_lock` catches some; runtime silent on others | Every sync primitive takes `&Cx`; obligation tracker detects held permits across yield points; `FuturelockViolation` error fires |
| **Class 2: block_on inside async** | Runtime panic or deadlock | No `block_on` in async context; `Cx` threading makes bridge boundaries explicit |
| **Class 2: Fire-and-forget tasks** | `tokio::spawn` returns `JoinHandle` you can ignore | Region-owned work: every task belongs to a `Scope`; parent waits for children; orphan tasks are impossible |
| **Class 2: Channel backpressure deadlock** | Bounded channels silently deadlock on cycles | Two-phase send (reserve/commit); reserve is cancel-safe; session channels enforce reply obligations |
| **Class 3: Livelock / retry storms** | Manual exp-backoff; no runtime support | Native `retry`, `hedge`, `circuit_breaker`, `bulkhead` combinators with budget-aware total cost control |
| **Class 4: Database contention** | rusqlite blocks tokio workers | Native `spawn_blocking` equivalent + `Pool`/`GenericPool` with obligation-tracked checkout |
| **Class 5: Runtime-init reentrant** | OnceLock deadlock on init | `OnceCell` is cancel-safe: failed init lets next caller retry; no permanent poisoning |
| **Class 8: Mutex poisoning** | `std::sync::Mutex` poisons on panic | Configurable `ObligationLeakResponse`: Panic / Log / Recover / Silent |
| **Class 9: Lost wakeups** | Manual Notify subscription timing | Waker dedup via `Arc<AtomicBool>`; capacity re-checks after waiter registration close the lost-wakeup race |
| **All classes: Deterministic repro** | Non-deterministic; timing-dependent | `LabRuntime` with DPOR explores all interleavings; fixed seed = deterministic replay |

---

## Cx and Scope: The Foundation

Every async function in asupersync takes `&Cx` as its first parameter. `Cx` carries:
- **Cancellation token** — propagates cancel from parent to children
- **Budget** — poll quota, cost quota, deadline
- **Capabilities** — what this code is allowed to do (narrowable at boundaries)
- **Tracing context** — structured spans
- **Random seed** — deterministic under lab runtime

```rust
use asupersync::{Cx, Scope, Outcome};

async fn handle_request(cx: &Cx, req: Request) -> Outcome<Response> {
    // Cx propagates cancellation, budget, capabilities
    let data = fetch_data(cx, &req).await?;
    let result = process(cx, data).await?;
    Outcome::ok(Response::from(result))
}
```

### Scope: Region-Owned Work

Every piece of concurrent work belongs to a `Scope` (region). The scope **waits for all children** before returning. No orphan tasks. No fire-and-forget.

```rust
async fn parallel_fetch(cx: &Cx, urls: &[Url]) -> Outcome<Vec<Data>> {
    let mut results = Vec::new();
    
    // Scope owns all child tasks; waits for all on exit
    Scope::new(cx, async |cx| {
        let handles: Vec<_> = urls.iter()
            .map(|url| cx.spawn(fetch_one(cx, url)))
            .collect();
        
        for h in handles {
            results.push(h.await?);
        }
        Outcome::ok(results)
    }).await
}
```

**Why this prevents bugs:**
- **No goroutine leaks** (Go's #1 problem) — every task has an owner
- **No dangling promises** (Node's fire-and-forget) — scope blocks until children complete
- **Cancellation propagates down** — cancel the scope, all children get cancelled
- **Budget propagates down** — children share the parent's poll/cost budget

### Checkpoint: Cooperative Cancellation

Inside any long-running loop, retry body, or handler, call `cx.checkpoint()`:

```rust
async fn long_work(cx: &Cx, items: &[Item]) -> Outcome<()> {
    for item in items {
        cx.checkpoint()?;  // yields if cancelled; returns Err if budget exhausted
        process_item(cx, item).await?;
    }
    Outcome::ok(())
}
```

Without checkpoints, a cancelled scope can't actually stop its children. The runtime detects this as `RegionCloseTimeout`.

**Rule:** Add `cx.checkpoint()` in:
- Every loop body
- Every retry iteration
- Long handlers (every ~100ms of expected wall time)
- Shutdown-sensitive code paths

---

## Two-Phase Channels: Cancel-Safe by Construction

All asupersync channels use **reserve/commit**:

```rust
// Phase 1: Reserve (cancel-safe — dropping permit aborts cleanly)
let permit = tx.reserve(cx).await?;

// Phase 2: Commit (linear — must happen or abort)
permit.send(message);
```

**Why this prevents bugs:**

In Tokio, `tx.send(msg).await` is a single operation. If the task is cancelled mid-send, the message may be partially enqueued or lost. In asupersync, reserve and commit are separate:
- Dropping a permit before commit = clean abort, no partial state
- The permit is a **linear resource** (obligation) — if you forget to commit, the obligation leak detector fires

### Channel Types

```rust
// MPSC: many producers, one consumer
let (tx, mut rx) = mpsc::channel::<T>(capacity);
let permit = tx.reserve(cx).await?;
permit.send(value);
let msg = rx.recv(cx).await?;

// Oneshot: single send, single receive
let (tx, rx) = oneshot::channel::<T>();
let permit = tx.reserve(cx)?;
permit.send(value);
let result = rx.recv(cx).await?;

// Broadcast: fan-out to many subscribers
let (tx, _) = broadcast::channel::<T>(capacity);
let mut rx = tx.subscribe();
let permit = tx.reserve(cx).await?;
permit.send(value);

// Watch: latest-value multicast (RCU-style)
let (tx, rx) = watch::channel(initial_value);
tx.send(new_value);
rx.changed(cx).await?;
let val = rx.borrow_and_clone();

// Session: typed RPC with reply obligation
// Reply is a LINEAR RESOURCE — if you don't reply, obligation leak fires
```

### Session Channels: The Deadlock-Proof RPC

Session channels enforce that **every request gets a reply**. The reply handle is an obligation — dropping it without resolving fires `ObligationLeak`.

This prevents the entire class of "forgot to reply" deadlocks that plague raw mpsc+oneshot bundles in Tokio.

---

## Obligations: The Leak Detector

Asupersync tracks **obligations** — permits, acks, leases, reply handles, lock guards — as first-class runtime resources. If a task completes while holding an obligation, the runtime fires `ObligationLeak`.

```rust
// WRONG: permit dropped without send/abort → ObligationLeak!
let permit = tx.reserve(cx).await?;
return Outcome::ok(());  // Leak detected!

// RIGHT: always resolve obligations
let permit = tx.reserve(cx).await?;
permit.send(message);  // Resolved
```

### ObligationLeakResponse

Configurable per-runtime:

| Response | Behavior | Use When |
|----------|----------|----------|
| `Panic` | Immediate abort with diagnostic | Lab/CI — fail fast |
| `Log` | Log warning, continue | Production starting point |
| `Recover` | Abort the leaked path, continue | Fault-tolerant production |
| `Silent` | No action | Intentional, rare |

With threshold-based escalation: after N leaks in M seconds, escalate from `Log` to `Panic`.

**Why this prevents bugs:**
- **Mutex held across await** → obligation tracker sees the guard alive at yield → `FuturelockViolation`
- **Channel permit reserved but never committed** → `ObligationLeak`
- **Session reply handle dropped** → `ObligationLeak`
- **Semaphore permit acquired but never released** → `ObligationLeak`

### FuturelockViolation

The specific error for "task holding obligations but not making progress":

```rust
// WRONG: await while holding permit
let permit = tx.reserve(cx).await?;
other_thing(cx).await;  // If blocks forever → FuturelockViolation
permit.send(msg);

// RIGHT: minimize hold duration
let msg = other_thing(cx).await?;
let permit = tx.reserve(cx).await?;
permit.send(msg);
```

This is the asupersync equivalent of `clippy::await_holding_lock`, but enforced at runtime with diagnostics.

---

## Lock Ordering: Enforced by Design

Asupersync's internal runtime uses a strict canonical lock order:

```
E(Config) → D(Instrumentation) → B(Regions) → A(Tasks) → C(Obligations)
```

Enforced by:
- `ShardGuard` variants with label system (compile-time)
- Debug checks that verify acquisition order (runtime)
- **23 dedicated tests** for lock ordering correctness

### ShardedState

Runtime state is split into independently-locked shards:

| Shard | Label | Contents |
|-------|-------|----------|
| E | Config | Immutable runtime configuration |
| D | Instrumentation | Trace surfaces, metrics |
| B | Regions | Region ownership tree, state transitions |
| A | Tasks | Task table, stored futures, intrusive queue links |
| C | Obligations | Permit/ack/lease lifecycle, leak tracking |

Hot-path polling proceeds without serializing every region or obligation mutation.

### ContendedMutex

`parking_lot::Mutex` wrapper with optional `lock-metrics` instrumentation:
- Wait time tracking
- Hold time tracking
- Contention event counting

Use for hot-path locks where you need **evidence** about contention, not guesswork.

### Rules for Application Code

1. Always acquire locks in a documented order (your own canonical ordering)
2. Never hold a lock across an await point (asupersync will catch this via FuturelockViolation)
3. Use `ContendedMutex` for shard locks when you need metrics
4. Prefer atomic operations over locks on hot paths
5. Use `Waker::will_wake` to skip redundant clone operations

### Lost-Wakeup Prevention (Built In)

Multiple strategies used internally:
- Permit-style `Parker` with queue rechecks after wakeup
- Capacity re-checks after waiter registration (closes the capacity-check/registration race)
- Both send and receive waiters woken on channel close
- `Arc<AtomicBool>` waker dedup across all channel types

You don't need to implement these yourself — they're built into every channel and sync primitive.

---

## Lab Runtime and DPOR: Deterministic Concurrency Testing

The **single most powerful tool** in the asupersync ecosystem for finding concurrency bugs.

`LabRuntime` is a deterministic scheduler that explores all possible interleavings of concurrent operations. It uses **DPOR (Dynamic Partial Order Reduction)** to prune equivalent interleavings, making exhaustive testing tractable.

```rust
use asupersync::test_utils::{run_test, LabConfig};

#[test]
fn test_concurrent_access() {
    run_test(LabConfig::default(), |cx| async move {
        let counter = Arc::new(Mutex::new(0));
        
        let c1 = counter.clone();
        cx.spawn(async move |cx| {
            let mut g = c1.lock(cx).await?;
            *g += 1;
            Outcome::ok(())
        });
        
        let c2 = counter.clone();
        cx.spawn(async move |cx| {
            let mut g = c2.lock(cx).await?;
            *g += 1;
            Outcome::ok(())
        });
        
        // LabRuntime explores ALL interleavings:
        // T1 first, T2 first, interleaved, etc.
        // Any deadlock, lost wakeup, or obligation leak is caught.
    });
}
```

### What Lab Runtime Catches That Normal Tests Miss

| Bug | Normal test | Lab runtime |
|-----|------------|-------------|
| Deadlock (AB-BA) | Timing-dependent; may pass 999/1000 runs | **Found deterministically** — explores the specific interleaving |
| Lost wakeup | Extremely rare; requires exact scheduling | **Found** — DPOR explores the "notify before subscribe" ordering |
| Obligation leak | Only if the specific cancel timing occurs | **Found** — explores cancel at every yield point |
| Channel cycle deadlock | Requires both channels to be exactly full simultaneously | **Found** — explores the "both full" state |
| FuturelockViolation | Requires contention on the specific lock + specific await | **Found** — explores all contention orderings |

### Lab Oracles

The lab runtime includes **oracles** — automatic property checks:

- **Quiescence oracle** — detects when all tasks are blocked with no possible progress (deadlock)
- **Obligation leak oracle** — detects unreleased obligations at scope exit
- **Loser drain oracle** — verifies that cancelled tasks clean up properly
- **Progress certificate** — tracks drain phases: `warmup → rapid_drain → slow_tail → stalled → quiescent`

### Deterministic Requirements

For lab tests to be reproducible:
- Use `cx.now()` instead of `std::time::Instant::now()`
- Use `cx.random_u64()` instead of `rand::random()`
- Use `DetHashMap`/`DetHashSet` instead of `HashMap`/`HashSet` (deterministic iteration order)
- Use `VirtualTcp` instead of real network I/O

### When to Use Lab Runtime

- **Always** for core concurrency primitives (channels, locks, actors)
- **Always** for migration validation (verify Tokio→asupersync parity)
- **Before every PR** that touches concurrent code paths
- **For debugging** — fixed seed + replay = deterministic repro of the interleaving that triggered the bug

---

## Supervision: OTP-Style Restart Topology

For long-lived services, use `AppSpec` + supervision instead of naked spawned tasks:

```rust
let spec = AppSpec::new()
    .worker("db_pool", db_pool_worker)
    .worker("cache", cache_worker)
    .supervisor("http", SupervisorSpec::new()
        .strategy(RestartStrategy::OneForOne)
        .child("handler_pool", handler_pool_worker)
    );

let app = spec.start(cx).await?;
```

**Why this prevents bugs:**
- **Crash isolation** — one worker panics, supervisor restarts it; siblings unaffected
- **Startup ordering** — dependencies start in declared order
- **Shutdown ordering** — reverse of startup; drain + cancel + wait
- **Restart policies** — `OneForOne`, `AllForOne`, `RestForOne` with max-restarts/period
- **No orphan tasks** — every task belongs to a supervisor tree

### GenServer: The Actor with Reply Obligations

```rust
use asupersync::gen_server::{GenServer, CallReply};

struct CacheServer {
    data: HashMap<String, String>,
}

impl GenServer for CacheServer {
    type Call = CacheRequest;
    type Cast = CacheNotify;
    
    async fn handle_call(&mut self, cx: &Cx, req: CacheRequest, reply: CallReply<CacheResponse>) {
        match req {
            CacheRequest::Get(key) => {
                let val = self.data.get(&key).cloned();
                reply.send(CacheResponse::Value(val));  // MUST reply — obligation!
            }
            CacheRequest::Set(key, val) => {
                self.data.insert(key, val);
                reply.send(CacheResponse::Ok);
            }
        }
    }
    
    async fn handle_cast(&mut self, cx: &Cx, msg: CacheNotify) {
        // Fire-and-forget messages (no reply obligation)
        match msg {
            CacheNotify::Clear => self.data.clear(),
        }
    }
}
```

**Why GenServer over raw channels:**
- `reply` is a linear resource — forgetting to reply fires `ObligationLeak`
- `call` has timeout built in (via Cx budget)
- `cast` is explicitly fire-and-forget (no false obligation)
- State is **private to the GenServer** — no shared Mutex

---

## Error Taxonomy: Structured Concurrency Errors

Asupersync errors are classified with structured types, not stringly-typed panics:

| ErrorKind | Meaning | Concurrency Relevance |
|-----------|---------|----------------------|
| `Cancelled` | Operation cancelled via cancellation protocol | Structured cancellation — not a bug |
| `Timeout` | Deadline exceeded | Budget system caught a stall |
| `BudgetExhausted` | Poll quota or cost quota exceeded | Prevents infinite retry loops |
| `ObligationLeak` | Permit/ack/lease not resolved before region close | **Deadlock prevention** — held resource detected |
| `RegionCloseTimeout` | Region stuck waiting for children | **Missing checkpoint** — children won't cancel |
| `FuturelockViolation` | Task holding obligations without poll progress | **Await-holding-lock detected at runtime** |
| `ChannelClosed` | Sender/receiver dropped | Normal lifecycle |
| `ChannelFull` | Bounded channel at capacity | Backpressure signal |
| `LockPoisoned` | Panic while holding lock | Same as std::sync, but configurable response |

### Outcome: Four-Way Result

```rust
match outcome {
    Outcome::Ok(val) => { /* success */ }
    Outcome::Err(e) => { /* application error */ }
    Outcome::Cancelled(reason) => {
        match reason.kind {
            CancelKind::User => { /* explicit cancel */ }
            CancelKind::Timeout => { /* deadline exceeded */ }
            CancelKind::FailFast => { /* sibling failed */ }
            CancelKind::RaceLost => { /* lost a race */ }
            CancelKind::ParentCancelled => { /* parent region cancelled */ }
            CancelKind::Shutdown => { /* runtime shutdown */ }
        }
    }
    Outcome::Panicked(payload) => { /* task panicked */ }
}
```

**Why this prevents bugs:** Cancellation is a *structured* signal, not an ad-hoc panic. You can distinguish "user cancelled" from "timeout" from "sibling failed" from "shutdown" — and handle each differently. In Tokio, all of these look like "task was dropped" with no explanation.

---

## Primitive Chooser (Asupersync-Native)

### First: Choose the Ownership Model

| Problem Shape | Prefer |
|--------------|--------|
| Short-lived fork/join request work | `Scope` + child regions |
| Single-owner mailbox state | `actor` |
| Request/reply stateful service | `GenServer` |
| Many long-lived children with restart topology | `AppSpec` + `supervision` |
| Protocol edge with linear reply/resource semantics | Session channels + tracked obligations |

**If state has one natural owner, do not turn it into shared-state-plus-locks.**

### Channel Chooser

| Primitive | Use When | Avoid When |
|----------|----------|------------|
| `mpsc` | Many producers, one consumer | Need typed request/reply or fan-out |
| `oneshot` | One result, one waiter | Multi-step protocol or streaming |
| `broadcast` | Many subscribers each see each event | Consumers need only the latest state |
| `watch` | Readers need current latest value | Every update must be individually observed |
| `session` | Request/reply with reply obligation | Dumb fire-and-forget queue |

### Sync Primitive Chooser

| Primitive | Use When | Avoid When |
|----------|----------|------------|
| `Mutex` | Exclusive mutable shared state | State wants a single mailbox owner (use actor) |
| `RwLock` | Reads dominate, writer preference OK | Writes frequent or fairness unclear |
| `Semaphore` | Concurrency permits need accounting | Need a queue or lock instead |
| `Barrier` | Fixed-size phase rendezvous | Dynamic participant counts |
| `Notify` | Wake waiters without storing data | Need data transfer (use channel) |
| `OnceCell` | Async one-time initialization | Init needs refresh or hot-swap (use `watch`) |
| `Pool`/`GenericPool` | Reusable resource checkout lifecycle | Tiny resources or ambiguous ownership |
| `ContendedMutex` | Need lock-contention evidence | Don't care about metrics |

### Combinator Chooser

Instead of hand-writing `select!`-style spaghetti:

| Combinator | Best For |
|-----------|---------|
| `timeout` | Bounding one operation |
| `retry` | Transient failure with bounded total cost |
| `hedge` | Tail-latency control (backup branch) |
| `quorum` | M-of-N success requirements |
| `bulkhead` | Isolate overload domains |
| `rate_limit` | Token-bucket throughput control |
| `circuit_breaker` | Protect failing dependencies |
| `pipeline` | Staged transforms with backpressure |
| `map_reduce` | Parallel work plus lawful reduction |
| `bracket` | Acquire/use/release with cleanup |
| `first_ok` | Fallback chain |

---

## Migration from Tokio: Concurrency Bug Fixes That Come Free

When migrating from Tokio to asupersync, many concurrency bugs are fixed **by the migration itself**:

| Tokio-Era Pattern | Better Asupersync Choice | Bug It Fixes |
|------------------|--------------------------|-------------|
| `tokio::spawn` fire-and-forget | Region-owned `cx.spawn` in `Scope` | Goroutine/task leak |
| `Arc<Mutex<State>>` shared state | `actor` or `GenServer` | Lock contention, lock ordering |
| `tokio::sync::mpsc` for request/reply | Session channel or `GenServer` | Forgot-to-reply deadlock |
| Open-coded `select!` retry/timeout | `retry`, `timeout`, `hedge`, `bulkhead` | Retry storm, livelock |
| Ad hoc connection pool | `Pool`/`GenericPool` | Checkout leak, exhaustion |
| `tokio::sync::broadcast` for config | `watch` channel | Stale-config race |
| No deterministic tests | `LabRuntime` + DPOR | All timing-dependent bugs |

### What to Watch During Migration

1. **Thread `&Cx` through all async APIs** — this is the most mechanical change
2. **Replace detached spawns with `Scope`** — every task gets an owner
3. **Add `cx.checkpoint()` to loops** — or face `RegionCloseTimeout`
4. **Replace `std::sync::Mutex` guards with asupersync `Mutex`** — obligation-tracked
5. **Replace raw channels with session channels** where reply obligations exist
6. **Add lab tests for every concurrent code path** — don't wait until the end

---

## Diagnosis: Asupersync-Specific Tools

### TaskInspector

Introspects live task state: blocked reasons, obligation holdings, budget usage, cancellation status.

### CancellationExplanation

Traces the full cancel propagation chain: who requested cancellation, why, and what was affected. Far more informative than Tokio's "task was dropped".

### TaskBlockedExplanation

Identifies what a task is waiting on: lock, channel receive, semaphore, another task, etc.

### ObligationLeak Diagnostics

Pinpoints which obligation was not resolved, who held it, and when.

### Spectral Health Monitor

Early-warning severity model over live wait graph: `none / watch / warning / critical`. Detects potential deadlocks before they fully form.

### Progress Certificates

Drain phase tracking: `warmup → rapid_drain → slow_tail → stalled → quiescent`. Uses Freedman/Azuma confidence bounds for statistical guarantees.

### Debugging Workflow

1. Reproduce under `LabRuntime` with fixed seed
2. Enable trace capture and futurelock detection
3. Check oracle failures (quiescence, obligation leak, loser drain)
4. Use `TaskInspector` for live task state
5. Use `CancellationExplanation` for cancel chain
6. Preserve crashpack and replay artifacts
7. Use evidence ledger for subtle failures

---

## Anti-Patterns (Asupersync-Specific)

### Architecture Mistakes
- Treating asupersync as a drop-in executor swap (it's an architectural choice)
- Keeping Tokio as a silent co-runtime in core code
- Hiding `Cx` in globals, thread-locals, or hidden framework state
- Building new features on compat because it's easier than going native
- Using `RuntimeBuilder + block_on` as the final architecture (graduate to `AppSpec` + supervision)

### Concurrency Mistakes
- Leaving `tokio::spawn` or detached equivalents inside handlers
- Starting work with no owning region
- Using race/select patterns that abandon losers without proving cleanup
- Forgetting `cx.checkpoint()` in loops, retries, long handlers
- Holding wide cancellation masks around normal business logic

### Resource / Cleanup Mistakes
- Holding permits, locks, or leases across indefinite waits
- Assuming drop-based cleanup is good enough (use two-phase commit)
- Failing to verify quiescence and leak behavior after migration
- Using plain channels where reply obligations should be explicit

### Testing Mistakes
- Leaving `#[tokio::test]` patterns after migration
- Using wall clock or ambient randomness in deterministic tests
- Accepting non-deterministic flakes as normal
- Only testing happy-path; never testing cancel/drain/finalize behavior
- Ignoring replay artifacts, futurelock warnings, or leak oracles

---

## Code Recipes

### 1. Basic Request Handler with Cx

```rust
async fn handle(cx: &Cx, req: Request) -> Outcome<Response> {
    let user = db::get_user(cx, req.user_id).await?;
    let data = service::fetch_data(cx, &user).await?;
    Outcome::ok(Response::from(data))
}
```

### 2. Scoped Parallel Fan-Out

```rust
async fn fetch_all(cx: &Cx, ids: &[Id]) -> Outcome<Vec<Data>> {
    Scope::new(cx, async |cx| {
        let handles: Vec<_> = ids.iter()
            .map(|id| cx.spawn(fetch_one(cx, *id)))
            .collect();
        let mut results = Vec::with_capacity(handles.len());
        for h in handles {
            results.push(h.await?);
        }
        Outcome::ok(results)
    }).await
}
```

### 3. Two-Phase Channel Send

```rust
let permit = tx.reserve(cx).await?;   // Phase 1: cancel-safe
permit.send(message);                  // Phase 2: linear, must happen
```

### 4. GenServer Actor

```rust
impl GenServer for MyService {
    type Call = MyRequest;
    type Cast = MyNotify;
    
    async fn handle_call(&mut self, cx: &Cx, req: MyRequest, reply: CallReply<MyResponse>) {
        let result = self.process(cx, req).await;
        reply.send(result);  // Obligation discharged
    }
}
```

### 5. Retry with Budget-Aware Backoff

```rust
use asupersync::combinators::retry;

let result = retry(cx, RetryPolicy::exponential(3, Duration::from_millis(100)), || {
    fetch_from_service(cx, &req)
}).await?;
```

### 6. Circuit Breaker

```rust
use asupersync::combinators::circuit_breaker;

let breaker = CircuitBreaker::new(CircuitBreakerConfig {
    failure_threshold: 5,
    recovery_timeout: Duration::from_secs(30),
    half_open_max: 1,
});

let result = breaker.call(cx, || external_service(cx)).await?;
```

### 7. Pool Checkout with Obligation

```rust
let conn = db_pool.checkout(cx).await?;  // Obligation: must return
let result = conn.query(cx, "SELECT ...").await?;
drop(conn);  // Returned to pool, obligation discharged
```

### 8. Watch Channel for Config Reload

```rust
let (tx, rx) = watch::channel(Config::default());

// Writer (cold path)
cx.spawn(async move |cx| {
    loop {
        cx.checkpoint()?;
        asupersync::time::sleep(cx, Duration::from_secs(60)).await?;
        tx.send(Config::reload().await);
    }
});

// Reader (hot path, no lock)
let config = rx.borrow_and_clone();
```

### 9. Supervisor Tree

```rust
let app = AppSpec::new()
    .worker("metrics", metrics_worker)
    .supervisor("api", SupervisorSpec::new()
        .strategy(RestartStrategy::OneForOne)
        .max_restarts(5, Duration::from_secs(60))
        .child("http", http_server)
        .child("grpc", grpc_server)
    )
    .worker("background", background_jobs);
```

### 10. Lab Test with DPOR

```rust
#[test]
fn test_concurrent_cache() {
    run_test(LabConfig::default(), |cx| async move {
        let cache = Arc::new(GenServer::start(cx, CacheServer::new()).await?);
        
        let c1 = cache.clone();
        cx.spawn(async move |cx| {
            c1.call(cx, CacheRequest::Set("key".into(), "val1".into())).await
        });
        
        let c2 = cache.clone();
        cx.spawn(async move |cx| {
            c2.call(cx, CacheRequest::Set("key".into(), "val2".into())).await
        });
        
        // DPOR explores: T1 first, T2 first, interleaved
        // Obligation leak oracle verifies all replies resolved
        // Quiescence oracle verifies no deadlock
    });
}
```

### 11. Bulkhead Isolation

```rust
use asupersync::combinators::bulkhead;

let fast_pool = bulkhead(cx, BulkheadConfig { max_concurrent: 100 });
let slow_pool = bulkhead(cx, BulkheadConfig { max_concurrent: 10 });

// Fast dependency gets 100 concurrent; slow gets 10
// One pool exhausting doesn't affect the other
let fast_result = fast_pool.call(cx, || fast_service(cx)).await?;
let slow_result = slow_pool.call(cx, || slow_service(cx)).await?;
```

### 12. Hedged Request (Tail Latency)

```rust
use asupersync::combinators::hedge;

// Send request to primary; if no response in 50ms, also send to backup
// Return whichever responds first; drain the loser
let result = hedge(cx, Duration::from_millis(50), || {
    primary_service(cx)
}, || {
    backup_service(cx)
}).await?;
```

### 13. Quorum Write

```rust
use asupersync::combinators::quorum;

// Write to 3 replicas; succeed if 2 respond OK
let result = quorum(cx, 2, vec![
    || write_replica_1(cx),
    || write_replica_2(cx),
    || write_replica_3(cx),
]).await?;
```

### 14. Bracket (Acquire/Use/Release)

```rust
use asupersync::combinators::bracket;

let result = bracket(
    cx,
    || acquire_resource(cx),           // acquire
    |resource| use_resource(cx, resource),  // use
    |resource| release_resource(cx, resource), // release (even on cancel/panic)
).await?;
```

---

## Audit Commands (Asupersync-Specific)

```bash
# Find any remaining tokio imports in core code (should be zero)
rg -n 'use tokio' src/ --type rust | grep -v compat/

# Find functions missing &Cx parameter
ast-grep run -l Rust -p 'async fn $F($$$) -> $R { $$$ }' --json | \
  jq -r '.[] | select(.matched | contains("cx") | not) | .file + ":" + (.range.start.line|tostring)'

# Find loops without checkpoint
rg -n --type rust -U 'loop \{[^}]*\}' src/ | grep -v 'checkpoint'

# Find detached spawns (should use region-owned cx.spawn)
rg -n 'tokio::spawn\|task::spawn_blocking' --type rust src/

# Find obligation leaks in test output
cargo test 2>&1 | grep -i 'ObligationLeak\|FuturelockViolation\|RegionCloseTimeout'

# Run lab tests with DPOR
cargo test --features lab -- --test-threads=1
```

---

## See Also

- [RUST.md](RUST.md) — Rust concurrency with Tokio and std ecosystem (for legacy/compat code)
- [SKILL.md](../SKILL.md) — the 9-class taxonomy that asupersync structurally prevents
- [THE-FOURTH-INSTANCE.md](THE-FOURTH-INSTANCE.md) — exhaustive audit (still applies to application logic)
- [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) — structured concurrency, actor model, CSP
- [FORMAL-METHODS.md](FORMAL-METHODS.md) — lab runtime is asupersync's answer to TLA+/loom
- [`/cs/asupersync-mega-skill/`](../../asupersync-mega-skill/SKILL.md) — full asupersync skill with all reference files

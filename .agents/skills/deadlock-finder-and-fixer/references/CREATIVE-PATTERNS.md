# Creative Concurrency Patterns

Patterns that prevent concurrency bugs **by construction** — not by discipline, detection, or debugging, but by making the wrong thing impossible or the right thing automatic.

---

## 1. Actor Model

**Core idea:** Each actor owns its state privately. Communication is exclusively via message passing. No shared mutable state.

**Why it prevents deadlocks:** No locks to order. No guards to hold across await. No shared state to race on. The only contention is the mailbox queue, which is a FIFO — no circular dependencies possible in a single actor.

**Implementations:**
- **Rust:** asupersync `GenServer` / `actor` (obligation-tracked replies); tokio mpsc + oneshot (manual)
- **Go:** goroutine + channel (idiomatic Go IS the actor model)
- **Python:** asyncio task + Queue; trio nursery; multiprocessing.Process + Queue
- **TypeScript:** worker_threads + postMessage
- **Erlang/Elixir:** BEAM VM (the gold standard: per-actor GC, supervision trees, hot code reload)
- **JVM:** Akka (Scala/Java); virtual threads + StructuredTaskScope (Java 21+)

**When to use:** Any time you find yourself writing `Arc<Mutex<State>>` and the state has one natural owner.

**Anti-pattern to avoid:** Actors that synchronously call each other in a cycle → distributed deadlock via mailbox pressure. Use async call semantics or break cycles with fire-and-forget casts.

---

## 2. Structured Concurrency

**Core idea:** Every concurrent operation belongs to a scope. The scope waits for all children before exiting. No orphan tasks.

**Why it prevents bugs:**
- **No goroutine/task leaks** — parent owns children; parent can't exit while children run
- **Cancellation propagates** — cancel the scope → all children cancelled
- **Exceptions propagate** — child failure reaches parent; parent cancels siblings
- **Resources are bounded** — scope lifetime bounds all resources allocated by children

**Implementations:**
- **Rust (asupersync):** `Scope`, `cx.spawn()` — the native model. Every task belongs to a region.
- **Rust (tokio):** `JoinSet` + `CancellationToken` — manual, not enforced
- **Python:** `trio.open_nursery()`, `anyio.create_task_group()`, `asyncio.TaskGroup` (3.11+)
- **Go:** `errgroup.WithContext` — partial (cancels on first error, but doesn't prevent orphan goroutines outside the group)
- **Kotlin:** `coroutineScope { ... }`, `CoroutineScope`
- **Java 21+:** `StructuredTaskScope`
- **Swift:** `TaskGroup`, `withThrowingTaskGroup`

**The spectrum of structure:**

```
Least structured               →                Most structured
──────────────────────────────────────────────────────────────
tokio::spawn    errgroup    asyncio.TaskGroup    trio nursery    asupersync Scope
(fire-forget)   (fan-out)   (3.11+ structured)   (strict scope)  (obligations+DPOR)
```

---

## 3. Single-Writer Principle

**Core idea:** Only one thread/process/actor writes to a given piece of state. Readers proceed without locks.

**Why it prevents bugs:**
- No write-write conflicts (there's only one writer)
- Readers never block (they see a consistent snapshot)
- No lock ordering to manage

**Implementations:**

| Pattern | Writer | Readers | Example |
|---------|--------|---------|---------|
| Actor mailbox | Actor task | Callers via typed API | asupersync GenServer, Go channel-based store |
| Watch channel | `tx.send()` | `rx.borrow()` | asupersync/tokio `watch` |
| `arc-swap` | `store(Arc::new(…))` | `load()` — lock-free | Config reload, routing tables |
| LMAX Disruptor | Single producer → ring buffer | Multiple consumers read barrier | Ultra-low-latency trading |
| Kafka partition | One partition owner | Consumer group | Event sourcing |
| SQLite WAL | One writer connection | Many reader connections | Database (see `DATABASE.md`) |

**When to use:** Almost always. The question is not "should I use single-writer?" but "can I restructure this shared state to have a single writer?"

---

## 4. CSP (Communicating Sequential Processes)

**Core idea:** Processes don't share memory; they communicate via channels. Each process is sequential internally. Concurrency exists only at channel boundaries.

Go is CSP by design: goroutines + channels. The Go proverb: *"Don't communicate by sharing memory; share memory by communicating."*

**The pattern:**

```
                    channel
[Process A] ──────────────────▶ [Process B]
   (owns state A)                  (owns state B)
```

No shared state. No locks. The channel IS the synchronization.

**Deadlock in CSP:** Still possible — if A sends to B and B sends to A on bounded channels, both block. Prevent by: making one direction unbounded, using select with timeout, or breaking the cycle.

---

## 5. Software Transactional Memory (STM)

**Core idea:** Shared mutable state is wrapped in transactional variables. Reads and writes are composed inside a transaction. If a conflict is detected at commit time, the transaction is retried automatically.

```haskell
-- Haskell STM (the canonical implementation)
transfer :: TVar Int -> TVar Int -> Int -> STM ()
transfer from to amount = do
    fromBal <- readTVar from
    toBal   <- readTVar to
    writeTVar from (fromBal - amount)
    writeTVar to   (toBal + amount)

-- Runs atomically; retries on conflict; no manual locks
atomically $ transfer accountA accountB 100
```

**Why it prevents bugs:** The runtime guarantees atomicity and isolation. You can't forget to acquire a lock, acquire in the wrong order, or hold across an await — the STM runtime handles it all.

**Limitations:** Overhead (optimistic retry on conflict); not available in most mainstream languages; I/O inside transactions is forbidden (or limited).

**Available in:** Haskell (production-quality), Clojure (refs), Rust (experimental: `stm-rs`), Scala (via Akka).

---

## 6. Epoch-Based Designs

**Core idea:** Divide time into epochs. Each epoch has a consistent snapshot of state. Writers create new epochs; readers observe the current epoch. Old epochs are garbage-collected when no reader is in them.

**Implementation patterns:**
- `crossbeam::epoch` — lock-free memory reclamation for Rust data structures
- `arc-swap` — simplified: each `store()` creates a new epoch (a new `Arc`)
- Database MVCC — each transaction sees a snapshot; old versions are vacuumed

**When to use:** Read-heavy workloads where writers are infrequent and can afford to copy-on-write. Configuration, routing tables, feature flags, schema caches.

---

## 7. Immutable Snapshots + Atomic Publish

**Core idea:** Never mutate in place. Build the new state as a new object, then atomically swap a pointer.

```rust
// Rust: atomic rename
let new_config = build_config();
let path = "/tmp/config.json.new";
fs::write(path, serde_json::to_string(&new_config)?)?;
fs::rename(path, "/etc/myapp/config.json")?;  // atomic on same filesystem
```

```go
// Go: atomic.Value
var config atomic.Value  // stores *Config
config.Store(newConfig)  // readers see old or new, never torn
c := config.Load().(*Config)
```

**Why it prevents bugs:** Readers never see partial state. There is no window where the state is "half-updated." The transition is a single pointer swap.

---

## 8. "Do Nothing" Designs

The most radical pattern: **eliminate the concurrency entirely**.

| Problem | "Do Nothing" Solution |
|---------|----------------------|
| Multiple writers to a DB | Queue writes through one actor |
| Race condition in cache | Use singleflight — only one fetch per key |
| Parallel file access | File reservation via agent-mail — cooperate, don't contend |
| Shared counter across threads | Thread-local counters + periodic merge |
| Concurrent test execution | `--test-threads=1` for tests that touch shared state |
| Multiple agents editing same file | Tmux process isolation + advisory leases |

If you can eliminate the concurrency, you've eliminated the bug class entirely. This isn't always possible, but it's always worth considering.

---

## 9. Backpressure-First Design

Instead of "how do I make this go faster?" ask "what happens when the consumer is slower than the producer?"

**Bounded channels** are a backpressure mechanism: the sender blocks (or gets an error) when the channel is full, naturally slowing the producer. This prevents:
- Memory exhaustion (unbounded queues)
- Livelock (retry storms from rejected work piling up)
- Cascading failure (one slow service overwhelms the next)

**Implementations:**
- **Rust:** bounded `mpsc`, `Semaphore` for `spawn_blocking`
- **Go:** buffered channels, `errgroup.SetLimit`
- **Node:** `stream.pipeline` with `highWaterMark`, `p-limit`
- **Python:** `asyncio.Semaphore`, bounded `asyncio.Queue`

---

## 10. Supervision Trees

Erlang's gift to systems programming: every process is supervised. Supervisor restarts crashed children. The restart policy is explicit.

```
           [Application Supervisor]
          /            |            \
    [DB Supervisor] [HTTP Supervisor] [Worker Supervisor]
     /       \         /      \         /    |    \
  [Pool]  [Migrate]  [S1]   [S2]    [W1]  [W2]  [W3]

RestartStrategy:
  - OneForOne: restart only the crashed child
  - AllForOne: restart all children if any crashes
  - RestForOne: restart the crashed child + all children started after it
```

**Asupersync:** `AppSpec` + `supervision` (see [ASUPERSYNC.md](ASUPERSYNC.md))
**Go:** manual (goroutine + restart loop + ctx)
**Node:** PM2, cluster module with restart
**Python:** Celery workers with auto-restart

**Why it prevents concurrency bugs:** A supervised process that deadlocks is detected (heartbeat timeout) and restarted. The deadlock is not fixed — it's tolerated and recovered from. This buys time for a proper fix while keeping the system available.

---

## Cross-Language Pattern Availability

| Pattern | Rust (asupersync) | Rust (tokio) | Go | Python | TypeScript |
|---------|-------------------|--------------|-----|---------|------------|
| Actor | GenServer | mpsc+oneshot | goroutine+chan | asyncio.Queue | worker+postMessage |
| Structured concurrency | Scope (native) | JoinSet (manual) | errgroup (partial) | trio/anyio/TaskGroup | — (tc39 proposal) |
| Single-writer | watch, actor | watch, actor | chan store | actor pattern | — (manual) |
| CSP | channels | channels | goroutine+chan | asyncio.Queue | — |
| STM | stm-rs (experimental) | — | — | — | — |
| Epoch-based | crossbeam::epoch | crossbeam::epoch | — | — | — |
| Immutable snapshot | arc-swap | arc-swap | atomic.Value | — | Immutable.js |
| Backpressure | bounded channels | bounded channels | buffered chan | asyncio.Semaphore | p-limit, streams |
| Supervision | AppSpec+supervision | — (manual) | — (manual) | Celery workers | PM2, cluster |

---

## See Also

- [ASUPERSYNC.md](ASUPERSYNC.md) — Cx, Scope, obligations, lab runtime
- [RUST.md](RUST.md) — actor pattern, watch channel, arc-swap in Rust
- [GO.md](GO.md) — CSP, goroutine+channel, errgroup
- [PYTHON.md](PYTHON.md) — trio nursery, anyio, asyncio.TaskGroup
- [DISTRIBUTED.md](DISTRIBUTED.md) — distributed locks, consensus, CRDTs, sagas
- [LOCK-FREE.md](LOCK-FREE.md) — epoch-based reclamation, hazard pointers

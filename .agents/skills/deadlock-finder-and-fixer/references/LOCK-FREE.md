# Lock-Free Data Structures and Memory Reclamation

<!-- TOC: Progress Guarantees | CAS Loops | ABA Problem | Memory Reclamation | Seqlocks | Flat Combiner | MPMC Ring Buffer | HTM | Code Patterns -->

Lock-free programming eliminates deadlocks by construction — no locks means no lock-ordering bugs. The trade-off: you must solve memory reclamation, ABA, and memory ordering correctly, or you get data corruption instead of deadlock.

Distilled from frankensqlite sessions (CommitSequenceCombiner, EBR, seqlocks, HTM), alien-cs-graveyard research, and asupersync formal semantics.

---

## Progress Guarantees

| Level | Guarantee | Practical Meaning |
|-------|-----------|-------------------|
| **Wait-free** | Every thread completes in bounded steps | Strongest; rare in practice |
| **Lock-free** | At least one thread makes progress | System-wide progress; individual threads may starve |
| **Obstruction-free** | A thread makes progress if alone | Weakest formal guarantee; needs contention management |
| **Blocking** | No formal progress guarantee | Uses locks; deadlock-avoidable via ordering |

**When to go lock-free:** High-contention hot paths where mutex serialization is the bottleneck. Measure first — `ContendedMutex` with lock-metrics gives you evidence.

**When NOT to go lock-free:** Most code. A `parking_lot::Mutex` is faster than a poorly-written CAS loop. Lock-free is not free — it trades deadlock risk for ABA/memory-ordering risk.

---

## CAS (Compare-And-Swap) Loops

The fundamental building block:

```rust
loop {
    let current = value.load(Ordering::Acquire);
    let new_value = compute(current);
    match value.compare_exchange(current, new_value, Ordering::AcqRel, Ordering::Acquire) {
        Ok(_) => break,       // success
        Err(_) => continue,   // another thread won; retry
    }
}
```

**Gotchas:**
- **Livelock:** If many threads CAS the same location, most fail and retry. Use exponential backoff.
- **ABA:** `current` matches but the underlying state changed and changed back. See next section.
- **Memory ordering:** `Relaxed` is wrong for most CAS loops. Use `AcqRel` for the success case.

---

## The ABA Problem

Thread 1 reads A, is suspended. Thread 2 changes A→B→A. Thread 1 resumes, CAS succeeds (sees A), but the state has changed underneath.

**Example:** Treiber stack pop:
```
Stack: A → B → C
Thread 1: reads top=A, next=B
Thread 1: suspended
Thread 2: pops A, pops B, pushes A (different payload)
Thread 1: CAS(top, A, B) succeeds — but B was already popped!
```

**Solutions:**

1. **Tagged pointer (double-width CAS):** Pack a version counter with the pointer. Increment on every CAS. ABA becomes ABA' because the tag differs.
   ```rust
   struct Tagged<T> { ptr: *mut T, tag: u64 }
   // CAS on the entire Tagged struct (128-bit CAS on x86_64)
   ```

2. **Epoch-based reclamation:** Defer freeing until all readers have advanced past the epoch. A freed-and-reused node won't appear at the same address until the epoch advances.

3. **Hazard pointers:** Announce which pointers you're reading. The writer checks hazard lists before reusing.

---

## Memory Reclamation

The hardest problem in lock-free programming: when can you free memory that other threads might still be reading?

### Epoch-Based Reclamation (EBR)

From frankensqlite sessions (session 2e0a8bba) — the preferred approach for Rust projects.

```rust
use crossbeam::epoch::{self, Atomic, Owned};

// Reader: pin the current epoch
let guard = epoch::pin();
let shared = ptr.load(Ordering::Acquire, &guard);
if let Some(data) = unsafe { shared.as_ref() } {
    // data is guaranteed valid while `guard` lives
    use_data(data);
}
// guard dropped → thread unpins from epoch

// Writer: swap pointer, defer old value for later reclamation
let new = Owned::new(NewData::build());
let old = ptr.swap(new, Ordering::AcqRel, &guard);
unsafe { guard.defer_destroy(old); }
// old is freed only after all prior-epoch readers have unpinned
```

**Advantages over hazard pointers:**
- Simpler: one global counter vs per-object metadata
- Amortized: reclamation cost spread across many operations
- `crossbeam::epoch` is production-ready in Rust

**Gotcha:** A thread stuck in a pinned epoch prevents ALL reclamation. Mitigation: periodic forced unpin, timeout detection, or asupersync's progress certificates.

**Gotcha (HTM interaction):** HTM abort during an epoch-pinned section can skip epoch advancement → unbounded memory growth. Mitigation: disable HTM during EBR critical sections.

### Hazard Pointers

From frankensqlite session 663adcfa:

```
Per-thread: vector of "I'm currently reading these pointers"
On retire: add to thread-local retired list
Periodically: scan ALL threads' hazard vectors → free retired nodes not in any hazard list
```

**Complexity:** O(threads × hazards) per scan. Better worst-case than EBR (no stuck-thread problem) but higher constant overhead.

### Deferred Free (Simple GC)

Add to global retire list; background sweeper frees periodically. ~50 LOC. No deterministic reclamation — memory grows if sweeper can't keep up.

### Comparison

| Strategy | Read Cost | Write Cost | Stuck Thread | Memory Bound | Complexity |
|----------|----------|------------|--------------|-------------|------------|
| EBR | Zero (just pin) | Amortized | Unbounded growth | No | Medium |
| Hazard Pointers | One store per read | O(T×H) scan | Bounded | Yes | High |
| Deferred Free | Zero | O(1) append | Unbounded | No | Low |
| Reference Counting | Atomic incr/decr | — | N/A | Yes | Low |

---

## Seqlocks

From frankensqlite session 3802de11 — optimistic reads for metadata that's read frequently and written rarely.

```rust
pub struct SeqLock<T> {
    seq: AtomicU64,
    data: UnsafeCell<T>,
}

impl<T: Copy> SeqLock<T> {
    pub fn read(&self) -> T {
        loop {
            let s1 = self.seq.load(Ordering::Acquire);
            if s1 & 1 == 1 { continue; }  // write in progress
            let value = unsafe { *self.data.get() };
            let s2 = self.seq.load(Ordering::Acquire);
            if s1 == s2 { return value; }
            // seq changed during read → retry
        }
    }
    
    pub fn write(&self, value: T) {
        let old = self.seq.fetch_add(1, Ordering::Release);  // odd = writing
        unsafe { *self.data.get() = value; }
        self.seq.fetch_add(1, Ordering::Release);  // even = stable
    }
}
```

**Use for:** Schema metadata, pragma settings, feature flags — anything read on every request but updated rarely.

**Performance:** Sub-microsecond reads (no atomics on fast path — just two loads + compare). 10-100x faster than `RwLock` for uncontended reads.

**Gotcha:** Readers can spin indefinitely if writer continuously mutates (adversarial). T must be `Copy` (reading torn values is UB otherwise).

---

## Flat Combiner Pattern

From frankensqlite session 6f58dbe1 — under high contention, one "combiner" thread executes everyone's operations.

```rust
pub struct FlatCombiner<Op, Res> {
    combiner_lock: AtomicBool,
    slots: Vec<CachePadded<AtomicCell<Option<Op>>>>,
    results: Vec<CachePadded<AtomicCell<Option<Res>>>>,
}

impl<Op, Res> FlatCombiner<Op, Res> {
    fn do_op(&self, thread_id: usize, op: Op) -> Res {
        // 1. Publish operation to my slot
        self.slots[thread_id].store(Some(op));
        
        // 2. Try to become the combiner
        if self.combiner_lock.compare_exchange(false, true, AcqRel, Acquire).is_ok() {
            // I'm the combiner: execute all pending operations
            for i in 0..self.slots.len() {
                if let Some(op) = self.slots[i].take() {
                    let result = execute(op);  // data stays hot in L1
                    self.results[i].store(Some(result));
                }
            }
            self.combiner_lock.store(false, Release);
        }
        
        // 3. Wait for my result
        loop {
            if let Some(result) = self.results[thread_id].take() {
                return result;
            }
            std::hint::spin_loop();
        }
    }
}
```

**When to use:** Contention > 8 threads on a single data structure. The combiner keeps data hot in one core's L1 cache — faster than N threads fighting over cache lines.

**From session evidence:** frankensqlite's `CommitSequenceCombiner` batches `fetch_add` on `commit_seq` counter during concurrent transaction commits. Measured 2-3x improvement under high contention.

**Gotcha:** Only beneficial for small critical sections. Breaks down with work-stealing executors (load imbalance). No benefit on low-contention paths.

---

## Hardware Transactional Memory (HTM)

From frankensqlite sessions and alien-cs-graveyard research.

Intel TSX/RTM allows speculative execution of critical sections in L1 cache without broadcasting cache invalidations:

```rust
#[cfg(target_arch = "x86_64")]
fn with_htm_fallback<F: FnOnce()>(f: F) {
    if unsafe { _xbegin() } == _XBEGIN_STARTED {
        f();                    // speculative execution
        unsafe { _xend(); }    // commit if no conflict
        return;
    }
    // Fallback: use traditional lock
    let _guard = LOCK.lock();
    f();
}
```

**Performance:** ~5-8ns uncontested fast path (no memory bus transactions).

**CRITICAL WARNING (from alien-cs-graveyard §4.2):**
> "HTM abort during EBR epoch-pinned section can skip epoch advancement → unbounded memory (HIGH risk). Mitigation: disable HTM during EBR critical sections."

**ARM TME:** Emerging alternative; less mature than Intel TSX.

**Status:** Specialized optimization. Only use when profiling shows cache-line contention is the bottleneck and the critical section fits in L1 (~32KB).

---

## MPMC Ring Buffer

Fixed-size circular buffer with atomic head/tail:

```rust
pub struct MpmcQueue<T> {
    buffer: Vec<AtomicCell<Option<T>>>,
    head: CachePadded<AtomicUsize>,  // consumer index
    tail: CachePadded<AtomicUsize>,  // producer index
    mask: usize,                      // capacity - 1 (power of 2)
}

impl<T> MpmcQueue<T> {
    pub fn push(&self, value: T) -> Result<(), T> {
        loop {
            let tail = self.tail.load(Acquire);
            let head = self.head.load(Acquire);
            if tail - head >= self.buffer.len() { return Err(value); } // full
            if self.tail.compare_exchange(tail, tail + 1, AcqRel, Acquire).is_ok() {
                self.buffer[tail & self.mask].store(Some(value));
                return Ok(());
            }
        }
    }
}
```

**ABA mitigation:** Separate `head` and `tail` indices (monotonically increasing); mask to buffer index. The index itself doesn't wrap — only the buffer position does.

**Alternatives in Rust:** `crossbeam::queue::ArrayQueue` (bounded MPMC), `crossbeam::queue::SegQueue` (unbounded MPMC).

---

## RCU (Read-Copy-Update)

From frankensqlite session 3802de11 — the kernel pattern adapted for userspace:

```
Read path:  no locks, no atomics — just dereference a pointer
Write path: 1. Copy old data → new allocation
            2. Modify the copy
            3. Atomic pointer swap (publish)
            4. Wait for grace period (all readers have seen new version)
            5. Free old data
```

**In Rust:** `arc-swap::ArcSwap` is the practical RCU:
```rust
let config = ArcSwap::from_pointee(Config::default());
// Read (hot path, no lock): let c = config.load();
// Write (cold path):       config.store(Arc::new(new_config));
```

**Grace period:** In `arc-swap`, the old `Arc` is dropped when its refcount hits zero — which happens when all readers who loaded it have finished.

---

## Cross-Language Lock-Free Availability

| Pattern | Rust | Go | C/C++ | Java |
|---------|------|-----|-------|------|
| CAS loop | `AtomicT::compare_exchange` | `sync/atomic.CompareAndSwap` | `__atomic_compare_exchange` | `AtomicReference.compareAndSet` |
| EBR | `crossbeam::epoch` | — | manual | — |
| Hazard pointers | manual | — | manual / libcds | — |
| Seqlock | manual | — | manual | `StampedLock.tryOptimisticRead` |
| MPMC queue | `crossbeam::queue` | channels | `boost::lockfree` | `ConcurrentLinkedQueue` |
| RCU | `arc-swap` | `atomic.Value` | kernel RCU / userspace | — |
| Flat combiner | manual | — | manual | — |

---

## When NOT to Use Lock-Free

- **Low contention.** `parking_lot::Mutex` has ~20ns uncontended. Your CAS loop won't beat it.
- **Complex invariants.** If the critical section updates 3 fields atomically, you need a lock (or STM).
- **Team unfamiliarity.** A correct lock is better than a buggy CAS loop. Lock-free bugs are harder to find and fix than deadlocks.
- **No profiling evidence.** If you haven't measured that locks are the bottleneck, don't optimize them away.

**The golden rule:** Use lock-free only when:
1. Profiling shows lock contention is the bottleneck (not just suspicion)
2. The critical section is small (< 100 instructions)
3. You have formal verification (loom, DPOR) or exhaustive testing
4. The team can maintain it

---

## See Also

- [RUST.md](RUST.md) — crossbeam, arc-swap, parking_lot in Rust context
- [ASUPERSYNC.md](ASUPERSYNC.md) — lab runtime DPOR for testing lock-free code
- [FORMAL-METHODS.md](FORMAL-METHODS.md) — loom, TLA+, linearizability proofs
- [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) — epoch-based designs, single-writer principle
- [FIX-CATALOG.md](FIX-CATALOG.md) — when to replace locks with lock-free alternatives

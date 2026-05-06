# Formal Methods for Concurrency

Tools that **prove** correctness rather than testing for bugs. Testing shows the presence of bugs; formal methods show their absence (within the model).

---

## Tool Landscape

| Tool | Language | What It Verifies | Effort | Coverage |
|------|----------|-----------------|--------|----------|
| **Asupersync LabRuntime + DPOR** | Rust | All interleavings of async tasks | Low (write test, run) | Exhaustive (within model) |
| **loom** | Rust | All interleavings of sync primitives | Medium (loom-gated types) | Exhaustive (small scope) |
| **shuttle** | Rust | Async task scheduling | Medium | Exhaustive (small scope) |
| **Miri** | Rust | Undefined behavior, data races | Low (cargo miri test) | Under-approximate |
| **TSAN** | Rust/C/Go | Data races at runtime | Low (compiler flag) | Under-approximate |
| **go test -race** | Go | Data races at runtime | Zero | Under-approximate |
| **TLA+** | Any (spec) | Deadlock-freedom, liveness | High (write spec) | Exhaustive (finite model) |
| **Lean** | Any (proof) | Arbitrary properties | Very high | Complete (mathematical) |
| **Property-based testing** | Any | Randomized invariant checks | Medium | Statistical |
| **rr --chaos** | C/Rust/Go | Scheduling-sensitive bugs | Low | Statistical (many runs) |

---

## Asupersync Lab Runtime + DPOR

The **most practical** formal method for our codebase. See [ASUPERSYNC.md](ASUPERSYNC.md) for full details.

**What makes it special:**
- **Mazurkiewicz trace semantics:** Quotients by independent action swaps → explores exactly one representative per equivalence class
- **A* geodesic normalization:** Finds minimal context-switch linearizations
- **Persistent directed homology:** Uses cell complexes to prioritize "interesting" schedules with non-trivial holes
- **E-processes:** Anytime-valid monitoring via Ville's inequality (supermartingale testing)
- **Conformal calibration:** Distribution-free threshold tuning for oracle anomaly detection

**Evidence ledgers** with Bayes factors explain *why* a violation was detected — not just "test failed" but "here's the mathematical evidence."

```rust
#[test]
fn lab_test_with_dpor() {
    run_test(LabConfig::default(), |cx| async move {
        // The lab explores ALL causally distinct interleavings
        // Oracles check: quiescence, obligation leaks, loser drain, determinism
    });
}
```

---

## loom (Rust Sync Primitives)

Deterministic permutation testing for `Mutex`, `RwLock`, `AtomicT`, `thread::spawn`:

```rust
#[cfg(loom)]
use loom::sync::{Arc, Mutex, atomic::AtomicUsize};
#[cfg(not(loom))]
use std::sync::{Arc, Mutex, atomic::AtomicUsize};

#[cfg(loom)]
#[test]
fn test_concurrent_increment() {
    loom::model(|| {
        let counter = Arc::new(AtomicUsize::new(0));
        let c1 = counter.clone();
        let t1 = loom::thread::spawn(move || {
            c1.fetch_add(1, Ordering::SeqCst);
        });
        let c2 = counter.clone();
        let t2 = loom::thread::spawn(move || {
            c2.fetch_add(1, Ordering::SeqCst);
        });
        t1.join().unwrap();
        t2.join().unwrap();
        assert_eq!(counter.load(Ordering::SeqCst), 2);
    });
}
```

Run: `RUSTFLAGS="--cfg loom" cargo test --release --lib`

**Scope:** Small concurrent primitives (channels, lock-free structures, state machines). NOT whole applications.

**From mcp-agent-mail-rust session:** loom was used to verify `CoalesceMap` (singleflight) correctness — testing leader/joiner pairing under all thread schedules.

---

## TLA+ and PlusCal

Specification language for distributed algorithms. You describe the state machine; TLC model checker explores all reachable states.

From codex session 2026-02-07 and alien-cs-graveyard:

```tla+
---- MODULE SimpleCounter ----
EXTENDS Integers

VARIABLE counter

Init == counter = 0

Increment == counter' = counter + 1

Spec == Init /\ [][Increment]_counter

THEOREM Spec => [](counter >= 0)  \* counter never negative
====
```

**Use for:** Proving deadlock-freedom in distributed protocols (Raft, Redlock, two-phase commit). Not for finding bugs in code — for finding bugs in *designs*.

**Asupersync connection:** The operational semantics are explicitly designed for TLA+ export. Cancel protocol transitions, obligation invariants, and region close semantics can be model-checked before implementation.

---

## Miri (Rust Undefined Behavior)

```bash
rustup +nightly component add miri
cargo +nightly miri test
```

Catches: data races, use-after-free, uninitialized reads, invalid pointer arithmetic, memory ordering violations.

**Slow.** Use on unit tests of core primitives, not the full suite.

From frankenfs session: miri was used to verify concurrent execution paths in filesystem operations.

---

## Linearizability

The correctness criterion for concurrent data structures. An operation is **linearizable** if there exists a point in time (the **linearization point**) during its execution where the operation appears to take effect atomically.

**How to check:** For each concurrent execution, find a sequential execution that produces the same results and respects the real-time ordering of non-overlapping operations.

**In practice:**
- loom verifies linearizability by exhausting all interleavings and checking assertions
- TLA+ can model linearizability as a refinement mapping
- Asupersync DPOR checks by exploring all orderings and verifying oracle invariants

---

## Property-Based Testing for Concurrency

Combine random inputs with concurrency:

```rust
use proptest::prelude::*;

proptest! {
    #[test]
    fn concurrent_map_is_consistent(ops in vec(op_strategy(), 0..100)) {
        let map = Arc::new(DashMap::new());
        let handles: Vec<_> = ops.into_iter().map(|op| {
            let m = map.clone();
            std::thread::spawn(move || apply_op(&m, op))
        }).collect();
        for h in handles { h.join().unwrap(); }
        // Verify: all inserted keys are present, no extra keys
        verify_consistency(&map);
    }
}
```

Combine with TSAN for maximum coverage: property-based tests generate diverse concurrent scenarios, TSAN catches data races in each.

---

## The Verification Stack

From least to most effort, most to least practical:

```
              ↑ Effort
              │
    Lean proofs │ ████████████████████  Complete, but person-years
              │
     TLA+ spec │ ███████████████       Exhaustive within model
              │
 loom/DPOR test │ █████████             Exhaustive within scope
              │
    TSAN / race │ ██████                Under-approximate, easy
              │
  Property-based│ █████                 Statistical, moderate
              │
    Unit tests  │ ███                   Point checks, fast
              │
              └──────────────────────── Coverage →
```

**Recommendation for most projects:**
1. TSAN / `go test -race` (always on in CI)
2. `clippy::await_holding_lock` (always on)
3. loom or DPOR for the 3-5 most critical concurrent primitives
4. Property-based tests for public APIs under concurrency
5. TLA+ only for distributed protocols where correctness is safety-critical

---

## See Also

- [ASUPERSYNC.md](ASUPERSYNC.md) — DPOR, evidence ledgers, progress certificates
- [LOCK-FREE.md](LOCK-FREE.md) — what needs formal verification the most
- [VALIDATION.md](VALIDATION.md) — practical validation tooling
- [RUST.md](RUST.md) — loom, miri, TSAN in Rust context
- [GO.md](GO.md) — go test -race, pprof goroutine dumps

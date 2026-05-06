# Validation Tooling

The tools that tell you whether your concurrency fix is actually correct. None of these replaces the others; layer them.

## TSAN — ThreadSanitizer

**What it catches:** data races (unsynchronized concurrent access where at least one access is a write). The oracle for Class 6 bugs.

**Rust.**

```bash
RUSTFLAGS="-Zsanitizer=thread" \
  cargo +nightly test --target x86_64-unknown-linux-gnu \
  -- --test-threads=16 2>&1 | tee /tmp/tsan.log

# Real hit detection:
rg '^WARNING: ThreadSanitizer' /tmp/tsan.log -A 30
```

**C / C++.**

```bash
clang -fsanitize=thread -g -O1 prog.c -o prog
./prog 2>&1 | tee /tmp/tsan.log
```

**Go.**

```bash
go test -race ./...
```

**TSAN options worth knowing:**
```
TSAN_OPTIONS="
  abort_on_error=1
  second_deadlock_stack=1
  history_size=7
  halt_on_error=0
"
```

- `abort_on_error=1` — crash on first race, for debugging
- `second_deadlock_stack=1` — show both stacks in deadlock reports (not just one)
- `history_size=7` — max memory access history (higher = more accurate, slower)
- `halt_on_error=0` — report all races, don't stop at first

**False positives.** TSAN has almost none for Rust (the compiler's aliasing rules align with TSAN's model). C++ can have false positives when using lock-free atomics — review each carefully.

## Loom

Rust's permutation-testing model checker. Exhaustively explores all possible orderings of concurrent operations in a small test harness. Catches bugs TSAN can't because TSAN only sees the orderings that actually happened; loom sees every possible ordering.

```toml
[target.'cfg(loom)'.dependencies]
loom = "0.7"
```

```rust
#[cfg(loom)]
use loom::sync::Arc;
#[cfg(not(loom))]
use std::sync::Arc;

#[cfg(loom)]
mod tests {
    use super::*;

    #[test]
    fn test_counter() {
        loom::model(|| {
            let counter = Arc::new(AtomicUsize::new(0));
            let t1 = loom::thread::spawn({
                let c = counter.clone();
                move || c.fetch_add(1, Ordering::Relaxed)
            });
            let t2 = loom::thread::spawn({
                let c = counter.clone();
                move || c.fetch_add(1, Ordering::Relaxed)
            });
            t1.join().unwrap();
            t2.join().unwrap();
            assert_eq!(counter.load(Ordering::Relaxed), 2);
        });
    }
}
```

Run:

```bash
RUSTFLAGS="--cfg loom" cargo test --release --lib
```

**Use loom for:** Rust concurrency primitives, lock-free data structures, carefully-written atomic code. **Don't use loom for:** whole applications (exponential blow-up in state space).

## `parking_lot::deadlock::check_deadlock()`

Runtime deadlock detection. Enable the `deadlock_detection` feature in debug builds.

```toml
[dependencies]
parking_lot = { version = "0.12", features = ["deadlock_detection"] }
```

```rust
#[cfg(debug_assertions)]
fn start_deadlock_detector() {
    std::thread::spawn(|| loop {
        std::thread::sleep(std::time::Duration::from_secs(10));
        let deadlocks = parking_lot::deadlock::check_deadlock();
        if deadlocks.is_empty() { continue; }
        eprintln!("DEADLOCK DETECTED ({})", deadlocks.len());
        for (i, threads) in deadlocks.iter().enumerate() {
            eprintln!("Deadlock #{}", i);
            for t in threads {
                eprintln!("  Thread Id {:?}", t.thread_id());
                eprintln!("  {:#?}", t.backtrace());
            }
        }
    });
}
```

Call once from `main()`. Every detection is a proof of a real deadlock — zero false positives.

## Miri

Rust's undefined-behavior checker. Runs the code on an interpreter that models the Rust abstract machine exactly. Catches:
- Data races (like TSAN, but from a language-level model)
- Use-after-free
- Invalid pointer arithmetic
- Uninitialized reads
- Memory-ordering violations

```bash
rustup +nightly component add miri
cargo +nightly miri test
```

**Slow.** Use on unit tests of the core concurrency primitive, not the whole suite.

## Clippy lints for concurrency

```bash
cargo +nightly clippy -- \
  -W clippy::await_holding_lock \
  -W clippy::await_holding_refcell_ref \
  -W clippy::mutex_atomic \
  -W clippy::rc_mutex \
  -W clippy::if_then_some_else_none
```

- **`await_holding_lock`** — Class 2 detection.
- **`await_holding_refcell_ref`** — variant for `RefCell`.
- **`mutex_atomic`** — `Mutex<bool>` where `AtomicBool` would work.
- **`rc_mutex`** — `Rc<Mutex<T>>` is a smell (Rc is not thread-safe, why is there a Mutex?).

## `rr` — Record & Replay

Deterministic replay of a program run. Record once, then replay the same crash or deadlock as many times as you like, stepping forward and backward.

```bash
# Record (run repeatedly until you catch the bug)
rr record ./my_program
# → "rr: Saving execution to trace directory /tmp/rr/my_program-0"

# Replay
rr replay /tmp/rr/my_program-0
(rr) break some_function
(rr) continue
(rr) print state
(rr) reverse-continue            # go BACKWARDS to find the cause
(rr) break my_module.rs:42
(rr) reverse-continue
```

**Use rr for:** intermittent bugs you can occasionally reproduce. **Limitations:** single-threaded execution model (rr serializes concurrent threads for determinism), Linux-only, x86_64-only.

## `rr --chaos` — Scheduler fuzzing

Randomizes the scheduler to surface race conditions:

```bash
rr record --chaos ./my_program
```

Combined with stress tests, this catches races that only manifest under rare scheduling.

## `tokio-console`

Runtime observability for Tokio.

```toml
[dependencies]
console-subscriber = "0.2"
tokio = { version = "1", features = ["full", "tracing"] }
```

```rust
fn main() {
    console_subscriber::init();
    // normal tokio::main body
}
```

```bash
tokio-console
```

Shows every task, resource (mutexes, semaphores), and waker with live updates. Essential for diagnosing async deadlocks and task starvation (Class 2).

## Stress tests

The poor-person's loom: run the concurrent code N times with high thread counts.

```rust
#[test]
fn stress_test() {
    for _ in 0..100 {
        let barrier = Arc::new(Barrier::new(16));
        let handles: Vec<_> = (0..16).map(|i| {
            let b = barrier.clone();
            std::thread::spawn(move || {
                b.wait();
                do_concurrent_op(i);
            })
        }).collect();
        for h in handles { h.join().unwrap(); }
    }
}
```

Run under TSAN for maximum catch.

## The Audit Checklist

Before declaring a concurrency fix done:

- [ ] **Reproducer exists** as a test.
- [ ] **Static audit** from [STATIC-AUDIT.md](STATIC-AUDIT.md) passes (no remaining hits for the hazard class).
- [ ] **`clippy::await_holding_lock`** clean (if async).
- [ ] **`cargo +nightly miri test`** clean on the core primitive.
- [ ] **TSAN / `go test -race`** clean on the test suite.
- [ ] **Loom test** passes on the core primitive (Rust).
- [ ] **`parking_lot::deadlock::check_deadlock()`** emits nothing during full test run.
- [ ] **Stress test** (100× iterations, high thread count) passes under TSAN.
- [ ] **tokio-console** shows no tasks stuck > 100 ms in steady state (async).
- [ ] **Regression written** that would have caught the original bug.
- [ ] **Commit / bead** documents *why* the fix works, not just *what* changed.
- [ ] **The Fourth Instance rule** applied: the same hazard class was searched repo-wide and every other instance was dispositioned.

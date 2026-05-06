# LD_PRELOAD / Runtime-Init / Reentrant Hazards

The full incident narrative, pattern, and fix from the `glibc_rust` project. This is the hardest class of deadlock in the nine-class taxonomy because the bug runs before `main`, no logging is available, and the failure mode is a silent hang.

## The Incident (glibc_rust)

**Source session:** `/home/ubuntu/.claude/projects/-data-projects-glibc-rust/3b5a23a6-2133-4efd-a713-7dd0256e313b/subagents/agent-ad965f6.jsonl`

**Setup.** `libglibc_rs_abi.so` is a Rust library that interposes standard libc symbols (`strlen`, `malloc`, `memcpy`, etc.) via `LD_PRELOAD`. Each interposed symbol runs a policy check and forwards to the real libc, optionally healing unsafe arguments.

**Symptom.** Running any binary with `LD_PRELOAD=libglibc_rs_abi.so /tmp/test_short` hangs. The process enters sleeping state (0% CPU). `gdb thread apply all bt` shows one thread in `futex_wait` — and that thread is the dynamic loader itself, attempting to resolve symbols during library load.

**Round 1 fix.** The engineer finds three `OnceLock` instances on the `strlen → runtime_policy::check_ordering() → kernel()` path. Each is rewritten as an atomic state machine (`AtomicU8` state + `AtomicPtr<T>` storage, states `UNINIT(0) → INITIALIZING(1) → READY(2)`, with `Option<&T>` returned while `INITIALIZING`). The engineer runs the test again.

**Still hangs.**

**Round 2 investigation.** The engineer spawns a subagent to find the fourth instance (see [THE-FOURTH-INSTANCE.md](THE-FOURTH-INSTANCE.md) for the anchor quote). The subagent traces the entire call path from `strlen` to every reachable blocking primitive.

**Findings:**

1. **Reentrant `std::env::var`.** `safety_level()` calls `std::env::var("GLIBC_RUST_MODE")`. Under the hood, `std::env::var` calls `getenv_r`, which walks the environ vector using — `strlen`. Which is interposed. Which calls `runtime_policy::check_ordering()`. Which calls `safety_level()`. Infinite loop.

2. **72-field `RuntimeMathKernel`.** The singleton behind `kernel()` contains 72 `parking_lot::Mutex<T>` fields, each for a different math operation. Constructing the kernel calls `Box::new(RuntimeMathKernel::new())`, and `Box::new` uses the global allocator. The global allocator for the binary under test is the Rust system allocator, which uses libc `malloc`. Which is interposed. Which needs the kernel. Circular.

3. **Stdlib lazy init.** Even with `OnceLock` replaced, Rust's own standard library has lazy-initialized globals for panic handling, TLS setup, and allocator configuration. These initialize on first call and use blocking primitives internally. If any of them fire on the dynamic-loader thread during symbol resolution, you hang.

## The Fix

**`LIBRARY_READY` pattern** — a standard jemalloc/tcmalloc recipe:

```rust
use std::sync::atomic::{AtomicBool, Ordering};

static LIBRARY_READY: AtomicBool = AtomicBool::new(false);

/// Runs before the dynamic loader ever calls an interposed symbol.
/// The `.init_array` section causes this to execute when the library is loaded.
#[link_section = ".init_array"]
static INIT: extern "C" fn() = init_library;

extern "C" fn init_library() {
    // All singleton init happens here, in a controlled context with
    // full runtime available. No exported symbol is interposed yet for
    // THIS thread because we haven't returned to the loader.
    runtime_policy::initialize();
    membrane_state::initialize();
    RuntimeMathKernel::initialize();
    // Crucially, also initialize anything that might be lazily evaluated
    // on the hot path: env vars, locale, allocator state.
    let _ = std::env::var("GLIBC_RUST_MODE");
    LIBRARY_READY.store(true, Ordering::Release);
}

#[no_mangle]
pub unsafe extern "C" fn strlen(s: *const c_char) -> usize {
    // Fast path: no Rust runtime touched at all.
    if !LIBRARY_READY.load(Ordering::Acquire) {
        return raw_byte_loop_strlen(s);
    }
    // Full policy-checked path.
    runtime_policy::check_ordering();
    let len = raw_byte_loop_strlen(s);
    runtime_policy::record(StrlenEvent { len });
    len
}

#[inline(always)]
unsafe fn raw_byte_loop_strlen(s: *const c_char) -> usize {
    // NO allocation. NO Rust stdlib. NO policy checks. Pure byte loop.
    let mut n = 0;
    while *s.add(n) != 0 { n += 1; }
    n
}
```

## Why This Works

1. **The fast path is strictly raw bytes.** Zero heap allocations, zero stdlib calls, zero blocking primitives. Even the dynamic loader calling `strlen` during its own initialization is safe because the code touches no shared state.
2. **Initialization happens exactly once, eagerly, in `.init_array`.** By the time any other thread or the loader can call an interposed symbol, `LIBRARY_READY = true`. The race window is minimal and is handled correctly by `Acquire`/`Release` ordering.
3. **Pre-warming `std::env::var`** in `init_library` is critical. It forces the env-var cache to populate *before* the reentrant path is possible.

## What Doesn't Work

- **Rewriting one `OnceLock` at a time.** The fourth, fifth, and sixth instance are the ones that bite. See [THE-FOURTH-INSTANCE.md](THE-FOURTH-INSTANCE.md).
- **Atomic state machines alone.** Sufficient for the top-level singleton, but `Box::new()` inside the init function still calls the allocator, which still reenters.
- **`LEVEL_RESOLVING` sentinel on `safety_level()` alone.** Prevents the direct reentrance but leaves every other reentrance path wide open.
- **`unsafe { libc::getenv() }` instead of `std::env::var`.** The membrane crate in `glibc_rust` has `#![deny(unsafe_code)]`, and even if you allowed it, `libc::getenv` still calls `strlen` internally on some libc versions.

## Generalization

The LD_PRELOAD case is the extreme form of a general pattern: **any code that runs before the runtime is fully ready cannot use the runtime.**

Other manifestations:

- **C++ static initializers.** Objects constructed in static initializers before `main`. If their constructor calls into a library that hasn't been initialized yet, you hang.
- **Signal handlers.** POSIX allows only async-signal-safe functions from a signal handler. Not `malloc`. Not any lock. The rule is identical: don't touch the runtime.
- **Panic handlers (Rust) / fatal error paths.** If panic handling allocates and the allocator is itself broken, you recurse into panic.
- **`#[ctor]` and `.init_array` in any language.** The init function runs before your normal control-flow assumptions hold.
- **`forkChild` handlers.** `fork` copies only the current thread, not other threads holding locks. The child can deadlock on a lock it sees held.

## The Audit

See [STATIC-AUDIT.md §Class 5](STATIC-AUDIT.md#class-5--runtime-init--reentrant-hazards-ld_preload). Every exported symbol in an `LD_PRELOAD`-targeted crate must begin with a `LIBRARY_READY` check; every blocking primitive in that crate must be called only from paths gated behind `LIBRARY_READY == true`.

## Also: `.init_array` alone is not enough

`.init_array` only runs on the thread that loads the library. If your library is loaded via `dlopen` after another thread has already been created, that other thread may still see `LIBRARY_READY == false` briefly. Use `Acquire`/`Release` — never `Relaxed` — for the flag.

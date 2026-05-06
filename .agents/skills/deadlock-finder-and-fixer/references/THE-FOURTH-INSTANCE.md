# The Fourth Instance

> **If you found three of something, there are almost certainly more. Keep searching until the audit is exhaustive.**

This is the single most load-bearing rule in this skill. Every serious concurrency bug in this repo's history is an instance of this rule being ignored. It is named for the following quote from the `glibc_rust` LD_PRELOAD debugging session:

> "I'm debugging an LD_PRELOAD deadlock in the glibc_rust project. When libglibc_rs_abi.so is loaded via LD_PRELOAD, calls to strlen hang. **I've already replaced three OnceLock instances with atomic state machines but it still deadlocks.** I need you to trace the FULL call path from strlen in the ABI crate to find ALL potential blocking points (OnceLock, Mutex, RwLock, lazy_static, thread_local, etc.)."
> — `/home/ubuntu/.claude/projects/-data-projects-glibc-rust/3b5a23a6-2133-4efd-a713-7dd0256e313b/subagents/agent-ad965f6.jsonl:1`

The engineer had found three `OnceLock`s, rewritten all three into atomic state machines, and was confident the fix was complete. It wasn't. There was a fourth, buried in `safety_level() → std::env::var() → strlen` — a reentrant call back through the exported symbol being interposed. And after that, a fifth: `RuntimeMathKernel::new()` contained 72 `parking_lot::Mutex` fields inside a `Box::new()` that the atomic state machine was still calling. And after *that*, a sixth: the Rust stdlib and allocator themselves had lazy initialization paths.

The eventual fix was not "replace another OnceLock". It was the **LIBRARY_READY pattern**: a single `AtomicBool` guarding a raw-byte-loop fast path in *every* exported symbol, plus an `.init_array` (or `#[ctor]`) constructor that ran eager initialization before any interposed symbol call. The standard jemalloc / tcmalloc recipe. Arriving at that design took hours of iteration because each "fix" revealed the next instance.

## The Rule

When you find any of:

- **A deadlock** at runtime — find every other code path that takes the same pair of locks, in any order.
- **A `std::sync::Mutex` held across `.await`** — find every other one in the crate.
- **A `block_on` inside an async context** — find every other one, including in dependencies.
- **An `rusqlite` call from an async handler without `spawn_blocking`** — find every other one.
- **A missing `PRAGMA busy_timeout`** on a SQLite `Connection::open` — find every other open site.
- **An `OnceLock` on a reentrant init path** — find every other blocking primitive on that path, including inside `Box::new`, `String::from`, `format!`, `std::env::var`, and the allocator.
- **A `file_reservation` pattern that was too broad** — find every other reservation that hasn't been reviewed.
- **A shared `env_lock` that poisoned** — find every other shared `Mutex` in your test fixtures.

**You are not done when you fix the one you saw. You are done when an exhaustive audit shows no others remain.**

## How to Audit Exhaustively

1. **Name the hazard precisely.** "Blocking primitive on LD_PRELOAD-reachable path." "Guard live across `.await`." "Conn open without busy_timeout." The precise phrasing is what lets you search for it.
2. **Write the search.** ast-grep for structure; ripgrep for text. See [STATIC-AUDIT.md](STATIC-AUDIT.md). Your search must be over-inclusive on purpose — false positives are cheap; false negatives bite.
3. **Run it over the entire crate / repo / monorepo.** Not just the file where the bug was.
4. **Review every hit by hand.** Do not trust a tool to rule out instances.
5. **Record dispositions.** For each hit: `BUG` (fix), `SAFE` (note why), `UNKNOWN` (investigate further). Commit the notes alongside the fix.
6. **Add a regression check** — a CI grep, an ast-grep rule, a clippy lint, or a `loom` test — that fires if the hazard reappears.

## Why People Skip the Audit

- **"The bug report mentioned one file."** The bug report is a symptom, not a complete description.
- **"The fix I made has tests that pass."** Tests can only catch bugs you've already thought to write tests for.
- **"I've been working on this for three hours."** The fourth instance doesn't care.
- **"My diff is small."** A 1-line fix that misses three others is worse than a 4-line fix that finds them all.
- **"CI is green."** CI runs the tests you have. Not the ones you don't.

Don't skip the audit.

## When It's Safe to Stop

You've done an exhaustive search; each hit has been reviewed; all legitimate hazards are fixed; a regression check is in place; and you've **written down the search you ran** so the next engineer (or the next audit) can reproduce it.

If you cannot write down the exact search you ran, you cannot claim the audit is exhaustive.

## Related Sections

- [STATIC-AUDIT.md](STATIC-AUDIT.md) — the actual grep / ast-grep recipes
- [LD-PRELOAD.md](LD-PRELOAD.md) — the full glibc-rust incident narrative
- [INCIDENTS.md](INCIDENTS.md) — more examples of the fourth-instance problem

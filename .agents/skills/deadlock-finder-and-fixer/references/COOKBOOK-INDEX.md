# Cookbook Index

Quick dispatch to the right language cookbook or topic reference.

## By Language

| Language | Cookbook | Key Tools |
|----------|---------|-----------|
| **Rust (asupersync)** | [ASUPERSYNC.md](ASUPERSYNC.md) | Cx, Scope, obligations, lab/DPOR, GenServer, two-phase channels |
| **Rust (tokio/std)** | [RUST.md](RUST.md) | tokio, parking_lot, crossbeam, rayon, dashmap, sqlx, axum, arc-swap, loom |
| **Go** | [GO.md](GO.md) | goroutines, channels, sync, context, errgroup, pprof, race detector |
| **Python** | [PYTHON.md](PYTHON.md) | GIL, asyncio, threading, multiprocessing, trio/anyio, py-spy, faulthandler |
| **TypeScript / Node.js** | [TYPESCRIPT.md](TYPESCRIPT.md) | event loop, promises, worker_threads, AbortController, React, Next.js |
| **C / C++** | [C-CPP.md](C-CPP.md) | pthread, memory model, std::atomic, signal safety, fork, io_uring, epoll |

## By Topic

| Topic | Reference | Key Content |
|-------|-----------|-------------|
| **"Something is hung"** | [SKILL.md](../SKILL.md) §Triage Table | Symptom → class → section |
| **Diagnosis operators** | [CONCURRENCY-OPERATORS.md](CONCURRENCY-OPERATORS.md) | S→C→G→T→R→E→V chain |
| **Database** | [DATABASE.md](DATABASE.md) + [DATABASE-ADVANCED.md](DATABASE-ADVANCED.md) | SQLite WAL + Postgres advisory/SKIP LOCKED |
| **Distributed** | [DISTRIBUTED.md](DISTRIBUTED.md) | Redlock, etcd, CRDTs, saga, outbox |
| **Lock-free** | [LOCK-FREE.md](LOCK-FREE.md) | CAS, ABA, epoch, seqlocks, flat combiner |
| **Formal methods** | [FORMAL-METHODS.md](FORMAL-METHODS.md) | loom, DPOR, TLA+, miri, TSAN |
| **Resilience** | [RESILIENCE-PATTERNS.md](RESILIENCE-PATTERNS.md) | Circuit breaker, bulkhead, singleflight, hedge |
| **Creative patterns** | [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) | Actor, STM, CSP, structured concurrency |
| **Fix catalog** | [FIX-CATALOG.md](FIX-CATALOG.md) | 14 canonical replacements |
| **Anti-patterns** | [ANTI-PATTERNS.md](ANTI-PATTERNS.md) | All classes consolidated |
| **Incidents** | [INCIDENTS.md](INCIDENTS.md) | 8 real project stories |

## By Bug Class

| Class | Primary Ref | Language Refs |
|-------|------------|---------------|
| 1. Classic deadlock | [SKILL.md §Class 1](../SKILL.md) | [RUST](RUST.md), [GO](GO.md), [C-CPP](C-CPP.md) |
| 2. Async deadlock | [ASYNC.md](ASYNC.md) | [ASUPERSYNC](ASUPERSYNC.md), [RUST](RUST.md), [PYTHON](PYTHON.md), [TYPESCRIPT](TYPESCRIPT.md) |
| 3. Livelock | [RESILIENCE-PATTERNS.md](RESILIENCE-PATTERNS.md) | All languages |
| 4. Database | [DATABASE.md](DATABASE.md) + [DATABASE-ADVANCED.md](DATABASE-ADVANCED.md) | [RUST](RUST.md), [GO](GO.md), [PYTHON](PYTHON.md), [TYPESCRIPT](TYPESCRIPT.md) |
| 5. Runtime-init | [LD-PRELOAD.md](LD-PRELOAD.md) | [C-CPP](C-CPP.md), [RUST](RUST.md) |
| 6. Data race | [VALIDATION.md](VALIDATION.md) | All languages (TSAN, race detector, asyncio debug) |
| 7. Multi-process | [SWARM.md](SWARM.md) | [DISTRIBUTED.md](DISTRIBUTED.md) |
| 8. Poisoning | [ANTI-PATTERNS.md §Class 8](ANTI-PATTERNS.md) | [RUST](RUST.md), [PYTHON](PYTHON.md) |
| 9. Memory ordering | [LOCK-FREE.md](LOCK-FREE.md) | [C-CPP](C-CPP.md), [RUST](RUST.md) |

## Quick Search

```bash
# Find content by keyword across all references
rg -i "keyword" /cs/deadlock-finder-and-fixer/references/

# Find all code recipes for a language
rg "^###" /cs/deadlock-finder-and-fixer/references/RUST.md | head -30
```

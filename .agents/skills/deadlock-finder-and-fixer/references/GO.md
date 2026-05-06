# Go Concurrency Cookbook

<!-- TOC: Goroutines | Channels | sync Package | Context | Errgroup | Race Detector | pprof | Runtime | Patterns | Anti-Patterns | Recipes -->

## Table of Contents

- [The Landscape](#the-landscape)
- [Goroutines in Depth](#goroutines-in-depth)
- [Channels Deep Dive](#channels-deep-dive)
- [The sync Package](#the-sync-package)
- [Context](#context)
- [errgroup and x/sync](#errgroup-and-xsync)
- [The Race Detector](#the-race-detector)
- [pprof for Goroutine Dumps](#pprof-for-goroutine-dumps)
- [The Go Runtime](#the-go-runtime)
- [Database Concurrency (database/sql, pgx, sqlx)](#database-concurrency-databasesql-pgx-sqlx)
- [HTTP Server Patterns](#http-server-patterns)
- [Creative Patterns](#creative-patterns)
- [Go-Specific Anti-Patterns](#go-specific-anti-patterns)
- [Code Recipe Library (25+)](#code-recipe-library-25)
- [Audit Commands](#audit-commands)

---

## The Landscape

Go's concurrency story is "CSP (Communicating Sequential Processes) + shared memory + a world-class scheduler". The good news: goroutines are cheap (8KB initial stack, can grow), channels are first-class, and the race detector is excellent. The bad news: Go gives you enough rope to hang yourself in creative ways.

**The Go concurrency bugs you'll actually see:**

1. **Goroutine leaks** — a goroutine waits forever on a channel/mutex/io and is never cleaned up. You'll see these as memory growth in pprof.
2. **Channel-close panics** — "send on closed channel" is a runtime panic.
3. **Deadlock on all goroutines** — the runtime detects this and prints `fatal error: all goroutines are asleep - deadlock!`.
4. **Race conditions** — shared state without synchronization. Caught by `go test -race`.
5. **WaitGroup misuse** — `wg.Add(1)` after `wg.Wait()`, or inside the goroutine that should be waited on.
6. **Context leaks** — cancel functions not called, ctx chains abandoned.
7. **`sync.Map` vs `map + RWMutex` confusion** — different semantics.

Most of these have *canonical* detection: the race detector catches data races; the runtime prints `fatal error: all goroutines are asleep`; pprof shows leaked goroutines. **If you're not running these, you're not concurrency-safe.**

## Goroutines in Depth

A goroutine is not a thread. The Go runtime multiplexes M goroutines onto N OS threads via the scheduler. The scheduler is **preemptive** (since Go 1.14) — your tight loop can no longer starve other goroutines.

```go
go func() {
    doWork()
}()
```

That `go` starts a goroutine. It runs until it returns or blocks. **Rules:**

1. **You cannot join a goroutine directly.** There is no `join()`. Use channels, `sync.WaitGroup`, or `errgroup.Group`.
2. **Goroutines that never terminate leak.** Every goroutine you spawn must have a termination path. If you can't name it, you have a leak.
3. **`go f()` with captured variables captures the variable, not the value.** Classic bug:
   ```go
   for i := 0; i < 10; i++ {
       go func() { fmt.Println(i) }()  // BUG: all print 10
   }
   // Fix: pass i as argument
   for i := 0; i < 10; i++ {
       go func(i int) { fmt.Println(i) }(i)
   }
   // Go 1.22+: the loop variable is per-iteration, fix not needed
   ```
4. **A goroutine panic crashes the whole program** unless you `recover()` in a deferred call. For server workloads, wrap handler goroutines:
   ```go
   go func() {
       defer func() {
           if r := recover(); r != nil {
               log.Printf("recovered: %v\n%s", r, debug.Stack())
           }
       }()
       handle(req)
   }()
   ```

### The goroutine leak pattern (most common Go bug)

```go
// BUG: if ctx is never cancelled AND ch is never written, goroutine leaks
func doWork(ctx context.Context, ch chan Result) {
    go func() {
        result := slowOperation()
        ch <- result  // ← blocks forever if no reader
    }()
}
```

Fixes:

```go
// Fix 1: use context
func doWork(ctx context.Context, ch chan<- Result) {
    go func() {
        result := slowOperation()
        select {
        case ch <- result:
        case <-ctx.Done():  // caller gave up; don't block forever
        }
    }()
}

// Fix 2: use buffered channel (size 1) so send never blocks
func doWork() <-chan Result {
    ch := make(chan Result, 1)
    go func() {
        ch <- slowOperation()  // buffer = 1, always succeeds
    }()
    return ch
}
```

### The "fatal error: all goroutines are asleep - deadlock!" message

The Go runtime detects the trivial case: when *every* goroutine is blocked, no forward progress is possible. It prints the message and dumps all stacks. This only catches **total** deadlocks — partial deadlocks where some goroutines are still running (but the critical ones are stuck) are invisible to the runtime.

When you see this message, read the stack dump from the bottom up:

```
fatal error: all goroutines are asleep - deadlock!

goroutine 1 [chan receive]:
main.main()
    /path/to/main.go:15 +0x50

goroutine 5 [chan send]:
main.producer(...)
    /path/to/main.go:22 +0x30

...
```

Each goroutine's state (`chan receive`, `chan send`, `semacquire`, etc.) tells you exactly where it's stuck.

## Channels Deep Dive

Go channels are typed bidirectional or unidirectional conduits. They have:

- **Send** (`ch <- value`)
- **Receive** (`value := <-ch`)
- **Close** (`close(ch)`)
- **Range** (`for v := range ch { ... }` — ends when ch is closed and drained)

### Buffered vs unbuffered

```go
unbuf := make(chan int)       // buffer = 0, synchronous rendezvous
buf   := make(chan int, 10)   // buffer = 10, sender blocks only when full
```

- **Unbuffered:** the sender blocks until a receiver is ready, and vice versa. Synchronization built in.
- **Buffered:** the sender blocks only when the buffer is full; the receiver blocks only when it's empty. Good for decoupling producers from consumers.

### The close-panic trap

```go
// PANIC: send on closed channel
close(ch)
ch <- 1  // → "panic: send on closed channel"
```

**Rule:** only the sender closes the channel, and only after ensuring no more sends will happen. If you have multiple senders, use a separate "done" channel or a `sync.WaitGroup` to coordinate.

```go
// Multi-sender pattern: use a separate done channel
var wg sync.WaitGroup
for i := 0; i < 5; i++ {
    wg.Add(1)
    go func(i int) {
        defer wg.Done()
        ch <- i
    }(i)
}
go func() {
    wg.Wait()
    close(ch)  // all senders done, safe to close
}()
for v := range ch { ... }
```

### select

```go
select {
case msg := <-ch1:
    handle1(msg)
case msg := <-ch2:
    handle2(msg)
case ch3 <- result:
    // sent successfully
case <-ctx.Done():
    return ctx.Err()
case <-time.After(5 * time.Second):
    return ErrTimeout
default:
    // none ready — non-blocking
}
```

**Gotchas:**

- **`default` makes select non-blocking.** Remove `default` to block until one case is ready.
- **`time.After` creates a new timer every call.** In a tight loop, use `time.NewTimer` + `Reset`.
- **Nil channels block forever.** `var ch chan int` is nil; sending/receiving blocks. Useful for disabling a branch in select: `ch = nil` to remove it.

### Channel direction for API clarity

```go
func producer(out chan<- int) { ... }  // send-only
func consumer(in <-chan int)  { ... }  // receive-only
```

Directional channels prevent the consumer from accidentally closing or sending.

## The sync Package

```go
import "sync"
```

### sync.Mutex / sync.RWMutex

```go
var mu sync.Mutex
mu.Lock()
defer mu.Unlock()
// critical section
```

**`defer Unlock()` is a near-universal idiom.** It pairs with `Lock()` even on early return or panic. The cost of `defer` is ~40ns — irrelevant unless you're in a hot loop.

**RWMutex:**

```go
var mu sync.RWMutex
mu.RLock()  // read lock — many readers allowed
defer mu.RUnlock()
```

`RWMutex` writer starves readers (Go guarantees no new readers can acquire while a writer is waiting) to prevent writer starvation. This means a steady stream of readers can *delay* a writer briefly but can't lock it out forever.

**Gotchas:**

1. **RLock is reentrant only if you're sure no writer is waiting** — actually, it's NOT reentrant; Go's documentation says: *"If any goroutine calls Lock while the lock is already held for reading or writing, Lock blocks until the lock is available. To ensure that the lock eventually becomes available, a blocked Lock call excludes new readers from acquiring the lock."* So calling `RLock` twice in the same goroutine is a deadlock hazard.

2. **Embedding `sync.Mutex` makes the outer struct non-copyable.** Go won't enforce this; `go vet` will. Use `-copylocks` flag.

### sync.Once

```go
var once sync.Once
var config *Config
func getConfig() *Config {
    once.Do(func() { config = loadConfig() })
    return config
}
```

Runs its function exactly once across all goroutines. If the function panics, `once.Do` considers it "done" and later calls return immediately without re-running. So:

```go
// BUG: partial state on panic
once.Do(func() {
    config = loadConfig()
    if config == nil {
        panic("no config")  // "done"; getConfig returns nil forever
    }
})
```

**Fix:** never panic in `Once.Do`. Return errors via a shared variable:

```go
var (
    once sync.Once
    config *Config
    loadErr error
)
func getConfig() (*Config, error) {
    once.Do(func() {
        config, loadErr = loadConfig()
    })
    return config, loadErr
}
```

### sync.WaitGroup

```go
var wg sync.WaitGroup
for _, item := range items {
    wg.Add(1)  // MUST be before go
    go func(item Item) {
        defer wg.Done()
        process(item)
    }(item)
}
wg.Wait()
```

**Rules:**

1. **Call `wg.Add()` outside the goroutine.** If you call it inside, there's a race: `Wait()` can return before the goroutine starts:
   ```go
   // BUG
   go func() {
       wg.Add(1)   // race with Wait
       defer wg.Done()
       // ...
   }()
   ```
2. **`wg.Add()` with negative counter panics.** `wg.Done()` is `wg.Add(-1)`.
3. **`wg.Wait()` can be called from multiple goroutines.**
4. **Don't reuse a WaitGroup before all `Done()` calls complete.** Reset implicitly.

### sync.Cond

Condition variables are rarely the right answer in Go (channels usually are), but exist:

```go
c := sync.NewCond(&sync.Mutex{})

// Waiter
c.L.Lock()
for !ready { c.Wait() }  // ALWAYS use a predicate loop
c.L.Unlock()

// Signaler
c.L.Lock()
ready = true
c.L.Unlock()
c.Signal()  // or c.Broadcast()
```

### sync.Map

```go
var m sync.Map
m.Store("key", value)
v, ok := m.Load("key")
m.Range(func(k, v any) bool { return true })
```

`sync.Map` is optimized for two specific patterns:
1. **Entry once, read many** (e.g., caches)
2. **Disjoint goroutines** (different goroutines touch different keys)

For general-purpose concurrent map use, `map + sync.RWMutex` is usually faster and type-safer.

### sync/atomic

```go
import "sync/atomic"

var counter atomic.Int64
counter.Add(1)
x := counter.Load()

var ready atomic.Bool
ready.Store(true)
if ready.Load() { ... }

var cfg atomic.Pointer[Config]
cfg.Store(&Config{})
c := cfg.Load()
```

Go 1.19+ has typed atomics (`atomic.Int64`, `atomic.Bool`, `atomic.Pointer[T]`). Use these over the old `atomic.LoadInt64` etc.

**Memory ordering:** Go's atomics are **sequentially consistent**. No weaker orderings available (Go's memory model is simpler than C++/Rust). This is a feature for correctness; it means you can't accidentally pick the wrong memory order.

## Context

`context.Context` is Go's cancellation primitive. It carries:
- A deadline
- A cancel function
- Key-value pairs (for request-scoped data — use sparingly)

```go
ctx, cancel := context.WithTimeout(parent, 5*time.Second)
defer cancel()  // ALWAYS defer cancel

select {
case <-ctx.Done():
    return ctx.Err()  // either DeadlineExceeded or Canceled
case result := <-doAsync(ctx):
    return result, nil
}
```

**Rules:**

1. **Always `defer cancel()`**. Even if you return early, cancel. `go vet` catches missing `cancel`.
2. **Pass `ctx` as the first argument, always named `ctx context.Context`.** Idiom.
3. **Don't store `ctx` in a struct.** Pass it through calls. (Exception: server state structs.)
4. **Don't pass a nil context.** Use `context.TODO()` if you don't have one.
5. **Every function that does I/O or long work should take a ctx and respect it.**

### Context propagation and leaks

```go
// BUG: cancel is never called if the goroutine never returns
func leak(parent context.Context) {
    ctx, _ := context.WithTimeout(parent, time.Minute)
    go func() {
        <-ctx.Done()  // will fire eventually, but cancel is a goroutine leak
    }()
}

// FIX: always defer cancel
func noLeak(parent context.Context) {
    ctx, cancel := context.WithTimeout(parent, time.Minute)
    go func() {
        defer cancel()  // goroutine owns the cancel
        // ...
    }()
}
```

## errgroup and x/sync

`golang.org/x/sync/errgroup` is the idiomatic fan-out/fan-in:

```go
import "golang.org/x/sync/errgroup"

g, ctx := errgroup.WithContext(ctx)
for _, url := range urls {
    url := url  // capture
    g.Go(func() error {
        return fetch(ctx, url)
    })
}
if err := g.Wait(); err != nil {
    return err
}
```

**What errgroup does:**
- Runs each `g.Go` in a goroutine.
- If any returns an error, `ctx` is cancelled (triggering siblings to stop early).
- `g.Wait()` returns the first error.
- `g.SetLimit(n)` (Go 1.20+) caps concurrent goroutines.

**Pattern: bounded fan-out with early cancel:**

```go
g, ctx := errgroup.WithContext(ctx)
g.SetLimit(16)
for _, item := range items {
    item := item
    g.Go(func() error {
        return processWith(ctx, item)
    })
}
return g.Wait()
```

### `x/sync/singleflight`

Deduplicates concurrent requests for the same key:

```go
var sfg singleflight.Group

func getUser(id string) (*User, error) {
    v, err, _ := sfg.Do(id, func() (any, error) {
        return fetchUser(id)  // called once per id, even if 100 callers
    })
    if err != nil { return nil, err }
    return v.(*User), nil
}
```

Classic thundering-herd fix: 1000 goroutines request the same cached-but-missing key, only one hits the DB.

### `x/sync/semaphore`

Weighted semaphore:

```go
sem := semaphore.NewWeighted(100)
for _, task := range tasks {
    if err := sem.Acquire(ctx, 1); err != nil { break }
    go func(t Task) {
        defer sem.Release(1)
        process(t)
    }(task)
}
```

## The Race Detector

```bash
go test -race ./...
go run -race main.go
go build -race  # slower, bigger binary, only for testing
```

The race detector catches data races at runtime by instrumenting every memory access. Output format:

```
==================
WARNING: DATA RACE
Read at 0x00c000010088 by goroutine 7:
  main.main.func2()
      /path/to/main.go:15 +0x38

Previous write at 0x00c000010088 by goroutine 6:
  main.main.func1()
      /path/to/main.go:10 +0x48

Goroutine 7 (running) created at:
  main.main()
      /path/to/main.go:12 +0xc0
```

**Two stacks:** the current access and the previous conflicting access. Both are relevant.

**Rules:**

1. **Run the race detector in CI** on every PR. It's ~2x slower but finds real bugs.
2. **Almost every reported race is real.** False positives are rare.
3. **Coverage matters:** the detector only catches races that *actually happen* during the test run. Stress tests with many goroutines increase coverage.
4. **Incompatible with some cgo.** Use `CGO_ENABLED=0` tests for pure-Go coverage.

## pprof for Goroutine Dumps

### HTTP endpoint (net/http/pprof)

```go
import _ "net/http/pprof"
go http.ListenAndServe("localhost:6060", nil)
```

Then:

```bash
# Goroutine dump
curl http://localhost:6060/debug/pprof/goroutine?debug=2

# CPU profile (30s)
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30

# Heap
go tool pprof http://localhost:6060/debug/pprof/heap

# Block profile (goroutines blocked on sync)
# Must enable first: runtime.SetBlockProfileRate(1)
go tool pprof http://localhost:6060/debug/pprof/block

# Mutex profile (mutex contention)
# Must enable: runtime.SetMutexProfileFraction(1)
go tool pprof http://localhost:6060/debug/pprof/mutex
```

### Goroutine dump without pprof

```go
import "runtime"

buf := make([]byte, 1<<20)  // 1 MiB
n := runtime.Stack(buf, true)  // true = all goroutines
os.Stderr.Write(buf[:n])
```

Wire this up to SIGUSR1:

```go
import "os/signal"
import "syscall"

c := make(chan os.Signal, 1)
signal.Notify(c, syscall.SIGUSR1)
go func() {
    for range c {
        buf := make([]byte, 1<<20)
        n := runtime.Stack(buf, true)
        log.Printf("=== goroutine dump ===\n%s\n", buf[:n])
    }
}()
```

Then `kill -USR1 $PID` gives you an on-demand dump without stopping the process.

### Reading a goroutine dump

```
goroutine 42 [chan receive, 5 minutes]:
main.worker(0xc000100000)
    /path/main.go:87 +0x120
created by main.main in goroutine 1
    /path/main.go:42 +0xa0
```

Fields:
- `42` — goroutine ID
- `chan receive` — current state (blocked on channel receive)
- `5 minutes` — how long it's been in this state
- Stack trace

**Red flags in a goroutine dump:**
- Many goroutines in the same state with the same stack — likely all blocked on the same thing (contention or deadlock)
- Goroutines stuck for minutes — leaks
- A goroutine in `semacquire` for an extended time — mutex contention
- `IO wait` with no timeout — possible I/O hang

## The Go Runtime

Key scheduler concepts:

- **G** (goroutine) — a runnable unit
- **M** (machine) — an OS thread
- **P** (processor) — a scheduling context (default count = `GOMAXPROCS`)

Each P has a local run queue of Gs. Ms execute Gs by binding to a P. When a G blocks on syscall, the M releases its P (other Gs can run on another M).

**`GOMAXPROCS`** defaults to the number of CPU cores. In container environments with CPU limits, Go <1.19 doesn't know about cgroup limits — set `GOMAXPROCS` explicitly or use `go.uber.org/automaxprocs`.

**`runtime.Gosched()`** yields the current goroutine; the scheduler may pick another. Rarely needed — the scheduler is preemptive.

**`runtime.GC()`** forces a garbage collection. Only useful in benchmarks.

**`GODEBUG`** environment variable:
- `GODEBUG=schedtrace=1000` prints scheduler statistics every second
- `GODEBUG=gctrace=1` prints GC details
- `GODEBUG=scheddetail=1` + schedtrace shows per-P details

## Database Concurrency (database/sql, pgx, sqlx)

`database/sql` is *pool-first*. You open one `*sql.DB` per database and it manages a pool of connections.

```go
db, err := sql.Open("postgres", dsn)
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(25)
db.SetConnMaxLifetime(5 * time.Minute)
```

**Rules:**

1. **`*sql.DB` is safe for concurrent use.** It's a pool, not a connection.
2. **`*sql.Tx` and `*sql.Conn` are NOT safe for concurrent use.** Don't share across goroutines.
3. **`QueryRow` must be followed by `Scan` before the next query** on the same connection. If you `defer rows.Close()` and then start another query on the same conn, deadlock.
4. **Always close `*sql.Rows`**: `defer rows.Close()`. Otherwise the connection is never returned to the pool.
5. **Set `SetMaxOpenConns` matching your database's limit.** Too many = DB rejects. Too few = Go queue.

### Common bug: connection pool exhaustion

```go
// BUG: rows never closed if early return
rows, err := db.Query("...")
if err != nil { return err }
for rows.Next() {
    if err := rows.Scan(&x); err != nil {
        return err  // rows still open, connection leaked
    }
}
```

Fix: always `defer rows.Close()` immediately after `db.Query`.

### pgx (Postgres-specific, higher perf)

```go
pool, err := pgxpool.New(ctx, dsn)
defer pool.Close()

err = pool.QueryRow(ctx, "SELECT ...").Scan(&v)
```

pgx supports context-aware cancellation natively. Every query takes `ctx` and is cancelled if `ctx` is.

### Transaction deadlocks (Postgres, MySQL)

Unlike SQLite, Postgres and MySQL detect deadlocks at the row level and **kill one transaction** with an error. Your retry logic must handle this:

```go
func retryOnDeadlock(fn func() error) error {
    for i := 0; i < 5; i++ {
        err := fn()
        if err == nil { return nil }
        if !isDeadlock(err) { return err }
        time.Sleep(time.Duration(1<<i) * 10 * time.Millisecond)
    }
    return fmt.Errorf("deadlock after 5 retries")
}
```

### SELECT FOR UPDATE and advisory locks

```go
tx.Exec(`SELECT * FROM accounts WHERE id = $1 FOR UPDATE`, id)
// ... modify, then commit

// Advisory lock (session-level)
tx.Exec(`SELECT pg_advisory_lock($1)`, key)
defer tx.Exec(`SELECT pg_advisory_unlock($1)`, key)
```

See [DISTRIBUTED.md](DISTRIBUTED.md) for the full advisory-lock catalog.

## HTTP Server Patterns

### The classic Go HTTP server

```go
srv := &http.Server{
    Addr:    ":8080",
    Handler: handler,
    ReadHeaderTimeout: 5 * time.Second,  // ALWAYS set this
    ReadTimeout:       10 * time.Second,
    WriteTimeout:      30 * time.Second,
    IdleTimeout:       120 * time.Second,
}
```

**Without timeouts, a slow client can pin a goroutine for hours.**

### Graceful shutdown

```go
go srv.ListenAndServe()

sig := make(chan os.Signal, 1)
signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
<-sig

ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
srv.Shutdown(ctx)  // stops accepting, waits for in-flight
```

### Per-request concurrency

Every incoming HTTP request is a goroutine. The handler can spawn more:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    g, ctx := errgroup.WithContext(r.Context())
    g.SetLimit(4)
    for _, source := range sources {
        source := source
        g.Go(func() error { return fetch(ctx, source) })
    }
    if err := g.Wait(); err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    // ...
}
```

The `r.Context()` is cancelled when the client disconnects — your errgroup will stop early.

### Gin, Echo, Fiber

Same underlying model, different ergonomics. The concurrency rules are the same:
- Every handler is a goroutine.
- `c.Request.Context()` (Gin) or `c.Context()` (Fiber) is the request ctx.
- Don't share `c` across goroutines — use the ctx and copies of needed data.

## Creative Patterns

### Pipelines

```go
// Stage 1: producer
func gen(nums ...int) <-chan int {
    out := make(chan int)
    go func() {
        defer close(out)
        for _, n := range nums { out <- n }
    }()
    return out
}

// Stage 2: transformer
func sq(in <-chan int) <-chan int {
    out := make(chan int)
    go func() {
        defer close(out)
        for n := range in { out <- n * n }
    }()
    return out
}

// Compose:
for n := range sq(gen(1, 2, 3, 4)) {
    fmt.Println(n)
}
```

### Fan-out / Fan-in

```go
// Fan-out: one producer, N workers
func fanOut(in <-chan Work, n int) []<-chan Result {
    workers := make([]<-chan Result, n)
    for i := 0; i < n; i++ {
        out := make(chan Result)
        workers[i] = out
        go func() {
            defer close(out)
            for w := range in { out <- process(w) }
        }()
    }
    return workers
}

// Fan-in: N channels, one merged channel
func fanIn(channels ...<-chan Result) <-chan Result {
    var wg sync.WaitGroup
    out := make(chan Result)
    for _, c := range channels {
        wg.Add(1)
        go func(c <-chan Result) {
            defer wg.Done()
            for r := range c { out <- r }
        }(c)
    }
    go func() { wg.Wait(); close(out) }()
    return out
}
```

### Worker Pool with bounded concurrency

```go
func workerPool(ctx context.Context, n int, tasks <-chan Task) <-chan Result {
    results := make(chan Result)
    var wg sync.WaitGroup
    for i := 0; i < n; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for {
                select {
                case task, ok := <-tasks:
                    if !ok { return }
                    results <- process(task)
                case <-ctx.Done():
                    return
                }
            }
        }()
    }
    go func() { wg.Wait(); close(results) }()
    return results
}
```

### Single-Writer via channel

```go
type Store struct {
    writes chan writeReq
    state  map[string]string  // private to goroutine
}

type writeReq struct {
    key, value string
    reply      chan error
}

func newStore() *Store {
    s := &Store{
        writes: make(chan writeReq, 32),
        state:  make(map[string]string),
    }
    go s.writer()
    return s
}

func (s *Store) writer() {
    for req := range s.writes {
        s.state[req.key] = req.value
        req.reply <- nil
    }
}

func (s *Store) Write(key, value string) error {
    reply := make(chan error, 1)
    s.writes <- writeReq{key, value, reply}
    return <-reply
}
```

## Go-Specific Anti-Patterns

- **Goroutines without a termination path.** Every `go` should be traceable to an exit.
- **`go func(){ wg.Add(1); ... }()`.** Race with `Wait()`. Add BEFORE go.
- **Closing a channel from the receiver side.** Senders may panic.
- **`time.After` in a loop.** Leaks timers. Use `time.NewTimer` + `Reset`.
- **`defer` in a loop inside a long-running function.** Defers accumulate until function returns; memory grows.
- **Ignoring the race detector in CI.**
- **`sync.Map` without need.** Usually `map + sync.RWMutex` is simpler and faster.
- **Missing `ctx` in internal APIs.** Any function that does I/O should take `ctx`.
- **`fmt.Println` in hot loops without synchronization.** The runtime serializes but it's slow; use structured logging.
- **Global state (package vars) as "shared state".** Usually a code smell hiding a coordinator you should make explicit.
- **Panic in a goroutine without `recover`** — crashes the whole program.
- **`runtime.GOMAXPROCS(1)` as "fix" for race conditions** — hides bugs; the code is still wrong.

## Code Recipe Library (25+)

### 1. Cancel propagation

```go
ctx, cancel := context.WithCancel(parent)
defer cancel()
err := doWork(ctx)
```

### 2. Timeout wrapping every RPC

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
resp, err := client.Call(ctx, req)
```

### 3. errgroup with concurrency limit

```go
g, ctx := errgroup.WithContext(ctx)
g.SetLimit(16)
for _, item := range items {
    item := item
    g.Go(func() error { return process(ctx, item) })
}
return g.Wait()
```

### 4. Leak-free goroutine with context

```go
go func() {
    defer log.Println("exited")
    for {
        select {
        case msg := <-in:
            handle(msg)
        case <-ctx.Done():
            return
        }
    }
}()
```

### 5. Single-flight dedup

```go
var g singleflight.Group
v, err, _ := g.Do(key, func() (any, error) { return fetch(key) })
```

### 6. Weighted semaphore

```go
sem := semaphore.NewWeighted(100)
sem.Acquire(ctx, 1)
defer sem.Release(1)
```

### 7. Pipeline with explicit close

```go
stage1 := producer(ctx)
stage2 := transform(ctx, stage1)
for v := range stage2 { ... }
```

### 8. Fan-in merge

```go
merged := fanIn(c1, c2, c3)
for v := range merged { ... }
```

### 9. Rate-limiter with `golang.org/x/time/rate`

```go
lim := rate.NewLimiter(rate.Every(time.Second), 10)  // 10/s, burst 10
if err := lim.Wait(ctx); err != nil { return err }
```

### 10. Retry with exponential backoff

```go
for attempt := 0; attempt < 5; attempt++ {
    err := op()
    if err == nil { return nil }
    if !retryable(err) { return err }
    time.Sleep(time.Duration(100<<attempt) * time.Millisecond)
}
```

### 11. Context with value (request ID, user, etc.)

```go
type ctxKey int
const userKey ctxKey = 0
ctx = context.WithValue(ctx, userKey, user)
u, _ := ctx.Value(userKey).(*User)
```

### 12. Graceful shutdown

```go
sig := make(chan os.Signal, 1)
signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
<-sig
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
srv.Shutdown(ctx)
```

### 13. Signal-driven goroutine dump

```go
c := make(chan os.Signal, 1)
signal.Notify(c, syscall.SIGUSR1)
go func() {
    for range c {
        buf := make([]byte, 1<<20)
        n := runtime.Stack(buf, true)
        log.Printf("GOROUTINE DUMP:\n%s", buf[:n])
    }
}()
```

### 14. WaitGroup with Add-before-go

```go
var wg sync.WaitGroup
for _, i := range items {
    wg.Add(1)
    go func(i Item) {
        defer wg.Done()
        process(i)
    }(i)
}
wg.Wait()
```

### 15. Close channel safely with multi-sender

```go
var wg sync.WaitGroup
for _, i := range inputs {
    wg.Add(1)
    go func(i Input) {
        defer wg.Done()
        for _, v := range compute(i) { ch <- v }
    }(i)
}
go func() { wg.Wait(); close(ch) }()
```

### 16. Non-blocking send

```go
select {
case ch <- v:
default:  // channel full, drop
}
```

### 17. Deadline check

```go
if deadline, ok := ctx.Deadline(); ok {
    if time.Until(deadline) < time.Second { return ctx.Err() }
}
```

### 18. Once.Do with error channel

```go
var (
    once sync.Once
    val  *Resource
    err  error
)
func get() (*Resource, error) {
    once.Do(func() { val, err = load() })
    return val, err
}
```

### 19. Lazy init with sync.Once and pointer

```go
var resource unsafe.Pointer  // or atomic.Pointer[Resource]
func get() *Resource {
    p := atomic.LoadPointer(&resource)
    if p != nil { return (*Resource)(p) }
    new := load()
    if atomic.CompareAndSwapPointer(&resource, nil, unsafe.Pointer(new)) {
        return new
    }
    return (*Resource)(atomic.LoadPointer(&resource))  // someone else won
}
```

### 20. database/sql with context

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
rows, err := db.QueryContext(ctx, "SELECT ...", args...)
if err != nil { return err }
defer rows.Close()
for rows.Next() { ... }
```

### 21. Transaction retry on deadlock

```go
for i := 0; i < 5; i++ {
    tx, _ := db.BeginTx(ctx, nil)
    err := fn(tx)
    if err == nil && tx.Commit() == nil { return nil }
    tx.Rollback()
    if !isDeadlock(err) { return err }
    time.Sleep(time.Duration(1<<i) * 10 * time.Millisecond)
}
```

### 22. Per-goroutine panic recovery

```go
go func() {
    defer func() {
        if r := recover(); r != nil {
            log.Printf("panic: %v\n%s", r, debug.Stack())
        }
    }()
    handle()
}()
```

### 23. Bounded channel as semaphore

```go
sem := make(chan struct{}, 10)
for _, task := range tasks {
    sem <- struct{}{}
    go func(t Task) {
        defer func() { <-sem }()
        process(t)
    }(task)
}
```

### 24. `sync.Pool` for allocation reuse

```go
var bufPool = sync.Pool{
    New: func() any { return new(bytes.Buffer) },
}
buf := bufPool.Get().(*bytes.Buffer)
defer func() { buf.Reset(); bufPool.Put(buf) }()
```

### 25. Actor via struct + channel

See the Single-Writer pattern above.

### 26. `context.AfterFunc` (Go 1.21+)

```go
stop := context.AfterFunc(ctx, func() { cleanup() })
defer stop()  // cancel the cleanup if not needed
```

### 27. SELECT FOR UPDATE in transaction

```go
tx, _ := db.BeginTx(ctx, nil)
defer tx.Rollback()
_, err := tx.ExecContext(ctx, `SELECT * FROM foo WHERE id = $1 FOR UPDATE`, id)
// ... update ...
tx.Commit()
```

## Audit Commands

```bash
# The three essential Go concurrency audits:

# 1. Race detector on tests
go test -race ./...

# 2. go vet (includes copy-of-mutex and context-leak checks)
go vet ./...

# 3. staticcheck (broader linter)
staticcheck ./...

# Find all goroutines in source (review each for termination path)
rg -n '\bgo [a-zA-Z_]' . --type go

# Find wg.Add inside go func (a common bug)
rg -n -U 'go func\(\) ?\{[^}]*wg\.Add' . --type go

# Find time.After in loops (timer leak)
rg -n -B2 -A2 'time\.After' . --type go | rg 'for|range' -B2 -A2

# Find channels closed from the receiver side (code review target)
rg -n 'close\([a-z]*[Cc]h' . --type go

# Find unbounded goroutine spawn
rg -n -U 'for [^{]* \{[^}]*go func' . --type go | head

# Find missing defer cancel()
rg -n -A3 'WithTimeout\|WithCancel\|WithDeadline' . --type go | grep -v 'defer cancel'

# pprof heap (find goroutine leaks via memory growth)
curl -s http://localhost:6060/debug/pprof/goroutine?debug=2 | head -500

# Mutex contention profile
curl -s http://localhost:6060/debug/pprof/mutex?debug=1
```

## See Also

- [SKILL.md](../SKILL.md) — the 9-class taxonomy and triage
- [DATABASE.md](DATABASE.md) — connection pools, deadlock retry, isolation levels
- [DISTRIBUTED.md](DISTRIBUTED.md) — Redlock, pg advisory, etcd leases
- [ASYNC.md](ASYNC.md) — cross-language async patterns
- [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) — actor, CSP, structured concurrency
- [RESILIENCE-PATTERNS.md](RESILIENCE-PATTERNS.md) — circuit breaker, bulkhead, singleflight

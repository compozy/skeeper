# Async / Tokio Concurrency Bugs

The specific set of concurrency bugs that exists because async runtimes look sequential in the source but yield between every `.await`. Distilled from the `remote-compilation-helper`, `mcp-agent-mail-rust`, `frankensearch`, `jeffreys-skills-md`, and `coding-agent-session-search` session clusters.

## The Four Core Mistakes

Async/Tokio deadlocks cluster into four patterns. All four were observed repeatedly across the sessions in this repo.

### 1. `std::sync::Mutex` held across `.await`

```rust
// BUG: guard is alive across the yield point
let guard = shared.lock().unwrap();
some_io().await;            // ← task yields; worker thread is blocked holding the lock
guard.update();              // ← another task on the same worker needs the lock, hangs forever
```

This is the single most common async concurrency bug. It looks fine. It compiles. It passes the tests when you run them sequentially. It deadlocks in production under load.

**Why it's bad.** `std::sync::Mutex` is a blocking primitive. When a task holds it and yields, it blocks the *thread*, not just the task. Other tasks scheduled on that worker cannot run. If any of those other tasks also need the lock, you're deadlocked.

**Fix (preferred).** Drop the guard before `.await`:

```rust
let value = {
    let guard = shared.lock().unwrap();
    guard.clone()  // or extract exactly what you need
};  // guard dropped here
some_io(value).await;
```

**Fix (only if you must hold across await).** Use `tokio::sync::Mutex`, which is async-aware:

```rust
let guard = shared.lock().await;      // no std::sync here
let value = guard.clone();
some_io(value).await;
// guard still live, but tokio::Mutex's wait is async, so it's safe
```

Note: `tokio::sync::Mutex` is *slower* than `std::sync::Mutex` (it goes through the task scheduler). Prefer dropping the guard.

**Detection:**
```bash
cargo +nightly clippy -- -W clippy::await_holding_lock
```

This lint is specifically for this bug. Turn it on in CI.

### 2. `block_on` inside an async runtime

```rust
// BUG: reentering the runtime
async fn handler() {
    let result = tokio::runtime::Handle::current().block_on(async {
        inner_work().await
    });
    // ...
}
```

Panic (if tokio detects it) or deadlock (if it doesn't). The runtime worker thread is stuck calling `block_on`, which waits for the inner future to complete. But the inner future needs the same worker thread to make progress.

**Why people do it.** They want to bridge from async to sync, or to "just block" on a small piece of async work.

**Fix.** `spawn_blocking` for sync work from async; restructure the bridge if you're going the other direction:

```rust
async fn handler() {
    let result = tokio::task::spawn_blocking(|| {
        expensive_sync_work()
    }).await.unwrap();
}
```

If the code is already async and you want to "just wait" for it, use `.await` — you're already in an async context.

### 3. Unbounded `spawn_blocking`

```rust
// BUG: spawning thousands of blocking tasks
for item in items {
    tokio::task::spawn_blocking(move || process_sync(item));
}
```

`spawn_blocking` uses a separate thread pool (default: 512 threads for the full tokio runtime, fewer on small configurations). Unbounded spawning queues tasks beyond that pool. New blocking tasks block their caller, which may be an async handler, which blocks the main worker pool. Cascading stall.

**Fix.** Semaphore-bounded spawn:

```rust
let sem = Arc::new(tokio::sync::Semaphore::new(16));
for item in items {
    let permit = sem.clone().acquire_owned().await?;
    tokio::task::spawn_blocking(move || {
        let _permit = permit;            // dropped at end of closure
        process_sync(item)
    });
}
```

Or: chunk the work and `yield_now()` between chunks to let other tasks progress.

### 4. `Arc<Mutex<T>>` as a `Send` workaround

```rust
// The code tries to spawn a task, the compiler complains about !Send,
// and the developer reaches for Arc<Mutex>:
let data = Arc::new(Mutex::new(expensive_state));
tokio::spawn(async move {
    let guard = data.lock().unwrap();
    process(&guard);
});
```

`Arc<Mutex<T>>` is often the *wrong* solution. The compiler said "this type is not Send" because the data isn't meant to be shared across threads. Wrapping it in `Mutex` forces it to be shared, but now you have lock contention on data that only one task actually uses.

**Fix.** Pass ownership, not a reference:

```rust
let data = expensive_state;
tokio::spawn(async move {
    process(&data);              // no lock needed, data is owned by this task
});
```

Or use channels:

```rust
let (tx, mut rx) = tokio::sync::mpsc::channel::<Request>(16);
tokio::spawn(async move {
    let mut state = expensive_state;   // private, owned, no Mutex
    while let Some(req) = rx.recv().await {
        state.handle(req);
    }
});
// Callers: tx.send(request).await?;
```

## Channel Patterns

### Bounded vs Unbounded

- **Unbounded** (`mpsc::unbounded_channel`) is a memory leak waiting to happen. If producers outpace consumers, memory grows without bound.
- **Bounded** (`mpsc::channel(N)`) gives you backpressure but risks deadlock if producer and consumer depend on each other (Pattern 5 below).

Always bound. If you need "unbounded-ish" for rare bursts, use a large bound (e.g., 10,000) and log/alert when near capacity.

### 5. Channel cycle deadlock

```rust
// BUG: circular channel dependency
async fn task_a(tx_b: Sender<X>, mut rx_b: Receiver<Y>) {
    loop {
        tx_b.send(make_x()).await?;       // waits if full
        let y = rx_b.recv().await?;       // waits if empty
    }
}
async fn task_b(tx_a: Sender<Y>, mut rx_a: Receiver<X>) {
    loop {
        let x = rx_a.recv().await?;       // waits if empty
        tx_a.send(make_y(x)).await?;      // waits if full
    }
}
```

If both channels are bounded and both fill at the same time, task A can't send (channel full), task B can't send (channel full), both are stuck.

**Fix.** Use `try_send` + drop-oldest policy; or make the channels unbounded *in one direction* (with monitoring); or restructure so there's a single request/response relationship rather than bidirectional streaming.

### 6. `Notify` subscription race

`tokio::sync::Notify` requires you to call `notified()` *before* the `notify_one()` could fire. Otherwise, the notification is dropped.

```rust
// BUG: subscription AFTER the event
if !ready.load(Ordering::Acquire) {
    notify.notified().await;                // ← may miss notification that already fired
}
```

**Fix.**

```rust
let notified = notify.notified();
tokio::pin!(notified);
if !ready.load(Ordering::Acquire) {
    notified.await;
}
```

Or use a `watch` channel, which carries the state + notification together.

## Task Starvation

Not technically a deadlock, but observationally identical: one task monopolizes a runtime worker, other tasks on that worker cannot be polled. Common causes:

- `std::thread::sleep` instead of `tokio::time::sleep`
- Synchronous I/O (`std::fs::read_to_string`) instead of `tokio::fs`
- CPU-heavy work in a task without `yield_now()` breakpoints
- C library calls that block (database drivers, crypto, image processing) — wrap in `spawn_blocking`

**Detection.** `tokio-console` shows task runtimes. Any task with `busy > 100ms` is suspicious.

## Diagnosis

Primary tool: **`tokio-console`**.

```toml
[dependencies]
console-subscriber = "0.2"
```

```rust
fn main() {
    console_subscriber::init();
    // ...
}
```

Then run `tokio-console` in another terminal. It shows every task, resource, and waker, with live updates.

Secondary: `gdb --batch -ex "thread apply all bt full"`. See gdb-for-debugging §"Async Runtime Debugging".

## Validation

- [ ] `cargo +nightly clippy -- -W clippy::await_holding_lock` clean
- [ ] No `block_on` inside async functions (audit manually)
- [ ] Every `spawn_blocking` is behind a `Semaphore` (or rate-limited upstream)
- [ ] No `Arc<Mutex<T>>` where ownership + channels would work
- [ ] Every channel is bounded (or monitored)
- [ ] `tokio-console` shows no tasks with `busy > 100ms` under normal load

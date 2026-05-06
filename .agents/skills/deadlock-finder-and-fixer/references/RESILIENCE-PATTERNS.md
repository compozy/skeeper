# Resilience Patterns

Patterns that prevent concurrency bugs indirectly by bounding resource consumption, isolating failures, and providing structured retry/fallback behavior. Without these, concurrency bugs manifest as cascading failures.

Sourced from asupersync combinator library, mcp-agent-mail-rust singleflight implementation, and 3000+ cass search hits across circuit breaker, bulkhead, and backpressure patterns.

---

## Circuit Breaker

Prevents cascading failure by fast-failing requests to a degraded dependency.

```
CLOSED → [failure_count > threshold] → OPEN
OPEN → [timeout elapsed] → HALF_OPEN
HALF_OPEN → [probe succeeds] → CLOSED
HALF_OPEN → [probe fails] → OPEN
```

```rust
// Asupersync native:
let breaker = CircuitBreaker::new(CircuitBreakerConfig {
    failure_threshold: 5,
    recovery_timeout: Duration::from_secs(30),
    half_open_max: 1,
});
let result = breaker.call(cx, || external_service(cx)).await?;
```

**Concurrency relevance:** Without a circuit breaker, a slow dependency accumulates blocked threads/tasks. Those tasks hold connections, semaphore permits, or locks. Eventually the caller's pool is exhausted → deadlock-like behavior (everything waiting, nothing progressing).

---

## Bulkhead

Isolate failure domains. Each dependency gets its own resource pool.

```rust
// Asupersync native:
let fast_pool = bulkhead(cx, BulkheadConfig { max_concurrent: 100 });
let slow_pool = bulkhead(cx, BulkheadConfig { max_concurrent: 10 });

// One pool exhausting doesn't affect the other
let fast = fast_pool.call(cx, || fast_service(cx)).await?;
let slow = slow_pool.call(cx, || slow_service(cx)).await?;
```

**Concurrency relevance:** Without bulkheads, one slow API call consumes all available connections/threads, blocking everything else. With bulkheads, only the slow dependency's pool is affected.

---

## Singleflight / Request Coalescing

Deduplicates concurrent requests for the same key. One "leader" fetches; others "join" and wait.

From mcp-agent-mail-rust `CoalesceMap` (session 24b430d6):

```rust
pub struct CoalesceMap<K, V> {
    map: Mutex<HashMap<K, Slot<V>>>,
    condvar: Condvar,
}

impl<K: Hash + Eq, V: Clone> CoalesceMap<K, V> {
    pub fn execute_or_join<F>(&self, key: K, f: F) -> Result<V>
    where F: FnOnce() -> Result<V>,
    {
        let mut state = self.map.lock();
        if let Some(slot) = state.get(&key) {
            return Ok(slot.wait_for_result());  // join existing
        }
        let slot = Slot::new();
        state.insert(key.clone(), slot.clone());
        drop(state);  // release lock before executing f
        
        let result = f()?;
        slot.complete(result.clone());
        Ok(result)
    }
}
```

31 edge-case tests from session evidence: max entries, timeout, concurrent leaders, cache eviction, error propagation.

**Concurrency relevance:** Prevents thundering herd / cache stampede. 1000 concurrent requests for the same cache key → 1 DB hit + 999 waits, not 1000 DB hits.

---

## Token Bucket Rate Limiter

```rust
// Asupersync native:
let limiter = rate_limit(cx, RateLimitConfig {
    rate: 100,                        // requests per second
    burst: 20,                        // burst allowance
    strategy: BackpressureStrategy::Wait,  // or Reject, or Shed
});
let result = limiter.call(cx, || operation(cx)).await?;
```

**Concurrency relevance:** Without rate limiting, burst traffic can overwhelm a dependency, exhaust its connection pool, and cascade back as timeouts → retry storms → livelock.

---

## Adaptive Concurrency Limiter

Dynamically adjusts concurrency limits based on latency signals. Similar to TCP Vegas congestion control:

```
If latency decreasing → increase limit (more capacity available)
If latency increasing → decrease limit (approaching saturation)
If timeouts spiking → drop limit sharply (overloaded)
```

**Concurrency relevance:** Static concurrency limits are either too high (overload) or too low (underutilization). Adaptive limits find the sweet spot automatically.

---

## Retry with Exponential Backoff + Jitter

```rust
// Asupersync native (budget-aware):
let result = retry(cx, RetryPolicy::exponential(
    max_attempts: 5,
    initial_delay: Duration::from_millis(100),
    max_delay: Duration::from_secs(10),
    jitter: Jitter::Full,
), || operation(cx)).await?;
```

**Always add jitter.** Without it, all clients retry at the same instant → synchronized retry storm → livelock.

**Formula:** `delay = min(max_delay, initial * 2^attempt) + random(0, delay)`

Full jitter: `delay = random(0, min(max_delay, initial * 2^attempt))`

Decorrelated jitter: `delay = random(initial, prev_delay * 3)`

---

## Deadline Propagation

Attach a deadline to every request. Pass it through every call in the chain.

```rust
// Asupersync: Cx carries budget/deadline automatically
async fn handle(cx: &Cx, req: Request) -> Outcome<Response> {
    cx.checkpoint()?;  // fails if deadline expired
    let data = fetch(cx, &req).await?;  // deadline propagates
    Outcome::ok(Response::from(data))
}
```

```go
// Go: context.WithTimeout
ctx, cancel := context.WithTimeout(parent, 5*time.Second)
defer cancel()
resp, err := client.Call(ctx, req)  // deadline propagates via ctx
```

**Concurrency relevance:** Without deadlines, a slow operation at depth N in a call chain ties up resources at depths 1..N-1 indefinitely. With deadlines, everything times out together.

---

## Backpressure + Watermarks

```
High watermark: stop accepting new work (queue > 80%)
Low watermark:  resume accepting (queue < 50%)
```

**Implementations:**
- **Bounded channels** (Rust/Go/Node): sender blocks or gets error when full
- **Semaphore** (all languages): acquire before processing; reject if no permits
- **Stream backpressure** (Node): check `write()` return → `pause()`/`resume()`
- **HTTP 429 Too Many Requests**: signal to clients to back off

---

## Load Shedding

When overloaded, reject a fraction of requests immediately rather than trying to serve them all slowly:

```rust
// Reject if queue depth > threshold
if queue.len() > MAX_QUEUE {
    return Outcome::Err(Error::Overloaded);
}
```

**Strategies:**
- **Random drop:** Drop N% of requests randomly
- **LIFO drop:** Drop oldest requests (they're most likely to timeout anyway)
- **Priority drop:** Keep high-priority, drop low-priority
- **Quota-based:** Per-tenant rate limits

---

## Hedge Request

Send backup request after P50 latency; return whichever arrives first:

```rust
// Asupersync native:
let result = hedge(cx, Duration::from_millis(50), || {
    primary_service(cx)
}, || {
    backup_service(cx)
}).await?;
// Loser is automatically cancelled and drained
```

**Concurrency relevance:** Tail latency often comes from one server being slow. Hedging avoids waiting for the slow server without adding much load (backup only sent after P50).

---

## Quorum Operations

```rust
// Asupersync native:
let result = quorum(cx, 2, vec![  // succeed if 2 of 3 respond
    || write_replica_1(cx),
    || write_replica_2(cx),
    || write_replica_3(cx),
]).await?;
```

Losers (replicas that haven't responded yet) are cancelled and drained.

---

## Anti-Patterns

- **Retry without backoff** → synchronized retry storm → livelock
- **Retry without budget** → infinite retries exhaust resources
- **Circuit breaker without half-open** → service never recovers
- **Bulkhead with shared pool** → defeats the purpose (no isolation)
- **Singleflight with no TTL** → stale results served forever
- **Deadline without propagation** → inner call doesn't respect outer timeout
- **Load shedding without client feedback** → clients don't know to back off

---

## See Also

- [ASUPERSYNC.md](ASUPERSYNC.md) — native combinator library (retry, hedge, quorum, bulkhead, circuit_breaker)
- [DISTRIBUTED.md](DISTRIBUTED.md) — distributed lock, saga, outbox patterns
- [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) — backpressure-first design, single-writer
- [GO.md](GO.md) — x/sync/singleflight, errgroup, context propagation
- [TYPESCRIPT.md](TYPESCRIPT.md) — p-limit, AbortController, stream backpressure

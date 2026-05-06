# Distributed Concurrency

<!-- TOC: Distributed Locks | Consensus | CRDTs | Optimistic vs Pessimistic | Idempotency | Saga/Outbox | Isolation Levels | Advisory Locks -->

Concurrency across processes, machines, and networks. When the latency between actors is milliseconds to seconds, not nanoseconds — different tools, same principles.

---

## Distributed Lock Implementations

### Redlock (Redis)

Quorum-based lock across N independent Redis instances. Acquire on N/2+1 nodes with TTL.

```
ACQUIRE:
  for each of N Redis nodes:
    SET lock_key value NX PX ttl_ms
  if majority succeeded within drift window → locked
  else release all → retry

RELEASE:
  for each of N Redis nodes:
    DEL lock_key (only if value matches — use Lua script)
```

**Gotchas:** clock drift between nodes; network partition can split the quorum; TTL must include processing time + drift margin. Martin Kleppmann's critique: Redlock is unsafe if you need true mutual exclusion. Use fencing tokens.

### pg_advisory_lock (PostgreSQL)

Application-level lock tied to a database session:

```sql
-- Session-level (held until session ends)
SELECT pg_advisory_lock(12345);
-- ... critical section ...
SELECT pg_advisory_unlock(12345);

-- Transaction-level (released on COMMIT/ROLLBACK)
SELECT pg_advisory_xact_lock(12345);

-- Non-blocking (try-lock)
SELECT pg_try_advisory_lock(12345);  -- returns true/false
```

**Deadlock prevention:** always acquire advisory locks in consistent numeric order. PostgreSQL will detect some deadlocks and kill one transaction, but prevention is better.

### etcd Lease-Based Coordination

```
1. Create lease with TTL
2. Acquire lock (PUT with lease)
3. Renew lease periodically (KeepAlive)
4. On loss: leadership gone, backoff, retry
5. Watch epoch to detect leadership change
```

etcd provides **linearizable** reads and writes. The lease mechanism prevents split-brain: if the leader's lease expires, its lock is automatically released.

### Comparison Matrix

| System | Consistency | TTL | Deadlock Detection | Performance |
|--------|------------|-----|-------------------|-------------|
| Redlock | Best-effort quorum | Built-in | No | Fast (~2ms) |
| pg_advisory | Serializable (within DB) | Session/txn | Yes (DB deadlock detector) | Medium (~5ms) |
| etcd lease | Linearizable | Built-in | No | Medium (~10ms) |
| ZooKeeper | Linearizable | Ephemeral nodes | Yes (ordered lock) | Slow (~50ms) |
| Consul session | Linearizable | TTL | No | Medium |

---

## Consensus and Leader Election

### Raft

The practical consensus algorithm. Three roles: Leader, Follower, Candidate.

**The split-brain prevention:** only one leader per term; term numbers monotonically increase; a leader with a stale term is rejected.

**Concurrency relevance:** if your distributed system needs "exactly one writer" (single-writer pattern scaled across machines), Raft gives you that guarantee.

### Fencing Tokens

Even with a distributed lock, the holder can be paused (GC, swap, network) and resume after the lock expired. Another process acquires the lock. Now two think they're the leader.

**Fix:** fencing tokens. The lock server issues a monotonically increasing token with each lock acquisition. The shared resource (DB, file, etc.) rejects writes with a stale token.

```
Lock server: token=34 → Client A
Lock server: token=35 → Client B (A's lease expired)
Client A (stale): writes with token=34 → REJECTED (current token is 35)
```

This is the only way to make distributed locks truly safe. Redlock without fencing is an optimization hint, not a mutex.

---

## CRDTs (Conflict-Free Replicated Data Types)

Data structures that merge without coordination. Each replica applies operations locally and syncs asynchronously. Merge is commutative + associative + idempotent → converges without locks.

### Common CRDTs

| Type | Operations | Merge | Use Case |
|------|-----------|-------|----------|
| G-Counter | increment(replica_id) | max per replica | Page views, likes |
| PN-Counter | increment/decrement | max per replica for each | Inventory |
| G-Set | add(element) | union | Tags, membership |
| OR-Set | add/remove | add-wins, union | Shopping cart |
| LWW-Register | set(value, timestamp) | highest timestamp wins | Last-write-wins fields |
| RGA | insert_after(id, char) | merge by position ID | Collaborative text (Google Docs) |

### When to Use CRDTs Instead of Locks

- **Multi-region replication** where latency makes locking impractical
- **Offline-first apps** that sync when reconnected
- **Counters / sets / text** that are written concurrently from many places
- **NOT for:** invariant enforcement (e.g., "balance >= 0") — CRDTs are eventually consistent

---

## Optimistic vs Pessimistic Concurrency

### Optimistic (version check)

```sql
-- Read with version
SELECT id, name, balance, version FROM accounts WHERE id = 1;
-- version = 5

-- Write with version check
UPDATE accounts SET balance = 900, version = 6
WHERE id = 1 AND version = 5;
-- 0 rows updated? → conflict! Retry.
```

HTTP equivalent: `ETag` / `If-Match`:
```
GET /api/user/1 → ETag: "v5"
PUT /api/user/1 with If-Match: "v5" → 200 OK (or 412 Precondition Failed)
```

### Pessimistic (SELECT FOR UPDATE)

```sql
BEGIN;
SELECT * FROM accounts WHERE id = 1 FOR UPDATE;  -- row locked
-- ... compute ...
UPDATE accounts SET balance = 900 WHERE id = 1;
COMMIT;  -- lock released
```

### SKIP LOCKED (Queue Pattern)

```sql
-- Worker picks next unlocked job
BEGIN;
SELECT * FROM jobs WHERE status = 'pending'
  ORDER BY created_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1;
UPDATE jobs SET status = 'processing' WHERE id = $1;
COMMIT;
```

### When to Choose Which

| Scenario | Use |
|----------|-----|
| Low contention, many readers | Optimistic (version check) |
| High contention, critical invariants | Pessimistic (SELECT FOR UPDATE) |
| Job queue, work claiming | SKIP LOCKED |
| Multi-region, offline-capable | CRDT |
| Need exactly-once coordination | Distributed lock + fencing token |

---

## Idempotency Keys

The most practical distributed concurrency pattern. Client assigns a UUID; server deduplicates.

```
POST /api/orders
Idempotency-Key: 550e8400-e29b-41d4-a716-446655440000

Server:
  1. Check idempotency store for key
  2. If found → return cached result
  3. If not → process, store result + key, return result
```

Implementation:

```sql
-- Idempotency store
CREATE TABLE idempotency_keys (
    key TEXT PRIMARY KEY,
    result JSONB NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ DEFAULT NOW() + INTERVAL '24 hours'
);

-- In transaction:
INSERT INTO idempotency_keys (key, result)
VALUES ($1, $2)
ON CONFLICT (key) DO NOTHING
RETURNING *;
-- If insert succeeded → new request, process it
-- If conflict → duplicate, return cached result
```

---

## Saga and Outbox Patterns

### Saga: Distributed Transaction via Compensation

When a transaction spans multiple services (DB + payment + shipping), you can't use a single DB transaction.

**Orchestration saga:**
```
Coordinator:
  1. Reserve inventory → success
  2. Charge payment → success
  3. Ship order → FAILURE
  4. Compensate: Refund payment
  5. Compensate: Release inventory
```

Each step has a compensating action. Steps are idempotent. Compensation is also idempotent.

### Outbox: Atomic DB + Message Bus

The dual-write problem: writing to DB AND publishing to a message queue is not atomic.

**Fix:** write both to the DB in one transaction, then poll the outbox:

```sql
BEGIN;
INSERT INTO orders (id, ...) VALUES (...);
INSERT INTO outbox (event_type, payload) VALUES ('order_created', '...');
COMMIT;
```

Background worker:
```python
while True:
    events = db.query("SELECT * FROM outbox WHERE sent = false LIMIT 100")
    for event in events:
        broker.publish(event.event_type, event.payload)
        db.execute("UPDATE outbox SET sent = true WHERE id = $1", event.id)
```

Consumer must be idempotent (the same event may be delivered more than once).

---

## Database Isolation Levels

| Level | Dirty Read | Non-Repeatable Read | Phantom | Performance |
|-------|-----------|-------------------|---------|-------------|
| READ UNCOMMITTED | Yes | Yes | Yes | Fastest |
| READ COMMITTED | No | Yes | Yes | Fast (Postgres default) |
| REPEATABLE READ | No | No | Yes | Medium (MySQL default) |
| SERIALIZABLE | No | No | No | Slowest |

### PostgreSQL SERIALIZABLE (SSI)

Postgres uses Serializable Snapshot Isolation. It doesn't lock — it tracks dependencies and aborts conflicting transactions:

```sql
SET TRANSACTION ISOLATION LEVEL SERIALIZABLE;
-- Your reads and writes here
-- If a conflict is detected → ERROR: could not serialize access
-- Retry the transaction
```

**Retry on serialization failure:**

```python
for attempt in range(5):
    try:
        with db.begin() as tx:
            tx.execute("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")
            # ... work ...
        break
    except SerializationFailure:
        time.sleep(0.01 * 2**attempt)
```

### MVCC and Transaction Wraparound

PostgreSQL uses MVCC (Multi-Version Concurrency Control). Old row versions accumulate. VACUUM reclaims them. If VACUUM falls behind, transaction ID wraparound becomes a risk (the "wraparound freeze" emergency).

**Monitoring:** `SELECT age(datfrozenxid) FROM pg_database;` — if age > 1 billion, you need VACUUM urgently.

---

## Resilience Patterns (Distributed)

### Circuit Breaker

```
CLOSED → [failures exceed threshold] → OPEN
OPEN → [timeout elapsed] → HALF_OPEN
HALF_OPEN → [probe succeeds] → CLOSED
HALF_OPEN → [probe fails] → OPEN
```

### Bulkhead

Isolate failure domains. Each external dependency gets its own connection pool / thread pool / semaphore. One failing dependency can't exhaust the shared pool.

### Backpressure + Watermarks

When a queue grows past high watermark → stop accepting. When it drains to low watermark → resume. Prevents unbounded memory growth.

### Thundering Herd / Cache Stampede

1000 requests hit an expired cache key simultaneously. All miss cache. All hit DB.

**Fixes:**
- **Singleflight / request coalescing** — one leader fetches, others wait for result
- **Probabilistic early expiry** — each request has a small chance of refreshing before actual expiry, spreading the load
- **Mutex on cache miss** — only one request fetches; others wait for the mutex

### Retry with Jitter

```python
delay = min(base * 2**attempt, max_delay)
jitter = random.uniform(0, delay)
sleep(delay + jitter)
```

Always jitter. Without it, all clients retry at the same instant → synchronized retry storm.

---

## See Also

- [DATABASE.md](DATABASE.md) — SQLite-specific concurrency
- [SWARM.md](SWARM.md) — multi-process / agent concurrency
- [RESILIENCE-PATTERNS.md](RESILIENCE-PATTERNS.md) — detailed circuit breaker, bulkhead, retry implementations
- [LOCK-FREE.md](LOCK-FREE.md) — CAS, ABA, hazard pointers (in-process counterpart)
- [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) — actor, structured concurrency, single-writer

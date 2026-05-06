# Database Advanced Concurrency

Beyond SQLite. Postgres isolation levels, advisory locks, SKIP LOCKED, MVCC, deadlock detection, ORM transaction modes, connection pool management, and Redis distributed patterns.

Sourced from miner results: 611 hits for `SELECT FOR UPDATE SKIP LOCKED`, 11857 for `Supabase connection pooler`, 21522 for `ON CONFLICT DO UPDATE`, 13963 for `lock_timeout`.

---

## PostgreSQL Concurrency Patterns

### Advisory Locks

Application-level locks not tied to rows or tables:

```sql
-- Session-level (held until session ends or explicit unlock)
SELECT pg_advisory_lock(12345);
-- ... critical section ...
SELECT pg_advisory_unlock(12345);

-- Transaction-level (auto-released on COMMIT/ROLLBACK)
SELECT pg_advisory_xact_lock(12345);

-- Non-blocking (returns true/false immediately)
SELECT pg_try_advisory_lock(12345);
```

**Deadlock prevention:** Always acquire advisory locks in consistent numeric order. PostgreSQL detects and kills one transaction on deadlock, but prevention is better.

**Use case from sessions:** Webhook deduplication — lock on hash of event ID before processing:

```sql
BEGIN;
SELECT pg_advisory_xact_lock(hashtext('webhook:' || $1));
-- Check if already processed
SELECT * FROM webhook_events WHERE event_id = $1 AND processed_at IS NOT NULL;
-- If not: process, then mark done
UPDATE webhook_events SET processed_at = NOW() WHERE event_id = $1;
COMMIT;
```

### SKIP LOCKED (Job Queue Pattern)

Multiple workers claim jobs without blocking each other:

```sql
BEGIN;
SELECT * FROM jobs
  WHERE status = 'pending'
  ORDER BY created_at
  FOR UPDATE SKIP LOCKED  -- skip rows locked by other workers
  LIMIT 1;
UPDATE jobs SET status = 'processing', worker_id = $1 WHERE id = $2;
COMMIT;
```

Workers that can't acquire a lock silently skip to the next row. Zero contention.

### Isolation Levels

| Level | Dirty Read | Non-Repeatable | Phantom | Postgres Default |
|-------|-----------|---------------|---------|-----------------|
| READ COMMITTED | No | Yes | Yes | Yes (default) |
| REPEATABLE READ | No | No | Yes | — |
| SERIALIZABLE | No | No | No | — (SSI) |

**PostgreSQL SERIALIZABLE** uses Serializable Snapshot Isolation (SSI) — it doesn't lock rows; it tracks read-write dependencies and aborts conflicting transactions:

```sql
SET TRANSACTION ISOLATION LEVEL SERIALIZABLE;
-- Your reads and writes
-- If conflict detected → ERROR: could not serialize access
-- Retry the transaction with exponential backoff
```

**Retry pattern:**

```typescript
async function withSerializable<T>(fn: (tx: Transaction) => Promise<T>): Promise<T> {
    for (let attempt = 0; attempt < 5; attempt++) {
        try {
            return await db.transaction(async (tx) => {
                await tx.execute(sql`SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`);
                return await fn(tx);
            });
        } catch (e: any) {
            if (e.code === '40001' && attempt < 4) {  // serialization_failure
                await sleep(10 * 2 ** attempt + Math.random() * 10);
                continue;
            }
            throw e;
        }
    }
    throw new Error('serialization failed after 5 attempts');
}
```

### MVCC and Transaction Wraparound

PostgreSQL uses Multi-Version Concurrency Control. Old row versions accumulate until VACUUM reclaims them. If VACUUM falls behind, transaction ID wraparound becomes an emergency:

```sql
-- Monitor transaction age (critical if > 1 billion)
SELECT datname, age(datfrozenxid) FROM pg_database ORDER BY age DESC;

-- Find idle-in-transaction connections (block VACUUM)
SELECT pid, state, query_start, age(xmin)
FROM pg_stat_activity
WHERE state = 'idle in transaction'
  AND query_start < NOW() - INTERVAL '10 minutes'
ORDER BY age(xmin) DESC;

-- Kill problematic sessions
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE state = 'idle in transaction'
  AND query_start < NOW() - INTERVAL '5 minutes';
```

### Timeouts (Essential Configuration)

```sql
-- Statement timeout: kill queries that run too long
SET statement_timeout = '5s';  -- per session
-- Or in connection string: ?statement_timeout=5000

-- Lock timeout: don't wait forever for row locks
SET lock_timeout = '2s';

-- Idle-in-transaction timeout (Postgres 10+)
SET idle_in_transaction_session_timeout = '30s';
```

**Always set these.** A query without a statement timeout can hold locks indefinitely.

### SELECT FOR UPDATE vs FOR SHARE

```sql
-- Exclusive lock (prevents other writers AND other FOR UPDATE)
SELECT * FROM accounts WHERE id = 1 FOR UPDATE;

-- Shared lock (allows other FOR SHARE, blocks FOR UPDATE)
SELECT * FROM accounts WHERE id = 1 FOR SHARE;

-- NOWAIT: fail immediately instead of waiting
SELECT * FROM accounts WHERE id = 1 FOR UPDATE NOWAIT;
-- ERROR: could not obtain lock on row (instead of blocking)
```

### Deadlock Detection

PostgreSQL automatically detects deadlocks and kills one transaction:

```
ERROR:  deadlock detected
DETAIL:  Process 12345 waits for ShareLock on transaction 67890;
         blocked by process 67891.
         Process 67891 waits for ShareLock on transaction 67892;
         blocked by process 12345.
```

**Prevention:** Acquire row locks in consistent order (e.g., by primary key ASC). Retry on deadlock with backoff.

```typescript
async function retryOnDeadlock<T>(fn: () => Promise<T>): Promise<T> {
    for (let i = 0; i < 5; i++) {
        try { return await fn(); }
        catch (e: any) {
            if (e.code === '40P01' && i < 4) {  // deadlock_detected
                await sleep(10 * 2 ** i);
                continue;
            }
            throw e;
        }
    }
    throw new Error('deadlock after 5 retries');
}
```

---

## Optimistic Concurrency (Versioned Rows)

No locks at read time; conflict detected at write time:

```sql
-- Read
SELECT id, name, balance, version FROM accounts WHERE id = 1;
-- version = 5

-- Write with version check
UPDATE accounts SET balance = 150, version = 6
WHERE id = 1 AND version = 5;
-- 0 rows updated → conflict (someone else updated since our read)
```

**HTTP equivalent:** `ETag` / `If-Match`:

```
GET /api/account/1 → ETag: "v5"
PUT /api/account/1 + If-Match: "v5" → 200 OK or 412 Precondition Failed
```

**When to use:** Low contention, many readers, acceptable retry overhead.

---

## Batch Upsert (ON CONFLICT)

Atomic insert-or-update without race window:

```sql
INSERT INTO user_accounts (user_id, account_id, balance)
VALUES (1, 'acct_123', 100), (2, 'acct_456', 200)
ON CONFLICT (user_id, account_id)
DO UPDATE SET balance = EXCLUDED.balance + user_accounts.balance;
```

Single atomic operation — no read-modify-write race.

---

## Connection Pool Management

### Pool Configuration

```typescript
// node-postgres
const pool = new Pool({
    max: 20,                        // max connections
    idleTimeoutMillis: 30000,      // reclaim idle connections
    connectionTimeoutMillis: 5000, // fail fast on acquire timeout
});

// ALWAYS release connections
const client = await pool.connect();
try {
    await client.query('BEGIN');
    await client.query('UPDATE ...', [args]);
    await client.query('COMMIT');
} catch (e) {
    await client.query('ROLLBACK');
    throw e;
} finally {
    client.release();  // CRITICAL
}
```

### pgbouncer Modes

| Mode | Behavior | Use When |
|------|----------|----------|
| **Session** | Client holds connection for entire session | Long-lived clients, prepared statements |
| **Transaction** | Connection returned after each transaction | Serverless, short-lived connections |
| **Statement** | Connection returned after each statement | Simple queries only |

**Supabase pooler** uses transaction mode by default. Session-level settings (`SET`) don't persist across transactions.

### Pool Exhaustion Detection

```sql
-- Current connections
SELECT count(*) FROM pg_stat_activity WHERE datname = 'mydb';

-- Connections by state
SELECT state, count(*)
FROM pg_stat_activity
WHERE datname = 'mydb'
GROUP BY state;

-- Waiting connections (blocked by locks)
SELECT * FROM pg_stat_activity WHERE wait_event_type = 'Lock';
```

---

## ORM Transaction Patterns

### Prisma

```typescript
// Interactive (holds connection for duration — use sparingly)
await prisma.$transaction(async (tx) => {
    const user = await tx.user.findUnique({ where: { id } });
    await tx.user.update({ where: { id }, data: { balance: user.balance - 100 } });
});

// Batch (single round-trip — preferred for simple operations)
await prisma.$transaction([
    prisma.user.update({ where: { id: 1 }, data: { balance: { decrement: 100 } } }),
    prisma.user.update({ where: { id: 2 }, data: { balance: { increment: 100 } } }),
]);
```

**Gotcha:** Interactive transactions hold a connection. In a loop with high concurrency → pool exhaustion.

### Drizzle

```typescript
await db.transaction(async (tx) => {
    await tx.update(users).set({ balance: sql`balance - 100` }).where(eq(users.id, 1));
    await tx.update(users).set({ balance: sql`balance + 100` }).where(eq(users.id, 2));
});
```

### SQLAlchemy (Python)

```python
# Session is NOT thread-safe — use scoped_session
from sqlalchemy.orm import scoped_session, sessionmaker
Session = scoped_session(sessionmaker(bind=engine))

# Each thread gets its own session
with Session() as session:
    user = session.query(User).filter_by(id=1).with_for_update().one()
    user.balance -= 100
    session.commit()
```

---

## Redis Distributed Patterns

### SETNX (Simple Distributed Lock)

```
SET lock_key unique_value NX PX 30000
# NX = only if not exists; PX = millisecond TTL

# Release (Lua script for atomicity):
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
else
    return 0
end
```

### Redlock (Quorum Lock)

Acquire on N/2+1 of N independent Redis nodes within drift window. See [DISTRIBUTED.md](DISTRIBUTED.md) for full protocol.

**Critical:** Use fencing tokens for true safety. Redlock without fencing is a performance hint, not a mutex.

### Redis Pipeline (Batch Atomicity)

```typescript
const pipeline = redis.pipeline();
pipeline.incr('counter:page_views');
pipeline.incr('counter:unique_visitors');
pipeline.expire('counter:page_views', 3600);
const results = await pipeline.exec();
```

Pipeline is atomic at the Redis level — commands execute without interleaving from other clients.

### Lua Scripts (Server-Side Atomicity)

```lua
-- Rate limiter (token bucket)
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])

local tokens = tonumber(redis.call("get", key) or capacity)
if tokens > 0 then
    redis.call("decr", key)
    redis.call("expire", key, math.ceil(capacity / rate))
    return 1  -- allowed
else
    return 0  -- rate limited
end
```

Lua scripts execute atomically on the Redis server — no interleaving possible.

---

## Cursor-Based Pagination Under Concurrency

```sql
-- OFFSET pagination breaks under concurrent deletes (skips/duplicates rows)
-- Use cursor-based instead:
SELECT * FROM items
WHERE id > $1  -- cursor from previous page's last row
ORDER BY id ASC
LIMIT 100;
```

Cursor pagination is stable under concurrent inserts and deletes.

---

## Idempotency Keys

```sql
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
-- Insert succeeded → new request, process it
-- Conflict → duplicate, return cached result
```

---

## See Also

- [DATABASE.md](DATABASE.md) — SQLite-specific concurrency (WAL, PRAGMAs, single-writer)
- [DISTRIBUTED.md](DISTRIBUTED.md) — Redlock, consensus, CRDTs, saga/outbox
- [TYPESCRIPT.md](TYPESCRIPT.md) — Prisma, Drizzle, node-postgres patterns
- [PYTHON.md](PYTHON.md) — SQLAlchemy, psycopg, Django ORM
- [GO.md](GO.md) — database/sql, pgx, connection pools

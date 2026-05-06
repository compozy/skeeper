# TypeScript / Node.js / Bun / Deno Concurrency Cookbook

<!-- TOC: Event Loop | Promises | worker_threads | Patterns | Database | Framework Gotchas | React | Anti-Patterns | Code Recipes -->

## Table of Contents

- [The Event Loop Model](#the-event-loop-model)
- [Promise Concurrency](#promise-concurrency)
- [worker_threads (True Parallelism)](#worker_threads)
- [AbortController / AbortSignal](#abortcontroller)
- [Stream Backpressure](#stream-backpressure)
- [Database Concurrency](#database-concurrency)
- [Framework-Specific Gotchas](#framework-specific-gotchas)
- [React Concurrency Hazards](#react-concurrency-hazards)
- [Anti-Patterns](#anti-patterns)
- [Code Recipe Library (20+)](#code-recipe-library-20)
- [Diagnosis Tools](#diagnosis-tools)
- [Audit Commands](#audit-commands)

---

## The Event Loop Model

Node.js has **one thread for JavaScript**. All I/O is non-blocking via libuv. "Concurrency" means interleaving — not parallelism. If you block the thread, **everything** stops.

**Phase order per event loop tick:**

```
┌─────────────────────────┐
│       timers             │ ← setTimeout, setInterval
├─────────────────────────┤
│     pending callbacks    │ ← I/O completions
├─────────────────────────┤
│     idle, prepare        │ ← internal
├─────────────────────────┤
│         poll             │ ← network I/O, file I/O
├─────────────────────────┤
│         check            │ ← setImmediate
├─────────────────────────┤
│    close callbacks       │ ← socket.destroy()
└─────────────────────────┘
   ↑ Between EVERY phase:
   │ Drain microtask queue (Promise.then, queueMicrotask)
```

**Microtasks** (Promise callbacks) run between every phase — they have higher priority than macrotasks. A tight microtask loop can starve timers and I/O:

```typescript
// BUG: infinite microtask loop starves everything
function loop() { Promise.resolve().then(loop); }
loop();  // event loop never progresses past microtask queue
```

---

## Promise Concurrency

### Promise.all vs Promise.allSettled vs Promise.race

```typescript
// Promise.all: ALL must succeed; first rejection fails everything
const results = await Promise.all([fetchA(), fetchB(), fetchC()]);

// Promise.allSettled: ALWAYS resolves; results include failures
const results = await Promise.allSettled([fetchA(), fetchB(), fetchC()]);
for (const r of results) {
    if (r.status === 'fulfilled') use(r.value);
    else log(r.reason);
}

// Promise.race: first to settle wins (success OR failure)
const fastest = await Promise.race([fetchA(), fetchB()]);

// Promise.any: first SUCCESS wins; all-rejection gives AggregateError
const first = await Promise.any([fetchA(), fetchB()]);
```

### Sequential await in Loop (N+1 Problem)

```typescript
// BUG: O(n) sequential — each await blocks the next
for (const id of ids) {
    const data = await fetchUser(id);  // 100 users = 100 serial round-trips
    results.push(data);
}

// FIX: parallel
const results = await Promise.all(ids.map(id => fetchUser(id)));

// FIX: bounded parallel with p-limit
import pLimit from 'p-limit';
const limit = pLimit(10);
const results = await Promise.all(ids.map(id => limit(() => fetchUser(id))));
```

### Unhandled Promise Rejections (Node 15+: Fatal)

```typescript
// BUG: unhandled rejection crashes the process
async function riskyWork() { throw new Error("boom"); }
riskyWork();  // no await, no .catch → unhandled rejection

// FIX: always handle
await riskyWork();  // or
riskyWork().catch(err => logger.error(err));

// SAFETY NET:
process.on('unhandledRejection', (reason, promise) => {
    logger.error({ reason, promise }, 'Unhandled rejection');
    process.exit(1);  // explicit crash-and-restart
});
```

### Fire-and-Forget: Use `void` Explicitly

```typescript
// BAD: silent fire-and-forget
updateAnalytics(data);  // missing await — ESLint: @typescript-eslint/no-floating-promises

// GOOD: explicit fire-and-forget with error handling
void updateAnalytics(data).catch(err => logger.warn(err));
```

---

## worker_threads

For **CPU-bound** work. Each worker has its own V8 isolate, its own event loop, its own memory. Communication via `postMessage` (structured clone) or `SharedArrayBuffer` (shared memory).

```typescript
// main.ts
import { Worker } from 'worker_threads';

const worker = new Worker('./cpu-worker.ts');
worker.postMessage({ data: heavyPayload });
worker.on('message', (result) => respond(result));
worker.on('error', (err) => logger.error(err));

// cpu-worker.ts
import { parentPort } from 'worker_threads';
parentPort?.on('message', (data) => {
    const result = expensiveComputation(data);
    parentPort?.postMessage(result);
});
```

### Worker Pool Pattern

```typescript
class WorkerPool {
    private workers: Worker[] = [];
    private idle: Worker[] = [];
    private queue: Array<{ data: any; resolve: Function; reject: Function }> = [];

    constructor(size: number, workerPath: string) {
        for (let i = 0; i < size; i++) {
            const w = new Worker(workerPath);
            this.workers.push(w);
            this.idle.push(w);
        }
    }

    async run<T>(data: unknown): Promise<T> {
        return new Promise((resolve, reject) => {
            const worker = this.idle.pop();
            if (worker) {
                this.dispatch(worker, data, resolve, reject);
            } else {
                this.queue.push({ data, resolve, reject });
            }
        });
    }

    private dispatch(worker: Worker, data: unknown, resolve: Function, reject: Function) {
        const onMessage = (result: any) => {
            worker.off('message', onMessage);
            worker.off('error', onError);
            this.idle.push(worker);
            this.processQueue();
            resolve(result);
        };
        const onError = (err: Error) => {
            worker.off('message', onMessage);
            worker.off('error', onError);
            this.idle.push(worker);
            this.processQueue();
            reject(err);
        };
        worker.on('message', onMessage);
        worker.on('error', onError);
        worker.postMessage(data);
    }

    private processQueue() {
        if (this.queue.length === 0 || this.idle.length === 0) return;
        const { data, resolve, reject } = this.queue.shift()!;
        this.dispatch(this.idle.pop()!, data, resolve, reject);
    }
}
```

### SharedArrayBuffer + Atomics

For tight synchronization without message passing overhead:

```typescript
const shared = new SharedArrayBuffer(4);
const view = new Int32Array(shared);

// Worker A: increment atomically
Atomics.add(view, 0, 1);

// Worker B: wait for value to change
Atomics.wait(view, 0, 0);  // blocks until view[0] != 0
const val = Atomics.load(view, 0);

// Worker A: wake B
Atomics.notify(view, 0, 1);
```

**Deadlock hazard:** `Atomics.wait` blocks the thread. If the main thread calls it, the event loop freezes. Only use in worker threads.

---

## AbortController

The standard cancellation mechanism for fetch, streams, and custom async operations:

```typescript
// Fetch with timeout
async function fetchWithTimeout(url: string, ms: number = 5000) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), ms);
    try {
        const res = await fetch(url, { signal: controller.signal });
        return await res.json();
    } finally {
        clearTimeout(timeout);
    }
}

// Composing signals (Node 20+)
const parent = new AbortController();
const child = AbortSignal.any([parent.signal, AbortSignal.timeout(5000)]);
const res = await fetch(url, { signal: child });
```

---

## Stream Backpressure

```typescript
// BUG: ignoring backpressure — memory grows unbounded
readable.on('data', (chunk) => {
    writable.write(chunk);  // may buffer if writable is slow
});

// FIX: check return value + drain
readable.on('data', (chunk) => {
    if (!writable.write(chunk)) {
        readable.pause();
    }
});
writable.on('drain', () => readable.resume());

// BEST: use pipeline (handles backpressure automatically)
import { pipeline } from 'stream/promises';
await pipeline(readable, transform, writable);
```

---

## Database Concurrency

### better-sqlite3 (Synchronous!)

```typescript
// better-sqlite3 is SYNC — blocks the event loop on every call
// MUST use in a worker thread for server workloads

// In worker:
import Database from 'better-sqlite3';
const db = new Database('app.db');
db.pragma('journal_mode = WAL');
db.pragma('busy_timeout = 5000');

// In main: use worker pool
const result = await dbWorkerPool.run({ query: 'SELECT ...', params: [] });
```

### Prisma $transaction

```typescript
// Interactive transaction (holds connection + locks)
await prisma.$transaction(async (tx) => {
    const user = await tx.user.findUnique({ where: { id } });
    await tx.user.update({ where: { id }, data: { balance: user.balance - 100 } });
});
// Connection released after transaction

// Batch transaction (single round-trip, no lock holding)
await prisma.$transaction([
    prisma.user.update({ where: { id: 1 }, data: { balance: { decrement: 100 } } }),
    prisma.user.update({ where: { id: 2 }, data: { balance: { increment: 100 } } }),
]);
```

### Drizzle with Transactions

```typescript
await db.transaction(async (tx) => {
    await tx.update(users).set({ balance: sql`balance - 100` }).where(eq(users.id, 1));
    await tx.update(users).set({ balance: sql`balance + 100` }).where(eq(users.id, 2));
});
```

### node-postgres Pool

```typescript
import { Pool } from 'pg';

const pool = new Pool({
    max: 20,
    idleTimeoutMillis: 30000,
    connectionTimeoutMillis: 5000,
});

// ALWAYS return connections to the pool
const client = await pool.connect();
try {
    await client.query('BEGIN');
    await client.query('UPDATE ...', [args]);
    await client.query('COMMIT');
} catch (e) {
    await client.query('ROLLBACK');
    throw e;
} finally {
    client.release();  // CRITICAL: return to pool
}

// Simpler: use pool.query (auto-release)
await pool.query('SELECT * FROM users WHERE id = $1', [id]);
```

---

## Framework-Specific Gotchas

### Next.js

**Server Actions race:** Double-submit on network retry.

```typescript
// FIX: idempotency key
'use server';
export async function createOrder(formData: FormData) {
    const key = formData.get('_idempotencyKey') as string;
    const existing = await db.query.orders.findFirst({
        where: eq(orders.idempotencyKey, key)
    });
    if (existing) return existing;  // dedup
    
    return await db.insert(orders).values({ ...data, idempotencyKey: key }).returning();
}
```

**`revalidatePath()` doesn't block:** State may be stale on next render.

### Express

```typescript
// BUG: async error not caught by error middleware
app.get('/api/data', async (req, res) => {
    const data = await fetchData();  // if this throws → unhandled rejection
    res.json(data);
});

// FIX: wrap async handlers
const asyncHandler = (fn: Function) => (req: any, res: any, next: any) =>
    Promise.resolve(fn(req, res, next)).catch(next);

app.get('/api/data', asyncHandler(async (req, res) => {
    const data = await fetchData();
    res.json(data);
}));
```

### NestJS

Middleware chains handle async properly, but **guards/interceptors must return promises**. Database transactions don't auto-nest.

### BullMQ

```typescript
// Idempotent job handler
const worker = new Worker('queue', async (job) => {
    const processed = await redis.get(`done:${job.data.idempotencyKey}`);
    if (processed) return JSON.parse(processed);
    
    const result = await processJob(job.data);
    await redis.set(`done:${job.data.idempotencyKey}`, JSON.stringify(result), 'EX', 86400);
    return result;
}, { concurrency: 5 });
```

---

## React Concurrency Hazards

### useEffect Stale Closure + Race

```typescript
// BUG: older request overwrites newer; stale userId captured
function UserProfile({ userId }: { userId: string }) {
    const [user, setUser] = useState<User | null>(null);
    
    useEffect(() => {
        fetch(`/api/users/${userId}`).then(r => r.json()).then(setUser);
    }, []);  // BUG: missing userId dependency
    
    // FIX: add dependency + AbortController
    useEffect(() => {
        const controller = new AbortController();
        fetch(`/api/users/${userId}`, { signal: controller.signal })
            .then(r => r.json())
            .then(setUser)
            .catch(err => {
                if (err.name !== 'AbortError') logger.error(err);
            });
        return () => controller.abort();
    }, [userId]);
}
```

### setState Race Conditions

```typescript
// BUG: two async updates, last-write-wins
const handleClick = async () => {
    const a = await fetchA();  // 200ms
    setData(a);
    const b = await fetchB();  // 100ms — might arrive first from another click
    setData(b);  // overwrites a even if a was the "correct" latest
};

// FIX: use ref for latest request
const requestRef = useRef(0);
const handleClick = async () => {
    const id = ++requestRef.current;
    const result = await fetchData();
    if (requestRef.current === id) {
        setData(result);  // only apply if this is still the latest request
    }
};
```

### Strict Mode Double-Render

React 18+ dev mode runs effects twice. Your code must be idempotent:

```typescript
// BUG: side effect runs twice in dev
useEffect(() => {
    incrementCounter();  // counter goes up by 2 in dev mode
}, []);

// FIX: make idempotent or use cleanup
useEffect(() => {
    const controller = new AbortController();
    fetchData({ signal: controller.signal });
    return () => controller.abort();
}, []);
```

---

## Anti-Patterns

- **Missing `await`** — `@typescript-eslint/no-floating-promises` catches this
- **Sequential `await` in loop** when parallel is possible
- **`Promise.all` without error handling** — one failure kills all
- **Blocking event loop** with sync crypto, JSON.parse of huge payloads, tight loops
- **`setTimeout` / `setInterval` without cleanup** — memory leaks
- **`time.After` pattern** (creating new timers each loop iteration)
- **Sharing `pg.Client` across requests** — use a pool
- **Not calling `client.release()`** on `pg.Pool.connect()` — pool exhaustion
- **`better-sqlite3` on main thread** — blocks event loop
- **Fire-and-forget promises without `void` or `.catch`** — unhandled rejection crash
- **`useEffect` without dependency array** — runs every render
- **`useEffect` cleanup missing `AbortController`** — race conditions, memory leaks
- **Interactive Prisma `$transaction` in a loop** — holds connections, pool exhaustion
- **`SharedArrayBuffer` + `Atomics.wait` on main thread** — blocks event loop

---

## Code Recipe Library (20+)

### 1. Bounded parallel with p-limit

```typescript
import pLimit from 'p-limit';
const limit = pLimit(10);
const results = await Promise.all(urls.map(url => limit(() => fetch(url))));
```

### 2. Fetch with timeout

```typescript
const controller = new AbortController();
const timeout = setTimeout(() => controller.abort(), 5000);
try { return await fetch(url, { signal: controller.signal }); }
finally { clearTimeout(timeout); }
```

### 3. Retry with exponential backoff

```typescript
async function retry<T>(fn: () => Promise<T>, attempts = 5, baseMs = 100): Promise<T> {
    for (let i = 0; i < attempts; i++) {
        try { return await fn(); }
        catch (e) {
            if (i === attempts - 1) throw e;
            await new Promise(r => setTimeout(r, baseMs * 2 ** i + Math.random() * baseMs));
        }
    }
    throw new Error('unreachable');
}
```

### 4. Worker pool (see worker_threads section)

### 5. AbortSignal.any for composing cancellation

```typescript
const signal = AbortSignal.any([parentSignal, AbortSignal.timeout(5000)]);
```

### 6. Stream pipeline

```typescript
import { pipeline } from 'stream/promises';
await pipeline(readable, transform, writable);
```

### 7. Pool query with auto-release

```typescript
const { rows } = await pool.query('SELECT * FROM users WHERE id = $1', [id]);
```

### 8. Idempotent server action

```typescript
const key = formData.get('_key');
if (await db.query.ops.findFirst({ where: eq(ops.key, key) })) return;
```

### 9. useEffect with abort

```typescript
useEffect(() => {
    const c = new AbortController();
    fetch(url, { signal: c.signal }).then(r => r.json()).then(setData);
    return () => c.abort();
}, [url]);
```

### 10. Async Express error wrapper

```typescript
const wrap = (fn: Function) => (req: any, res: any, next: any) =>
    Promise.resolve(fn(req, res, next)).catch(next);
```

### 11. BullMQ bounded concurrency

```typescript
new Worker('queue', handler, { concurrency: 5, limiter: { max: 100, duration: 60000 } });
```

### 12. Graceful shutdown

```typescript
process.on('SIGTERM', async () => {
    server.close();
    await pool.end();
    process.exit(0);
});
```

### 13. Promise.allSettled for fault-tolerant batch

```typescript
const results = await Promise.allSettled(tasks.map(t => process(t)));
const successes = results.filter(r => r.status === 'fulfilled').map(r => r.value);
const failures = results.filter(r => r.status === 'rejected').map(r => r.reason);
```

### 14. Mutex via promise chain (single-threaded mutex)

```typescript
class AsyncMutex {
    private queue = Promise.resolve();
    async lock<T>(fn: () => Promise<T>): Promise<T> {
        let release!: () => void;
        const next = new Promise<void>(r => (release = r));
        const prev = this.queue;
        this.queue = next;
        await prev;
        try { return await fn(); }
        finally { release(); }
    }
}
```

### 15. Request coalescing (singleflight)

```typescript
const inflight = new Map<string, Promise<any>>();
async function singleflight<T>(key: string, fn: () => Promise<T>): Promise<T> {
    if (inflight.has(key)) return inflight.get(key)!;
    const p = fn().finally(() => inflight.delete(key));
    inflight.set(key, p);
    return p;
}
```

### 16. Debounced async (React)

```typescript
function useDebouncedAsync<T>(fn: (q: string) => Promise<T>, ms: number) {
    const [result, setResult] = useState<T>();
    const timer = useRef<NodeJS.Timeout>();
    const callback = useCallback((q: string) => {
        clearTimeout(timer.current);
        timer.current = setTimeout(async () => {
            setResult(await fn(q));
        }, ms);
    }, [fn, ms]);
    return [result, callback] as const;
}
```

### 17. Event loop lag detection

```typescript
let last = Date.now();
setInterval(() => {
    const now = Date.now();
    const lag = now - last - 1000;
    if (lag > 100) logger.warn(`Event loop lag: ${lag}ms`);
    last = now;
}, 1000);
```

### 18. Structured concurrency via AsyncDisposableStack (TC39 proposal)

```typescript
// Future: using declarations (TypeScript 5.2+)
await using server = createServer();
await using db = connectDB();
// Both disposed in reverse order on scope exit
```

### 19. Rate limiter via token bucket

```typescript
class TokenBucket {
    private tokens: number;
    constructor(private rate: number, private capacity: number) {
        this.tokens = capacity;
        setInterval(() => { this.tokens = Math.min(this.capacity, this.tokens + this.rate); }, 1000);
    }
    async acquire(): Promise<void> {
        while (this.tokens <= 0) await new Promise(r => setTimeout(r, 100));
        this.tokens--;
    }
}
```

### 20. Cluster for multi-process

```typescript
import cluster from 'cluster';
import { cpus } from 'os';

if (cluster.isPrimary) {
    for (let i = 0; i < cpus().length; i++) cluster.fork();
    cluster.on('exit', (worker) => {
        logger.warn(`Worker ${worker.id} died, restarting`);
        cluster.fork();
    });
} else {
    startServer();
}
```

---

## Diagnosis Tools

```bash
# clinic.js: flamegraph, identify event loop bottlenecks
npx clinic flame -- node server.js

# Node inspector
node --inspect server.js
# Then open chrome://inspect

# V8 profiler
node --prof server.js
node --prof-process isolate-*.log > processed.txt

# 0x flamegraph
npx 0x server.js

# Event loop lag monitoring
# Use clinic.js or the lag detection recipe above
```

---

## Audit Commands

```bash
# Find missing await (ESLint)
npx eslint --rule '@typescript-eslint/no-floating-promises: error' src/

# Find sync I/O in async code
rg -n 'readFileSync|writeFileSync|execSync' --type ts src/

# Find unhandled promise rejection risk
rg -n '\.then\(' --type ts src/ | grep -v '\.catch\|await'

# Find setTimeout/setInterval without clear
rg -n 'setInterval|setTimeout' --type ts src/ | grep -v 'clearInterval\|clearTimeout'

# Find better-sqlite3 on main thread (should be in worker)
rg -n "require.*better-sqlite3\|from 'better-sqlite3'" --type ts src/ | grep -v worker

# Find pool.connect without release
rg -n 'pool\.connect' --type ts -A 10 | grep -v 'release\|finally'
```

## See Also

- [SKILL.md](../SKILL.md) — 9-class taxonomy
- [DATABASE.md](DATABASE.md) — SQLite/Postgres concurrency
- [RESILIENCE-PATTERNS.md](RESILIENCE-PATTERNS.md) — circuit breaker, bulkhead, retry
- [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) — worker pools, structured concurrency

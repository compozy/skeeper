# Python Concurrency Cookbook

<!-- TOC: The GIL | asyncio | threading | multiprocessing | concurrent.futures | trio/anyio | Diagnosis | Database | Framework Gotchas | Anti-Patterns | Code Recipes -->

## Table of Contents

- [The GIL: What It Is and Isn't](#the-gil)
- [asyncio in Depth](#asyncio-in-depth)
- [threading](#threading)
- [multiprocessing](#multiprocessing)
- [concurrent.futures](#concurrentfutures)
- [trio and anyio (Structured Concurrency)](#trio-and-anyio)
- [Diagnosis Tools](#diagnosis-tools)
- [Database Concurrency](#database-concurrency)
- [Framework-Specific Gotchas](#framework-specific-gotchas)
- [Anti-Patterns](#anti-patterns)
- [Code Recipe Library (20+)](#code-recipe-library-20)
- [Audit Commands](#audit-commands)

---

## The GIL

The Global Interpreter Lock is Python's most misunderstood feature. It **does** protect Python object refcounts and built-in types. It **does not** protect your application logic.

**What the GIL serializes:** Python bytecode execution. Only one thread runs Python at a time.

**What the GIL does NOT serialize:**
- C extensions that release the GIL (numpy, pandas during computation)
- I/O operations (socket, file, database — GIL released during `read()`/`write()`)
- Lock acquisition/release (the mechanics are GIL-protected but the semantics are your problem)
- `asyncio` — runs on one thread; concurrency is cooperative, not preemptive

**Practical consequence:** In Python, you can have data races on application objects even with the GIL, because:
1. Context switches happen between bytecodes, not between lines
2. `dict[key] = value` is atomic, but `dict[key] += 1` is not (read-modify-write = 3 bytecodes)
3. C extensions may release the GIL and run in parallel

### Free-Threaded Python (3.13+, PEP 703)

Build with `--disable-gil`. Threads run truly in parallel. Consequence: **every data race that was previously hidden by the GIL is now a real bug**. Use `threading.Lock` everywhere you'd use `sync.Mutex` in Go.

---

## asyncio in Depth

### The Event Loop Model

asyncio runs on **one thread**. Concurrency is cooperative: tasks yield at `await` points. There is no preemption, no data race between two coroutines executing between `await`s — but TOCTOU bugs exist across `await` points.

### The Top 8 asyncio Bugs

**1. Task Destroyed But Pending (fire-and-forget)**

```python
# BUG: task is GC'd before completion
asyncio.create_task(background_work())  # no reference held!

# FIX: hold reference
task = asyncio.create_task(background_work())
task.add_done_callback(lambda t: t.result() if not t.cancelled() else None)

# FIX: or use a task set
background_tasks = set()
task = asyncio.create_task(background_work())
background_tasks.add(task)
task.add_done_callback(background_tasks.discard)
```

**2. asyncio.gather without return_exceptions**

```python
# BUG: one exception cancels all tasks, partial results lost
results = await asyncio.gather(task1, task2, task3)

# FIX: fault-tolerant
results = await asyncio.gather(task1, task2, task3, return_exceptions=True)
for i, result in enumerate(results):
    if isinstance(result, Exception):
        logger.error(f"Task {i} failed: {result}")
```

**3. threading.Lock in async code**

```python
# BUG: blocks the entire event loop
lock = threading.Lock()
with lock:
    await some_io()  # event loop frozen during acquire

# FIX: use asyncio.Lock
lock = asyncio.Lock()
async with lock:
    await some_io()
```

**4. Missing await on coroutine**

```python
# BUG: returns coroutine object, never executes
result = some_async_func()  # RuntimeWarning: coroutine was never awaited

# FIX:
result = await some_async_func()
```

**5. Blocking call on event loop thread**

```python
# BUG: blocks all other tasks
async def handler(request):
    data = requests.get("https://api.example.com")  # sync HTTP!
    return Response(data)

# FIX: use async HTTP client
async def handler(request):
    async with httpx.AsyncClient() as client:
        data = await client.get("https://api.example.com")
    return Response(data)

# FIX: or offload to thread
async def handler(request):
    data = await asyncio.to_thread(requests.get, "https://api.example.com")
    return Response(data)
```

**6. Timeout not cleaning up**

```python
# BUG: task continues running after timeout
try:
    result = await asyncio.wait_for(long_task(), timeout=30.0)
except asyncio.TimeoutError:
    pass  # task still running!

# FIX: ensure cancellation
task = asyncio.create_task(long_task())
try:
    result = await asyncio.wait_for(asyncio.shield(task), timeout=30.0)
except asyncio.TimeoutError:
    task.cancel()
    try:
        await task
    except asyncio.CancelledError:
        pass
```

**7. asyncio.run() nesting**

```python
# BUG: "This event loop is already running"
async def inner():
    return 42

async def outer():
    return asyncio.run(inner())  # CRASH: nested event loop

# FIX: just await
async def outer():
    return await inner()

# FIX: if bridging sync→async
import asyncio
result = asyncio.run(inner())  # only from sync context
```

**8. Semaphore for rate limiting**

```python
# Rate-limit concurrent operations
sem = asyncio.Semaphore(10)

async def limited_task(item):
    async with sem:  # max 10 concurrent
        await process(item)

await asyncio.gather(*(limited_task(i) for i in range(100)))
```

---

## threading

### The Canonical Patterns

```python
import threading

# Mutex
lock = threading.Lock()
with lock:  # auto release on exit, even on exception
    shared_state.update()

# RLock (reentrant — same thread can acquire multiple times)
rlock = threading.RLock()
with rlock:
    with rlock:  # OK — same thread
        pass

# Condition variable (always use predicate loop)
cv = threading.Condition()
with cv:
    while not ready:
        cv.wait()  # releases lock, waits, re-acquires
    do_work()

# Event (simple flag)
event = threading.Event()
event.wait(timeout=30)  # blocks until set or timeout
event.set()  # unblocks all waiters

# Barrier (N-way rendezvous)
barrier = threading.Barrier(4)
barrier.wait()  # blocks until 4 threads arrive
```

### Thread.join Without Timeout

```python
# BUG: hangs forever if thread is stuck
thread.join()

# FIX: timeout + health check
thread.join(timeout=30)
if thread.is_alive():
    logger.error("Thread hung — cannot forcibly kill Python threads!")
    # Python has NO way to kill a thread. Design for cancellation instead.
```

**Python threads cannot be forcibly killed.** You must design cooperative cancellation (check a flag, use `threading.Event`, or use `multiprocessing` for killable workers).

---

## multiprocessing

### The Fork Safety Problem

```python
# BUG: parent creates SQLite conn, child inherits via fork
conn = sqlite3.connect("db.sqlite")
p = multiprocessing.Process(target=worker, args=(conn,))  # CRASH or deadlock

# FIX: create conn in each child
def worker(db_path):
    conn = sqlite3.connect(db_path, timeout=30.0)
    try:
        conn.execute("SELECT 1")
    finally:
        conn.close()

p = multiprocessing.Process(target=worker, args=("db.sqlite",))
```

`fork()` copies only the current thread. If another thread held a lock at fork time, the child inherits a locked mutex with no thread to unlock it → deadlock.

**Rule:** On macOS/Linux with threads, use `multiprocessing.set_start_method("spawn")` instead of the default `"fork"`. `spawn` is safe but slower (pickles everything).

### Pool with Bounded Concurrency

```python
from multiprocessing import Pool

with Pool(4) as pool:
    results = pool.map(heavy_cpu_work, items)
# Pool is cleaned up on exit
```

### Queue-Based Communication

```python
from multiprocessing import Process, Queue

def worker(q):
    result = expensive_computation()
    q.put(result)

q = Queue()
p = Process(target=worker, args=(q,))
p.start()
result = q.get(timeout=60)  # blocks with timeout
p.join(timeout=30)
```

---

## concurrent.futures

### ThreadPoolExecutor (I/O-bound)

```python
from concurrent.futures import ThreadPoolExecutor, as_completed

with ThreadPoolExecutor(max_workers=10) as executor:
    futures = {executor.submit(fetch, url): url for url in urls}
    for future in as_completed(futures):
        url = futures[future]
        try:
            data = future.result(timeout=30)
        except Exception as e:
            logger.error(f"{url} failed: {e}")
```

### ProcessPoolExecutor (CPU-bound)

```python
from concurrent.futures import ProcessPoolExecutor

with ProcessPoolExecutor(max_workers=4) as executor:
    results = list(executor.map(heavy_compute, items))
```

### Deadlock Trap: Submitting to Own Pool

```python
# BUG: all workers waiting for results from the same pool
def recursive_work(n):
    if n <= 0:
        return 1
    future = pool.submit(recursive_work, n - 1)  # deadlock if pool full
    return future.result()

pool = ThreadPoolExecutor(max_workers=4)
pool.submit(recursive_work, 100)  # 4 workers all waiting for sub-results
```

Fix: use separate pools, or restructure to avoid self-submission.

---

## trio and anyio (Structured Concurrency)

### trio: The Gold Standard

```python
import trio

async def main():
    async with trio.open_nursery() as nursery:
        nursery.start_soon(task_a)
        nursery.start_soon(task_b)
    # Nursery guarantees: all tasks completed or cancelled on exit
    # No orphan tasks. No fire-and-forget. Period.

trio.run(main)
```

**Cancel scopes** propagate down automatically:

```python
async with trio.open_nursery() as nursery:
    with trio.CancelScope(deadline=trio.current_time() + 30):
        nursery.start_soon(long_task)
    # If long_task takes >30s, it's cancelled
```

### anyio: Portable Structured Concurrency

```python
import anyio

async def main():
    async with anyio.create_task_group() as tg:
        tg.start_soon(task_a)
        tg.start_soon(task_b)

anyio.run(main)  # works on asyncio OR trio
```

**Why structured concurrency prevents bugs:**
- **No orphan tasks** — every task belongs to a nursery/task group
- **Cancellation propagates** — cancel the scope, children are cancelled
- **Exceptions propagate** — child exception reaches the nursery, which cancels siblings
- **Cleanup is guaranteed** — nursery `__aexit__` waits for all children

This is Python's equivalent of asupersync's `Scope`.

---

## Diagnosis Tools

### py-spy (375 CASS hits)

```bash
# Live thread dump (no restart needed)
py-spy dump --pid $PID

# Flamegraph
py-spy record -o profile.svg --pid $PID --duration 30

# Top-like view
py-spy top --pid $PID
```

Look for threads stuck in `Lock.acquire()`, `queue.get()`, or `socket.recv()`.

### faulthandler

```python
import faulthandler
import signal

# Enable on SIGUSR1 for on-demand dumps
faulthandler.register(signal.SIGUSR1)
# Then: kill -USR1 $PID → dumps all thread stacks to stderr
```

### asyncio Debug Mode

```python
asyncio.run(main(), debug=True)
# Or: PYTHONASYNCIODEBUG=1

# Detects:
# - Coroutines that were never awaited
# - Callbacks taking >100ms (blocking the event loop)
# - Tasks destroyed while pending
```

### threading.enumerate()

```python
for t in threading.enumerate():
    print(f"{t.name}: alive={t.is_alive()}, daemon={t.daemon}")
```

---

## Database Concurrency

### SQLite + Python Threading

```python
# BUG: sharing one connection across threads
conn = sqlite3.connect("db.sqlite")
# Thread A: conn.execute("INSERT ...")
# Thread B: conn.execute("SELECT ...")  # "database is locked"

# FIX: connection per thread + busy_timeout
import threading
local = threading.local()

def get_conn():
    if not hasattr(local, "conn"):
        local.conn = sqlite3.connect("db.sqlite", timeout=30.0)
        local.conn.execute("PRAGMA journal_mode=WAL")
        local.conn.execute("PRAGMA busy_timeout=5000")
    return local.conn
```

### SQLAlchemy Session is NOT Thread-Safe

```python
# BUG: sharing session across threads
session = Session()
# Thread A: session.query(User).all()
# Thread B: session.add(User(...))  # CRASH or data corruption

# FIX: scoped session (one per thread)
from sqlalchemy.orm import scoped_session, sessionmaker
Session = scoped_session(sessionmaker(bind=engine))

# Each thread gets its own session automatically
session = Session()
```

### psycopg Connection Pool

```python
from psycopg_pool import ConnectionPool

pool = ConnectionPool(
    conninfo="postgresql://...",
    min_size=5,
    max_size=20,
    timeout=30.0,  # wait for available connection
)

with pool.connection() as conn:
    conn.execute("SELECT 1")
# Connection returned to pool on exit
```

---

## Framework-Specific Gotchas

### FastAPI

```python
# BUG: async endpoint doing sync I/O blocks entire server
@app.post("/process")
async def process(data: Data):
    result = heavy_sync_work(data)  # blocks event loop!
    return {"result": result}

# FIX: use run_in_executor or make endpoint sync (FastAPI runs sync endpoints in threadpool)
@app.post("/process")
def process(data: Data):  # sync = auto-threadpool
    result = heavy_sync_work(data)
    return {"result": result}

# FIX: or explicit offload
@app.post("/process")
async def process(data: Data):
    result = await asyncio.to_thread(heavy_sync_work, data)
    return {"result": result}
```

### Django ORM

```python
# Deadlock: two requests locking rows in different orders
# Request A: UPDATE users SET ... WHERE id=1; UPDATE users SET ... WHERE id=2;
# Request B: UPDATE users SET ... WHERE id=2; UPDATE users SET ... WHERE id=1;

# FIX: consistent ordering + select_for_update
with transaction.atomic():
    users = User.objects.filter(id__in=[1, 2]).order_by('id').select_for_update()
    for user in users:
        user.balance -= amount
        user.save()
```

### Celery

```python
# Deadlock: task A waits for task B, task B waits for task A
# Both in same queue with limited workers

# FIX: never wait for results of tasks in the same queue
# FIX: use different queues for different dependency levels
@app.task(queue='high')
def task_a():
    result = task_b.apply_async(queue='low')
    return result.get(timeout=30)  # different queue, won't self-deadlock

# FIX: idempotent tasks (Celery may deliver twice)
@app.task(bind=True, max_retries=3)
def send_email(self, email_id):
    if Email.objects.filter(id=email_id, sent=True).exists():
        return  # already sent — idempotent
    email = Email.objects.get(id=email_id)
    mailer.send(email)
    email.sent = True
    email.save()
```

### gunicorn

```
# sync workers (default): 1 request per worker, GIL-friendly, simple
gunicorn app:app -w 4 --worker-class sync

# gthread: threads per worker, GIL contention on CPU-bound
gunicorn app:app -w 4 --worker-class gthread --threads 4

# uvicorn: full async, recommended for I/O-bound
gunicorn app:app -w 4 -k uvicorn.workers.UvicornWorker
```

**Rule:** If your app is async (FastAPI, Starlette), use uvicorn workers. If sync (Flask, Django), use sync or gthread.

---

## Anti-Patterns

- **`asyncio.create_task()` without holding reference** — GC'd before completion
- **`asyncio.gather()` without `return_exceptions=True`** — one failure kills all
- **`threading.Lock` in async code** — blocks entire event loop
- **Missing `await`** — coroutine never runs, RuntimeWarning
- **Sync I/O in async handler** — `requests.get()`, `open().read()`, `sqlite3.execute()`
- **`asyncio.run()` nested** — "event loop already running"
- **`thread.join()` without timeout** — hangs forever if thread stuck
- **Sharing SQLite conn across threads** — "database is locked"
- **Sharing SQLAlchemy session across threads** — data corruption
- **`multiprocessing` with `fork` + threads** — child inherits locked mutexes
- **Pickle failures in `multiprocessing`** — lambdas, local functions not pickleable
- **Recursive pool self-submission** — all workers waiting for sub-results
- **`threading.Condition.wait()` without predicate loop** — spurious wakeups
- **`time.sleep()` in async code** — use `await asyncio.sleep()`
- **`__del__` method accessing event loop** — loop may be closed

---

## Code Recipe Library (20+)

### 1. asyncio task set (no orphans)

```python
tasks = set()
task = asyncio.create_task(work())
tasks.add(task)
task.add_done_callback(tasks.discard)
```

### 2. Bounded concurrent fetch

```python
sem = asyncio.Semaphore(10)
async def fetch(url):
    async with sem:
        async with httpx.AsyncClient() as c:
            return await c.get(url)
results = await asyncio.gather(*(fetch(u) for u in urls))
```

### 3. asyncio.to_thread for sync offload

```python
result = await asyncio.to_thread(sync_heavy_work, arg1, arg2)
```

### 4. Structured concurrency with anyio

```python
async with anyio.create_task_group() as tg:
    tg.start_soon(task_a)
    tg.start_soon(task_b)
```

### 5. Thread-local SQLite connections

```python
local = threading.local()
def get_conn():
    if not hasattr(local, "conn"):
        local.conn = sqlite3.connect("db.sqlite", timeout=30.0)
        local.conn.execute("PRAGMA journal_mode=WAL")
        local.conn.execute("PRAGMA busy_timeout=5000")
    return local.conn
```

### 6. Process pool for CPU-bound

```python
with ProcessPoolExecutor(max_workers=cpu_count()) as pool:
    results = list(pool.map(heavy_compute, items))
```

### 7. Queue-based IPC

```python
q = multiprocessing.Queue()
p = multiprocessing.Process(target=worker, args=(q,))
p.start()
result = q.get(timeout=60)
p.join(timeout=30)
```

### 8. Cancellable async timeout

```python
async def with_timeout(coro, seconds):
    try:
        return await asyncio.wait_for(coro, timeout=seconds)
    except asyncio.TimeoutError:
        raise TimeoutError(f"Operation exceeded {seconds}s")
```

### 9. Retry with backoff

```python
async def retry(fn, max_attempts=5, base_delay=0.1):
    for attempt in range(max_attempts):
        try:
            return await fn()
        except Exception as e:
            if attempt == max_attempts - 1:
                raise
            delay = base_delay * (2 ** attempt) + random.uniform(0, base_delay)
            await asyncio.sleep(delay)
```

### 10. Signal-driven thread dump

```python
import faulthandler, signal
faulthandler.register(signal.SIGUSR1)
# kill -USR1 $PID → full thread dump to stderr
```

### 11. Condition variable with predicate

```python
with cv:
    while not ready:
        cv.wait(timeout=30)
    process()
```

### 12. Event for cooperative shutdown

```python
shutdown = threading.Event()
def worker():
    while not shutdown.is_set():
        do_work()
        shutdown.wait(timeout=1.0)  # check every second
# Main: shutdown.set()  # all workers exit within 1s
```

### 13. FastAPI sync endpoint (auto threadpool)

```python
@app.post("/heavy")
def heavy_endpoint(data: Data):  # sync = runs in threadpool
    return heavy_sync_work(data)
```

### 14. Django select_for_update with ordering

```python
with transaction.atomic():
    rows = MyModel.objects.filter(id__in=ids).order_by('id').select_for_update()
    for row in rows:
        row.update_field()
        row.save()
```

### 15. Celery idempotent task

```python
@app.task(bind=True, max_retries=3)
def process_order(self, order_id):
    if Order.objects.filter(id=order_id, processed=True).exists():
        return
    order = Order.objects.get(id=order_id)
    do_processing(order)
    order.processed = True
    order.save()
```

### 16. asyncio.TaskGroup (Python 3.11+)

```python
async with asyncio.TaskGroup() as tg:
    task1 = tg.create_task(fetch_a())
    task2 = tg.create_task(fetch_b())
# All tasks completed; exceptions propagate
result_a = task1.result()
result_b = task2.result()
```

### 17. multiprocessing with spawn (fork-safe)

```python
import multiprocessing
multiprocessing.set_start_method("spawn")  # safe with threads
```

### 18. asyncio.wait with FIRST_COMPLETED

```python
done, pending = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
for task in pending:
    task.cancel()
```

### 19. Thread-safe counter

```python
import threading
class Counter:
    def __init__(self):
        self._value = 0
        self._lock = threading.Lock()
    def increment(self):
        with self._lock:
            self._value += 1
            return self._value
```

### 20. Connection pool health check

```python
async def health_check(pool):
    try:
        async with pool.connection() as conn:
            await conn.execute("SELECT 1")
        return True
    except Exception:
        return False
```

---

## Audit Commands

```bash
# Find sync I/O in async functions
rg -n 'async def' --type py -A 20 | rg 'requests\.|open\(|sqlite3\.' | head

# Find missing await
python -W all your_script.py 2>&1 | grep "was never awaited"

# Find threading.Lock in async code
rg -n 'threading\.Lock' --type py

# Find shared state without locks
rg -n 'global ' --type py  # each is a code review target

# Find multiprocessing with default fork
rg -n 'multiprocessing\.(Process|Pool)' --type py | grep -v 'set_start_method'

# Run with asyncio debug
PYTHONASYNCIODEBUG=1 python your_script.py

# py-spy thread dump
py-spy dump --pid $(pgrep -f your_app)
```

## See Also

- [SKILL.md](../SKILL.md) — 9-class taxonomy
- [DATABASE.md](DATABASE.md) — SQLite concurrency (cross-language)
- [ASYNC.md](ASYNC.md) — cross-language async patterns
- [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) — structured concurrency (trio is the exemplar)

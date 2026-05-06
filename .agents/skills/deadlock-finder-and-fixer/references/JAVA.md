# Java / JVM Concurrency Cookbook

<!-- TOC: Memory Model | synchronized | j.u.c. | Virtual Threads | CompletableFuture | Diagnosis | Anti-Patterns | Recipes -->

## Table of Contents

- [The Java Memory Model (JMM)](#the-java-memory-model)
- [synchronized and Intrinsic Locks](#synchronized)
- [java.util.concurrent](#javautilconcurrent)
- [Virtual Threads and Structured Concurrency](#virtual-threads)
- [CompletableFuture](#completablefuture)
- [Database Concurrency (JDBC, JPA, Connection Pools)](#database-concurrency)
- [Diagnosis Tools](#diagnosis-tools)
- [Anti-Patterns](#anti-patterns)
- [Code Recipe Library (20+)](#code-recipe-library-20)
- [Audit Commands](#audit-commands)

---

## The Java Memory Model

The JMM defines **happens-before** relationships that guarantee visibility of writes across threads. Without a happens-before edge, one thread's write may never be seen by another.

**Happens-before edges (automatic):**
- `synchronized` block exit → next `synchronized` block entry on same monitor
- `volatile` write → subsequent `volatile` read of same variable
- `Thread.start()` → first action in started thread
- Last action in thread → `Thread.join()` return
- `Executor.submit()` → task body; task body → `Future.get()` return

**The golden rule:** If two threads access the same mutable state and at least one writes, you need synchronization. The JMM guarantees nothing without it.

### volatile

```java
volatile boolean ready = false;
Object data;

// Thread A (publisher):
data = new Object();         // happens-before the volatile write
ready = true;                // volatile write

// Thread B (consumer):
if (ready) {                 // volatile read
    data.toString();         // guaranteed to see the initialized object
}
```

`volatile` guarantees visibility (no caching in thread-local registers) and ordering (no reordering past the volatile access). It does NOT guarantee atomicity of compound operations (`counter++` is still a race).

---

## synchronized

### The Basics

```java
// Method-level (locks `this`)
public synchronized void increment() {
    count++;
}

// Block-level (locks specific object)
public void increment() {
    synchronized (lock) {
        count++;
    }
}

// Static method (locks the Class object)
public static synchronized void staticMethod() { ... }
```

### Deadlock: The Classic AB-BA

```java
// Thread 1:
synchronized (lockA) {
    synchronized (lockB) { ... }  // holds A, wants B
}

// Thread 2:
synchronized (lockB) {
    synchronized (lockA) { ... }  // holds B, wants A → DEADLOCK
}
```

**Fix:** Always acquire locks in the same order. Sort by `System.identityHashCode()` if you don't control the order:

```java
void transferSafe(Account from, Account to, int amount) {
    Account first = System.identityHashCode(from) < System.identityHashCode(to) ? from : to;
    Account second = first == from ? to : from;
    synchronized (first) {
        synchronized (second) {
            from.debit(amount);
            to.credit(amount);
        }
    }
}
```

### Integer Cache Trap

```java
// BUG: Integer.valueOf(1) returns cached object — all code synchronizing on
// Integer.valueOf(1) shares the same monitor!
synchronized (Integer.valueOf(42)) { ... }  // global lock on cached Integer

// FIX: use dedicated lock objects
private final Object lock = new Object();
synchronized (lock) { ... }
```

---

## java.util.concurrent

### ReentrantLock (Explicit Locking)

```java
ReentrantLock lock = new ReentrantLock();

lock.lock();
try {
    // critical section
} finally {
    lock.unlock();  // ALWAYS in finally
}

// Timed tryLock (deadlock avoidance)
if (lock.tryLock(1, TimeUnit.SECONDS)) {
    try { ... }
    finally { lock.unlock(); }
} else {
    // couldn't acquire — log, retry, or abort
}
```

**Advantages over `synchronized`:**
- `tryLock()` with timeout (deadlock detection)
- Fair ordering (`new ReentrantLock(true)` — FIFO)
- Multiple `Condition` objects per lock
- `lockInterruptibly()` — cancellable lock acquisition

### ReadWriteLock

```java
ReadWriteLock rwLock = new ReentrantReadWriteLock();

// Read (many concurrent):
rwLock.readLock().lock();
try { return data.get(key); }
finally { rwLock.readLock().unlock(); }

// Write (exclusive):
rwLock.writeLock().lock();
try { data.put(key, value); }
finally { rwLock.writeLock().unlock(); }
```

### StampedLock (Optimistic Reads)

```java
StampedLock sl = new StampedLock();

// Optimistic read (no locking, very fast):
long stamp = sl.tryOptimisticRead();
double x = this.x, y = this.y;
if (!sl.validate(stamp)) {
    // Someone wrote — fall back to read lock
    stamp = sl.readLock();
    try { x = this.x; y = this.y; }
    finally { sl.unlockRead(stamp); }
}
// Use x, y
```

This is Java's equivalent of a seqlock. Zero overhead on uncontended reads.

### ConcurrentHashMap

```java
ConcurrentHashMap<String, Integer> map = new ConcurrentHashMap<>();

// Atomic compute (no external locking needed):
map.compute("key", (k, v) -> v == null ? 1 : v + 1);

// Atomic putIfAbsent:
map.putIfAbsent("key", 0);

// Atomic merge:
map.merge("key", 1, Integer::sum);
```

**Gotcha:** The lambda in `compute()` runs while holding the bin lock. Do NOT call other `ConcurrentHashMap` methods inside it — deadlock risk.

```java
// BUG: compute() holds bin lock; get() may need same bin
map.compute("a", (k, v) -> {
    int other = map.get("b");  // may deadlock if "b" is in same bin
    return v + other;
});

// FIX: read first, compute separately
int other = map.get("b");
map.compute("a", (k, v) -> v + other);
```

### Executors and Thread Pools

```java
// Fixed pool (bounded):
ExecutorService pool = Executors.newFixedThreadPool(10);

// Cached pool (unbounded — DANGEROUS for many tasks):
ExecutorService pool = Executors.newCachedThreadPool();

// Virtual thread executor (Java 21+):
ExecutorService pool = Executors.newVirtualThreadPerTaskExecutor();

// ALWAYS shut down:
pool.shutdown();
if (!pool.awaitTermination(30, TimeUnit.SECONDS)) {
    pool.shutdownNow();
}
```

**Gotcha:** `newCachedThreadPool()` creates unlimited threads. Under load, it can spawn thousands → OOM. Always prefer bounded pools.

### CountDownLatch

```java
CountDownLatch latch = new CountDownLatch(3);

// Workers:
for (int i = 0; i < 3; i++) {
    executor.submit(() -> {
        doWork();
        latch.countDown();
    });
}

// Waiter:
latch.await(30, TimeUnit.SECONDS);
```

### Semaphore

```java
Semaphore sem = new Semaphore(10);  // max 10 concurrent

sem.acquire();
try { doWork(); }
finally { sem.release(); }

// Non-blocking:
if (sem.tryAcquire(1, TimeUnit.SECONDS)) {
    try { doWork(); }
    finally { sem.release(); }
} else {
    // at capacity
}
```

### BlockingQueue

```java
BlockingQueue<Task> queue = new LinkedBlockingQueue<>(100);

// Producer:
queue.put(task);  // blocks if full

// Consumer:
Task task = queue.take();  // blocks if empty

// Bounded with timeout:
if (!queue.offer(task, 1, TimeUnit.SECONDS)) {
    // queue full, shed load
}
```

---

## Virtual Threads and Structured Concurrency

### Virtual Threads (Java 21+)

Lightweight threads multiplexed onto a small pool of carrier (OS) threads. Millions of virtual threads on a single JVM.

```java
// Create:
Thread vt = Thread.ofVirtual().start(() -> doWork());

// Or via executor:
try (var executor = Executors.newVirtualThreadPerTaskExecutor()) {
    for (int i = 0; i < 100_000; i++) {
        executor.submit(() -> {
            // Each task gets its own virtual thread
            httpClient.send(request, BodyHandlers.ofString());
        });
    }
}
```

**The Pinning Problem:** `synchronized` blocks **pin** virtual threads to carrier threads, negating the benefit. Under high concurrency with many `synchronized` blocks, you exhaust carrier threads → effective deadlock.

```java
// BAD: pins the virtual thread
synchronized (lock) {
    // Long I/O operation — carrier thread is pinned
    httpClient.send(request, BodyHandlers.ofString());
}

// GOOD: use ReentrantLock (doesn't pin)
lock.lock();
try {
    httpClient.send(request, BodyHandlers.ofString());
} finally {
    lock.unlock();
}
```

**Detection:** `-Djdk.tracePinnedThreads=short` prints when virtual threads are pinned.

### StructuredTaskScope (Java 21+ Preview)

Java's nursery equivalent:

```java
try (var scope = new StructuredTaskScope.ShutdownOnFailure()) {
    Subtask<String> user  = scope.fork(() -> fetchUser(id));
    Subtask<String> order = scope.fork(() -> fetchOrder(id));
    
    scope.join();              // wait for all
    scope.throwIfFailed();     // propagate first exception
    
    return new Response(user.get(), order.get());
}
// All subtasks complete or are cancelled when scope closes
```

**ShutdownOnSuccess:** Return first successful result, cancel the rest:

```java
try (var scope = new StructuredTaskScope.ShutdownOnSuccess<String>()) {
    scope.fork(() -> fetchFromPrimary());
    scope.fork(() -> fetchFromBackup());
    scope.join();
    return scope.result();  // first to succeed
}
```

---

## CompletableFuture

### Basics

```java
CompletableFuture<String> future = CompletableFuture.supplyAsync(() -> fetchData());

// Chain:
future.thenApply(data -> process(data))
      .thenAccept(result -> save(result))
      .exceptionally(ex -> { log.error(ex); return null; });

// Combine:
CompletableFuture.allOf(futureA, futureB).thenRun(() -> {
    String a = futureA.join();
    String b = futureB.join();
});
```

### Deadlock: Blocking Inside the Common ForkJoinPool

```java
// BUG: supplyAsync uses ForkJoinPool.commonPool() by default
// If all pool threads are blocked waiting for other completable futures → deadlock
CompletableFuture.supplyAsync(() -> {
    return CompletableFuture.supplyAsync(() -> heavyWork()).join();  // blocks a pool thread
}).join();

// FIX: use dedicated executor
ExecutorService myPool = Executors.newFixedThreadPool(10);
CompletableFuture.supplyAsync(() -> heavyWork(), myPool);
```

---

## Database Concurrency (JDBC, JPA, Connection Pools)

### Connection Pool (HikariCP)

```java
HikariConfig config = new HikariConfig();
config.setJdbcUrl("jdbc:postgresql://localhost/db");
config.setMaximumPoolSize(20);
config.setConnectionTimeout(5000);   // fail fast on acquire
config.setIdleTimeout(300000);
config.setMaxLifetime(1800000);

HikariDataSource ds = new HikariDataSource(config);
```

### JPA / Hibernate Session is NOT Thread-Safe

```java
// BUG: sharing EntityManager across threads
EntityManager em = emf.createEntityManager();
// Thread A: em.find(User.class, 1)
// Thread B: em.persist(new User())  // CRASH or data corruption

// FIX: EntityManager per thread (or per request)
EntityManager em = emf.createEntityManager();
try {
    em.getTransaction().begin();
    // ... work ...
    em.getTransaction().commit();
} finally {
    em.close();
}
```

### Optimistic Locking with @Version

```java
@Entity
public class Account {
    @Id Long id;
    @Version Long version;  // auto-incremented on UPDATE
    BigDecimal balance;
}

// Hibernate throws OptimisticLockException if version mismatch on save
try {
    account.setBalance(newBalance);
    em.merge(account);
    em.flush();
} catch (OptimisticLockException e) {
    // Conflict — reload and retry
}
```

### Pessimistic Locking

```java
Account account = em.find(Account.class, id, LockModeType.PESSIMISTIC_WRITE);
// Row locked until transaction commits
account.setBalance(account.getBalance().subtract(amount));
em.merge(account);
```

---

## Diagnosis Tools

### jstack (Thread Dump)

```bash
jstack $PID > thread_dump.txt

# Auto-detects deadlocks:
# "Found one Java-level deadlock:"
# Thread 1: waiting to lock monitor 0x...
# Thread 2: waiting to lock monitor 0x...
```

### JMX Deadlock Detection

```java
ThreadMXBean tmx = ManagementFactory.getThreadMXBean();
long[] deadlocked = tmx.findDeadlockedThreads();
if (deadlocked != null) {
    ThreadInfo[] infos = tmx.getThreadInfo(deadlocked, true, true);
    for (ThreadInfo ti : infos) {
        System.err.println(ti);
    }
}
```

### VisualVM / JFR (Java Flight Recorder)

```bash
# Start JFR recording:
jcmd $PID JFR.start duration=60s filename=recording.jfr

# Or via JVM flags:
java -XX:StartFlightRecording=duration=60s,filename=rec.jfr MyApp

# Analyze with JDK Mission Control (jmc)
```

### Virtual Thread Pinning Detection

```bash
java -Djdk.tracePinnedThreads=short MyApp
# Prints stack traces when virtual threads are pinned to carrier threads
```

### Async Profiler (Lock Contention)

```bash
# Profile lock contention:
./profiler.sh -e lock -d 30 -f lock_profile.html $PID
```

---

## Anti-Patterns

- **`synchronized` on `Integer.valueOf()`** — shared cached object; global lock
- **`synchronized` in virtual thread code** — pins carrier thread; use `ReentrantLock`
- **`Executors.newCachedThreadPool()`** — unbounded thread creation; use fixed pool
- **`compute()` calling other CHM methods** — bin-level deadlock
- **Sharing `EntityManager` across threads** — not thread-safe
- **`CompletableFuture` blocking in common ForkJoinPool** — pool exhaustion deadlock
- **`Thread.stop()` / `Thread.suspend()`** — deprecated; inherently unsafe
- **Double-checked locking without `volatile`** — broken before Java 5
- **`ThreadLocal` without `remove()`** — memory leak in thread pools
- **`synchronized` on mutable field** — lock identity changes; no mutual exclusion

---

## Code Recipe Library (20+)

### 1. Lock ordering with identity hash

```java
void transfer(Account a, Account b, int amt) {
    Object first = System.identityHashCode(a) < System.identityHashCode(b) ? a : b;
    Object second = first == a ? b : a;
    synchronized (first) { synchronized (second) { /* transfer */ } }
}
```

### 2. Timed tryLock

```java
if (lock.tryLock(1, TimeUnit.SECONDS)) {
    try { work(); } finally { lock.unlock(); }
} else { handleTimeout(); }
```

### 3. StampedLock optimistic read

```java
long stamp = sl.tryOptimisticRead();
double x = this.x;
if (!sl.validate(stamp)) {
    stamp = sl.readLock();
    try { x = this.x; } finally { sl.unlockRead(stamp); }
}
```

### 4. ConcurrentHashMap.computeIfAbsent

```java
map.computeIfAbsent("key", k -> expensiveCreate(k));
```

### 5. Virtual thread executor

```java
try (var ex = Executors.newVirtualThreadPerTaskExecutor()) {
    ex.submit(() -> handleRequest(req));
}
```

### 6. StructuredTaskScope fan-out

```java
try (var scope = new StructuredTaskScope.ShutdownOnFailure()) {
    var a = scope.fork(() -> fetchA());
    var b = scope.fork(() -> fetchB());
    scope.join(); scope.throwIfFailed();
    return combine(a.get(), b.get());
}
```

### 7. CompletableFuture with dedicated executor

```java
var pool = Executors.newFixedThreadPool(10);
CompletableFuture.supplyAsync(() -> work(), pool);
```

### 8. Semaphore-bounded work

```java
Semaphore sem = new Semaphore(10);
sem.acquire(); try { work(); } finally { sem.release(); }
```

### 9. BlockingQueue producer-consumer

```java
BlockingQueue<Task> q = new LinkedBlockingQueue<>(100);
// Producer: q.put(task);
// Consumer: Task t = q.take();
```

### 10. CountDownLatch barrier

```java
CountDownLatch latch = new CountDownLatch(n);
// Workers: latch.countDown();
// Waiter: latch.await(30, TimeUnit.SECONDS);
```

### 11. Atomic reference CAS

```java
AtomicReference<State> ref = new AtomicReference<>(initial);
ref.updateAndGet(old -> computeNew(old));
```

### 12. ExecutorService graceful shutdown

```java
pool.shutdown();
if (!pool.awaitTermination(30, TimeUnit.SECONDS)) {
    pool.shutdownNow();
}
```

### 13. ThreadLocal with cleanup

```java
ThreadLocal<Connection> tlConn = new ThreadLocal<>();
try {
    tlConn.set(getConnection());
    work();
} finally {
    tlConn.get().close();
    tlConn.remove();  // prevent leak in thread pool
}
```

### 14. JPA pessimistic lock

```java
em.find(Entity.class, id, LockModeType.PESSIMISTIC_WRITE);
```

### 15. JPA optimistic lock retry

```java
for (int i = 0; i < 5; i++) {
    try { doUpdate(); break; }
    catch (OptimisticLockException e) {
        if (i == 4) throw e;
        Thread.sleep(50 * (1 << i));
    }
}
```

### 16. Condition variable

```java
Condition notEmpty = lock.newCondition();
lock.lock();
try {
    while (queue.isEmpty()) notEmpty.await();
    return queue.poll();
} finally { lock.unlock(); }
```

### 17. Phaser (dynamic barrier)

```java
Phaser phaser = new Phaser(1);
for (Task t : tasks) {
    phaser.register();
    executor.submit(() -> {
        t.run();
        phaser.arriveAndDeregister();
    });
}
phaser.arriveAndAwaitAdvance();
```

### 18. CyclicBarrier

```java
CyclicBarrier barrier = new CyclicBarrier(n, () -> mergeResults());
// Each worker: barrier.await();
```

### 19. Exchanger

```java
Exchanger<Buffer> exchanger = new Exchanger<>();
// Producer: fullBuffer = exchanger.exchange(emptyBuffer);
// Consumer: emptyBuffer = exchanger.exchange(fullBuffer);
```

### 20. Fork/Join recursive parallel

```java
class SumTask extends RecursiveTask<Long> {
    protected Long compute() {
        if (size < THRESHOLD) return directSum();
        SumTask left = new SumTask(array, lo, mid);
        SumTask right = new SumTask(array, mid, hi);
        left.fork();
        long rightResult = right.compute();
        return left.join() + rightResult;
    }
}
ForkJoinPool.commonPool().invoke(new SumTask(array, 0, array.length));
```

---

## Audit Commands

```bash
# Thread dump (auto-detects deadlocks)
jstack $PID | tee thread_dump.txt
grep -A5 "Found one Java-level deadlock" thread_dump.txt

# Find synchronized blocks (code review targets)
rg -n 'synchronized' --type java src/

# Find synchronized on Integer/String literals (BUG)
rg -n 'synchronized.*Integer\.valueOf\|synchronized.*"' --type java src/

# Find CachedThreadPool (potential unbounded)
rg -n 'newCachedThreadPool' --type java src/

# Find ThreadLocal without remove() (memory leak)
rg -n 'ThreadLocal' --type java src/ | rg -v 'remove'

# Virtual thread pinning
java -Djdk.tracePinnedThreads=short -jar app.jar

# Lock contention profiling
./async-profiler/profiler.sh -e lock -d 30 -f locks.html $PID
```

## See Also

- [SKILL.md](../SKILL.md) — 9-class taxonomy
- [CROSS-LANGUAGE.md](CROSS-LANGUAGE.md) — Java vs other languages
- [DATABASE-ADVANCED.md](DATABASE-ADVANCED.md) — JDBC, JPA, connection pools
- [CREATIVE-PATTERNS.md](CREATIVE-PATTERNS.md) — structured concurrency, actor model
- [LOCK-FREE.md](LOCK-FREE.md) — StampedLock is Java's seqlock

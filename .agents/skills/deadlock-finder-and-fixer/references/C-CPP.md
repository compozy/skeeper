# C/C++ Systems Concurrency Cookbook

<!-- TOC: pthread | Memory Model | Condition Variables | Signal Safety | Fork Hazards | False Sharing | io_uring / epoll | Detection Tools | Code Recipes -->

C and C++ are where concurrency bugs are hardest — no compiler guardrails (unlike Rust), no GC pause-based simplification (unlike Go/Java), and the memory model has more freedom (and more footguns) than any other language.

Sourced from frankenlibc, frankensqlite, glibc-rust, and process-triage sessions. 846 cass hits for `pthread_mutex_lock`, 751 for `pthread_cond_wait`, 5424 for `memory_order`, 557 for `futex`.

---

## pthread Mutex Patterns

### The Correct Pattern

```c
pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;

void critical_section(void) {
    pthread_mutex_lock(&mu);
    // ... work ...
    pthread_mutex_unlock(&mu);
}
```

### Recursive Mutexes: Almost Always Wrong

```c
// DON'T: recursive mutex hides design problems
pthread_mutexattr_t attr;
pthread_mutexattr_init(&attr);
pthread_mutexattr_settype(&attr, PTHREAD_MUTEX_RECURSIVE);

// DO: restructure so you don't need reentrancy
// Split the function into locked and unlocked parts:
static void do_work_locked(State *s) { /* called with mu held */ }
void do_work(void) {
    pthread_mutex_lock(&mu);
    do_work_locked(&state);
    pthread_mutex_unlock(&mu);
}
```

Recursive mutexes increase contention and hide refactoring bugs. The deadlock on unlock count mismatch is subtle and hard to debug.

### Robust Mutexes (Process-Shared)

```c
pthread_mutexattr_t attr;
pthread_mutexattr_init(&attr);
pthread_mutexattr_settype(&attr, PTHREAD_MUTEX_ROBUST);
pthread_mutexattr_setpshared(&attr, PTHREAD_PROCESS_SHARED);

// If holder dies, next locker gets EOWNERDEAD
int ret = pthread_mutex_lock(&mu);
if (ret == EOWNERDEAD) {
    // State may be inconsistent — repair it
    repair_state();
    pthread_mutex_consistent(&mu);  // mark as recovered
}
```

Use for cross-process mutexes in shared memory. Without `ROBUST`, a crashed holder deadlocks all waiters permanently.

### Trylock with Backoff

```c
int acquire_with_backoff(pthread_mutex_t *mu, int max_attempts) {
    for (int i = 0; i < max_attempts; i++) {
        if (pthread_mutex_trylock(mu) == 0) return 0;
        usleep((1 << i) * 100 + rand() % 100);  // exp backoff + jitter
    }
    return EBUSY;
}
```

---

## C++ Memory Model

### The Ordering Levels

```
relaxed < consume < acquire < release < acq_rel < seq_cst
           (deprecated)
```

| Ordering | Use When | Example |
|----------|----------|---------|
| `relaxed` | Statistics counters only (no data publication) | `counter.fetch_add(1, relaxed)` |
| `acquire` | Reader of published data | `ptr.load(acquire)` |
| `release` | Publisher of data | `ptr.store(new_val, release)` |
| `acq_rel` | Both reader and publisher (CAS loops) | `val.compare_exchange(old, new, acq_rel, acquire)` |
| `seq_cst` | When in doubt, or global ordering needed | Default; safest but slowest |

### The Most Common Bug: Relaxed on Data Publication

```cpp
// BUG: reader may see the pointer before the object is initialized
data = new MyStruct();
ready.store(true, std::memory_order_relaxed);  // WRONG
// Another thread:
if (ready.load(std::memory_order_relaxed)) {
    data->use();  // may see uninitialized data!
}

// FIX: release/acquire pairing
data = new MyStruct();
ready.store(true, std::memory_order_release);
// Another thread:
if (ready.load(std::memory_order_acquire)) {
    data->use();  // guaranteed to see fully initialized data
}
```

### x86 vs ARM Memory Models

- **x86 (TSO):** Most loads and stores are already ordered. `relaxed` often "works" on x86 but breaks on ARM. Don't rely on this — write correct code for the weakest model.
- **ARM (weakly ordered):** Stores can be reordered with loads. `relaxed` really means relaxed. Your code MUST use the correct ordering or it WILL break on ARM.

### Double-Checked Locking (Correct C++11)

```cpp
std::atomic<Singleton*> instance{nullptr};
std::mutex init_mutex;

Singleton* get_instance() {
    Singleton* p = instance.load(std::memory_order_acquire);
    if (!p) {
        std::lock_guard<std::mutex> lock(init_mutex);
        p = instance.load(std::memory_order_relaxed);  // recheck under lock
        if (!p) {
            p = new Singleton();
            instance.store(p, std::memory_order_release);
        }
    }
    return p;
}
```

Pre-C++11 double-checked locking is **always broken** due to lack of memory model guarantees.

---

## Condition Variables

### The ONLY Correct Pattern

```c
pthread_mutex_lock(&mu);
while (!predicate) {                     // ALWAYS while, NEVER if
    pthread_cond_wait(&cv, &mu);         // releases mu, waits, re-acquires
}
// predicate is true, mu is held
do_work();
pthread_mutex_unlock(&mu);
```

**Why `while` not `if`:** Spurious wakeups are allowed by POSIX. `pthread_cond_wait` can return even if nobody called `signal`/`broadcast`. The `while` loop re-checks the predicate after every wakeup.

### Signaling

```c
// Signal AFTER modifying the predicate, outside the lock if possible:
pthread_mutex_lock(&mu);
predicate = true;
pthread_mutex_unlock(&mu);
pthread_cond_signal(&cv);  // wake one waiter

// Or broadcast (wake all):
pthread_cond_broadcast(&cv);
```

### C++ std::condition_variable

```cpp
std::mutex mu;
std::condition_variable cv;
bool ready = false;

// Waiter:
std::unique_lock<std::mutex> lock(mu);
cv.wait(lock, [&]{ return ready; });  // predicate version (correct by construction)

// Signaler:
{
    std::lock_guard<std::mutex> lock(mu);
    ready = true;
}
cv.notify_one();
```

The lambda predicate version of `cv.wait(lock, pred)` is preferred — it handles the while loop internally.

---

## Signal Safety

### The Async-Signal-Safe Rule

Inside a signal handler, you may ONLY call async-signal-safe functions. The complete list (POSIX):

```
_Exit, _exit, abort, accept, access, aio_error, aio_return, aio_suspend,
alarm, bind, cfgetispeed, cfgetospeed, cfsetispeed, cfsetospeed, chdir,
chmod, chown, clock_gettime, close, connect, creat, dup, dup2, execle,
execve, fchmod, fchown, fcntl, fdatasync, fork, fstat, fsync, ftruncate,
getegid, geteuid, getgid, getgroups, getpgrp, getpid, getppid, getuid,
kill, link, listen, lseek, lstat, mkdir, mkfifo, open, pathconf, pause,
pipe, poll, posix_trace_event, pselect, raise, read, readlink, recv,
recvfrom, recvmsg, rename, rmdir, select, sem_post, send, sendmsg,
sendto, setgid, setpgid, setsid, setsockopt, setuid, shutdown, sigaction,
sigaddset, sigdelset, sigemptyset, sigfillset, sigismember, signal,
sigpause, sigpending, sigprocmask, sigqueue, sigset, sigsuspend, sleep,
sockatmark, socket, socketpair, stat, symlink, sysconf, tcdrain, tcflow,
tcflush, tcgetattr, tcgetpgrp, tcsendbreak, tcsetattr, tcsetpgrp, time,
timer_getoverrun, timer_gettime, timer_settime, times, umask, uname,
unlink, utime, wait, waitpid, write
```

**NOT safe:** `malloc`, `free`, `printf`, `fprintf`, `syslog`, `pthread_mutex_lock`, any C++ operator new/delete, any STL operation.

### The Correct Signal Handler Pattern

```c
volatile sig_atomic_t got_signal = 0;

void handler(int sig) {
    got_signal = 1;  // only set a flag — nothing else
}

int main(void) {
    struct sigaction sa = { .sa_handler = handler };
    sigaction(SIGTERM, &sa, NULL);
    
    while (!got_signal) {
        // main event loop
        do_work();
    }
    // clean shutdown here, where all functions are safe
    cleanup();
    return 0;
}
```

### Self-Pipe Trick (for event loops)

```c
int signal_pipe[2];
pipe(signal_pipe);
fcntl(signal_pipe[1], F_SETFL, O_NONBLOCK);

void handler(int sig) {
    int saved_errno = errno;
    write(signal_pipe[1], "x", 1);  // write() is async-signal-safe
    errno = saved_errno;
}

// In event loop: poll/select on signal_pipe[0]
```

---

## Fork Hazards

### The Problem

`fork()` copies only the calling thread. If other threads held locks at fork time, those locks are **permanently locked** in the child — the threads that would unlock them don't exist.

```c
// Thread A: holds mutex M
// Thread B: calls fork()
// Child: only Thread B exists; mutex M is locked; nobody will unlock it
// Child calls pthread_mutex_lock(&M) → DEADLOCK
```

### pthread_atfork

```c
pthread_mutex_t mu;

void prefork(void)  { pthread_mutex_lock(&mu); }    // acquire ALL locks
void postfork_parent(void) { pthread_mutex_unlock(&mu); } // release in parent
void postfork_child(void)  { pthread_mutex_unlock(&mu); } // release in child

pthread_atfork(prefork, postfork_parent, postfork_child);
```

**Rules:**
1. `prefork` must acquire ALL locks in the canonical order
2. `postfork_parent` releases them in reverse order
3. `postfork_child` reinitializes or releases them

### The Real Fix: fork + exec

```c
// Don't fork() in a multithreaded program unless you immediately exec()
pid_t pid = fork();
if (pid == 0) {
    // Child: exec immediately — don't touch any shared state
    execvp(argv[0], argv);
    _exit(127);  // exec failed
}
```

### posix_spawn (Safer Alternative)

```c
posix_spawn(&pid, "/path/to/binary", &file_actions, &attrs, argv, envp);
```

`posix_spawn` is designed to be safe in multithreaded programs — it's the recommended replacement for `fork + exec`.

---

## False Sharing

### The Problem

Two threads writing to different variables that share a cache line (typically 64 bytes) cause cache-line ping-pong between cores — up to 40% slowdown.

```cpp
// BUG: counter_a and counter_b on the same cache line
struct Counters {
    std::atomic<uint64_t> counter_a;  // bytes 0-7
    std::atomic<uint64_t> counter_b;  // bytes 8-15 — SAME CACHE LINE
};
// Thread A increments counter_a; Thread B increments counter_b
// Both cores invalidate each other's cache line on every write

// FIX: pad to cache line boundary
struct Counters {
    alignas(64) std::atomic<uint64_t> counter_a;  // own cache line
    alignas(64) std::atomic<uint64_t> counter_b;  // own cache line
};
```

C++17 provides `std::hardware_destructive_interference_size` (typically 64):

```cpp
struct alignas(std::hardware_destructive_interference_size) PaddedCounter {
    std::atomic<uint64_t> value;
};
```

---

## io_uring and epoll Patterns

### io_uring Thread Safety

From frankensqlite session 6ac7a450:

```c
// Each thread needs its own submission queue, OR use external synchronization
struct io_uring ring;
io_uring_queue_init(256, &ring, IORING_SETUP_SINGLE_ISSUER);
// IORING_SETUP_SINGLE_ISSUER: only one thread submits (safe, no lock needed)
// Without it: must protect io_uring_get_sqe() + io_uring_submit() with a mutex
```

### epoll Edge-Triggered (EPOLLET) Gotcha

```c
// Edge-triggered: event fires ONCE when state changes
// YOU MUST drain the entire queue per event

struct epoll_event ev;
ev.events = EPOLLIN | EPOLLET;  // edge-triggered
epoll_ctl(epfd, EPOLL_CTL_ADD, fd, &ev);

// In event handler:
while (1) {
    ssize_t n = read(fd, buf, sizeof(buf));
    if (n == -1) {
        if (errno == EAGAIN || errno == EWOULDBLOCK) break;  // drained
        handle_error();
    }
    if (n == 0) { close(fd); break; }  // EOF
    process(buf, n);
}
// BUG if you only read once: remaining data sits in kernel buffer,
// no new event fires (edge already triggered), connection hangs forever
```

---

## Detection Tools

### ThreadSanitizer (TSAN)

```bash
# C/C++
clang -fsanitize=thread -g -O1 program.c -o program
./program 2>&1 | tee tsan.log

# Output:
# WARNING: ThreadSanitizer: data race (pid=...)
#   Write of size 4 at 0x... by thread T2:
#     #0 function_a at file.c:42
#   Previous read of size 4 at 0x... by main thread:
#     #0 function_b at file.c:87
```

### Helgrind (Valgrind)

```bash
valgrind --tool=helgrind ./program
# 10-100x slower than TSAN but catches lock-order violations
```

### DRD (Valgrind Data Race Detector)

```bash
valgrind --tool=drd ./program
# Similar to Helgrind but different algorithm; catches different bugs
```

### rr (Record and Replay)

```bash
rr record --chaos ./program   # randomize scheduling
rr replay                     # deterministic replay
(rr) reverse-continue         # go BACKWARDS to find the cause
```

### Linux lockdep (Kernel)

The kernel's lock dependency checker. Not available for userspace directly, but the concept (build a lock-order graph, detect cycles) is what `parking_lot::deadlock_detection` implements for Rust.

---

## Code Recipes

### 1. Thread-safe singleton (C11)

```c
#include <threads.h>
static once_flag init_flag = ONCE_FLAG_INIT;
static Config *config;
void init_config(void) { config = load_config(); }
Config* get_config(void) {
    call_once(&init_flag, init_config);
    return config;
}
```

### 2. Reader-writer lock

```c
pthread_rwlock_t rw = PTHREAD_RWLOCK_INITIALIZER;
// Readers:
pthread_rwlock_rdlock(&rw);
read_data();
pthread_rwlock_unlock(&rw);
// Writer:
pthread_rwlock_wrlock(&rw);
write_data();
pthread_rwlock_unlock(&rw);
```

### 3. Barrier (N-way sync)

```c
pthread_barrier_t barrier;
pthread_barrier_init(&barrier, NULL, N);
// Each thread:
compute_phase_1();
pthread_barrier_wait(&barrier);  // all threads sync here
compute_phase_2();
```

### 4. Thread-local storage

```c
_Thread_local int my_errno = 0;  // C11
// or: __thread int my_errno = 0;  // GCC extension
```

### 5. Atomic flag (spinlock)

```c
atomic_flag lock = ATOMIC_FLAG_INIT;
while (atomic_flag_test_and_set(&lock)) { /* spin */ }
// critical section
atomic_flag_clear(&lock);
```

### 6. Producer-consumer with condvar

```c
pthread_mutex_lock(&mu);
while (queue_empty()) pthread_cond_wait(&not_empty, &mu);
item = dequeue();
pthread_mutex_unlock(&mu);
pthread_cond_signal(&not_full);
```

---

## Audit Commands

```bash
# Find signal handler functions
rg -n 'sigaction|signal\(' --type c --type cpp

# Find malloc/free in signal handlers (BUG)
# Manual review: check each handler for non-async-signal-safe calls

# Find fork() in multithreaded code
rg -n '\bfork\b\(\)' --type c --type cpp

# Find relaxed atomic stores (potential data publication bug)
rg -n 'memory_order_relaxed' --type cpp | rg 'store'

# Find condition variables without predicate loops
# Manual review: check for if(pred) cv.wait vs while(pred) cv.wait

# Run TSAN
clang -fsanitize=thread -g -O1 *.c -o test && ./test

# Check for false sharing
# Look for arrays of atomics without alignas(64)
rg -n 'atomic.*\[' --type cpp
```

## See Also

- [SKILL.md](../SKILL.md) — 9-class taxonomy
- [LOCK-FREE.md](LOCK-FREE.md) — CAS, epoch reclamation, seqlocks
- [LD-PRELOAD.md](LD-PRELOAD.md) — reentrant init hazards (glibc-rust)
- [FORMAL-METHODS.md](FORMAL-METHODS.md) — TSAN, helgrind, rr
- [gdb-for-debugging](../../gdb-for-debugging/SKILL.md) — lock graph construction, thread categorizer

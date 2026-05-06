# Diagnosis — Pointers to Tools

Concurrency bugs are diagnosed by state capture, not by guessing. This reference is a routing table: the symptom tells you which tool, and the tool lives in another skill. Follow the link, get the recipe, come back here with the finding and jump to the relevant class in the main `SKILL.md`.

> The heavy runtime work lives in [`/cs/gdb-for-debugging/SKILL.md`](../../gdb-for-debugging/SKILL.md). Don't re-implement here.

## Quick Decision Table

| Symptom | Primary Tool | Secondary |
|---------|--------------|-----------|
| Process 0% CPU, won't respond | `gdb --batch -ex "thread apply all bt full"` | strace -k -f -p PID |
| Process 100% CPU, stuck | `perf top -p PID` + `gdb --batch -ex "thread apply all bt 3"` | `strace -c -p PID` for syscall rate |
| Async task never completes | `tokio-console` | gdb: worker threads in epoll_wait? |
| Worker thread blocked (async) | gdb-for-debugging §Async Runtime | strace -p $worker_tid |
| SQLite "database is locked" | `lsof /path/db.sqlite` | `fuser /path/db.sqlite` |
| File lock contention | `lsof \| grep flock` | `cat /proc/locks` |
| Tmux pane hung | `tmux capture-pane -p -t SESSION:PANE` | system-performance-remediation §tmux cleanup |
| Memory corruption from race | ASAN / MSAN | TSAN for data races |
| Rust data race | TSAN (`-Zsanitizer=thread`) | `loom` for exhaustive checking |
| Go data race | `go test -race` | - |
| Intermittent test failure | `rr --chaos` + `rr replay` | Add artificial delays + TSAN |
| Deadlock cycle proof | gdb-for-debugging §Lock Graph Construction | `parking_lot` deadlock detection |
| Core dump analysis | gdb-for-debugging §Core Dump Forensics | coredumpctl |

## Pre-Flight Checklist

Before you attach a debugger:

1. **Take the snapshot.** The process will go away the moment you care. `gdb --batch -ex "thread apply all bt full" -p $PID 2>&1 | tee /tmp/bt.txt` — save to file, then analyze.
2. **Check ptrace scope.** `cat /proc/sys/kernel/yama/ptrace_scope`. If it's `1`, `sudo sysctl kernel.yama.ptrace_scope=0` (temporarily) or run gdb as root.
3. **Verify binary + symbols match.** Stripped binaries give you addresses, not function names. `file $binary` shows if symbols are present.
4. **Know what "normal" looks like.** A tokio app with 8 workers all in `epoll_wait` is healthy. The same state under load with hanging requests is async deadlock. The backtrace alone doesn't tell you which.

## Key Recipes (Inline)

Full tooling is in `gdb-for-debugging`, but these are the three commands you'll want most.

### Recipe A: Is this a classic deadlock?

```bash
gdb --batch \
  -ex "set pagination off" \
  -ex "thread apply all bt full" \
  -p $PID 2>&1 | tee /tmp/bt.txt

# Find threads blocked on a mutex
grep -B 20 '__lll_lock_wait\|futex_wait_queue' /tmp/bt.txt | grep -E 'Thread|mutex='

# For each blocked mutex, find the owner
gdb --batch -ex "print ((pthread_mutex_t*)0xADDRESS)->__data.__owner" -p $PID
```

Then build the wait-for graph manually (or use gdb-for-debugging's `mutex_owners.py`). A cycle = deadlock, proven.

### Recipe B: Is this an async deadlock (Tokio)?

```bash
# Check worker thread states
ps -Lp $PID -o tid,comm --no-headers | grep tokio-runtime

# Are they all in epoll_wait? (healthy-looking but tasks stuck)
gdb --batch -ex "thread apply all bt 3" -p $PID 2>&1 | \
  grep -A3 'tokio-runtime' | grep epoll_wait
```

- **All in `epoll_wait` + tasks pending = async deadlock.** Mutex across await, channel cycle, or task cancellation race.
- **One worker NOT in `epoll_wait`, burning CPU = task starvation.** Someone is blocking the runtime thread.
- **All in `futex_wait` = runtime itself stuck.** Scheduler contention (rare).

### Recipe C: Is this a SQLite lock contention?

```bash
# Who has the file open?
lsof /path/to/db.sqlite

# Which process has it locked?
fuser /path/to/db.sqlite

# What's the holder doing?
gdb --batch -ex "thread apply all bt 3" -p $HOLDER_PID
```

Then jump to `DATABASE.md` for the fix.

## When You Can't Attach GDB

- **Container / k8s.** `kubectl debug` with `shareProcessNamespace: true`, or `docker exec` + `apt install gdb`. See gdb-for-debugging §Container Debugging.
- **Stripped binary.** `addr2line` + symbol file (if you have one) or skip to `perf` which doesn't need symbols for counting.
- **Production, can't afford a stop-the-world snapshot.** `strace -k -f -p $PID` samples without stopping the process. `perf record -p $PID` for statistical profiling.
- **ptrace blocked.** `cat /proc/$PID/task/*/stack` samples the kernel stack of each thread — very cheap, always available.

## Artifact Collection for Post-Mortem

If you have the process and it's hung:

```bash
mkdir -p /tmp/hang-$(date +%s) && cd $_
ps -Lp $PID -o tid,pcpu,pmem,stat,comm --no-headers > threads.txt
gdb --batch -ex "set pagination off" -ex "thread apply all bt full" -p $PID > gdb.txt 2>&1
strace -k -f -p $PID -o strace.txt &
STRACE_PID=$!
sleep 5
kill $STRACE_PID
cat /proc/$PID/maps > maps.txt
cat /proc/$PID/status > status.txt
cat /proc/$PID/stack > stack.txt
for tid in $(ls /proc/$PID/task); do
    cat /proc/$PID/task/$tid/stack > task_$tid.txt 2>/dev/null
done
lsof -p $PID > lsof.txt 2>/dev/null
```

Everything you need for asynchronous analysis is now in one directory.

## When the Bug Is Intermittent

See gdb-for-debugging §"Race Condition Methodology" for the reproduce → detect → localize → fix → verify workflow. Short version:

1. `rr record` the program under load; capture a failing run.
2. `rr replay` deterministically; work backwards from the corruption / hang.
3. TSAN if it's a data race.
4. `loom` if it's a Rust primitive.
5. Strategic `std::thread::sleep` at suspect points to widen the race window.

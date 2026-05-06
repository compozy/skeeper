# Multi-Process / Swarm Concurrency

When the concurrency crosses process boundaries — multiple agents, multiple writer processes, multiple containers, a tmux mux + its children — ordinary in-process synchronization no longer applies. Distilled from `mcp-agent-mail-rust`, `ntm`, `wezterm`, and process-triage sessions.

## The Big Idea

**Shared-memory concurrency does not cross process boundaries.** You cannot share a `Mutex` between two Rust processes. You cannot guarantee that a `RwLock` writer will see a consistent view. What you *can* do:

1. **File locks (`flock`).** Cooperative, advisory, cross-process. Everybody has to call it.
2. **Advisory leases with TTL** (our preferred pattern, via MCP Agent Mail). Non-blocking, lease expires if holder crashes.
3. **Single-writer pattern.** One process owns the shared resource; others submit requests via IPC.
4. **Content-addressable storage.** Never update in place; append-only / immutable with atomic rename for publication.
5. **Message passing.** Channels, queues, pipes. No shared state at all.

Every cross-process concurrency incident in this repo traces back to violating one of these.

## Pattern 1 — Agent Mail File Reservations

The canonical deadlock-free advisory-lease model. See [`../agent-mail/SKILL.md`](../../agent-mail/SKILL.md).

```
file_reservation_paths(
    project_key = "/abs/path/project",
    agent_name  = "GreenCastle",
    paths       = ["src/auth/**/*.ts"],
    ttl_seconds = 3600,
    exclusive   = true,
    reason      = "bd-123"
)
```

Returns atomically:

```json
{
  "granted":   ["src/auth/login.ts", "src/auth/session.ts"],
  "conflicts": [
    {"agent": "BlueLake", "paths": ["src/auth/session.ts"]}
  ]
}
```

**Why it's deadlock-free by construction:**

- **No circular wait.** The request is atomic; you either get the grant or you get the conflict list. You never block waiting.
- **No hold-and-wait.** Conflicts are visible before any action. You can abandon the attempt without holding anything.
- **No mutual exclusion on the OS level.** TTL + heartbeat, not a kernel lock.
- **Crash tolerance.** If an agent crashes holding reservations, the TTL expires and the reservations are released.

**Rules for using it correctly:**

1. **Scope patterns tightly.** Reserving `**/*` is a smell. Reserve the specific subtree you're editing.
2. **TTL shorter than any possible operation time.** If the task takes 60 min, TTL should be 90 min, not 6 hours.
3. **Heartbeat if the task is long-lived.** Re-reserve before TTL expires.
4. **Release explicitly when done.** Don't rely on TTL — release immediately so others aren't blocked.
5. **Respect conflicts.** If you see a conflict, back off, message the holder, or wait for TTL. Don't just re-request in a tight loop.

## Pattern 2 — Single-Writer Process

For resources that don't have a natural reservation model (e.g., a SQLite database, a shared config file, a build artifact directory), centralize writes:

```
┌───────────┐  requests   ┌──────────────┐
│ worker 1  │────────────►│              │
├───────────┤             │   WRITER     │───► shared resource
│ worker 2  │────────────►│  (one proc)  │
├───────────┤             │              │
│ worker N  │────────────►│              │
└───────────┘             └──────────────┘
```

Workers never write directly. They submit requests to the writer (via Unix socket, HTTP, MCP, message bus, etc.). The writer owns the resource and serializes operations internally.

**When to use:** SQLite files, append-only logs, build caches, any resource with a tight consistency requirement.

## Pattern 3 — Process Isolation via NTM/tmux

`ntm` spawns each agent in its own tmux pane, which means its own OS process, its own memory, its own locks. Communication is only via:
- MCP (for structured tool calls)
- Agent Mail (for messages + file reservations)
- stdout/stderr (captured by tmux)

**What this buys:**

- **No in-process lock contention.** Agents cannot share a `Mutex` because they don't share memory. The whole class of bugs is gone.
- **Fault isolation.** A hung agent is killed and restarted without affecting others. See the tmux-pane-hung incidents in `ntm/c06045ff`, `ntm/dc0b4cab`, `ntm/9efd1ae4`.
- **Observability.** `tmux capture-pane` gives you the last-known state of each agent for post-mortem.

**Rules:**

- **Wezterm mux is sacred.** Do not kill, restart, or upgrade the mux server casually — it owns every agent session. See `system-performance-remediation` for the incident where `vfs_cache_pressure=50` caused the mux to OOM, destroying 382 agent sessions.
- **Stale panes are OK to kill individually.** Use `zellij delete-all-sessions` or `tmux kill-session` for specific dead sessions, but never the whole mux.

## Pattern 4 — Advisory File Locking with `flock`

When you don't have Agent Mail available (e.g., a non-Rust / non-Python tool), use `flock(2)`:

```bash
(
  flock -x 200 || { echo "busy"; exit 1; }
  # ... do the work ...
) 200> /var/lock/my_resource.lock
```

```rust
use fs2::FileExt;
let file = std::fs::File::create("/var/lock/my_resource.lock")?;
file.lock_exclusive()?;           // blocks until available
// ... do the work ...
file.unlock()?;
```

**Caveats:**

- **Advisory.** Only processes that also call `flock` respect it. A process that doesn't is unaffected.
- **No TTL.** If the holder crashes and the OS doesn't release the lock cleanly, you're stuck.
- **NFS hazards.** `flock` on NFS is unreliable on older implementations; use `fcntl` locks (POSIX) or avoid networked storage.

## Pattern 5 — Atomic Rename for Publication

To update a file "atomically" across processes:

```bash
echo "new content" > /path/to/file.tmp
mv /path/to/file.tmp /path/to/file          # atomic on the same filesystem
```

Readers always see either the old or the new file — never a partial write. Used by our `br sync --flush-only` flow for beads JSONL export.

Limitations:
- Same filesystem required (`mv` is only atomic within a single fs).
- Doesn't help if you need *multi-file* atomicity (that requires a real transaction).

## Pattern 6 — Process Triage for Hung Agents

From `system-performance-remediation`:

```bash
# Find stuck processes
ps -eo pid,etimes,pcpu,args --sort=-etimes | \
  grep -E 'bun test|cargo test|claude|codex' | awk '$2 > 3600'

# Kill with escalation
kill $PID; sleep 3; kill -0 $PID 2>/dev/null && kill -9 $PID
```

**The Kill Hierarchy** (safest first):
1. Zombies
2. Exited tmux/zellij sessions
3. Stuck tests (>12h)
4. Orphaned poll loops
5. Stuck CLI (>5 min)
6. Duplicate builds
7. Old dev servers
8. Stale gemini agents (>24h)
9. Old tmux sessions (no activity)
10. Old agents (>16h)
11. Active agents (<16h) — **High risk**
12. System processes — **NEVER TOUCH**

## Anti-Patterns

- **Multiple writer processes against the same SQLite DB** without coordination. See `DATABASE.md`.
- **Broad file reservation patterns** (`*`, `**/*`, absolute paths) — reserve precisely what you edit.
- **TTL longer than the operation** — hung agents leave leases open.
- **Reservations without release** — rely on explicit release, not TTL expiry.
- **Shared `Mutex` between processes** — not possible; don't try.
- **`flock` on NFS** — use POSIX `fcntl` or avoid networked filesystems for coordination.
- **Killing a writer process** without giving it a chance to flush — use `SIGTERM` then wait, not `SIGKILL` immediately.

## Validation

- [ ] No process writes directly to a shared resource without either a reservation, `flock`, or a single-writer pattern.
- [ ] Every `file_reservation_paths` call has TTL < operation lifetime.
- [ ] Every reservation has a matching `release_file_reservations` in the happy path.
- [ ] SQLite databases have one writer process + many readers.
- [ ] Tmux panes are reaped via `delete-all-sessions` or explicit `kill-session`, never by killing the mux.
- [ ] The Kill Hierarchy from system-performance-remediation is followed for troubleshooting.

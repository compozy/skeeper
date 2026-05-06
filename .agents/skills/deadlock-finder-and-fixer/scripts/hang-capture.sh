#!/bin/bash
# hang-capture.sh — Capture all evidence from a hung process before it dies
# Usage: ./hang-capture.sh <PID>
# Creates /tmp/hang-<timestamp>/ with all captured artifacts

set -euo pipefail

PID="${1:?Usage: hang-capture.sh <PID>}"
TIMESTAMP=$(date +%s)
DIR="/tmp/hang-${TIMESTAMP}"

mkdir -p "$DIR"
echo "Capturing evidence for PID $PID → $DIR"

# Parallel capture (process may die any moment)
{
    ps -Lp "$PID" -o tid,pcpu,pmem,stat,comm --no-headers > "$DIR/threads.txt" 2>/dev/null
    echo "  threads.txt done"
} &

{
    # Try GDB first (best evidence)
    if command -v gdb &>/dev/null; then
        # Check ptrace scope — warn but don't auto-modify (requires sudo)
        ptrace_scope=$(cat /proc/sys/kernel/yama/ptrace_scope 2>/dev/null || echo "unknown")
        if [ "$ptrace_scope" = "1" ]; then
            echo "  WARNING: ptrace_scope=1 (restricted). GDB may fail."
            echo "  Run: sudo sysctl kernel.yama.ptrace_scope=0"
        fi
        gdb_exit=0
        timeout 30s gdb --batch \
            -ex "set pagination off" \
            -ex "thread apply all bt full" \
            -p "$PID" > "$DIR/gdb.txt" 2>&1 || gdb_exit=$?
        if [[ $gdb_exit -ne 0 ]]; then
            echo "GDB failed or timed out (exit $gdb_exit). Partial output may exist." >> "$DIR/gdb.txt"
        fi
        echo "  gdb.txt done"
    else
        echo "gdb not found — install with: apt install gdb" > "$DIR/gdb.txt"
    fi
} &

{
    timeout 5s strace -k -f -p "$PID" -o "$DIR/strace.txt" 2>/dev/null || true
    echo "  strace.txt done"
} &

{
    cat "/proc/$PID/maps" > "$DIR/maps.txt" 2>/dev/null || true
    cat "/proc/$PID/status" > "$DIR/status.txt" 2>/dev/null || true
    cat "/proc/$PID/stack" > "$DIR/stack.txt" 2>/dev/null || true
    echo "  /proc files done"
} &

{
    # Per-thread kernel stacks
    shopt -s nullglob
    for task_dir in "/proc/$PID"/task/*; do
        [[ -d "$task_dir" ]] || continue
        tid="${task_dir##*/}"
        cat "$task_dir/stack" > "$DIR/task_${tid}.txt" 2>/dev/null || true
    done
    shopt -u nullglob
    echo "  task stacks done"
} &

{
    lsof -p "$PID" > "$DIR/lsof.txt" 2>/dev/null || true
    echo "  lsof.txt done"
} &

# Wait for all captures
wait

# Summary
echo ""
echo "Evidence captured in $DIR:"
ls -la "$DIR/"
echo ""
echo "Next steps:"
echo "  1. Check gdb.txt for thread backtraces"
echo "  2. Look for threads in __lll_lock_wait or futex_wait"
echo "  3. Build the lock-wait graph (see gdb-for-debugging skill)"
echo "  4. Classify using the Symptom Triage Table (SKILL.md)"

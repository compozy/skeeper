---
title: Repair workflow for failed syncs
type: feature
---

When a strict hook fails partway through (network drop, auth expiry, sidecar contention), Skeeper now records a resumable transaction at `.git/skeeper/transaction.json` instead of leaving the working tree in an unknown state. The new `skeeper repair` subcommands act on that record:

- `skeeper repair status` shows the active transaction phase and any pending audit bypass.
- `skeeper repair resume` re-runs reconciliation against the recorded plan once the underlying problem is fixed.
- `skeeper repair abort` clears the transaction — only safe before the main index has been mutated.

`skeeper status` also surfaces the repair state inline so you do not need to remember to check.

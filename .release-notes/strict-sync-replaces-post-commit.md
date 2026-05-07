---
title: Strict sync replaces async post-commit
type: breaking
---

The async post-commit hook with its 750 ms budget and `.git/skeeper/queue.json` retry queue is gone. Skeeper now installs strict `pre-commit`, `pre-merge-commit`, and `pre-push` hooks that mirror specs, push the sidecar, write `skeeper.lock`, and stage it **before** Git creates the main commit. If the sidecar push fails, the main commit fails with it.

This is intentional: a committed main change can no longer silently drift from its sidecar.

How to upgrade:

1. Pull the new release.
2. Run `skeeper hooks install` once per clone. This removes the legacy post-commit block, installs the strict blocks, writes `.gitattributes`, and configures the `skeeper.lock` merge driver.
3. Commit the resulting `skeeper.lock` if `skeeper sync` reports new state.
4. If you need to commit during a known-broken sidecar window, use the audited `SKEEPER_SKIP=1` bypass and run `skeeper sync` afterward. `git commit --no-verify` is unsupported.

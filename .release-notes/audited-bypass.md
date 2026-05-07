---
title: Audited bypass with SKEEPER_SKIP=1
type: feature
---

Skipping a hook should leave a paper trail. Setting `SKEEPER_SKIP=1` lets the strict `pre-commit` and `pre-merge-commit` hooks pass without syncing, but every bypass:

1. Records a JSON audit entry at `.git/skeeper/bypass.json` with reason and timestamp.
2. Prints a warning to stderr.
3. Stays visible in `skeeper status`, `skeeper fsck`, and the `pre-push` hook until the next successful `skeeper sync` clears it.

The variable name is configurable via `settings.hooks.allow_skip_env`. `git commit --no-verify` is unsupported because Git skips all hook code and cannot record the audit entry.

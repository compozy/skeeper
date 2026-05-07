---
title: Merge driver for skeeper.lock
type: feature
---

`skeeper.lock` is a structured file with sidecar SHAs that 3-way text merge cannot reason about safely. The new `skeeper merge-driver` command resolves conflicts deterministically by re-running reconciliation against the merged worktree, and `skeeper hooks install` wires it through `.gitattributes` automatically.

Try it:

```bash
skeeper hooks install
git merge other-branch   # any skeeper.lock conflict regenerates instead of aborting
```

When merging outside the hook (for example a rebase resolved by hand), run `skeeper sync` to refresh the lock and stage it.

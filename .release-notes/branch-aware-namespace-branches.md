---
title: Branch-aware namespace history
type: feature
---

Sidecar branches now follow the pattern `<namespace>/__branches__/<source-branch>`, so feature-branch spec history is isolated from `main` automatically. Two engineers iterating on `feature/auth-redesign` see their own sidecar branch; `main` keeps a clean canonical history.

The `__branches__` segment is reserved and rejected as a namespace name to keep the routing unambiguous.

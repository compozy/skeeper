---
title: skeeper hydrate restores from locked commits
type: highlight
---

`skeeper hydrate` no longer reaches for a best-effort branch tip. It reads the exact sidecar commit SHA stored in `skeeper.lock` for the current main commit and restores spec files from that commit. Fresh clones, bisects, and historical checkouts all see the spec state that actually shipped with the code, not whatever happens to be at the head of the namespace branch today.

If you need the live tip for diagnostics, `skeeper log <path> --latest` and `skeeper status` still surface it.

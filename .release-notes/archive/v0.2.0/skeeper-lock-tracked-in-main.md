---
title: skeeper.lock is tracked in the main repo
type: breaking
---

A new `skeeper.lock` file is now committed to the main repository. It pins each main commit to exact sidecar commits per namespace and records sidecar URL, source branch, namespace branch, sidecar commit SHA, content digest, file count, and byte count. The managed hooks write and stage it; `skeeper verify` and the CI Action check it; `skeeper hydrate` restores from it.

How to upgrade:

1. Run `skeeper hooks install` once. The installer also configures the `skeeper.lock` merge driver via `.gitattributes`.
2. Run `skeeper sync` to generate the initial lockfile.
3. Commit `skeeper.lock` alongside your normal change.
4. Add the file to your code-review checklist or grant it auto-merge: it changes on every spec edit.

Do not edit the SHAs in `skeeper.lock` by hand. Use `skeeper sync` or `skeeper merge-driver` to regenerate it.

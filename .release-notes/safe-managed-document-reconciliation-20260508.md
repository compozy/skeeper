---
title: Safe managed-document reconciliation
type: highlight
---

Skeeper now treats managed local documents as protected working-tree state instead of disposable hydrate cache.

This release adds `skeeper diff`, `skeeper reconcile`, `skeeper rescue`, and `skeeper update` so agents and humans can inspect path-level drift, choose an explicit reconciliation strategy, preserve pruned files in `.git/skeeper/rescue/`, and run the common update/verify/hydrate/fsck/hooks workflow through one high-level command.

`skeeper hydrate` now fails closed when local managed files would be overwritten or orphaned. Use `--keep-local`, `--adopt-local`, `--prune-local`, `--merge`, `--ours`, or `--theirs` to make the intended resolution explicit. JSON status commands also return non-zero when `ok=false`, making the new behavior reliable for CI and agent automation.

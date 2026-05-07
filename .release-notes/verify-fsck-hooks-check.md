---
title: Read-only verification commands
type: feature
---

Three new read-only commands prove sidecar state without mutating files or refs:

- `skeeper verify` cross-checks `skeeper.lock` against the sidecar remote. The same path runs inside the managed `pre-push` hook and inside the GitHub Action.
- `skeeper fsck` compares the working tree's spec files against the locked sidecar content and reports drift with structured diagnostic codes.
- `skeeper hooks check` validates that the managed hook blocks are present, ordered last in `pre-commit`, and that the merge driver is configured.

Every command supports `--json` for CI consumption. `verify` and `fsck` accept `--source-branch` to check a specific branch instead of the current `HEAD`.

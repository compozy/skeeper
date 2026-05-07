---
title: New adopt, untrack, and pattern commands
type: feature
---

Three new commands cover the full lifecycle of bringing existing files under sidecar coverage and inspecting how globs route into namespaces:

- `skeeper adopt <path-or-glob>...` mirrors files already in the main index into the sidecar and removes them from main-index tracking in a single transaction. Supports `--dry-run`, `--json`, `--force`, `--commit --message <msg>`.
- `skeeper untrack <path-or-glob>...` reverses adoption: stops tracking matched specs in the main repository after the sidecar has the latest content.
- `skeeper pattern test <glob>` previews which working-tree files a glob would match, scoped to a namespace with `--namespace`.
- `skeeper pattern add <glob> [--namespace <name>] [--exclude <glob>]... [--adopt-existing]` updates `.skeeper.yml`, refreshes the managed `.gitignore` block, and (with `--adopt-existing`) runs the adoption transaction in one step.

All four commands accept `--json` for scripting and `--force` to override the broad-plan guardrails configured under `settings.guardrails`.

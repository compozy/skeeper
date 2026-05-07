---
name: skeeper-project
description: Project operating guide for the `skeeper` Go CLI repository. Use when an LLM/agent works inside `/Users/pedronauck/Dev/projects/skeeper` or changes/reviews `skeeper` code, tests, docs, release workflows, project-local skills, sidecar behavior, config, hooks, matcher, state, CLI/Cobra, or Go tooling. Do not use for unrelated repositories or generic Go help outside `skeeper`.
---

# Skeeper Project

## Purpose

Use this skill as the project-specific operating contract for work in the `skeeper` repository. Prefer current repository instructions, `README.md`, and source code over historical plan docs when behavior differs.

## Workflow

1. Read `/Users/pedronauck/.codex/RTK.md`, then prefix shell commands with `rtk`.
2. Read the session ledger under `.codex/ledger/` if one exists, scan other `*-MEMORY-*.md` files for cross-agent awareness, and keep the active ledger current.
3. Inspect the worktree before edits with `rtk git status --short`. Preserve unrelated dirty files and never run destructive Git commands without explicit user permission.
4. Use `rtk rg` and `rtk rg --files` for local code discovery. Do not use web search for local repository code.
5. Select and read the task skills required by the active work. For Go, CLI, config, logging, or runtime work, read `$golang-pro`; for bug fixes read `$systematic-debugging` and `$no-workarounds`; for tests read `$testing-anti-patterns`; before completion read `$verification-before-completion`.
6. Read `references/domain-map.md` when choosing packages or changing runtime behavior.
7. Read `references/repo-contract.md` before changing code, tests, docs, release files, project-local skills, or workflows.
8. Implement the root-cause change with focused diffs. Prefer existing package boundaries and helpers over new abstractions.
9. Run targeted verification first, then run `rtk make verify` before reporting completion. If lint or modernize rewrites files, inspect the diff and rerun the relevant checks.

## Project Rules

- Keep Cobra commands thin. Put behavior in internal services and test those services directly.
- Pass `context.Context` through runtime boundaries and wrap errors with useful context.
- Use structured parsers and typed data. Avoid ad hoc string parsing when Go libraries or project helpers exist.
- Add dependencies only with `go get`; never edit `go.mod` by hand.
- Treat tests as a bug-finding tool. If a test exposes broken behavior, fix production code instead of weakening the assertion.
- Use real Git integration or E2E tests for sidecar, hook, hydrate, sync, status, and log behavior when final confidence depends on Git semantics.
- Keep `.skeeper.yml` behavior aligned with the current schema: `sidecar`, optional `bootstrap`, and `namespaces[]` with `name`, `patterns`, and optional `exclude`.

## Error Handling

- If `rtk make verify` fails, the task is not complete. Diagnose the root cause and rerun after fixes.
- If the worktree contains unrelated changes, leave them alone and continue around them.
- If repository docs disagree, verify against current source code and tests before choosing behavior.
- If a required external tool is missing, report the exact command and failure, then use the closest project-approved verification that still exercises the changed behavior.

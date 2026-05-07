# Repository Contract

Use this reference before changing `skeeper` code, tests, docs, workflows, or project-local skills.

## Non-Negotiables

- Prefix shell commands with `rtk` after reading `/Users/pedronauck/.codex/RTK.md`.
- Do not run `git restore`, `git checkout`, `git reset`, `git clean`, `git rm`, or other destructive Git commands without explicit user permission.
- Do not touch unrelated dirty files. If a file has unrelated edits, work with them or ask only when they block the task.
- Use `rg` or `rg --files` for local project discovery.
- Inspect dependent package APIs before writing integration code or tests.
- Do not add dependencies by hand in `go.mod`; use `go get`.
- Do not use workarounds, lint suppressions, swallowed errors, timing hacks, or test-only production APIs.
- Do not claim completion until `rtk make verify` passes.

## Skill Dispatch

Load every skill that matches the task domain:

- Go, CLI, config, logging, runtime: `$golang-pro`
- Bug, failure, regression, unexpected behavior: `$systematic-debugging` and `$no-workarounds`
- Tests, fixtures, mocks, assertions: `$testing-anti-patterns` and `$golang-pro`
- TUI or terminal forms: `$bubbletea` and `$golang-pro`
- Concurrency, races, hangs, locks: `$deadlock-finder-and-fixer` and `$golang-pro`
- Refactoring or architecture audit: `$refactoring-analysis` or `$architectural-analysis`
- Security review: `$security-review`
- Documentation: `$documentation-writer`
- Final handoff: `$verification-before-completion`

## Implementation Discipline

- Preserve the single-binary, local-first architecture unless a written techspec says otherwise.
- Keep `cmd/skeeper` and `internal/cli` as thin adapters over internal services.
- Use small interfaces at boundaries such as process execution or I/O.
- Wrap errors with `%w` and match with `errors.Is` or `errors.As`.
- Avoid `panic`, `log.Fatal`, unowned goroutines, ignored errors, and hardcoded config.
- Prefer table-driven tests with subtests. Use `t.TempDir()` for filesystem isolation.
- Mock at I/O boundaries through interfaces, then validate Git behavior with real temporary repositories when behavior depends on Git.

## Verification

Run focused checks while iterating, for example:

```bash
rtk go test ./internal/config ./internal/sidecar ./internal/cli
rtk go test ./test/e2e
```

Before handoff, run:

```bash
rtk make verify
rtk git diff --check
```

Read the output. `make verify` runs formatting, lint, tests with race detection, and build. Lint has zero tolerance, and `make lint` may apply fixes, so inspect the diff after it runs.

## Failure Handling

- If a test reveals a bug, fix the production code unless the test itself is demonstrably wrong.
- If `make verify` fails for an unrelated existing issue, capture exact evidence and still run the strongest targeted checks for the changed area.
- If a doc or plan file conflicts with current code, treat it as historical until current tests or accepted user direction confirm otherwise.

# Domain Map

Use this reference to choose the right files and tests before changing `skeeper` behavior.

## Current Product Contract

`skeeper` versions spec artifacts in a sidecar Git repository while keeping the main repository diff clean. The current public config schema is:

```yaml
sidecar: git@github.com:user/project-specs.git
bootstrap: brew tap compozy/compozy && brew install --cask skeeper
namespaces:
  - name: project
    patterns:
      - "**/SPEC.md"
    exclude:
      - "tmp/**"
```

Unknown keys are rejected. Each namespace owns `patterns` minus `exclude`; effective ownership overlap is an error. Sidecar storage uses `<namespace>/<path>`, and sidecar branches use `<namespace>/__branches__/<source-branch>`.

Historical docs may mention older top-level `directory` or `patterns` fields. Do not reintroduce those unless the user explicitly asks for a migration or compatibility change.

## Package Responsibilities

- `cmd/skeeper`: binary entry point only.
- `internal/cli`: Cobra commands, flag parsing, exit-code mapping, stdout/stderr wiring.
- `internal/cli/inittui`: interactive init form built with Charm `huh`.
- `internal/config`: `.skeeper.yml` loading, validation, normalization, and saving.
- `internal/matcher`: doublestar matching, gitignore-aware discovery, namespace ownership, and overlap detection.
- `internal/gitexec`: context-aware Git helpers and command execution boundaries.
- `internal/hooks`: managed post-commit hook installation.
- `internal/managedblock`: marker-based managed block insertion and removal.
- `internal/state`: durable queue and sync log under `.git/skeeper/`.
- `internal/sidecar`: orchestration for init, hydrate, sync, status, log, namespace branch/path mapping, push/pull, queueing, and hooks.
- `internal/version`: build metadata.
- `test/e2e`: compiled-binary tests with real temporary Git repositories.
- `magefile.go` and `Makefile`: toolchain, formatting, lint, test, build, release snapshot, and JS/Bun checks.

## Runtime Invariants

- `.skeeper.yml` is committed and must be deterministic.
- `.skeeper/` is the local sidecar clone and must stay ignored by the main repo.
- `.git/skeeper/queue.json` stores retry state from failed hook syncs.
- `.git/skeeper/sync.log` is an append-only audit trail.
- The post-commit hook must not block or fail the user's main commit; failed hook sync work is queued.
- Manual `skeeper sync` must drain queued work before fresh sync work.
- `skeeper sync --pull` must reconcile sidecar remote changes before retrying local work.

## Test Selection

- Config schema or validation: `rtk go test ./internal/config`
- Glob matching, gitignore behavior, ownership overlap: `rtk go test ./internal/matcher`
- Git helper behavior: `rtk go test ./internal/gitexec`
- Hook managed blocks: `rtk go test ./internal/hooks ./internal/managedblock`
- Queue and log durability: `rtk go test ./internal/state`
- Init, hydrate, sync, status, log orchestration: `rtk go test ./internal/sidecar ./internal/cli`
- Real lifecycle behavior: `rtk go test ./test/e2e`

Run broader packages when a change crosses boundaries, then finish with `rtk make verify`.

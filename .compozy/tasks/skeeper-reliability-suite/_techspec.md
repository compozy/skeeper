# Skeeper Reliability Suite TechSpec

## MVP Boundary

MVP boundary: tasks 01-12 implement the lockfile-backed local reliability suite for Skeeper, and tasks 13-14 prepare and execute verification and peer review. The MVP includes the central reconciler, `skeeper.lock`, strict managed hooks, `sync`, `adopt`, `untrack`, `pattern`, `fsck`, `verify`, `hooks`, `merge-driver`, `repair`, and a same-repository GitHub Action wrapper.

Post-MVP work is deferred: `redact --history`, daemonized background sync, shell prompt integration, Marketplace-specific Action polish, full config migration tooling, and sidecar history rewrite automation beyond rename preservation. Explicitly out of scope: implementing product code in this spec-authoring pass, preserving the current non-blocking post-commit queue model, using `git commit --no-verify` as a supported bypass, and adding database-backed storage.

## Executive Summary

Skeeper currently mirrors ignored spec files into a sidecar repository through a non-blocking post-commit hook. That keeps commits fast, but it leaves several risk surfaces: adoption of already tracked specs requires manual Git choreography, queued sync failures can be missed, CI has no durable proof that a main commit corresponds to a sidecar state, and health checks do not compare the current staged or working content against the actual sidecar content.

This TechSpec replaces the queue-centered model with a lockfile-backed model. A central reconciler computes ownership, ignored-state, tracked-state, sidecar-state, lockfile-state, and operation plans. A strict `pre-commit` hook runs after existing hook content, mirrors the staged index rather than the working tree, pushes the sidecar, writes and stages `skeeper.lock`, then blocks on failure unless an explicit audited bypass is used. A read-only `pre-push` gate and `skeeper verify` validate the lock against the sidecar remote. `skeeper fsck` diagnoses current working-tree drift against the locked sidecar state.

## Architectural Boundaries

- `internal/reconcile` owns all plan construction. CLI commands, hooks, and CI verification must not recompute namespace ownership, ignored matches, sidecar refs, digests, or guardrail decisions independently.
- `internal/lockfile` owns canonical JSON encoding, decoding, validation, merge-driver regeneration, and namespace digest calculation for `skeeper.lock`.
- `internal/hooks` owns managed hook installation, ordering, migration, and validation only. It must call the public service layer; it must not copy sync, verify, fsck, or repair logic.
- `internal/sidecar` remains the sidecar Git orchestration layer. It applies reconciler plans, performs clone/fetch/switch/commit/push operations, and delegates lock persistence to `internal/lockfile`.
- `internal/state` owns local transaction journals, bypass journals, and repair state under `.git/skeeper/`. The old queue file model is removed from the active workflow.
- `internal/cli` stays thin Cobra wiring. CLI handlers call service methods and render human/JSON output; they do not inspect Git state directly.
- `internal/gitexec` remains the only package that shells out to Git/GitHub commands or uses go-git helpers. Clone, fetch, switch, rebase, commit, push, cat-file, ls-files, check-ignore, and hook-invoked index inspection remain shell-backed because the behavior must match user Git exactly.
- No package may import from `internal/cli` except tests. Core services must be usable by hooks, CLI, Action wrapper tests, and E2E tests without Cobra dependencies.
- The same-repository GitHub Action is a wrapper around the released CLI. It must not reimplement verification in JavaScript or shell beyond authentication/setup.

## Public Interfaces

### CLI Commands

- `skeeper sync [--dry-run] [--json] [--commit --message <msg>]`
- `skeeper adopt <path-or-glob>... [--dry-run] [--json] [--force] [--commit --message <msg>]`
- `skeeper untrack <path-or-glob>... [--dry-run] [--json] [--force] [--commit --message <msg>]`
- `skeeper pattern test <glob> [--namespace <name>] [--json]`
- `skeeper pattern add <glob> [--namespace <name>] [--exclude <glob>]... [--adopt-existing] [--dry-run] [--json] [--force] [--commit --message <msg>]`
- `skeeper fsck [--json] [--source-branch <branch>]`
- `skeeper verify [--json] [--source-branch <branch>]`
- `skeeper hooks install [--json]`
- `skeeper hooks check [--json]`
- `skeeper merge-driver [--json]`
- `skeeper repair status|resume|abort [--json]`
- Existing `hydrate`, `status`, and `log` become lock-aware. `hydrate` and `log` read the locked sidecar commit by default; `log --latest` fetches the namespace branch and labels whether the latest ref diverges from the locked commit.

### Go Interfaces

```go
package reconcile

type RepoRoot string
type NamespaceName string
type SidecarRef string

type Planner interface {
	PlanSync(ctx context.Context, root RepoRoot, opts SyncPlanOptions) (Plan, error)
	PlanAdopt(ctx context.Context, root RepoRoot, targets []string, opts AdoptPlanOptions) (Plan, error)
	PlanUntrack(ctx context.Context, root RepoRoot, targets []string, opts UntrackPlanOptions) (Plan, error)
	PlanPattern(ctx context.Context, root RepoRoot, glob string, opts PatternPlanOptions) (Plan, error)
	PlanVerify(ctx context.Context, root RepoRoot, opts VerifyPlanOptions) (Plan, error)
	PlanFSCK(ctx context.Context, root RepoRoot, opts FSCKPlanOptions) (Plan, error)
}
```

```go
package reconcile

type Plan struct {
	Kind       PlanKind
	Root       RepoRoot
	SidecarURL string
	Namespaces []NamespacePlan
	Operations []Operation
	Warnings   []Diagnostic
	Failures   []Diagnostic
	Guardrails GuardrailReport
}
```

```go
package lockfile

type Store interface {
	Load(root reconcile.RepoRoot) (Lock, error)
	Write(root reconcile.RepoRoot, lock Lock) error
	Digest(ctx context.Context, sidecarDir string, namespace config.Namespace, ref reconcile.SidecarRef) (NamespaceDigest, error)
	RegenerateForMerge(ctx context.Context, root reconcile.RepoRoot) (Lock, error)
}
```

```go
package hooks

type Manager interface {
	Install(ctx context.Context, root reconcile.RepoRoot, opts InstallOptions) (InstallResult, error)
	Check(ctx context.Context, root reconcile.RepoRoot) (CheckResult, error)
}
```

```go
package state

type TransactionStore interface {
	Begin(ctx context.Context, tx Transaction) error
	Current(ctx context.Context) (Transaction, bool, error)
	MarkPhase(ctx context.Context, id string, phase TransactionPhase) error
	Complete(ctx context.Context, id string) error
	Abort(ctx context.Context, id string) error
}
```

```go
package sidecar

type HealthService interface {
	Verify(ctx context.Context, dir string, opts VerifyOptions) (VerifyResult, error)
	FSCK(ctx context.Context, dir string, opts FSCKOptions) (FSCKResult, error)
}
```

## Data Model And Config Rationale

### `.skeeper.yml`

`settings` is a new optional top-level object for operational defaults. It keeps global behavior out of namespace ownership rules.

- `settings.guardrails.max_files`: integer, default `100`; triggers confirmation or `--force` for broad plans.
- `settings.guardrails.max_bytes`: integer, default `10485760`; triggers confirmation or `--force` for large plans.
- `settings.hooks.pre_push_timeout`: duration string, default `30s`; bounds the read-only pre-push gate.
- `settings.hooks.allow_skip_env`: string, default `SKEEPER_SKIP`; names the audited pre-commit bypass environment variable.

Namespaces gain one optional field:

- `respect_gitignore`: boolean, default `true`; when true, root `.gitignore`, nested `.gitignore`, `.git/info/exclude`, and global excludes prune matches. When false, all four Git ignore sources are bypassed for that namespace. Skeeper always excludes `.git/` and `.skeeper/` internals regardless of this setting.

Existing `exclude` remains the only public exclusion model. Negative globs inside `patterns` are rejected so ownership has one semantics. `settings.lockfile.path` is not part of the MVP; the only supported lock path is root-level `skeeper.lock`.

### `skeeper.lock`

`skeeper.lock` is a tracked root file with canonical JSON encoding. It is the CI-verifiable pointer from a main commit to sidecar content. It intentionally does not contain spec file contents or a timestamp that churns every commit.

```json
{
  "version": 1,
  "sidecar": "git@github.com:user/project-specs.git",
  "source_branch": "main",
  "namespaces": [
    {
      "name": "project",
      "sidecar_branch": "project/__branches__/main",
      "commit": "0123456789abcdef0123456789abcdef01234567",
      "digest": "sha256:...",
      "files": 12,
      "bytes": 40960
    }
  ]
}
```

Field rationale:

- `version` enables lock format evolution without adding `.skeeper.yml` schema versioning.
- `sidecar` catches accidental verification against the wrong remote. It is canonicalized on write and verify to a single SSH-style URL with a `.git` suffix.
- `source_branch` records which main branch produced the sidecar branch mapping.
- `namespaces[].name` maps lock entries to config namespaces.
- `namespaces[].sidecar_branch` avoids hidden branch reconstruction in CI output.
- `namespaces[].commit` is the exact sidecar commit expected by the main checkout.
- `namespaces[].digest` is the per-namespace tree digest from sorted `(slash path, size, sha256(content))` entries.
- `namespaces[].files` and `namespaces[].bytes` give reviewable scale signals.

There is no top-level aggregate digest in the MVP. All lock validation is per namespace, and the canonical JSON itself is compared only for stable encoding tests.

### Lockfile Merge Strategy

`skeeper hooks install` registers a `.gitattributes` entry for `skeeper.lock` and configures `skeeper merge-driver` as the merge driver. The merge driver never hand-merges scalar SHA values. It regenerates `skeeper.lock` from the merged staged index by re-running the same reconciler path that `pre-commit` uses, then allows the merge commit's `pre-commit` hook to re-sync and push the sidecar. If the merge driver cannot reach the sidecar remote or a namespace conflict occurs, it leaves a transaction journal and fails with a deterministic repair instruction. Manual lock conflict editing is documented as unsupported.

### Side-Table vs JSON Decision

This CLI has no database. There is no SQLite table, side table, or JSON metadata blob in the runtime. The durable cross-repository contract is `skeeper.lock`, a typed JSON lock artifact. JSON is appropriate here because the file is a canonical, schema-validated interchange artifact committed to Git, not an opaque bag inside a database row. Local transaction and bypass journals are also typed JSON files under `.git/skeeper/` because they are private operational state, not queryable matchable state.

## Safety Invariants

1. A main-repo file may be removed from the Git index by `adopt` or `untrack` only after the relevant sidecar namespace commit has been pushed successfully and the transaction journal records that pushed phase.
2. `pre-commit` mirrors the staged index, not the working tree. It reads tracked staged blobs through Git index APIs and reads ignored/untracked Skeeper-owned spec paths through explicit reconciler ownership rules.
3. `pre-commit` is the only hook allowed to mutate `skeeper.lock`; it must stage the lock before allowing the commit to proceed.
4. The Skeeper managed pre-commit block must run last after preserved user hook content. `hooks check` fails if any non-Skeeper content follows the Skeeper block.
5. `pre-push` must not mutate `refs/heads/*`, the worktree, the index, sidecar commits, or remote refs. It may fetch into `refs/remotes/*` and update `FETCH_HEAD` with a bounded timeout.
6. `verify` validates `skeeper.lock` against the sidecar remote and does not require local hooks.
7. `fsck` compares current working-tree specs against the locked sidecar commit and never mutates files or refs.
8. The namespace digest is deterministic across platforms: slash-separated paths, sorted entries, byte-for-byte file hashes, stable JSON output, and no timestamp participation.
9. Namespace ownership is unique. Overlap is an error; no order-based precedence is allowed.
10. `exclude` is the only public exclusion mechanism; `!patterns` in `patterns` are rejected.
11. Sidecar branch switching must abort if the sidecar worktree or index is dirty unless the dirtiness belongs to a current transaction journal. It must never hard reset or clean user edits silently.
12. Transaction journals are resumable or abortable; the system must not silently ignore an incomplete multi-step operation.
13. Broad glob guardrails cannot be bypassed in non-TTY mode without `--force`.
14. Commit creation from Skeeper is always opt-in. TTY defaults to not committing, and non-TTY requires `--commit --message <msg>`.
15. `Main-Commit:` trailers are removed from sidecar commits in the pre-commit model. Main-to-sidecar correlation is the `skeeper.lock` history in the main repository.
16. The only supported bypass for strict pre-commit is `SKEEPER_SKIP=1`. The hook records `.git/skeeper/bypass.json`, prints a warning, and `pre-push`, `status`, `fsck`, and `verify` surface stale-lock diagnostics until `skeeper sync` repairs it. Documentation explicitly rejects `git commit --no-verify` as an unsupported bypass.

## Command Semantics

### `sync`

Manual `skeeper sync` builds a plan from the working tree because it is an explicit repair/sync command, not a commit hook. It fetches and rebases each sidecar namespace branch before push. On non-fast-forward rejection it retries once after fetch/rebase. If the rebase conflicts, it records the namespace and phase in a transaction journal and exits with a deterministic `repair resume` instruction. After a successful push it verifies the pushed sidecar tree, writes and stages `skeeper.lock`, then offers optional commit in TTY.

### `pre-commit`

The managed `pre-commit` block runs last. It builds the mirror set from the staged index plus explicitly owned ignored/untracked spec files. It fetches and rebases sidecar branches before push using the same contention algorithm as `sync`. It drops the legacy `Main-Commit:` trailer and writes sidecar commit messages that identify namespace, source branch, and namespace digest.

### `untrack`

`skeeper untrack <path-or-glob>...` means "stop tracking this file in the main repository while preserving Skeeper sidecar coverage." It never deletes the sidecar copy as its primary action. For each target, it verifies a single configured namespace owner, syncs and pushes that namespace first, updates the managed `.gitignore` block if needed, runs `git rm --cached` only for tracked main files, and stages `skeeper.lock` plus index changes. If a target would be untracked in both main and sidecar because it is no longer owned by any namespace, the command fails unless a future explicit sidecar-delete command is introduced; `--force` cannot bypass loss of both coverage paths.

### `pattern add`

`pattern add` updates `.skeeper.yml` and the managed `.gitignore` block. With `--adopt-existing`, it runs the same adoption transaction as `adopt` after config validation. If adding a pattern creates overlap, it fails and suggests `exclude:` edits rather than adding precedence. Removing or adding patterns is documented as a main-repo mutation because it changes `.skeeper.yml`, `.gitignore`, and usually `skeeper.lock`.

### `repair`

`repair status` reports the current transaction or bypass journal. `repair resume` requires network access for phases at or after sidecar push and reuses the recorded plan inputs; it fails if the config no longer matches the recorded owner set. `repair abort` is allowed before main index mutation; after main index mutation it refuses to roll back automatically and prints the exact files that require human review. Conflicting transactions are not allowed; a new mutating command fails until the existing journal is completed or aborted.

## Implementation Steps

1. Add lockfile package with canonical JSON load/write/validate, namespace digest calculation, canonical sidecar URL comparison, and merge-driver regeneration.
2. Add reconciler package and migrate existing matcher/config/sidecar route resolution into shared plan construction.
3. Replace queue-centered hook behavior with managed `pre-commit`, `pre-push`, and `merge-driver` installation/checking. `hooks install` first removes any Skeeper managed block from `post-commit`, `pre-commit`, and `pre-push`, then writes exactly one current managed block where needed.
4. Update `sync` to apply reconciler plans, fetch/rebase/push sidecar namespaces, verify post-push parity, write/stage `skeeper.lock`, and offer optional commit.
5. Implement the staged-index pre-commit path using Git index reads, not working-tree reads, and make the Skeeper block the final managed pre-commit block.
6. Implement explicit `SKEEPER_SKIP=1` bypass journaling and stale-lock diagnostics in `status`, `fsck`, `verify`, and `pre-push`.
7. Implement `pattern test` and `pattern add`, including namespace selection, `--adopt-existing`, `--exclude`, guardrails, dry-run, JSON output, and managed `.gitignore` side effects.
8. Implement `adopt` and `untrack` over exact paths and globs, with owner validation, sidecar push-before-main-index mutation, transaction journaling, and optional commit.
9. Implement `verify` against sidecar remote and `fsck` against the lock commit.
10. Make `hydrate`, `log`, and `status` lock-aware. `log --latest` labels latest-vs-locked divergence.
11. Preserve renames where possible by applying exact-hash path moves from the previous locked tree before mirror writes; fall back to add/delete with a structured `rename.fallback` warning in human output, JSON output, and `slog`.
12. Add same-repository GitHub Action wrapper that downloads the released Skeeper binary. Credential precedence is: explicit SSH private key first via tempfile plus `GIT_SSH_COMMAND`, then explicit token for HTTPS. Secrets are never printed, and logs redact credential-derived URLs.
13. Update README and command reference. Required README changes include the highlights bullet, How It Works section and diagram, failed-sync recovery section, local-only state table, CLI reference, CI Action usage, hook model explanation, and troubleshooting.
14. Run full unit, integration, E2E, actionlint, and `make verify` validation.
15. Run a follow-up Claude/Opus peer-review round before implementation approval.

## Test Strategy

- Config tests cover `settings`, `respect_gitignore`, rejection of `!patterns`, guardrail defaults, sidecar URL canonicalization, and overlap errors.
- Lockfile tests cover canonical JSON ordering, validation failures, digest stability, platform-independent path handling, sidecar remote mismatch, missing sidecar commit, tampered digest, and merge-driver regeneration.
- Reconciler unit tests cover ownership, ignored matches, excluded matches, tracked/untracked state, staged-index reads, broad glob guardrails, dry-run plans, and JSON output shape.
- Real-Git sidecar tests cover sync writing and staging `skeeper.lock`, verify success against a fresh bare remote, verify failure on tampered digest, verify failure on missing sidecar commit, verify failure on remote URL mismatch, fsck detecting working-tree drift, hydrate from lock commit, and log from lock commit plus `--latest`.
- Hook tests cover managed `pre-commit` and `pre-push` idempotence, preservation of existing hook content, removal/replacement of old post-commit managed blocks, Skeeper pre-commit block ordering after formatter/Husky content, pre-commit blocking on failed sidecar push, two-writer sidecar contention with fetch/rebase, audited `SKEEPER_SKIP=1`, and pre-push read-only behavior.
- Pre-push read-only tests snapshot the main worktree, index, and `refs/heads/*` before and after the hook and assert they are byte-identical. The test allows changes only under `refs/remotes/*` and `FETCH_HEAD`.
- Adoption and untrack tests cover sidecar push before `git rm --cached`, dirty unrelated file rejection, glob expansion across namespaces, managed `.gitignore` updates, non-TTY `--force`, no loss of both main and sidecar coverage, and transaction resume/abort.
- Pattern tests cover `pattern test`, `pattern add --adopt-existing`, namespace inference, multiple-namespace `--namespace` requirement, overlap failure, ignored match reporting, and explicit `respect_gitignore: false` behavior.
- Git history tests cover `git commit --amend` with pre-commit rerun, merge commits with `skeeper merge-driver`, and rebase replay of commits that previously updated `skeeper.lock`.
- GitHub Action tests cover `actionlint` and a hermetic Action job that runs `skeeper verify` against a local bare remote in CI. This is mandatory, not conditional on local infrastructure.
- Final verification commands: `rtk go test ./...`, `rtk go run github.com/rhysd/actionlint/cmd/actionlint@latest`, `rtk git diff --check`, and `rtk make verify`.

## Agent Manageability Plan

- Every plan-backed command supports `--json` for deterministic machine inspection.
- `status`, `verify`, `fsck`, `hooks check`, `repair status`, and bypass diagnostics expose actionable categories, stable paths, and suggested recovery commands.
- Non-TTY operation is explicit: no prompts, no implicit commits, and broad plans require `--force`.
- Error messages name the recovery command when possible, especially `skeeper sync`, `skeeper repair resume`, `skeeper hooks install`, and `skeeper merge-driver`.
- Rename fallback emits stable `rename.fallback` diagnostics in human output, JSON output, and `slog`.

## Config Lifecycle

- New config fields are optional and have runtime defaults.
- Unknown fields remain rejected.
- MVP does not add `schema_version`.
- README examples include `settings` only when explaining advanced guardrails/hooks; default quick start stays minimal.
- Tests prove old namespace configs without `settings` normalize with defaults, even though alpha breaking changes remain allowed.

## Web And Docs Impact

- README sections to rewrite or remove in the same PR:
  - Highlights bullet that currently promises a non-blocking post-commit hook.
  - How It Works prose and Mermaid diagram.
  - Recover from a failed sync section.
  - Local-only state table, replacing queue state with transaction and bypass journals.
  - CLI reference for `sync`, `hydrate`, `status`, `log`, `hooks`, `verify`, `fsck`, `repair`, `pattern`, `adopt`, and `untrack`.
  - New CI Action usage section.
  - Troubleshooting for `SKEEPER_SKIP=1`, sidecar contention, merge-driver failures, and lock mismatch.
- Release notes must call out the breaking operational change from post-commit queue to pre-commit lockfile.
- No website, generated API docs, MCP resources, extension manifests, or public HTTP endpoints are affected.

## Assumptions And Defaults

- This is one large PR, not separate feature PRs and not feature-flagged.
- Breaking alpha behavior is allowed where it simplifies the new reliability model.
- `redact --history` is deferred.
- `skeeper.lock` is tracked in the main repo and uses canonical JSON despite having no `.json` extension.
- `sync` manual updates and stages `skeeper.lock`; if it stages changes in TTY it offers optional commit with default "do not commit".
- `pre-commit` uses staged index content; manual `sync` uses working-tree content because it is an explicit repair command.
- `verify` checks lock-to-sidecar parity; `fsck` checks working-tree-to-lock parity.
- The GitHub Action lives in this repository and downloads the released binary for the action ref/tag.

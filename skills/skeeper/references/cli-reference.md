# Skeeper CLI Reference

Authoritative flag-level reference for every `skeeper` subcommand. Cross-checked against `internal/cli/root.go` and the per-command files in `internal/cli/`.

## Global behavior

- All commands accept Cobra's `--help`.
- Mutating commands accept `--dry-run` (no writes), `--json` (deterministic JSON output), and `--force` (override broad-plan guardrails) where listed below.
- Hidden flags such as `--hook` exist for the managed hooks to invoke the same code paths as user-facing commands. Do not pass them by hand.
- Exit code `0` indicates success. Any other code indicates a structured error; pair with `--json` to capture the diagnostic stream.

## `skeeper init`

Bootstrap `.skeeper.yml`, the sidecar repository, the managed `.gitignore` block, and the merge driver entry. Interactive when run without flags.

| Flag                             | Description                                           |
| -------------------------------- | ----------------------------------------------------- |
| `--sidecar <url>`                | Use an existing sidecar repo instead of creating one  |
| `--sidecar-name <name>`          | Repo name when creating a new GitHub sidecar via `gh` |
| `--visibility <public\|private>` | Visibility for a freshly created sidecar              |
| `--namespace <name>`             | Namespace name for this project                       |
| `--patterns <glob>`              | Repeatable; one glob per flag invocation              |
| `--bootstrap <command>`          | Optional bootstrap command stored in `.skeeper.yml`   |

Creating a fresh sidecar requires `gh` on `PATH`. Reusing an existing one requires only `git`.

## `skeeper hydrate`

Restore matched spec files into the working tree from the sidecar commits recorded in `skeeper.lock`. Used on fresh clones, after bisects, and when the working copy diverges from the lock. Takes no flags. Always reads from the locked commit, never the latest branch tip.

## `skeeper sync`

Mirror current specs into the sidecar and stage `skeeper.lock`. The hook variant is invoked automatically by `pre-commit`/`pre-merge-commit`; the manual variant operates on the working tree, not the index.

| Flag              | Description                                              |
| ----------------- | -------------------------------------------------------- |
| `--dry-run`       | Show the plan without mutating sidecar or lockfile       |
| `--json`          | Write machine-readable output                            |
| `--commit`        | Commit the staged skeeper changes in the main repository |
| `--message <msg>` | Required commit message when `--commit` is used          |
| `--force`         | Allow plans that exceed `settings.guardrails`            |
| `--hook`          | (hidden) Hook-mode entry point used by managed hooks     |

Output (text) lists each namespace with its branch, changed-file count, short sidecar SHA, and digest.

## `skeeper adopt <path-or-glob>...`

Move existing main-tracked specs under sidecar coverage in a single transaction.

| Flag              | Description                                  |
| ----------------- | -------------------------------------------- |
| `--dry-run`       | Show the plan without mutating files         |
| `--json`          | Machine-readable output                      |
| `--force`         | Override guardrails                          |
| `--commit`        | Commit staged changes in the main repository |
| `--message <msg>` | Commit message used with `--commit`          |

## `skeeper untrack <path-or-glob>...`

Reverse `adopt`. The sidecar still receives the latest content before the main repo stops tracking the file. Same flags as `adopt`.

## `skeeper pattern test <glob>`

Show every working-tree file the glob would match.

| Flag                 | Description                                     |
| -------------------- | ----------------------------------------------- |
| `--namespace <name>` | Restrict matching to a single namespace's rules |
| `--json`             | Machine-readable output                         |

## `skeeper pattern add <glob>`

Add a glob to a namespace, update `.skeeper.yml`, refresh the managed `.gitignore` block, and optionally adopt files matched by the new pattern.

| Flag                           | Description                                                   |
| ------------------------------ | ------------------------------------------------------------- |
| `--namespace <name>`           | Namespace receiving the new glob                              |
| `--exclude <glob>`             | Repeatable exclude added with the pattern                     |
| `--adopt-existing`             | Run the adoption transaction on files matched by the new glob |
| `--dry-run`                    | Show planned writes                                           |
| `--json`                       | Machine-readable output                                       |
| `--force`                      | Override guardrails                                           |
| `--commit` / `--message <msg>` | Commit staged changes                                         |

## `skeeper status`

Show sidecar URL, source branch, lock state, per-namespace digests, repair state, audit bypass, and structured diagnostics.

| Flag     | Description             |
| -------- | ----------------------- |
| `--json` | Machine-readable output |

Per-namespace block reports: `name`, `branch`, locked SHA, last sync SHA + age, remote, tracked-file count.

## `skeeper log <path>`

Show sidecar history for a single spec file.

| Flag                       | Description                                                |
| -------------------------- | ---------------------------------------------------------- |
| `--latest`                 | Read the namespace branch tip instead of the locked commit |
| `--source-branch <branch>` | Inspect a specific source branch                           |

## `skeeper fsck`

Compare working-tree specs against the locked sidecar content. Read-only; never mutates files or refs.

| Flag                       | Description                  |
| -------------------------- | ---------------------------- |
| `--json`                   | Machine-readable diagnostics |
| `--source-branch <branch>` | Expected source branch       |

Diagnostic codes are stable identifiers (e.g. `lock_missing`, `digest_mismatch`) suitable for CI assertions.

## `skeeper verify`

Validate `skeeper.lock` against the sidecar remote. Same code path as the managed `pre-push` hook and the GitHub Action.

| Flag                       | Description                    |
| -------------------------- | ------------------------------ |
| `--json`                   | Machine-readable output        |
| `--source-branch <branch>` | Expected source branch         |
| `--hook`                   | (hidden) Hook-mode entry point |

## `skeeper hooks install`

Remove legacy post-commit blocks, install strict `pre-commit` / `pre-merge-commit` / `pre-push` blocks, write `.gitattributes`, and configure the `skeeper.lock` merge driver. Idempotent.

| Flag     | Description                                            |
| -------- | ------------------------------------------------------ |
| `--json` | Machine-readable output reporting installed hook paths |

## `skeeper hooks check`

Validate that the managed hook blocks are present, that `pre-commit` is ordered last, that `pre-merge-commit` carries the sync block, and that the merge driver is configured.

| Flag     | Description             |
| -------- | ----------------------- |
| `--json` | Machine-readable output |

## `skeeper merge-driver [base current other]`

Regenerate `skeeper.lock` during merges. Called automatically by Git via the `.gitattributes` entry installed by `skeeper hooks install`. With no args, regenerates the lock for the current working tree; with three args (`%O %A %B`), Git invokes it with the three side files of the merge.

| Flag     | Description             |
| -------- | ----------------------- |
| `--json` | Machine-readable output |

## `skeeper repair status`

Show the active resumable transaction and any pending audit bypass.

| Flag     | Description             |
| -------- | ----------------------- |
| `--json` | Machine-readable output |

Output reports the transaction `id`, `phase`, and the most recent `bypass.reason` if present.

## `skeeper repair resume`

Re-run reconciliation against the recorded plan once the underlying issue is fixed.

## `skeeper repair abort`

Clear the recorded transaction. Only safe before main-index mutation; use `resume` once the index has been touched.

## `skeeper version`

Print Skeeper's build metadata: semver tag, commit SHA, and build date (from `-X` linker flags).

## State files written by Skeeper

- `<repo>/skeeper.lock` — committed; structured lockfile keyed by namespace.
- `<repo>/.git/skeeper/transaction.json` — local-only resumable transaction.
- `<repo>/.git/skeeper/bypass.json` — local-only audited bypass entry.
- `<repo>/.skeeper/` — gitignored worktree of the sidecar checkout.
- `<repo>/.gitattributes` — managed entry routing `skeeper.lock` through `skeeper merge-driver`.
- `<repo>/.gitignore` — managed `# >>> skeeper >>>` block listing namespace patterns and `.skeeper/`.

Local `.git/skeeper/*` files are never committed and are reset by `skeeper repair abort` (transaction) or by a successful `skeeper sync` (bypass).

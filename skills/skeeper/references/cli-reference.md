# Skeeper CLI Reference

Authoritative flag-level reference for the public `skeeper` command surface. Cross-checked against `internal/cli/root.go` and the per-command files in `internal/cli/`.

## Global Behavior

- All commands accept Cobra's `--help`.
- Mutating commands accept `--dry-run`, `--json`, and `--force` where listed below.
- Exit code `0` indicates success. Any other code indicates a structured error; pair with `--json` to capture diagnostics.
- Git hook and merge-driver plumbing is hidden under `skeeper internal`. It is not part of the user workflow surface.
- Maintenance check: update this reference from `skeeper --help` and each public subcommand's `--help` whenever `internal/cli/root.go` changes.

## Public Command Surface

```text
skeeper init
skeeper status
skeeper pull
skeeper push
skeeper sync
skeeper restore
skeeper track
skeeper untrack
skeeper repair
skeeper log
skeeper version
```

Cobra also provides `skeeper completion` for shell completion scripts.

## `skeeper init`

Bootstrap `.skeeper.yml`, the sidecar repository, managed hooks, the managed `.gitignore` block, and merge-driver wiring. Interactive when run without flags in a terminal.

| Flag                                       | Description                                           |
| ------------------------------------------ | ----------------------------------------------------- |
| `--sidecar <url>`                          | Use an existing sidecar repo instead of creating one  |
| `--sidecar-name <name>`                    | Repo name when creating a new GitHub sidecar via `gh` |
| `--visibility <public\|private\|internal>` | Visibility for a freshly created sidecar              |
| `--namespace <name>`                       | Namespace name for this project                       |
| `--track <glob>`                           | Repeatable managed spec glob                          |
| `--patterns <glob>`                        | Compatibility spelling for `--track`                  |
| `--bootstrap <command>`                    | Optional bootstrap command stored in `.skeeper.yml`   |

Creating a fresh sidecar requires `gh` on `PATH`. Reusing an existing one requires only `git`.

## `skeeper status`

Show sidecar URL, source branch, lock state, hook health, per-namespace counts, audit bypass state, active repair transaction state, diagnostics, and the next action.

| Flag      | Description                                       |
| --------- | ------------------------------------------------- |
| `--json`  | Machine-readable output                           |
| `--check` | Exit non-zero when Skeeper health requires action |
| `--paths` | Include path-level drift details in text and JSON |

Path classes include `unchanged`, `missing_local`, `local_only`, `local_modified`, `sidecar_modified`, `both_modified_conflict`, `namespace_removed`, and `config_unowned`.

`status --check --json` is the CI-friendly lock/sidecar/hook health check.

## `skeeper pull`

Fetch sidecar refs, materialize remote docs into the working tree, preserve local-only docs, and update `skeeper.lock` when the pull succeeds. By default it also fast-forwards the main repository when safe.

| Flag       | Description                                 |
| ---------- | ------------------------------------------- |
| `--json`   | Write machine-readable output               |
| `--no-git` | Skip main-repository fetch and fast-forward |

Use `pull` when another clone has published docs and this worktree needs them locally.

## `skeeper push`

Publish local managed docs into the sidecar, write `skeeper.lock`, and stage the lockfile. Default push is non-destructive: remote-only sidecar files are preserved.

| Flag              | Description                                              |
| ----------------- | -------------------------------------------------------- |
| `--dry-run`       | Show the plan without mutating sidecar or lockfile       |
| `--json`          | Write machine-readable output                            |
| `--commit`        | Commit the staged Skeeper changes in the main repository |
| `--message <msg>` | Required commit message when `--commit` is used          |
| `--force`         | Allow plans that exceed `settings.guardrails`            |
| `--prune`         | Explicitly delete remote-only files absent locally       |

Use `--prune` only when the local set is intentionally authoritative.

## `skeeper sync`

Run a sidecar pull, then a push, so disjoint docs from multiple clones converge to the union.

| Flag              | Description                                              |
| ----------------- | -------------------------------------------------------- |
| `--dry-run`       | Show the push plan without mutating sidecar or lockfile  |
| `--json`          | Write machine-readable output                            |
| `--commit`        | Commit the staged Skeeper changes in the main repository |
| `--message <msg>` | Required commit message when `--commit` is used          |
| `--force`         | Allow plans that exceed `settings.guardrails`            |
| `--prune`         | Explicitly delete remote-only files absent locally       |

Common two-clone flow:

```bash
# clone A
skeeper sync
git commit -m "skeeper: sync docs"
git push

# clone B
git pull
skeeper sync
git commit -m "skeeper: sync docs"
git push
```

## `skeeper restore`

Restore exact docs from the sidecar commits recorded in `skeeper.lock`. Existing local content at restored paths is preserved in rescue storage before overwrite.

| Flag        | Description                         |
| ----------- | ----------------------------------- |
| `--all`     | Restore every locked managed path   |
| `--dry-run` | Show the plan without writing files |
| `--json`    | Write machine-readable output       |

Forms:

```bash
skeeper restore <path...>
skeeper restore --all
```

Use `restore` for local recovery from the lock. Use `pull` for latest remote sidecar state.

## `skeeper track <glob>`

Add a glob to a namespace, update `.skeeper.yml`, refresh the managed `.gitignore` block, and optionally publish existing matching files.

| Flag                           | Description                                    |
| ------------------------------ | ---------------------------------------------- |
| `--namespace <name>`           | Namespace receiving the new glob               |
| `--exclude <glob>`             | Repeatable exclude added with the tracked glob |
| `--sync`                       | Publish existing files matched by the new glob |
| `--dry-run`                    | Show planned writes                            |
| `--json`                       | Machine-readable output                        |
| `--force`                      | Override guardrails                            |
| `--commit` / `--message <msg>` | Commit staged changes                          |

## `skeeper untrack <path-or-glob>...`

Stop tracking specs in the main repository after sidecar sync. The sidecar receives the latest content before the main repo stops tracking the target.

| Flag                           | Description                                  |
| ------------------------------ | -------------------------------------------- |
| `--dry-run`                    | Show the plan without mutating files         |
| `--json`                       | Machine-readable output                      |
| `--force`                      | Override guardrails                          |
| `--commit` / `--message <msg>` | Commit staged changes in the main repository |

## `skeeper repair`

Diagnose and safely repair local Skeeper state. This is the only public recovery door.

| Flag      | Description                           |
| --------- | ------------------------------------- |
| `--check` | Diagnose without writing safe repairs |
| `--json`  | Machine-readable output               |

`repair` handles hook drift, strict-hook bypasses, interrupted transactions, missing local sidecar objects, stale bypass records after a healthy sync, and rescue reporting. It stops when a human must choose between conflicting local and sidecar data.

## `skeeper log <path>`

Show sidecar history for a single spec file.

| Flag                       | Description                                                |
| -------------------------- | ---------------------------------------------------------- |
| `--latest`                 | Read the namespace branch tip instead of the locked commit |
| `--source-branch <branch>` | Inspect a specific source branch                           |

## `skeeper version`

Print Skeeper's build metadata: semver tag, commit SHA, and build date from linker flags.

## State Files Written by Skeeper

- `<repo>/skeeper.lock` — committed; structured lockfile keyed by namespace.
- `<repo>/.git/skeeper/transaction.json` — local-only resumable transaction.
- `<repo>/.git/skeeper/bypass.json` — local-only audited bypass entry.
- `<repo>/.git/skeeper/hydration.json` — local-only base journal for merge-aware restores.
- `<repo>/.git/skeeper/rescue/<id>/` — local-only files preserved before overwrite or prune operations.
- `<repo>/.skeeper/` — gitignored worktree of the sidecar checkout.
- `<repo>/.gitattributes` — managed entry routing `skeeper.lock` through hidden merge-driver plumbing.
- `<repo>/.gitignore` — managed `# >>> skeeper >>>` block listing namespace patterns and `.skeeper/`.

Local `.git/skeeper/*` files are never committed and are repaired by `skeeper repair` or cleared by a successful `skeeper sync` when appropriate.

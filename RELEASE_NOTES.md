## 0.2.0 - 2026-05-07


### 🎉 Features

- Support few new commands

- **BREAKING:** support few new commands


### 📚 Documentation

- Update readme

- Remove skill from readme

- Remove wrong skill

- Improve readme and skill

- Update readme

### Release Notes

#### Breaking Changes

##### skeeper.lock is tracked in the main repo
A new `skeeper.lock` file is now committed to the main repository. It pins each main commit to exact sidecar commits per namespace and records sidecar URL, source branch, namespace branch, sidecar commit SHA, content digest, file count, and byte count. The managed hooks write and stage it; `skeeper verify` and the CI Action check it; `skeeper hydrate` restores from it.

How to upgrade:

1. Run `skeeper hooks install` once. The installer also configures the `skeeper.lock` merge driver via `.gitattributes`.
2. Run `skeeper sync` to generate the initial lockfile.
3. Commit `skeeper.lock` alongside your normal change.
4. Add the file to your code-review checklist or grant it auto-merge: it changes on every spec edit.

Do not edit the SHAs in `skeeper.lock` by hand. Use `skeeper sync` or `skeeper merge-driver` to regenerate it.

##### Strict sync replaces async post-commit
The async post-commit hook with its 750 ms budget and `.git/skeeper/queue.json` retry queue is gone. Skeeper now installs strict `pre-commit`, `pre-merge-commit`, and `pre-push` hooks that mirror specs, push the sidecar, write `skeeper.lock`, and stage it **before** Git creates the main commit. If the sidecar push fails, the main commit fails with it.

This is intentional: a committed main change can no longer silently drift from its sidecar.

How to upgrade:

1. Pull the new release.
2. Run `skeeper hooks install` once per clone. This removes the legacy post-commit block, installs the strict blocks, writes `.gitattributes`, and configures the `skeeper.lock` merge driver.
3. Commit the resulting `skeeper.lock` if `skeeper sync` reports new state.
4. If you need to commit during a known-broken sidecar window, use the audited `SKEEPER_SKIP=1` bypass and run `skeeper sync` afterward. `git commit --no-verify` is unsupported.

#### Features

##### New adopt, untrack, and pattern commands
Three new commands cover the full lifecycle of bringing existing files under sidecar coverage and inspecting how globs route into namespaces:

- `skeeper adopt <path-or-glob>...` mirrors files already in the main index into the sidecar and removes them from main-index tracking in a single transaction. Supports `--dry-run`, `--json`, `--force`, `--commit --message <msg>`.
- `skeeper untrack <path-or-glob>...` reverses adoption: stops tracking matched specs in the main repository after the sidecar has the latest content.
- `skeeper pattern test <glob>` previews which working-tree files a glob would match, scoped to a namespace with `--namespace`.
- `skeeper pattern add <glob> [--namespace <name>] [--exclude <glob>]... [--adopt-existing]` updates `.skeeper.yml`, refreshes the managed `.gitignore` block, and (with `--adopt-existing`) runs the adoption transaction in one step.

All four commands accept `--json` for scripting and `--force` to override the broad-plan guardrails configured under `settings.guardrails`.

##### Audited bypass with SKEEPER_SKIP=1
Skipping a hook should leave a paper trail. Setting `SKEEPER_SKIP=1` lets the strict `pre-commit` and `pre-merge-commit` hooks pass without syncing, but every bypass:

1. Records a JSON audit entry at `.git/skeeper/bypass.json` with reason and timestamp.
2. Prints a warning to stderr.
3. Stays visible in `skeeper status`, `skeeper fsck`, and the `pre-push` hook until the next successful `skeeper sync` clears it.

The variable name is configurable via `settings.hooks.allow_skip_env`. `git commit --no-verify` is unsupported because Git skips all hook code and cannot record the audit entry.

##### Branch-aware namespace history
Sidecar branches now follow the pattern `<namespace>/__branches__/<source-branch>`, so feature-branch spec history is isolated from `main` automatically. Two engineers iterating on `feature/auth-redesign` see their own sidecar branch; `main` keeps a clean canonical history.

The `__branches__` segment is reserved and rejected as a namespace name to keep the routing unambiguous.

##### Official compozy/skeeper GitHub Action
The repo now ships a same-repository GitHub Action (`action.yml`) that downloads the released Skeeper binary for the requested version and delegates to the CLI. Default arguments run `skeeper verify` so pull requests fail when `skeeper.lock` and the sidecar remote disagree.

```yaml
- uses: compozy/skeeper@v0.2.0
  with:
    args: |
      verify
      --json
    ssh-private-key: ${{ secrets.SKEEPER_SSH_PRIVATE_KEY }}
```

Credential precedence: `ssh-private-key` writes a temp key and sets `GIT_SSH_COMMAND`; otherwise `token` configures HTTPS GitHub credentials; otherwise the runner's existing Git/SSH config is used. Secrets are masked before configuration, the temp key is wiped on `always()` cleanup, and Linux/macOS/Windows on amd64/arm64 are all resolved through the same release manifest.

##### Merge driver for skeeper.lock
`skeeper.lock` is a structured file with sidecar SHAs that 3-way text merge cannot reason about safely. The new `skeeper merge-driver` command resolves conflicts deterministically by re-running reconciliation against the merged worktree, and `skeeper hooks install` wires it through `.gitattributes` automatically.

Try it:

```bash
skeeper hooks install
git merge other-branch   # any skeeper.lock conflict regenerates instead of aborting
```

When merging outside the hook (for example a rebase resolved by hand), run `skeeper sync` to refresh the lock and stage it.

##### skeeper log --latest
`skeeper log <path>` reads the locked sidecar commit by default, matching `skeeper hydrate`. The new `--latest` flag fetches the namespace branch and reads its current tip instead, which is useful when investigating why the working tree disagrees with the lockfile or when reviewing in-flight changes from another contributor before they land in `main`.

##### Repair workflow for failed syncs
When a strict hook fails partway through (network drop, auth expiry, sidecar contention), Skeeper now records a resumable transaction at `.git/skeeper/transaction.json` instead of leaving the working tree in an unknown state. The new `skeeper repair` subcommands act on that record:

- `skeeper repair status` shows the active transaction phase and any pending audit bypass.
- `skeeper repair resume` re-runs reconciliation against the recorded plan once the underlying problem is fixed.
- `skeeper repair abort` clears the transaction — only safe before the main index has been mutated.

`skeeper status` also surfaces the repair state inline so you do not need to remember to check.

##### Read-only verification commands
Three new read-only commands prove sidecar state without mutating files or refs:

- `skeeper verify` cross-checks `skeeper.lock` against the sidecar remote. The same path runs inside the managed `pre-push` hook and inside the GitHub Action.
- `skeeper fsck` compares the working tree's spec files against the locked sidecar content and reports drift with structured diagnostic codes.
- `skeeper hooks check` validates that the managed hook blocks are present, ordered last in `pre-commit`, and that the merge driver is configured.

Every command supports `--json` for CI consumption. `verify` and `fsck` accept `--source-branch` to check a specific branch instead of the current `HEAD`.

#### Highlights

##### skeeper hydrate restores from locked commits
`skeeper hydrate` no longer reaches for a best-effort branch tip. It reads the exact sidecar commit SHA stored in `skeeper.lock` for the current main commit and restores spec files from that commit. Fresh clones, bisects, and historical checkouts all see the spec state that actually shipped with the code, not whatever happens to be at the head of the namespace branch today.

If you need the live tip for diagnostics, `skeeper log <path> --latest` and `skeeper status` still surface it.

## 0.1.1 - 2026-05-07

### 🎉 Features

- Reliability suite: strict pre-commit/pre-merge-commit sync, tracked `skeeper.lock`, pre-push verification, repair state, pattern/adopt/untrack commands, and CI Action verification.

- Sync on init

- Multiple namespaces

### 📚 Documentation

- Add official skill

- Update readme

## 0.1.0 - 2026-05-06

### ♻️ Refactoring

- Libraries

### 🎉 Features

- First version

- Add directory

### 🐛 Bug Fixes

- Address final-verification blockers and risks

### 📚 Documentation

- Readme

### 📦 Build System

- Release

- Changelog fix

### 🔧 CI/CD

- Codeowners

### 🔧 Miscellaneous Tasks

- Bootstrap skeeper from go-devstack

### 🧪 Testing

- Fix e2e

# Release Notes

Historical release notes are maintained by `pr-release`.
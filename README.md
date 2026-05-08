<div align="center">
  <p>
    <img src="docs/assets/skeeper-readme-hero.png" alt="skeeper hero showing AI specs syncing into a sidecar Git repository" width="100%">
  </p>
  <p>
    <a href="https://github.com/compozy/skeeper/actions/workflows/ci.yml">
      <img src="https://github.com/compozy/skeeper/actions/workflows/ci.yml/badge.svg" alt="CI">
    </a>
    <a href="https://pkg.go.dev/github.com/compozy/skeeper">
      <img src="https://pkg.go.dev/badge/github.com/compozy/skeeper.svg" alt="Go Reference">
    </a>
    <a href="LICENSE">
      <img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT">
    </a>
    <a href="https://github.com/compozy/skeeper/releases">
      <img src="https://img.shields.io/github/v/release/compozy/skeeper?include_prereleases" alt="Release">
    </a>
  </p>
</div>

Spec docs drift from code, or they bloat every PR. Skeeper picks neither.

It mirrors `SPEC.md`, ADRs, RFCs, and AI plan files into a sidecar Git repository and commits a tiny `skeeper.lock` to your main repo that pins every commit to exact sidecar commits. PR diffs stay focused on code, spec history stays auditable, and nothing silently drifts because the managed Git hooks fail the commit if the sidecar state cannot be proven.

## ✨ Highlights

- **Lockfile-backed reliability.** `skeeper.lock` records sidecar URL, source branch, namespace branch, sidecar commit, per-namespace digest, file count, and byte count.
- **Strict managed hooks.** The managed `pre-commit` and `pre-merge-commit` hooks sync staged content, push the sidecar, write and stage `skeeper.lock`, and fail closed. The managed `pre-push` hook verifies the lock against the sidecar remote.
- **Specs stay local to their code.** Edit `SPEC.md`, `docs/specs/**`, `.claude/plans/**`, ADRs, RFCs, or custom globs where they naturally belong.
- **Shared sidecars without collisions.** Namespaces isolate stored paths and sidecar branches inside one sidecar remote.
- **Branch-aware history.** Namespace branches use `<namespace>/__branches__/<source-branch>`.
- **Fresh-clone hydration.** `skeeper hydrate` restores files from the locked sidecar commits, not a best-effort latest branch.
- **Safe reconciliation.** `hydrate`, `fsck`, `diff`, and `reconcile` classify per-path drift before any local managed document is overwritten or moved.
- **Agent-friendly commands.** `status`, `sync`, `verify`, `fsck`, `diff`, `reconcile`, `update`, `hooks check`, `repair status`, `rescue`, `pattern`, `adopt`, and `untrack` all support deterministic output where needed.
- **Skill for AI agents.** A bundled skill at [`skills/skeeper/SKILL.md`](skills/skeeper/SKILL.md) teaches coding agents the strict-sync workflow, namespaces, and recovery commands.

## 🎯 Who Is This For

- Teams using AI coding agents that produce `SPEC.md`, PRD, TechSpec, and plan markdown next to code.
- Engineering organizations running ADRs, RFCs, and design docs in-repo without making every PR a docs+code review.
- Solo developers who want full spec history (`git log`, `git blame`, branches, PRs) without polluting their main repository's diff.

## 📦 Installation

#### Homebrew

```bash
brew tap compozy/compozy
brew install --cask skeeper
```

#### NPM

```bash
npm install -g @compozy/skeeper
```

#### Go

```bash
go install github.com/compozy/skeeper/cmd/skeeper@latest
```

#### GitHub Releases

Download the archive for your OS and architecture from [GitHub Releases](https://github.com/compozy/skeeper/releases), then place the `skeeper` binary on your `PATH`.

#### From Source

```bash
git clone git@github.com:compozy/skeeper.git
cd skeeper
make verify
go build -o bin/skeeper ./cmd/skeeper
```

#### Docker

```bash
git clone git@github.com:compozy/skeeper.git
cd skeeper
make docker-build
docker run --rm -v "$PWD:/workspace" -w /workspace skeeper:dev status
```

Prerequisites:

- `git` on `PATH`
- `gh` only when `skeeper init` creates a new GitHub sidecar repo; existing sidecars can be reused with `--sidecar`

## 🔄 How It Works

Spec files live in the main worktree but are ignored by the main repository through a managed `.gitignore` block. The sidecar repository stores mirrored files under `<namespace>/<path>` and pushes them to `<namespace>/__branches__/<source-branch>`.

On commit, the managed `pre-commit` block runs last. On automatic merge commits, the managed `pre-merge-commit` block runs the same strict sync path because Git does not run `pre-commit` for merge commits. Both hooks build a plan from the staged index plus explicitly owned ignored/untracked spec paths, fetch and rebase sidecar branches, mirror content into `.skeeper/`, commit and push the sidecar, write `skeeper.lock`, and stage that lock before Git creates the main commit.

```mermaid
flowchart TD
    Start([👤 git commit]):::user --> UserHook[🪝 Existing user hook content]:::user
    UserHook --> Block

    subgraph Block [📦 Skeeper pre-commit block]
        direction TB
        S1[🧮 Reconcile staged specs<br/>+ ownership] --> S2[🔄 Fetch &amp; rebase<br/>sidecar branch]
        S2 --> S3[🪞 Mirror namespace files<br/>into .skeeper/]
        S3 --> S4[📤 Commit &amp; push sidecar]
        S4 --> S5[🔒 Write &amp; stage<br/>skeeper.lock]
    end

    Block --> Commit[✅ Main commit proceeds]:::ok
    Commit --> Push([🚀 git push]):::user
    Push --> Verify[🔍 Skeeper pre-push verify]:::skeeper
    Verify --> Done([🎉 Sidecar verified]):::ok

    classDef user fill:#dbeafe,stroke:#1d4ed8,color:#0c1e3e
    classDef skeeper fill:#fef3c7,stroke:#b45309,color:#3b2c00
    classDef ok fill:#dcfce7,stroke:#15803d,color:#052e16
    class S1,S2,S3,S4,S5 skeeper
```

If sync fails, the commit fails. This is intentional: a committed main change should not silently drift from the sidecar. The audited bypass is `SKEEPER_SKIP=1`; it records `.git/skeeper/bypass.json`, prints a warning, and `pre-push`, `status`, `fsck`, and `verify` continue to surface stale-lock diagnostics until `skeeper sync` repairs the state. `git commit --no-verify` is unsupported because Git skips all hook code and cannot record an audit trail.

## ⚙️ Configuration

`skeeper init` writes `.skeeper.yml` at the repository root. Commit it.

```yaml
sidecar: git@github.com:user/myproject-specs.git

namespaces:
  - name: project
    patterns:
      - "**/SPEC.md"
      - "docs/specs/**"
      - ".claude/plans/**"
      - "**/*.spec.md"
    exclude:
      - "docs/specs/private/**"
```

Advanced operational defaults are optional:

```yaml
settings:
  guardrails:
    max_files: 100
    max_bytes: 10485760
  hooks:
    pre_push_timeout: 30s
    allow_skip_env: SKEEPER_SKIP

namespaces:
  - name: generated
    patterns:
      - "generated/specs/**"
    respect_gitignore: false
```

Rules:

- Unknown keys are rejected.
- Every namespace needs a `name` and at least one glob in `patterns`.
- `exclude` is the only public exclusion mechanism. Negative globs in `patterns` are rejected.
- Ownership must be unique. If two namespaces own the same file, the plan fails and asks for an `exclude` fix.
- `respect_gitignore: false` bypasses root `.gitignore`, nested `.gitignore`, `.git/info/exclude`, and global excludes for that namespace. `.git/` and `.skeeper/` are always excluded.

Local-only state lives under `.git/skeeper/`:

| File               | Purpose                                        |
| ------------------ | ---------------------------------------------- |
| `transaction.json` | Current resumable mutating operation and phase |
| `bypass.json`      | Latest audited strict-hook bypass              |
| `hydration.json`   | Last locked sidecar blobs hydrated locally     |
| `rescue/`          | Local files moved aside before prune/overwrite |

## 🚀 Quick Start

```bash
skeeper init
```

Interactive init asks for the sidecar mode, repository name or URL, namespace, bootstrap command, and optional extra context globs. With flags:

```bash
skeeper init \
  --sidecar-name myproject-specs \
  --visibility private \
  --namespace project \
  --patterns "**/SPEC.md" \
  --patterns "docs/specs/**"
```

Use an existing shared sidecar:

```bash
skeeper init \
  --sidecar git@github.com:user/shared-specs.git \
  --namespace project \
  --patterns "**/SPEC.md"
```

Then edit specs and commit normally:

```bash
$EDITOR src/auth/SPEC.md
git add src/auth/service.go src/auth/SPEC.md
git commit -m "auth: design OAuth provider flow"
```

The `pre-commit` and `pre-merge-commit` hooks mirror specs and stage `skeeper.lock`. If a hook stages a new lock, review it and include it in the commit.

## 🛟 Failed Sync Recovery

Inspect local repair state:

```bash
skeeper repair status
```

Resume the recorded operation when network/auth/sidecar contention has been fixed:

```bash
skeeper repair resume
```

Abort only before the main index has been mutated:

```bash
skeeper repair abort
```

Run a fresh repair sync when a bypass or stale lock is reported:

```bash
skeeper sync
skeeper verify
```

## 📖 CLI Reference

Most commands accept `--json` when their output is useful for scripts or agents. Mutating commands usually also accept `--dry-run` for a preview and `--force` only for plans that exceed configured guardrails.

<details>
<summary><code>skeeper init</code> — Create or connect a sidecar repository</summary>

```bash
skeeper init [flags]
```

Run `init` once per main repository. Without flags in an interactive terminal, it opens the guided setup. With flags, it can create a GitHub sidecar or connect an existing remote.

| Flag             | Default   | Description                                       |
| ---------------- | --------- | ------------------------------------------------- |
| `--sidecar`      |           | Existing sidecar repository URL                   |
| `--sidecar-name` |           | GitHub sidecar repository name or `OWNER/REPO`    |
| `--visibility`   | `private` | GitHub repository visibility                      |
| `--namespace`    |           | Sidecar namespace for this project                |
| `--patterns`     |           | Managed spec glob; repeat for multiple patterns   |
| `--bootstrap`    |           | Optional install command stored in `.skeeper.yml` |

Examples:

```bash
skeeper init
skeeper init --sidecar git@github.com:user/shared-specs.git --namespace project --patterns "**/SPEC.md"
```

</details>

<details>
<summary><code>skeeper sync</code> — Publish managed specs and stage <code>skeeper.lock</code></summary>

```bash
skeeper sync [--dry-run] [--json] [--commit --message <msg>] [--force]
```

`sync` mirrors working-tree managed files into the sidecar repository, pushes the namespace branch, writes `skeeper.lock`, and stages the lockfile in the main repository. Managed hooks use the same sync path against staged content.

| Flag        | Default | Description                                          |
| ----------- | ------- | ---------------------------------------------------- |
| `--dry-run` | `false` | Preview the sidecar and lockfile plan                |
| `--json`    | `false` | Emit machine-readable output                         |
| `--commit`  | `false` | Commit staged Skeeper changes in the main repository |
| `--message` |         | Main repository commit message used with `--commit`  |
| `--force`   | `false` | Allow plans that exceed configured guardrails        |

</details>

<details>
<summary><code>skeeper status</code>, <code>verify</code>, and <code>fsck</code> — Inspect sync health</summary>

```bash
skeeper status [--json]
skeeper verify [--json] [--source-branch <branch>]
skeeper fsck [--json] [--source-branch <branch>]
```

Use `status` for the local picture: sidecar URL, current branch, lock state, namespaces, repair transactions, bypass journal, and diagnostics.

Use `verify` before push or in CI. It checks `skeeper.lock` against the sidecar remote and does not require hooks.

Use `fsck` when local files may have drifted. It compares the working-tree specs against the locked sidecar content and reports exact drift diagnostics without mutating files or refs.

</details>

<details>
<summary><code>skeeper update</code> — Fresh-clone or agent-safe update workflow</summary>

```bash
skeeper update [--json] [--no-git] [--reconcile <mode>] [--ours|--theirs]
```

`update` is the high-level clone workflow for agents and fresh worktrees: fast-forward the main repository, verify the lock, hydrate managed files, run `fsck`, and validate hook installation.

| Flag          | Default  | Description                                                                  |
| ------------- | -------- | ---------------------------------------------------------------------------- |
| `--json`      | `false`  | Emit machine-readable status                                                 |
| `--no-git`    | `false`  | Skip main repository fetch and fast-forward                                  |
| `--reconcile` | `report` | Drift mode: `report`, `keep-local`, `adopt-local`, `prune-local`, or `merge` |
| `--ours`      | `false`  | Resolve conflicts using local worktree content                               |
| `--theirs`    | `false`  | Resolve conflicts using locked sidecar content after rescue                  |

</details>

<details>
<summary><code>skeeper adopt</code>, <code>untrack</code>, and <code>pattern</code> — Change managed coverage</summary>

```bash
skeeper adopt <path-or-glob>... [--dry-run] [--json] [--force] [--commit --message <msg>]
skeeper untrack <path-or-glob>... [--dry-run] [--json] [--force] [--commit --message <msg>]
skeeper pattern test <glob> [--namespace <name>] [--json]
skeeper pattern add <glob> [--namespace <name>] [--exclude <glob>]... [--adopt-existing] [--dry-run] [--json] [--force] [--commit --message <msg>]
```

Use `adopt` when files already exist in the main repository and should move under sidecar coverage. It syncs sidecar coverage before removing main-index tracking.

Use `untrack` when a managed path should stop being tracked in the main repository after the sidecar has the content.

Use `pattern test` before changing `.skeeper.yml`. Use `pattern add --adopt-existing` when a new glob should immediately publish already-matching files into the sidecar.

| Flag               | Commands                          | Description                                     |
| ------------------ | --------------------------------- | ----------------------------------------------- |
| `--namespace`      | `pattern test/add`                | Select the namespace to inspect or update       |
| `--exclude`        | `pattern add`                     | Add an exclusion with the new pattern           |
| `--adopt-existing` | `pattern add`                     | Adopt existing files matched by the new pattern |
| `--dry-run`        | `adopt`, `untrack`, `pattern add` | Preview without writing changes                 |
| `--commit`         | `adopt`, `untrack`, `pattern add` | Commit staged main-repository changes           |
| `--message`        | `adopt`, `untrack`, `pattern add` | Commit message used with `--commit`             |
| `--force`          | `adopt`, `untrack`, `pattern add` | Allow plans beyond configured guardrails        |

</details>

<details>
<summary><code>skeeper diff</code>, <code>hydrate</code>, and <code>reconcile</code> — Inspect and resolve drift</summary>

```bash
skeeper diff [--json] [--namespace <name>] [--class <class>] [--extra] [--missing] [--modified]
skeeper hydrate [--dry-run] [--json] [--keep-local|--adopt-local|--prune-local|--merge] [--ours|--theirs]
skeeper reconcile [--dry-run] [--json] [--adopt-local|--prune-local|--merge] [--ours|--theirs]
```

Use `diff` for a read-only list of managed paths that differ from `skeeper.lock`. Drift classes include `local_only`, `missing_local`, `local_modified`, `sidecar_modified`, `both_modified_conflict`, `namespace_removed`, and `config_unowned`.

Use `hydrate` after clone or branch switch to restore managed files from locked sidecar commits. By default it fails closed if local managed files would be overwritten or orphaned.

Use `reconcile` when you want the explicit status/add/merge equivalent for managed documents.

| Flag            | Commands               | Description                                                 |
| --------------- | ---------------------- | ----------------------------------------------------------- |
| `--namespace`   | `diff`                 | Restrict output to one namespace                            |
| `--class`       | `diff`                 | Filter by drift class; repeat for multiple classes          |
| `--extra`       | `diff`                 | Show local-only files                                       |
| `--missing`     | `diff`                 | Show missing local files                                    |
| `--modified`    | `diff`                 | Show local, sidecar, and conflict modifications             |
| `--keep-local`  | `hydrate`              | Restore safe files and keep local drift                     |
| `--adopt-local` | `hydrate`, `reconcile` | Publish local drift into the sidecar                        |
| `--prune-local` | `hydrate`, `reconcile` | Move local-only files to `.git/skeeper/rescue/<id>/`        |
| `--merge`       | `hydrate`, `reconcile` | Three-way merge conflicts using the hydration journal       |
| `--ours`        | `hydrate`, `reconcile` | Resolve conflicts using local worktree content              |
| `--theirs`      | `hydrate`, `reconcile` | Resolve conflicts using locked sidecar content after rescue |

</details>

<details>
<summary><code>skeeper rescue</code> and <code>repair</code> — Recover from interrupted or protective operations</summary>

```bash
skeeper rescue list [--json]
skeeper rescue restore <id> [path...] [--json] [--overwrite]
skeeper repair status [--json]
skeeper repair resume
skeeper repair abort
```

`rescue` manages files moved aside before prune or overwrite operations. Use `rescue list` to find rescue manifests and `rescue restore` to bring back all files or selected paths.

`repair` manages local recovery state under `.git/skeeper/`. Use `repair status` after a failed hook or sync, `repair resume` after fixing network/auth/sidecar contention, and `repair abort` only when the recorded transaction has not mutated the main index.

| Flag          | Commands                  | Description                        |
| ------------- | ------------------------- | ---------------------------------- |
| `--overwrite` | `rescue restore`          | Overwrite existing restore targets |
| `--json`      | `rescue`, `repair status` | Emit machine-readable output       |

</details>

<details>
<summary><code>skeeper hooks</code> and <code>merge-driver</code> — Install and maintain Git integration</summary>

```bash
skeeper hooks install [--json]
skeeper hooks check [--json]
skeeper merge-driver [--json]
```

`hooks install` installs the managed `pre-commit`, `pre-merge-commit`, and `pre-push` blocks, writes `.gitattributes`, and configures the `skeeper.lock` merge driver. It also removes legacy Skeeper post-commit blocks.

`hooks check` validates that the managed hook installation is healthy.

`merge-driver` regenerates `skeeper.lock` during Git merges. It is normally invoked by Git with `%O %A %B` paths after `hooks install` configures the driver; manual lockfile editing is unsupported.

</details>

<details>
<summary><code>skeeper log</code>, <code>version</code>, and <code>completion</code> — Utility commands</summary>

```bash
skeeper log <path> [--latest] [--source-branch <branch>]
skeeper version
skeeper completion <bash|fish|powershell|zsh>
```

`log` shows sidecar history for one managed spec path. By default it reads the locked commit; use `--latest` to fetch and inspect the latest namespace branch instead.

`version` prints build version, commit, and build date.

`completion` is provided by Cobra and generates shell completion scripts.

</details>

## 🤖 CI Action

Use the same-repository Action to verify `skeeper.lock` in CI:

```yaml
name: skeeper

on:
  pull_request:
  push:
    branches: [main]

jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: compozy/skeeper@v0.2.1
        with:
          args: |
            verify
            --json
          ssh-private-key: ${{ secrets.SKEEPER_SSH_PRIVATE_KEY }}
```

Credential precedence:

1. `ssh-private-key` writes a temp key and sets `GIT_SSH_COMMAND`.
2. `token` configures HTTPS GitHub credentials.
3. Existing runner Git/SSH credentials are used when neither input is provided.

Secrets are masked before configuration. The wrapper downloads the released Skeeper binary for the action ref/tag and delegates verification to the CLI.

## 🩺 Troubleshooting

**`SKEEPER_SKIP=1` was used**

Run `skeeper status`, then `skeeper sync`, then `skeeper verify`. The bypass journal remains visible until sync clears it.

**Sidecar push was rejected**

Run `skeeper repair status`. If the failure happened before main-index mutation, fix network/auth or sidecar contention and run `skeeper repair resume`. If the main index was already mutated, inspect the listed files manually.

**`skeeper.lock` conflicts during merge**

Run `skeeper hooks install` to ensure the merge driver is configured, then rerun the merge. Manual editing of scalar sidecar SHAs is unsupported; regenerate the lock through `skeeper merge-driver` or `skeeper sync`.

**`skeeper hydrate` is blocked by local managed files**

Run `skeeper diff` to inspect exact paths. Use `skeeper reconcile --adopt-local` when the local files should be published into the sidecar, or `skeeper reconcile --prune-local` when they should be moved to rescue before restoring the locked content. Use `skeeper rescue list` and `skeeper rescue restore <id>` to recover pruned files.

**`verify` reports a lock mismatch**

The main commit and sidecar remote disagree. Run `skeeper sync`, include the updated `skeeper.lock`, and rerun `skeeper verify`.

**A namespace overlaps another namespace**

Move shared files into exactly one namespace by adding `exclude:` entries. Skeeper does not use order-based precedence.

## 🚫 When Skeeper Is the Wrong Tool

- Repositories where specs already belong in the main diff and reviewers explicitly want them inline.
- Teams that need PR review on the spec content itself before merge — Skeeper mirrors after the main commit succeeds, by design.
- Repositories without a stable sidecar Git host: Skeeper fails the commit when the sidecar is unreachable (the audited `SKEEPER_SKIP=1` bypass exists, but it is not a substitute for a working remote).
- Storing build artifacts, generated code, or large binaries. Default guardrails cap mutating plans at 100 files and 10 MiB on purpose.

## 🛠️ Development

```bash
mise install
bun install
make hooks-install
make verify
```

Common targets:

```bash
make fmt
make lint
make test
make build
make cover
make release-snapshot
```

Contributor guidance, commit conventions, and agent instructions live in [`CLAUDE.md`](CLAUDE.md) and [`AGENTS.md`](AGENTS.md).

## 📄 License

[MIT](LICENSE)

# skeeper

`skeeper` versions Spec-Driven Development artifacts in a sidecar Git repository so specs stay reproducible without polluting the main project PR.

- Pinned **Go 1.26.2** via `mise.toml` + `.go-version`
- Zero-tolerance **golangci-lint v2** with 21 linters + gofmt/goimports/golines
- `gopls` modernize analyzer integrated into `make lint`
- Race-enabled tests via `gotestsum`, coverage via `make cover`
- **Cobra** CLI (`cmd/skeeper` → `internal/cli`)
- `.skeeper.yml` project config, `.skeeper/` sidecar clone, `.git/skeeper/` local sync state
- Shell-backed `git` and `gh` integration for debuggable repository operations
- **Husky + lint-staged + commitlint + oxfmt + oxlint** for non-Go files and commit hygiene
- **GoReleaser** multi-arch release pipeline
- **GitHub Actions** CI (detect-changes + verify) and Release (tag-driven)
- **Distroless multi-stage Dockerfile** with version ldflags injection
- **CodeRabbit** review config (`.coderabbit.yaml`)
- 30+ curated AI agent skills + 5 archetype agents wired under `.claude/`

## Prerequisites

- [mise](https://mise.jdx.dev/) (recommended) or Go 1.26.2 + Bun 1.3.4
- `git`, `gh`, `make`

## Quick Start

```bash
mise trust && mise install
bun install
make hooks-install
make verify
```

## Commands

### Go pipeline

```bash
make deps             # go mod tidy
make fmt              # gofmt every .go file
make lint             # golangci-lint v2 + gopls modernize (auto-fix)
make modernize        # gopls modernize idioms only
make test             # gotestsum + -race -parallel=4
make test-integration # tests with `-tags integration`
make cover            # coverage.out + coverage.html
make build            # bin/skeeper with version ldflags
make verify           # fmt -> lint -> test -> build (BLOCKING gate)
make tools            # install gotestsum, golangci-lint, modernize, goreleaser
```

### JS/TS toolchain

```bash
make bun-lint        # oxfmt + oxlint over non-Go files
make bun-fmt         # apply oxfmt formatting
make bun-fmt-check   # check oxfmt without writing
```

### Release & containers

```bash
make release-snapshot # local goreleaser snapshot under dist/
make docker-build     # docker build -t skeeper:dev .
```

## CLI Usage

```bash
go run ./cmd/skeeper --help
go run ./cmd/skeeper version
go run ./cmd/skeeper init
go run ./cmd/skeeper hydrate
go run ./cmd/skeeper sync
go run ./cmd/skeeper status
go run ./cmd/skeeper log src/auth/SPEC.md
```

## Sidecar Workflow

Run `skeeper init` once in a project. It creates a private GitHub sidecar repository with `gh repo create`, clones it into `.skeeper/`, writes `.skeeper.yml`, adds `.skeeper/` and the configured spec patterns to `.gitignore`, and installs a managed post-commit hook.

The committed config file looks like this:

```yaml
sidecar: git@github.com:user/myproject-specs.git
bootstrap: brew install user/tap/skeeper
patterns:
  - "**/SPEC.md"
  - "docs/specs/**"
  - ".claude/plans/**"
  - "**/*.spec.md"
```

Developers edit specs at their natural paths beside code. On `git commit`, the hook runs `skeeper sync --hook` with a short foreground budget. The hook always exits successfully; network or auth failures are queued under `.git/skeeper/` and can be retried with `skeeper sync`.

Fresh clones run `skeeper hydrate` to clone `.skeeper/`, restore matched specs into the main working tree, and install the hook.

## Project Layout

```
cmd/skeeper/       CLI entrypoint (thin shim into internal/cli)
internal/cli/      Cobra root + sidecar subcommands
internal/config/   .skeeper.yml config loading and validation
internal/gitexec/  Context-aware git and gh process runner
internal/hooks/    Managed post-commit hook installation
internal/matcher/  Doublestar spec-file discovery
internal/sidecar/  Clone, hydrate, sync, status, and log orchestration
internal/state/    Local queue and sync log under .git/skeeper/
internal/version/  Build metadata injection (Version, Commit, BuildDate)
.agents/skills/    Curated agent skills (real folders)
.claude/skills/    Symlinks into .agents/skills/ (Claude Code compatibility)
.claude/agents/    Decision-archetype agents (architect, devil's advocate, ...)
```

## Conventional Commits

Commit-msg hook enforces Conventional Commits. Allowed types:

```
build, chore, ci, docs, feat, fix, perf, refactor, test
```

## Release Flow

Tag-driven. Push a `v*` tag to trigger goreleaser:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

Optional channels (Homebrew tap, deb/rpm, cosign, SBOMs) are scaffolded as commented blocks in `.goreleaser.yml`.

## Agent Tooling

- `CLAUDE.md` / `AGENTS.md` — coding style, skill dispatch protocol, anti-patterns
- `.agents/skills/` — 30+ curated skills (Go, TUI, debugging, testing, security, docs, spec authoring, code review)
- `.claude/agents/` — 5 decision archetypes for advisory passes
- `.coderabbit.yaml` — automated PR review config with test-file enforcement rules

## License

[Choose your license]

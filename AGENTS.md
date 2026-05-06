# CLAUDE.md

This file provides project guidance for coding agents working in this repository.

## HIGH PRIORITY

- **IF YOU DON'T CHECK SKILLS** your task will be invalidated and we will generate rework
- **YOU CAN ONLY** finish a task if `make verify` passes at 100% (runs `fmt + lint + test + build`). No exceptions — failing any of these commands means the task is **NOT COMPLETE**
- **`make lint` has zero tolerance**. **Zero issues allowed** — any golangci-lint issue is a blocking failure
- **ALWAYS** check dependent package APIs before writing integration code or tests to avoid writing wrong code
- **NEVER** use workarounds — always use the `no-workarounds` skill for any fix/debug task + `testing-anti-patterns` for tests
- **ALWAYS** use the `no-workarounds` and `systematic-debugging` skills when fixing bugs or complex issues
- **NEVER** use web search tools to search local project code — for local code, use Grep/Glob instead
- **YOU SHOULD NEVER** add dependencies by hand in `go.mod` — always use `go get` instead

## MANDATORY REQUIREMENTS

- **MUST** run `make verify` before completing ANY subtask
- **ALWAYS USE** the `golang-pro` skill before writing any Go code
- **ALWAYS USE** the `systematic-debugging` + `no-workarounds` skills before fixing any bug
- **ALWAYS USE** the `testing-anti-patterns` skill before writing or modifying tests
- **ALWAYS USE** the `verification-before-completion` skill before claiming any task is done
- **Skipping any verification check will result in IMMEDIATE TASK REJECTION**

## Project Overview

**skeeper** is a single-binary Go command-line interface built with production-grade patterns and agent-ready tooling. The CLI is structured around `cobra` and exposes subcommands wired through `internal/cli`.

**Toolchain pin:** Go **1.26.2** (managed via `mise.toml` + `.go-version`), Bun **1.3.4** (`.bun-version`), `golangci-lint v2.11.4`, `gotestsum v1.13.0`, `gopls/modernize v0.21.1`, `goreleaser v2.6.1`. Run `mise install` to provision everything.

## Package Layout

| Path               | Responsibility                                               |
| ------------------ | ------------------------------------------------------------ |
| `cmd/skeeper`      | CLI entry point — thin shim that delegates to `internal/cli` |
| `internal/cli`     | Cobra root command, subcommand wiring, exit-code mapping     |
| `internal/config`  | Config loading and validation                                |
| `internal/version` | Build metadata (Version, Commit, BuildDate via -X)           |
| `internal/logger`  | Structured logging setup                                     |

## Build & Development Commands

```bash
# Full verification pipeline (BLOCKING GATE for any change)
make verify              # Serial: fmt -> lint -> test -> build

# Go pipeline
make fmt                 # gofmt every .go file
make lint                # golangci-lint v2 + gopls modernize (zero-tolerance)
make modernize           # gopls modernize idioms only
make test                # gotestsum + -race -parallel=4
make test-integration    # tests with `-tags integration`
make cover               # coverage.out + coverage.html
make build               # bin/skeeper with version ldflags

# Dependencies and tools
make deps                # go mod tidy + verify
make tools               # install gotestsum, golangci-lint, modernize, goreleaser

# JS/TS toolchain (for hooks + non-Go files)
make bun-lint            # oxfmt + oxlint
make bun-fmt             # apply oxfmt
make bun-fmt-check       # check oxfmt without writing

# Hooks and release
make hooks-install       # husky install (requires `bun install` first)
make release-snapshot    # local goreleaser snapshot under dist/
make docker-build        # build distroless container
```

## Hooks & Conventional Commits

- `pre-commit`: runs `lint-staged` — `make fmt` for `*.go`, `oxfmt`+`oxlint` for JS/TS, `oxfmt` for CSS/HTML/JSON/YAML/MD.
- `commit-msg`: enforced by `commitlint` with `@commitlint/config-conventional`.
- Allowed types: `build, chore, ci, docs, feat, fix, perf, refactor, test`. Scope is optional.
- Bootstrap: `bun install && make hooks-install` after cloning.

## Release Flow

Releases are tag-driven. Push a `v*` tag to trigger `.github/workflows/release.yml`:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

Goreleaser (community v2) produces multi-OS/arch binaries (linux/darwin/windows × amd64/arm64), tarballs/zip archives, checksums, source tarball, and an auto-generated changelog. Optional channels (Homebrew tap, deb/rpm via nfpm, cosign signing, SBOMs) are scaffolded as commented blocks in `.goreleaser.yml`.

## CRITICAL: Git Commands Restriction

- **ABSOLUTELY FORBIDDEN**: **NEVER** run `git restore`, `git checkout`, `git reset`, `git clean`, `git rm`, or any other git commands that modify or discard working directory changes **WITHOUT EXPLICIT USER PERMISSION**
- **DATA LOSS RISK**: These commands can **PERMANENTLY LOSE CODE CHANGES** and cannot be easily recovered
- **REQUIRED ACTION**: If you need to revert or discard changes, **YOU MUST ASK THE USER FIRST**
- If the worktree contains unexpected edits, read them and work around them; do not revert them

## Code Search and Discovery

- **TOOL HIERARCHY**: Use tools in this order:
  1. **Grep** / **Glob** — preferred for local project code
  2. **`find-docs` skill** — for external Go libraries and framework documentation
  3. **Web search tools** — for web research, latest news, code examples
- **FORBIDDEN**: Never use web search tools for local project code

## Coding Style

- Format with `make fmt` and lint with `make lint`.
- Prefer explicit error returns with wrapped context using `fmt.Errorf("context: %w", err)`.
- Use `errors.Is()` and `errors.As()` for error matching; do not compare error strings.
- No `panic()` or `log.Fatal()` in production paths; reserve these for truly unrecoverable startup failures only.
- Use `log/slog` for structured logging. Do not use `log.Printf` or `fmt.Println` for operational output.
- Pass `context.Context` as the first argument to all functions crossing runtime boundaries; avoid `context.Background()` outside `main` and focused tests.
- Design small, focused interfaces; accept interfaces, return structs.
- Use functional options pattern for complex constructors.
- Use compile-time interface verification: `var _ Interface = (*Type)(nil)`.
- Do not use `interface{}`/`any` when a concrete type is known.
- Do not use reflection without performance justification.
- Keep comments short and focused on intent, invariants, or protocol edge cases.

## Testing

- Table-driven tests with subtests (`t.Run`) as the default pattern.
- Use `t.Parallel()` for independent subtests.
- Use `t.TempDir()` for filesystem isolation instead of manual temp directory management.
- Mark test helper functions with `t.Helper()` so stack traces point to the caller.
- Run tests with `-race` flag; the race detector must pass before committing.
- Mock dependencies via interfaces, not test-only methods in production code.
- Prefer root-cause fixes in failing tests over workarounds that mask the real issue.

## Architecture

### Concurrency discipline

- Every goroutine must have explicit ownership and shutdown via `context.Context` cancellation.
- No fire-and-forget goroutines; track all goroutines with `sync.WaitGroup` or equivalent.
- Use `select` with `ctx.Done()` in all long-running goroutine loops.
- Prefer channel-based communication over shared memory with mutexes when practical.
- Use `sync.RWMutex` for read-heavy shared state, `sync.Mutex` for write-heavy.

### Runtime discipline

- Keep the system single-binary and local-first.
- Introduce sidecars or external control planes only with a written techspec.
- Keep execution paths deterministic and observable.

## Agent Skill Dispatch Protocol

Every agent MUST follow this protocol before writing code:

### Step 1: Identify Task Domain

Scan the task description and target files to determine which domains are involved:

- **Go / Runtime** keywords: package, struct, interface, goroutine, channel, context, slog, functional options, constructor, error handling
- **CLI / Cobra** keywords: subcommand, flag, root, args, exit code, stdin/stdout, prompt
- **TUI** keywords: bubbletea, tea, model, view, update, terminal, ANSI, charm, lipgloss
- **Concurrency** keywords: goroutine, channel, mutex, race, deadlock, livelock, hang
- **Config** keywords: config, TOML, environment, validation, settings
- **Logging** keywords: logger, logging, slog, log level, observer
- **Bug fix** keywords: bug, fix, error, failure, crash, unexpected, broken, regression
- **Writing tests** keywords: test, spec, mock, stub, fixture, assertion, coverage, table-driven
- **Task completion** keywords: done, complete, finished, ship
- **Architecture audit** keywords: architecture, dead code, code smell, anti-pattern, duplication
- **Refactoring** keywords: refactor, restructure, extract, rename, move, decompose
- **Performance** keywords: slow, latency, benchmark, profile, allocs, hot path, optimize
- **Security** keywords: secret, token, credential, auth, injection, traversal, vulnerability
- **Creative / new features** keywords: new, feature, design, add, create, implement
- **Spec / planning** keywords: PRD, techspec, plan, requirements, scope, milestones
- **Task breakdown / execution** keywords: tasks, breakdown, execute, kickoff, follow-up
- **Code review** keywords: review, PR, comments, feedback, coderabbit, follow-up
- **Documentation** keywords: README, docs, tutorial, how-to, reference, explanation
- **Diagrams** keywords: architecture diagram, sequence, flow, mermaid, ASCII

### Step 2: Activate All Matching Skills

Use the `Skill` tool to activate every skill that matches the identified domains:

| Domain                     | Required Skills                                      | Conditional Skills           |
| -------------------------- | ---------------------------------------------------- | ---------------------------- |
| Go / Runtime               | `golang-pro`                                         | `context7` (external libs)   |
| CLI / Cobra                | `golang-pro`                                         |                              |
| TUI                        | `golang-pro` + `bubbletea`                           |                              |
| Concurrency                | `golang-pro` + `deadlock-finder-and-fixer`           |                              |
| Config / Logging           | `golang-pro`                                         |                              |
| Bug fix                    | `systematic-debugging` + `no-workarounds`            | `testing-anti-patterns`      |
| Writing tests              | `testing-anti-patterns` + `golang-pro`               |                              |
| Task completion            | `verification-before-completion`                     |                              |
| Architecture audit         | `architectural-analysis`                             | `adversarial-review`         |
| Refactoring                | `refactoring-analysis`                               | `golang-pro`                 |
| Performance                | `extreme-software-optimization`                      | `golang-pro`                 |
| Security review            | `security-review`                                    |                              |
| Creative / new features    | `brainstorming`                                      |                              |
| Spec authoring (PRD)       | `cy-create-prd`                                      | `cy-spec-preflight`          |
| Spec authoring (Techspec)  | `cy-create-techspec` + `cy-spec-preflight`           | `cy-spec-peer-review`        |
| Task breakdown             | `cy-create-tasks`                                    |                              |
| Task execution             | `cy-execute-task`                                    | `cy-workflow-memory`         |
| Final verification         | `cy-final-verify` + `verification-before-completion` |                              |
| Code review (round / impl) | `cy-review-round` / `cy-impl-peer-review`            |                              |
| Fixing review feedback     | `cy-fix-reviews`                                     | `coderabbit-review`          |
| Documentation              | `documentation-writer`                               | `crafting-effective-readmes` |
| Diagrams                   | `mermaid-diagrams` / `architecture-diagram`          |                              |
| Git rebase / conflicts     | `git-rebase`                                         |                              |
| Lessons / postmortems      | `lesson-learned`                                     |                              |
| Skill discovery            | `find-skills`                                        | `skill-best-practices`       |

### Step 3: Verify Before Completion

Before any agent marks a task as complete:

1. Activate `verification-before-completion` skill
2. Run `make verify`
3. Read and verify the full output — no skipping
4. Only then claim completion

## Anti-Patterns for Agents

**NEVER do these:**

1. **Skip skill activation** because "it's a small change" — every domain change requires its skill
2. **Activate only one skill** when the code touches multiple domains
3. **Forget `verification-before-completion`** before marking tasks done
4. **Write tests without `testing-anti-patterns`** — leads to mock-testing-mocks and production pollution
5. **Fix bugs without `systematic-debugging`** — leads to symptom-patching instead of root cause fixes
6. **Apply workarounds without `no-workarounds`** — type assertions, lint suppressions, error swallowing are rejected
7. **Claim task is done when any check has warnings or errors** — zero warnings, zero errors. No exceptions
8. **Add dependencies by hand in go.mod** — always use `go get`
9. **Use web search tools for local code** — only for external library documentation
10. **Run destructive git commands without permission** — `git restore`, `git reset`, `git clean` require explicit user approval
11. **Use `panic()` or `log.Fatal()` in production handlers** — leads to unrecoverable crashes without cleanup
12. **Fire-and-forget goroutines** — every goroutine must have explicit ownership and shutdown handling
13. **Use `time.Sleep()` in orchestration** — use proper synchronization primitives instead
14. **Ignore errors with `_`** — every error must be handled or have a written justification
15. **Hardcode configuration** — use TOML config or functional options

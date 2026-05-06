---
name: cy-spec-peer-review
description: Runs an optional cross-LLM peer review of a TechSpec via compozy exec --ide claude --model opus --reasoning-effort xhigh and packages findings for user-directed incorporation. Use when a TechSpec draft has already been approved by the user and they want an external review round, especially for autonomy/network/memory-impacting designs. Do not use for PRDs, automatic approval gates, code review batches, or auto-looped review cycles.
trigger: explicit
argument-hint: "[techspec-path]"
---

# Spec Peer Review

Codex authors AGH TechSpecs with `gpt-5.4` at `reasoning_effort=xhigh`; Claude Opus pressure-tests them. This skill runs that pressure-test only when the user explicitly asks for a review round after approving the current draft. It does not auto-run, auto-incorporate findings, or auto-loop additional rounds.

## User Decisions

When this skill instructs the agent to ask whether to incorporate findings or run another round, it MUST use the runtime's dedicated interactive question tool — the tool or function that presents a question to the user and pauses execution until the user responds.

If the runtime does not provide such a tool, present the question as the complete assistant message and stop generating. Do not answer the question on the user's behalf.

## Required Inputs

- **techspec-path** (optional): explicit path to the `_techspec.md` under review. When omitted, resolve to the most recently modified `.compozy/tasks/<slug>/_techspec.md` whose sibling `_meta.md` shows `Pending: > 0` or no `_meta.md` exists yet.

## Procedures

**Step 1: Validate Input and Context**

1. Resolve `techspec-path`. If omitted, list candidate paths and pick the freshest.
2. Confirm the user has already approved the current draft or explicitly asked to review the saved spec as-is.
3. Read the spec and confirm it is a final-shape TechSpec (has `Architectural Boundaries`, `Implementation Steps`, `Test Strategy` sections) — not a draft.
4. Read `references/quality-markers.md` and verify the spec carries the six markers (boundary statement, listed boundaries, Go interface signatures, data-model field rationale, side-table-vs-JSON decisions, lease/safety invariants enumerated). If any marker is missing, abort and report the missing markers — Opus review is wasted on incomplete specs.
5. Resolve the slug from the path; ensure `.compozy/tasks/<slug>/` exists and is writable.
6. Determine the next review round number by listing existing `qa/peer-review-result-round*.json` files. Start at `round1` when none exist.

**Step 2: Compose the Review Prompt**

1. Read `references/peer-review-prompt.md` for the canonical Opus prompt template.
2. Substitute the placeholders: `{techspec_path}`, `{adr_paths}` (any `adrs/*.md` siblings), `{related_research}` (any `analysis/*.md` siblings).
3. Write the assembled prompt to `.compozy/tasks/<slug>/qa/peer-review-prompt-roundN.md` (create the `qa/` folder if needed).

**Step 3: Execute the Cross-LLM Review**

1. Run `compozy exec --ide claude --model opus --reasoning-effort xhigh --format json --prompt-file .compozy/tasks/<slug>/qa/peer-review-prompt-roundN.md`.
2. Capture stdout to `.compozy/tasks/<slug>/qa/peer-review-result-roundN.json` and stderr to `.compozy/tasks/<slug>/qa/peer-review-result-roundN.err`.
3. If the command returns a non-zero exit code, fail loudly. Do not retry silently. Inspect the stderr for model misconfiguration (see Error Handling).

**Step 4: Summarize Findings**

1. Parse the JSON output. Expect three sections: `blockers`, `nits`, `readiness`.
2. Write `.compozy/tasks/<slug>/qa/peer-review-summary-roundN.md` with:
   - readiness verdict (`READY` / `BLOCKED` / `NEEDS_REWORK`)
   - one-line rationale per blocker
   - nits list
   - recommended sections and ADRs likely affected
3. Present a concise user-facing summary of the review. Include the verdict, blocker/nit counts, the main themes, and the artifact paths written for the round.
4. Do NOT modify the TechSpec or ADRs yet.

**Step 5: User-Directed Incorporation**

1. Ask the user which findings to incorporate:
   - A) all blockers
   - B) selected blockers/nits
   - C) nothing
   - D) manual edits before any incorporation
2. Apply only the findings the user selected. Do not silently apply all blockers or all nits.
3. If incorporation requires an ADR update, update only the ADRs tied to the selected findings.
4. Record the incorporation decision in `.compozy/tasks/<slug>/qa/peer-review-incorporation-roundN.md`, listing:
   - incorporated items
   - deferred items
   - files changed
5. Show the user what changed and what remains deferred.

**Step 6: Optional Additional Rounds**

1. Ask whether the user wants another peer-review round or wants to stop with the current saved spec.
2. If the user requests another round, re-run from Step 2 against the updated TechSpec and create a fresh `roundN+1` artifact set.
3. Do not auto-loop. The user explicitly requests further rounds.

## Error Handling

- **Model misconfiguration (`The model 'X' does not exist`):** stop and surface the configured model. The IDE may be set to a stale name like `gpt-5.5`. Do not mutate the call to substitute a model — verify with the user. (See `docs/_memory/lessons/L-010-model-name-validation.md`.)
- **`compozy exec` not found:** the skill assumes Compozy CLI is on `PATH`. If absent, fail with the install hint rather than swallowing.
- **Quality markers missing:** if the Step 1 quality-marker check fails, do not run Opus. Print the missing markers and exit so the user can amend the spec first.
- **Empty Opus output:** treat empty `blockers`/`nits`/`readiness` as suspect (likely a prompt or model issue). Re-prompt the user before declaring `READY`.
- **Existing peer-review files:** never overwrite. Prompt, result, summary, and incorporation files are all versioned with `-roundN`.

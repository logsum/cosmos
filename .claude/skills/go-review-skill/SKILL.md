---
name: go-review
description: >
  Deep code review for Go projects covering uncommitted changes. Runs Go-specific
  static analysis (go vet, staticcheck, golangci-lint if available), then invokes
  claude-opus to produce a prioritised security/bug/design report and an interactive
  fix plan the user must approve before any implementation begins. Also maintains
  TODO.md. Use this skill whenever the user says "review", "code review", "check my
  changes", "audit my code", or asks for a fix plan on a Go project.
---

# /go-review — Deep Go code review + approved fix plan

## Overview

This skill performs a thorough review of all uncommitted changes in a Go project.
It runs static-analysis tools first (real data), then uses **claude-opus** to reason
over the diff and produce findings. Finally it presents a fix plan for your approval
before touching a single line of code.

---

## Step 1 — Gather raw data (run all of these)

```bash
# 1. Repo snapshot
git status --porcelain=v1

# 2. Full diff of everything uncommitted (staged + unstaged)
git diff HEAD

# 3. Changed file list
git diff HEAD --name-only

# 4. Go vet (always available)
go vet ./...

# 5. staticcheck (install if missing: go install honnef.co/go/tools/cmd/staticcheck@latest)
staticcheck ./... 2>/dev/null || echo "staticcheck not installed"

# 6. golangci-lint (install if missing: https://golangci-lint.run/usage/install/)
golangci-lint run --out-format=line-number 2>/dev/null || echo "golangci-lint not installed"

# 7. Tests that touch changed packages (fast signal on breakage)
go test $(git diff HEAD --name-only | grep '\.go$' | xargs -I{} dirname {} | sort -u | xargs -I{} echo "./{}" 2>/dev/null) 2>&1 | tail -40

# 8. Codex AI review (additional agent feedback)
codex exec --skip-git-repo-check "please review the uncommited code for any criticality and issue that needs to be addressed before a commit" --json \
| jq -r 'select(.item?.type=="agent_message") | .item.text' 2>/dev/null || echo "codex not available"

# 9. Read TODO.md if present
cat TODO.md 2>/dev/null || echo "No TODO.md found"
```

Collect all output. Do NOT skip any command even if a previous one failed.

---

## Step 2 — Invoke claude-opus for deep review

Call the Anthropic API with model `claude-opus-4-5` (or the latest opus available).

**System prompt:**
```
You are a senior Go engineer performing a thorough code review.
Your analysis must be precise, evidence-based, and Go-idiomatic.
Respond only with valid JSON (no markdown fences, no preamble).
```

**User prompt:** build a JSON payload containing:
- `diff`: the full `git diff HEAD` output
- `go_vet`: go vet output
- `staticcheck`: staticcheck output
- `golangci_lint`: golangci-lint output
- `test_results`: test output
- `codex_review`: codex AI review output (validate these suggestions before including)
- `todo_md`: current TODO.md content

**Important:** For `codex_review` feedback, validate each issue independently — only include items that are genuinely problematic. Discard false positives (feedback items that you don't agree) or stylistic preferences that don't impact correctness/security/stability.

Ask opus to return **only** this JSON structure:

```json
{
  "summary": ["bullet 1", "bullet 2"],
  "critical": [
    {
      "id": "C1",
      "title": "...",
      "file": "path/to/file.go",
      "lines": "42-57",
      "description": "...",
      "evidence": "exact snippet or tool output",
      "fix_hint": "..."
    }
  ],
  "security": [ /* same shape as critical */ ],
  "design": [ /* same shape */ ],
  "tests_docs": [ /* same shape */ ],
  "todo_updates": {
    "add": [
      {
        "severity": "Critical|High|Medium|Low",
        "area": "...",
        "description": "...",
        "evidence": "file:lines",
        "acceptance": "done when ..."
      }
    ],
    "resolve": ["existing TODO item text that the diff clearly fixes"]
  }
}
```

Parse the JSON response. If parsing fails, retry once with a simpler prompt asking
for only the `critical` and `security` arrays.

---

## Step 3 — Present findings to the user

Render the opus response as readable markdown in the conversation:

```
## 🔍 Code Review — <date>

### Summary
- …

### 🚨 Critical Findings
**[C1] Title** · `file.go:42-57`
Description…
> Evidence snippet
💡 Fix hint: …

### 🔐 Security Findings
…

### 🏗 Design & Maintainability
…

### 🧪 Tests / Docs
…
```

---

## Step 4 — Build a fix plan and ask for approval

After displaying findings, produce a numbered fix plan:

```
## 📋 Proposed Fix Plan

The following changes are ready to implement. Please approve before I proceed.

1. **[C1] Fix nil pointer dereference in handler.go:42**
   → Add nil guard before dereferencing `req.Body`

2. **[S1] Remove hardcoded token in config.go:17**
   → Replace with os.Getenv("API_TOKEN"); add to .env.example

3. **[D1] Extract DB logic from HTTP handler (handler.go:80-130)**
   → Move to internal/store package, inject via interface

…

> Type **approve** to implement all, **approve 1,3** to implement specific items,
> or **reject** to stop here. You can also say **edit** to modify the plan.
```

**WAIT for explicit user approval before doing anything else.**

Do NOT start implementing, do NOT edit any files at this point.

---

## Step 5 — Compact context, then implement (only after approval)

Once the user approves (all or partial):

1. **Compact the context** by running:
   ```bash
   # Signal to Claude Code to compact before continuing
   # In practice: remind the user to run /compact in Claude Code,
   # or if in an automated flow, summarise the approved plan into
   # a brief handoff note and start a fresh sub-task.
   ```
   
   Tell the user:
   > "Before I start implementing, please run **`/compact`** in Claude Code to free
   > up context window. Once done, paste the approved item numbers and I'll proceed."

2. After compacting, implement **only** the approved items, one by one.
   - Make minimal, targeted changes.
   - Run `go build ./...` and `go vet ./...` after each change.
   - Run relevant tests: `go test ./...`
   - Confirm each fix with a brief before/after note.

---

## Step 6 — Update TODO.md

After implementation (or even if the user rejects the fix plan — findings still matter):

- Add new TODO items from `todo_updates.add` (deduplicate).
- Mark resolved items with `[x]` 
- Write the file back to disk.


---

## Go-specific checklist (opus must address these)

| Category | Things to check |
|---|---|
| Error handling | errors ignored with `_`, missing `errors.As`/`errors.Is`, sentinel errors |
| Goroutines | goroutine leaks, missing `context` cancellation, unbounded spawning |
| Concurrency | data races (look for unprotected map/slice writes), mutex misuse |
| Context | `context.Background()` in handlers, context not threaded through |
| Interfaces | over-use of `interface{}` / `any`, missing interface for testability |
| Defer | `defer` in loops, deferred `rows.Close()` without error check |
| Panics | `panic` in library code, missing recover in goroutines |
| Imports | init() side-effects, cyclic package risk |
| Security | `exec.Command` with user input, `fmt.Sprintf` in SQL, `math/rand` for crypto |
| Tests | table-driven tests, subtests with `t.Run`, missing `t.Parallel()` |
| Stability | any mistake that can cause issue with the stability of the program |
| Design | any design issue that can harm the project structure now or in near future |

---

## Notes

- Always use **claude-opus** (not sonnet/haiku) for the review — the quality difference
  on complex multi-file diffs is significant.
- If `golangci-lint` is not installed, recommend the user install it and note findings
  may be incomplete.
- If the diff is empty (`git diff HEAD` returns nothing), tell the user there are no
  uncommitted changes and exit gracefully.
- Never auto-implement anything. The approval gate in Step 4 is mandatory.

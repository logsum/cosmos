# Cosmos — Implementation TODO

Current state: **Core loop and LLM provider wired to TUI.** Chat page streams real LLM responses, tool-use loop works end-to-end (with stub executor), pricing/token tracking live in status bar. V8 engine and filesystem layer not yet implemented.

---

## Phase 1: Core Foundation

The orchestration layer that connects the UI to everything else.

### 1.1 User Configuration (`config/defaults.go`)
- [x] Define `Config` struct: AWS region, default model, sessions dir, agents dir, audit path, policy path
- [x] Load from `~/.cosmos/config.toml` with sensible defaults
- [x] Create dirs on first run (`~/.cosmos/`, `~/.cosmos/agents/`, `~/.cosmos/sessions/`)

### 1.2 Provider Interface (`core/provider/provider.go`)
- [x] Define `Provider` interface: `Send(ctx, messages, tools) -> StreamIterator`
- [x] Define message types: `Message`, `ToolCall`, `ToolResult`, `StreamChunk`
- [x] Define `StreamIterator` interface for token-by-token delivery
- [x] Define `ModelInfo` struct (name, context window, input/output pricing)
- [x] `ListModels(ctx) -> []ModelInfo`

### 1.3 AWS Bedrock Provider (`core/provider/bedrock.go`)
- [x] Implement `Provider` for Bedrock Converse / ConverseStream API
- [x] AWS credential chain (env vars, profile, IAM role)
- [x] Streaming response parsing (ContentBlockDelta, ToolUse blocks)
- [x] Map Bedrock tool_use format to internal `ToolCall` struct
- [x] Map internal `ToolResult` back to Bedrock format
- [x] Model listing from Bedrock `ListFoundationModels`
- [x] Error handling: throttling, auth failures, model not found

### 1.4 Token Counting & Cost (`core/pricing.go`)
- [x] Track cumulative input/output tokens per session
- [x] Cost computed dynamically from `ModelInfo` pricing (not a static table)
- [x] Expose `CostSnapshot` struct with `FormatTokens()` / `FormatCost()` for status bar updates
- [x] Context usage percentage calculation (tokens used / model context window)

### 1.5 Core Loop (`core/loop.go`)

Split into sub-tasks to enable incremental progress without blocking on Phase 3 (V8 engine).

#### 1.5a Basic Loop & Message Streaming
- [x] Create `core/loop.go` with `Session` struct
- [x] Implement: user message → provider → stream tokens to UI (via channel)
- [x] Conversation history: append user + assistant messages
- [x] No tool handling yet, just text responses
- [x] Wire this to UI in task 1.6

#### 1.5b Tool-Use Detection & Stubbed Dispatch
- [x] Parse `tool_use` blocks from provider responses
- [x] Define `ToolExecutor` interface (stub implementation for now)
- [x] Implement multi-turn loop:
  1. Send user message + conversation history + tool definitions to provider
  2. Stream response tokens to UI
  3. If response contains `tool_use` → dispatch to stub executor, collect mock result
  4. Send tool result back to LLM → repeat until no more tool calls
  5. Final text response → push to UI
- [x] Conversation history: append tool call + tool result messages
- [x] This proves the loop works without needing V8 (Phase 3)

#### 1.5c Tool Definitions (stub list)
- [x] Create empty/mock tool definition list for testing
- [x] Pass tool definitions to provider in the loop
- [x] Will be replaced with real manifests when Phase 3.3 (Agent Loader) is complete

#### 1.5d Context Usage Monitoring
- [x] Add context usage calculation using `pricing.Tracker`
- [x] Emit warning at 50% context usage (suggest `/compact` to user)
- [x] Force compaction automatically at 90% context usage
- [x] Display context percentage in status bar or chat

#### 1.5e Compaction Implementation
- [x] Implement `/compact` command handling in session
- [x] Generate summarization prompt from conversation history
- [x] Send history to LLM for summarization
- [x] Replace conversation messages with summary message
- [x] Reset token counters appropriately
- [x] Notify user of compaction result

### 1.6 Wire Core to UI
- [x] `main.go` / `app/bootstrap.go`: create core session, wire to UI via composition root
- [x] Chat page: send `PromptSubmitMsg` to core via channel, receive streamed tokens
- [x] Replace mock echo in `chat.go` with real assistant responses
- [x] Accumulate messages in chat history
- [x] Status bar: live-update token counts and cost from `core.Tracker` via `onUpdate` callback
- [x] Status bar: display actual model name from config (formatted via `ui.formatModelName`)
- [x] Currency: read `config.Currency`, if not USD call `CurrencyEngine.FetchRate()` once at startup, build `CurrencyFormatter`, pass to `NewTracker`

### 1.7 Chat Rendering - Markdown Support (`ui/chat.go`)
- [x] Add glamour dependency for markdown rendering with chroma syntax highlighting
- [x] Enhance `chatMessage` struct with rendering cache fields (`renderedLines`, `isMarkdown`, `renderError`)
- [x] Implement `getGlamourRenderer(width)` with "dark" style and dynamic word wrap
- [x] Implement `renderMessageMarkdown(text)` to render markdown to ANSI-formatted lines
- [x] Implement `trimEmptyLines()` helper for cleaning rendered output
- [x] Modify `ChatCompletionMsg` handler to render markdown on message finalization
- [x] Update `View()` to use cached `renderedLines` when available, fallback to `wrapText()`
- [x] Update `buildAllRenderedLines()` identically to preserve critical scrollback invariant
- [x] Graceful fallback to plain text on any rendering errors

---

## Phase 2: Manifest & Policy

The permission layer that governs what tools can do.

### 2.1 Manifest Parsing (`engine/manifest/schema.go`)
- [x] Parse `cosmo.manifest.json` into `Manifest` struct
- [x] Validate required fields: name, version, entry, functions, permissions
- [x] Parse permission keys with glob support (`fs:read:./src/**`)
- [x] Parse function definitions (name, params with types, returns)
- [x] Timeout parsing (string like `"30s"` to `time.Duration`)
- [x] Ed25519 signature verification for permission block integrity
- [x] Unit tests (`engine/manifest/manifest_test.go`)

### 2.2 Policy Evaluator (`engine/policy/evaluator.go`)
- [x] `Evaluate(agentName, permissionKey, rules) -> Decision` (allow/deny/prompt_once/prompt_always)
- [x] Default-deny for undeclared permissions
- [x] Glob matching for permission keys (`fs:read:./src/**` matches `fs:read:./src/main.go`) via `doublestar`
- [x] `~` expansion in permission paths
- [x] Specificity-based rule matching (exact > glob > broad, narrower glob wins, most restrictive tie-break)
- [x] Load per-project overrides from `.cosmos/policy.json` (team overrides take absolute precedence)
- [x] Persist `request_once` decisions to `.cosmos/policy.json` (atomic write, `0600` permissions)
- [x] Unit tests (`engine/policy/policy_test.go`) — 41 tests including concurrency with `-race`

### 2.3 Audit Logging (`engine/policy/audit.go`)
- [x] JSON-lines writer to `.cosmos/audit-{session-id}.jsonl` (per-session logs)
- [x] Fields: timestamp, agent, tool, permission, decision, arguments
- [x] Redact sensitive data in arguments (paths containing tokens, keys, passwords)
- [x] Session-based cleanup: delete all session data (audit logs, snapshots, session state) after 30 days via `engine/maintenance` package
- [x] Reader for Agents History page to consume

**Implementation Notes:**
- Audit logs are per-session: `.cosmos/audit-{sessionID}.jsonl`
- No per-file rotation; cleanup happens at application bootstrap
- The `engine/maintenance/cleanup.go` package scans and deletes session data older than 30 days
- Cleanup is synchronous at startup, non-blocking, and logs warnings on error
- See `app/bootstrap.go` phase 1.5 for integration

### 2.4 Permission Request UI
- [x] Define `PermissionRequestEvent` / `ChatPermissionRequestMsg` / `PermissionDecisionMsg` types
- [x] Adapter wraps core channel in callback (`RespondFunc`) so `ui` never imports `core`
- [x] Render inline permission prompt in Chat page: yellow `⚠` bar with `[y] Allow  [n] Deny`
- [x] Accept/deny input from user (App-level y/n routing when `permissionPending`)
- [x] Timeout handling: core-owned single timer, emits `PermissionTimeoutEvent`, honors `DefaultAllow`
- [x] Route decision back to policy evaluator via `RecordOnceDecision()`
- [x] Wire to real manifest rules for all agents via `ToolManifestProvider` interface

---

## Phase 3: V8 Engine

Sandboxed JavaScript execution for tools.

### 3.1 V8 Runtime (`engine/runtime/`)
- [x] Add `rogchap.com/v8go` dependency
- [x] Create one V8 isolate per tool (true isolation)
- [x] Load and compile JS source into V8 context
- [x] Inject Go-side APIs as global functions in V8 context (APIRegistry extensibility point)
- [x] Per-tool timeout enforcement (from manifest, max 5 min global cap)
- [x] Error capture: JS exceptions → Go error with stack trace
- [x] Lazy loading: tools compiled on first invocation, not at startup
- [x] Hot reload: detect `.js` file changes, recompile isolate (stat-on-use)
- [x] Function name validation (JS identifier check to prevent script injection)
- [x] Default `console.log` no-op binding

### 3.2 Go-side API Injection
Each API is a Go function registered into the V8 context:

- [x] `fs.read(path)` — read file, scoped by manifest glob permissions
- [x] `fs.write(path, content)` — write file, scoped + VFS snapshot before write
- [x] `fs.list(path)` — list directory
- [x] `fs.stat(path)` — file metadata
- [x] `fs.unlink(path)` — delete file, scoped + VFS snapshot
- [x] `http.get(url, headers)` — HTTP GET
- [x] `http.post(url, body, headers)` — HTTP POST
- [x] `storage.get(key)` / `storage.set(key, value)` — per-tool KV store
- [x] `ui.emit(message)` — send progress/status to chat window

Every API call goes through the policy evaluator before executing.

### 3.3 Agent Loader (`engine/loader/`)
- [x] Discover agents from `engine/agents/*/cosmo.manifest.json` (built-in)
- [x] Discover agents from `~/.cosmos/agents/*/cosmo.manifest.json` (user)
- [x] On name conflict: user version wins
- [x] Validate manifest on first load (lazy)
- [x] Build tool definition list for LLM (function name, description, params schema)
- [x] Expose loaded agents to Agents page (Tools sub-view)
- [x] Path traversal guard on entry file resolution
- [x] Agent name sanitization in storage path

### 3.4 Tool Dispatch Integration
- [x] Core loop calls `engine.Execute(agentName, functionName, args)` on tool_use
- [x] Wire to real manifest permission checks for all agents via `ToolManifestProvider` interface
- [ ] Engine checks policy → runs in V8 → returns result or error
- [ ] Concurrent read-only tools; sequential write tools (derived from permissions)
- [ ] Return structured result to core loop for LLM consumption

---

## Phase 4: VFS & Changelog

File safety net — every write is reversible.

### 4.1 VFS Layer
- [ ] Wrap all `fs.*` APIs through VFS
- [ ] Before any destructive op (write, truncate, unlink): snapshot original file
- [ ] Store snapshots in `.cosmos/snapshots/<session-id>/<hash>`
- [ ] Track interaction grouping (which tool call triggered which writes)

### 4.2 Changelog Integration
- [ ] Replace mock data in `changelog.go` with real VFS snapshot entries
- [ ] Group entries by interaction (multi-file edits in one exchange)
- [ ] Implement "Restore" action: revert all files in a group to snapshot state
- [ ] Timestamp and description from audit log correlation

---

## Phase 5: Session Management

### 5.1 Session Persistence (`core/session.go`)
- [ ] Save session on exit: messages, token usage, cost, agents invoked, files modified
- [ ] File format: `~/.cosmos/sessions/<project-path-dotted>-<timestamp>.json`
- [ ] Session description: user's last prompt
- [ ] Load session for restore

### 5.2 Chat Commands
- [ ] `/model <name>` — switch model, tab-completion from `ListModels`
- [ ] `/clear` — clear conversation, start fresh session
- [ ] `/compact` — summarize conversation to reduce token usage
- [ ] `/context` — show token usage / context window percentage
- [ ] `/restore <session>` — restore saved session, tab-completion from session list
- [ ] Command parsing in chat input (detect `/` prefix, route accordingly)

---

## Phase 6: Agent Creation

### 6.1 LLM-Driven Agent Generation
- [ ] Agents page "Create" sub-view: send user prompt to LLM with agent-creation system prompt
- [ ] LLM generates `index.js` + `cosmo.manifest.json`
- [ ] Save to `~/.cosmos/agents/<name>/`
- [ ] User enable/disable toggle for generated agents
- [ ] Manifest Ed25519 signing for newly created agents (private key: `~/.cosmos/agents.private.key`)
- [ ] Generate user Ed25519 keypair at install/setup if missing

---

## Phase 7: Polish & Hardening

### 7.1 Chat Rendering Enhancements
- [ ] Inline diffs when agents modify files (`+`/`-` lines with highlighting)
- [ ] Copy to clipboard for code blocks and messages
- [ ] Hotkey menu on messages: retry, edit, copy, delete
- [ ] Spinner during tool execution
- [ ] `⚒` icon for tool invocations in chat

### 7.2 Bundled Default Agents
- [ ] `code-analyzer` — analyze codebase structure and quality
- [ ] `code-editor` — read and write source files
- [ ] `test-runner` — execute tests via Docker
- [ ] Write manifests with appropriate permissions for each

### 7.3 Security Hardening
- [ ] Manifest Ed25519 verification on every load (detect tampering)
- [ ] Embed trusted Ed25519 public keys in code (include Giacomo default key; allow additional user keys)
- [ ] Re-approval flow when manifest permissions change between versions
- [ ] Verify no host process execution paths exist outside Docker
- [ ] Fuzz manifest parser with malformed inputs
- [ ] Fuzz policy evaluator with edge-case permission keys
- [ ] **HTTP: detect and reject silently truncated responses** — `io.LimitReader` caps at 10 MB but caller gets partial data without warning; check `Content-Length` and signal truncation as error (`api_http.go`)
- [ ] **fs.write: tighten `MkdirAll` behavior** — directory creation is a side effect not independently permission-checked; use `0o700` instead of `0o755`, consider requiring parent to already exist (`api_fs.go`)
- [ ] **`escapeJSString`: escape NUL bytes and all control characters** — current implementation skips `\x00` and U+0000–U+001F; use `\uNNNN` format for all control chars and validate input is valid UTF-8 (`runtime.go`)
- [ ] **`ui.emit`: add rate limiting and size bounds** — no permission check, no message size cap, no rate limit; a malicious tool can flood the UI or OOM with a huge string; add max message size (~64 KB), rate limit (~100/s), and strip ANSI escape sequences (`api_ui.go`)
- [ ] **`fs.read`: add file size limit** — `os.ReadFile` has no cap; a tool can OOM by reading `/dev/zero` or a multi-GB file; check `os.Stat` size before reading, cap at ~50 MB (`api_fs.go`)
- [ ] **Agent loader: resolve symlinks in discovery paths** — `discoverAgents` and `loadAgent` do not resolve symlinks; a symlink in `~/.cosmos/agents/` can load code from outside the expected tree; apply `filepath.EvalSymlinks` to agent dirs before path traversal checks (`loader.go`)

### 7.3.1 Concurrency & Storage Safety
- [ ] **Rethink storage for concurrency** — `storage.set` uses a read-modify-write pattern on a JSON file without file locking; currently safe because write tools run sequentially, but this assumption is fragile and undocumented at the storage layer. Options: (a) file-level `flock` during read-modify-write, (b) in-memory cache per agent with mutex + periodic flush, (c) explicit concurrency contract in `ToolExecutor` interface. Should also document and enforce the read-concurrent/write-sequential scheduling invariant at the engine level, not rely on it being implicit.

### 7.4 Testing
- [ ] Unit tests for manifest parsing (valid, invalid, edge cases)
- [ ] Unit tests for policy evaluator (all modes, glob matching, default deny)
- [ ] Unit tests for audit log (write, read, rotation, redaction)
- [ ] Integration tests for V8 runtime (small JS fixtures)
- [ ] UI tests: simulate messages, verify `View()` output
- [ ] End-to-end: user prompt → LLM → tool call → V8 → result → UI

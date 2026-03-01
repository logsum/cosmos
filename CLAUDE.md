# CLAUDE.md

This file is the authoritative design reference for Cosmos. It guides both human developers and Claude Code when working on this codebase.

## What is Cosmos

Cosmos is a secure coding agent with a TUI (Terminal User Interface) built in Go. Its core differentiator is **security and control**: tools and agents are written in JavaScript and executed inside V8 isolates, with no direct host access. All interaction with the outside world goes through permission-controlled Go-side APIs.

The LLM orchestrates agents and tools. Tools run sandboxed in V8. Every permission is declared in a manifest, evaluated by a policy engine, and logged in an audit trail. The user sees everything in a tabbed terminal interface and approves sensitive operations inline in the chat.

## Platform Support

Cosmos targets **macOS and Linux only**. Windows is not supported and there are no plans to add Windows support. Platform-specific code (e.g., `os.Rename` atomicity, symlink handling, `O_NOFOLLOW` flags) assumes POSIX semantics without Windows compatibility shims.

## Build and Run Commands

```bash
go build -o cosmos       # Build
go run main.go           # Run directly
./cosmos                 # Run compiled binary
go mod download          # Install dependencies
go mod tidy              # Update dependencies
```

## Project Structure

```
cosmos/
├── main.go                    # Minimal entry point (~25 lines)
├── go.mod / go.sum
├── CLAUDE.md                  # This file — design reference
│
├── app/                       # Bootstrap layer (composition root)
│   ├── app.go                 # Application struct + Run method
│   ├── bootstrap.go           # Dependency wiring functions
│   ├── adapter.go             # Event translation between core and UI
│   └── bootstrap_test.go      # Unit tests for wiring logic
│
├── ui/                        # TUI layer (exists)
│   ├── scaffold.go            # Central tabbed UI manager
│   ├── app.go                 # Top-level wrapper with conditional prompt
│   ├── chat.go                # Chat page — LLM conversation
│   ├── agents.go              # Agents page — history, tools, create
│   ├── changelog.go           # Changelog page — file modification history
│   ├── info_page.go           # Simple centered info (template page)
│   ├── tabbar.go              # Tab bar component
│   ├── statusbar.go           # Status bar component
│   ├── splash.go              # Welcome screen with ASCII art
│   ├── notifier.go            # Channel-based UI update trigger
│   ├── keymap.go              # Key binding definitions
│   ├── pricing_modal.go       # Pricing display modal
│   └── setup.go               # Default scaffold + page registration
│
├── core/                      # Orchestration layer
│   ├── loop.go                # LLM <-> tool-use loop
│   ├── events.go              # Framework-agnostic event types emitted by the loop
│   ├── session.go             # Session state, persistence, restore
│   ├── pricing.go             # Token counting & cost per model
│   ├── currency.go            # Currency conversion (Frankfurter API) & display formatting
│   └── provider/              # LLM provider abstraction (interface only)
│       ├── provider.go        # Provider interface + types
│       └── provider_test.go   # Interface contract tests
│
├── providers/                 # LLM provider implementations
│   └── bedrock/               # AWS Bedrock implementation
│       ├── bedrock.go         # Provider implementation
│       ├── convert.go         # Message/type conversion
│       ├── stream.go          # Streaming response iterator
│       ├── pricing.go         # Dynamic pricing fetch from AWS Pricing API
│       ├── bedrock_test.go    # Implementation tests
│       └── pricing_test.go    # Pricing tests
│
├── engine/                    # V8 execution runtime
│   ├── tools/                 # [DEPRECATED] Stub implementation
│   │   └── stub.go            # Temporary executor replaced by V8 runtime
│   ├── runtime/               # ✅ V8 isolate management, Go-side APIs
│   │   ├── runtime.go         # V8Executor implementation
│   │   ├── api_fs.go          # Filesystem APIs (read, write, list, stat, unlink)
│   │   ├── api_http.go        # HTTP client APIs (get, post)
│   │   ├── api_storage.go     # KV storage APIs (get, set)
│   │   ├── api_ui.go          # UI emit API
│   │   └── runtime_test.go    # V8 runtime tests
│   ├── loader/                # ✅ Agent discovery and loading
│   │   ├── loader.go          # Loads from builtin/user directories
│   │   └── loader_test.go
│   ├── manifest/              # ✅ Manifest parsing & validation
│   │   ├── schema.go          # Manifest parsing, Ed25519 signing
│   │   └── manifest_test.go
│   ├── policy/                # ✅ Permission enforcement & audit
│   │   ├── evaluator.go       # Permission checks (allow/deny/request_once/request_always)
│   │   ├── audit.go           # JSON-lines audit logging with redaction
│   │   └── policy_test.go
│   ├── maintenance/           # ✅ Session cleanup
│   │   └── cleanup.go         # Delete old session data (30 days)
│   └── agents/                # Bundled default agents (loader ready, directory empty)
│       └── [no agents bundled yet]
│
└── config/                    # ✅ Global defaults
    └── defaults.go
```

## Architecture Overview

### Package Communication

`core` is the orchestrator. The `app` package is the **Composition Root** where all dependencies are wired together. `main.go` calls `app.Bootstrap()` to create and configure all components, then runs the application. The UI sends user messages to core via channels, core runs the LLM loop, and emits framework-agnostic events. An adapter in `app/adapter.go` translates these core events into Bubble Tea messages for the UI. This keeps `core` decoupled from any TUI framework.

```
User Input (TUI)
       │
       ▼
   core/loop.go ──► LLM Provider (Bedrock, etc.)
       │                    │
       │              tool_use response
       │                    │
       ▼                    ▼
  engine/policy ──► engine/runtime (V8)
  (check perms)     (execute JS tool)
       │                    │
       │              result / error
       │                    │
       ▼                    ▼
   core/loop.go ──► send result back to LLM
       │                    ... (loop until done)
       ▼
   UI update via Notifier
```

### Package Dependency Rules

The dependency graph is strictly **one-directional**: `main.go` → `app` → `{core, ui, providers}` → `engine`. The `ui` package is a **leaf** — it is consumed by `app` but never imported by `core` or `engine`.

- **`core` must never import `cosmos/ui`** or any TUI framework (`bubbletea`, `lipgloss`, `glamour`, etc.). The core package defines its own event types (`core/events.go`) and a framework-agnostic `Notifier` interface (`Send(msg any)`). Translation to UI-specific message types happens in the adapter layer inside `app/adapter.go`.
- **`engine` must never import `cosmos/ui`** or `cosmos/core`. It exposes interfaces that `core` consumes.
- **`app` is the Composition Root.** It creates the adapter (`coreNotifierAdapter`) that type-switches on core events and forwards corresponding `ui.*Msg` values to the Bubble Tea runtime. Any new core event type needs a case added there.
- **Adding a new event**: define the type in `core/events.go`, emit it in `core/loop.go`, add a case to `coreNotifierAdapter.Send()` in `app/adapter.go`, update the compile-time test in `app/adapter_test.go`, and add the corresponding `ui.*Msg` in `ui/messages.go` if a new UI message is needed.

**Event Handling Safety:**

The `core.Notifier` interface uses `Send(msg any)` to remain framework-agnostic, but this creates a weakly typed contract. Two safety mechanisms mitigate this:

1. **Compile-time documentation test** (`app/adapter_test.go`): `TestAllCoreEventsHandled()` lists all core event types. When adding a new event, this test must be updated, serving as a checklist for the developer.

2. **Runtime logging** (`app/adapter.go`): The adapter's `default` case logs unhandled events to stderr, making integration mistakes immediately visible during development and testing.

Together, these ensure that forgotten event types are caught either at compile time (via test updates) or runtime (via logs), preventing silent failures.

### Design Patterns

- **Elm Architecture**: All UI components follow Bubble Tea's Model-View-Update
- **Composition**: Scaffold composes tab bar, status bar, and pluggable pages
- **Message Passing**: Components communicate via typed messages, not direct calls
- **Lazy Rendering**: Only the active page renders; inactive pages are dormant
- **Channel-based Notifier**: Thread-safe mechanism for engine/core to trigger UI updates
- **Composition Root**: All dependency wiring happens in the `app` package, keeping `main.go` minimal

---

## Bootstrap Layer (`app/`)

The `app` package is the **Composition Root** where all dependencies are instantiated and wired together.

### Purpose
- Separate application lifecycle from entry point (`main.go`)
- Make dependency wiring testable
- Enable reuse of bootstrap logic in CLI commands, tests, alternative UIs

### Structure
- **`app.go`** - Application struct and lifecycle (`Run()` method)
- **`bootstrap.go`** - Dependency construction functions (one per phase)
- **`adapter.go`** - Event translation between core and UI
- **`bootstrap_test.go`** - Unit tests for wiring logic

### Bootstrap Phases

The `Bootstrap()` function orchestrates dependency creation in 8 phases:

1. **Load configuration** - Parse config file, ensure directories exist
2. **Currency formatter** - Fetch exchange rates if non-USD (falls back to USD on error)
3. **LLM provider** - Initialize Bedrock with pricing config
4. **UI setup** - Create scaffold and notifier
5. **Pricing tracker** - Wire callbacks to status bar updates
6. **Core session** - Create stub executor (`engine/tools`), tool definitions, event adapter
7. **UI configuration** - Set up pages, status items, current directory
8. **Bubble Tea program** - Create TUI with correct screen mode (no alt screen)

Each phase is a separate function in `bootstrap.go` for testability:
- `loadConfig()` - Returns `(config.Config, []string, error)`
- `setupCurrencyFormatter(ctx, cfg)` - Returns `(*core.CurrencyFormatter, error)`
- `setupProvider(ctx, cfg)` - Returns `(provider.Provider, error)`
- `setupTracker(notifier, formatter)` - Returns `*core.Tracker`
- `setupSession(ctx, cfg, provider, tracker, notifier)` - Returns `(*core.Session, []provider.ToolDefinition)`
- `configureUI(scaffold, session, tools, model)` - Returns `error`
- `setupProgram(scaffold, notifier)` - Returns `*tea.Program`

### Application Lifecycle

The `Application` struct holds all wired components:

```go
type Application struct {
    Config            config.Config
    Session           *core.Session
    Scaffold          *ui.Scaffold
    Program           *tea.Program
    CurrencyFormatter *core.CurrencyFormatter
    Tracker           *core.Tracker
}
```

The `Run(ctx)` method manages the lifecycle:
1. Derive a cancelable context (`context.WithCancel`) so in-flight provider calls are interrupted on exit
2. Start core session (`Session.Start(ctx)`)
3. Run Bubble Tea program (blocks until exit)
4. On exit: cancel context (interrupts provider calls), then stop core session (`defer Session.Stop()`)

### Adding a New Bootstrap Phase

To add a new dependency to bootstrap:

1. Create a new function in `bootstrap.go`:
   ```go
   func setupMetrics(cfg config.Config) (*metrics.Collector, error) {
       // Initialize and return component
   }
   ```

2. Call it from `Bootstrap()` in the appropriate order:
   ```go
   // 9. Initialize metrics
   metricsCollector, err := setupMetrics(cfg)
   if err != nil {
       return nil, fmt.Errorf("initializing metrics: %w", err)
   }
   ```

3. Add to `Application` struct if needed:
   ```go
   type Application struct {
       // ... existing fields
       Metrics *metrics.Collector
   }
   ```

4. Add unit test in `bootstrap_test.go`:
   ```go
   func TestSetupMetrics(t *testing.T) {
       cfg := config.Config{MetricsEnabled: true}
       collector, err := setupMetrics(cfg)
       if err != nil {
           t.Fatalf("setupMetrics failed: %v", err)
       }
       if collector == nil {
           t.Fatal("expected non-nil collector")
       }
   }
   ```

### Testing

```bash
go test ./app -v               # Unit tests
go test ./app -v -short        # Skip network tests (currency API)
```

Unit tests verify each bootstrap phase independently. Integration tests (marked with `t.Skip()`) require full environment (AWS credentials, network access).

### Error Handling

Bootstrap functions return structured errors. Non-fatal errors (e.g., currency fetch) fall back to defaults and log warnings. Fatal errors (e.g., config load, provider init) return early with context.

---

## UI Layer

### Scaffold (ui/scaffold.go)

Central UI manager. Manages tabbed interface: tab bar bottom-left, status bar bottom-right, page content in the body. Pages are pluggable `tea.Model` implementations registered via `AddPage(key, title, page)`.

### App Wrapper (ui/app.go)

Wraps Scaffold with a text input prompt. The prompt is **conditionally visible** — it is hidden on the "agents" and "changelog" pages (only shown on "chat"). Captures Enter to emit `PromptSubmitMsg` routed to the active page.

### Pages

**Chat** (ui/chat.go): Conversation with the LLM. Displays user (purple bar) and assistant (orange bar) messages. Text wrapping, scroll via `tea.Printf`. First prompt prints the splash screen. This is where permission prompts from `request_once`/`request_always` appear inline.

**Agents** (ui/agents.go): Three sub-views switched with keys 1/2/3:
- **[1] History** (default): Execution log with expandable details per entry. Shows status (success/failed/running), agent name, tools used. Will read from the audit log file.
- **[2] Tools**: List of available tools with name, description, and V8 load status.
- **[3] Create**: Submit a prompt to have the LLM generate a new agent (JS code + manifest), saved to `~/.cosmos/agents/<name>/`. User can enable/disable generated agents.

**Changelog** (ui/changelog.go): Tracks all files modified by agents during a session. Grouped by interaction — if multiple files are edited in one exchange, they can be restored together. Powered by the VFS layer which snapshots files before any write operation. Expandable entries with a "Restore" action.

### Styling

Fixed color palette (no theme configuration):
- **208 (Orange)**: Primary accent — borders, highlights, active selections
- **93 (Purple/Violet)**: Section headers, user message indicator
- **245 (Gray)**: Dimmed/inactive text
- **240 (Dark Gray)**: Pipe separators
- **135 (Purple)**: Splash screen border
- **46 (Green)**: Success status
- **196 (Red)**: Failed status
- **226 (Yellow)**: Running status

### Keybindings

- `Shift+Left/Right` or `[` / `]`: Switch tabs
- `Ctrl+C`: Exit
- `?`: Help/keybinding overlay for current page
- `Enter`: Send message (chat), expand/collapse (changelog, agents)
- `Up/Down`: Navigate lists (changelog, agents)
- `1/2/3`: Switch agents sub-views
- Minimum terminal size enforced; status bar items can collapse if terminal is too narrow

### Adding a New Page

1. Create `ui/mypage.go`, implement `tea.Model` (Init, Update, View)
2. Register in `ui/setup.go` via `s.AddPage("key", "Title", NewMyPage(s))`
3. If the page should not show the prompt, add its key to `isPromptEnabled()` in `ui/app.go`

### Terminal Scrollback Strategy (Chat Page)

**Critical Design Decision:** Cosmos runs in the **primary screen buffer** (NOT alternate screen), enabling native terminal scrollback.

#### Why No Alternate Screen

- **DO NOT** use `tea.WithAltScreen()` in `main.go`
- Alternate screen mode creates an isolated buffer with no scrollback history
- Users need to scroll back to see the welcome logo, old messages, and code that scrolled off
- iTerm/terminal native scroll (cmd+↑/↓, mouse wheel) is the expected UX

#### Line-Level Flushing Strategy

The chat maintains an in-memory message buffer displayed in `View()`. As new content arrives and **lines scroll off** the visible window, **only those specific lines** are written to stdout (not entire messages).

**Implementation (ui/chat.go):**

```
┌─ In-Memory State ────────────────────────────────┐
│  messages: []chatMessage                         │
│  accumulatedText: string (streaming assistant)   │
│  flushedLineCount: int (lines already flushed)   │
└──────────────────────────────────────────────────┘
                    │
                    ▼
    ┌─ View() renders last N lines ─────┐
    │  - Completed messages              │
    │  - Streaming message (if active)   │
    │  - Proper wrapping, colored bars   │
    └────────────────────────────────────┘
                    │
                    ▼ (as lines scroll off)
    ┌─ flushOldMessages() ──────────────┐
    │  1. buildAllRenderedLines()       │
    │     → ALL messages + streaming    │
    │     → Returns []string (1 line each)
    │  2. Calculate: totalLines - visibleLines
    │  3. Flush lines[flushedLineCount:firstVisibleLine]
    │  4. Update flushedLineCount       │
    └────────────────────────────────────┘
                    │
                    ▼
         stdout → terminal scrollback
         (with colored bars ▌, proper wrapping)
```

**Key Properties:**

1. **Granular flushing**: Only lines that scrolled off are written, not entire messages
2. **Partial messages**: If a long message is half-visible, only the scrolled-off portion is flushed
3. **No duplication**: Each line written exactly once
4. **Streaming included**: `buildAllRenderedLines()` MUST include `accumulatedText` to match `View()` exactly
5. **ANSI colors**: Stdout uses ANSI codes (not lipgloss) for colored bars `▌`

**Critical Invariant:**

```
buildAllRenderedLines() output === View() output
```

If these diverge (e.g., streaming message missing from one), line counts will be wrong and flushing breaks. See inline warnings in `ui/chat.go` for safeguards.

#### Text Wrapping & Newlines

- **`wrapText()`** preserves newlines by splitting on `\n` first, then wrapping each line
- Code blocks, lists, and paragraph structure are maintained
- Empty lines (blank lines between paragraphs) are preserved

#### Debugging Scrollback Issues

If duplicates or missing lines appear in scrollback:

1. Check `buildAllRenderedLines()` includes streaming message (`m.accumulatedText`)
2. Verify line count calculation: `totalLines - visibleLines`
3. Ensure `flushedLineCount` is updated after each flush
4. Confirm both View() and buildAllRenderedLines() use same `wrapText()` width

---

## V8 Engine (`engine/`)

### V8 Runtime (`engine/runtime/`) ✅

**Status:** Fully implemented with `rogchap.com/v8go`

The V8 runtime provides true sandboxed JavaScript execution for tools. Each tool runs in its own V8 isolate with zero direct host access.

**Implementation Features:**

- ✅ **One isolate per tool** - True sandboxing. A misbehaving tool cannot corrupt another tool's heap
- ✅ **Lazy loading** - Tools are compiled into V8 on first execution, not at startup
- ✅ **Hot reload** - Stat-on-use: checks file modification time and recompiles if JS changed
- ✅ **Timeout enforcement** - Per-manifest timeout, clamped to 5-minute global maximum
- ✅ **Error capture** - JavaScript exceptions converted to Go errors with stack traces
- ✅ **Context cancellation** - Respects `context.Context` for clean shutdown
- ✅ **Function validation** - JS identifier check prevents script injection attacks

**V8Executor Interface** (implemented in `engine/runtime/runtime.go`):

```go
type V8Executor struct {
    isolates   map[string]*isolateState  // Per-tool V8 isolate
    apiRegistry APIRegistry               // Go-side APIs injected into V8
    mu         sync.Mutex                 // Protects isolates map
}

func (e *V8Executor) Execute(ctx context.Context, name string, input map[string]any) (string, error)
func (e *V8Executor) RegisterTool(spec ToolSpec) error
```

**Stub Executor (Deprecated):**

The `engine/tools/stub.go` package contains a legacy stub implementation used during early development. It is **replaced** by the V8 runtime but remains in the codebase for backward compatibility during testing.

### Agent Loading (`engine/loader/`) ✅

**Status:** Fully implemented

The loader discovers and loads agents from disk:

1. **Built-in agents**: `engine/agents/*/cosmo.manifest.json` (directory exists but no agents bundled yet)
2. **User agents**: `~/.cosmos/agents/*/cosmo.manifest.json`
3. **Conflict resolution**: User version wins on name conflict
4. **Validation**: Manifests parsed and validated on load
5. **Tool registration**: Registers tools with V8Executor
6. **ToolDefinition generation**: Converts manifests to LLM-compatible schemas

**Key Functions:**

```go
func Load(builtinDir, userDir string, executor *V8Executor) ([]ToolDefinition, []AgentInfo, []error)
```

- Non-fatal errors collected (invalid agents logged but don't halt)
- Path traversal protection on entry file resolution
- Agent name sanitization for storage paths

### APIs Injected into V8 ✅

**Status:** Fully implemented (except `docker.*`)

Tools have zero direct host access. All capabilities come from Go-side APIs injected into the V8 context:

| API | Status | Description | Implementation |
|---|---|---|---|
| `fs.read(path)` | ✅ | Read file contents | `api_fs.go` - Permission checked, symlinks resolved, 50 MB size limit recommended |
| `fs.write(path, content)` | ✅ | Write file contents | `api_fs.go` - Permission checked, parent dirs created (0700), overwrites existing |
| `fs.list(path)` | ✅ | List directory contents | `api_fs.go` - Returns array of filenames |
| `fs.stat(path)` | ✅ | Get file metadata | `api_fs.go` - Returns size, modTime, isDir |
| `fs.unlink(path)` | ✅ | Delete file | `api_fs.go` - Permission checked |
| `http.get(url, headers)` | ✅ | HTTP GET request | `api_http.go` - 10 MB response limit, 30s timeout |
| `http.post(url, body, headers)` | ✅ | HTTP POST request | `api_http.go` - 10 MB response limit, 30s timeout |
| `storage.get(key)` | ✅ | Get from KV store | `api_storage.go` - Per-agent JSON file storage |
| `storage.set(key, value)` | ✅ | Set in KV store | `api_storage.go` - Atomic read-modify-write |
| `ui.emit(message)` | ✅ | Send to chat window | `api_ui.go` - Progress updates, no permission check |
| `docker.*` | ⚠️ Planned | Docker build/run | For build agents |

**Permission Integration:**

- ✅ All tools are evaluated against their manifest permission rules via `ToolManifestProvider` interface
- Tools with no declared permissions are treated as pure functions (allowed)
- Rate limiting (5s window) prevents permission prompt spam

**Security Notes:**

- ✅ No shell/process execution on host
- ✅ No inter-tool communication
- ⚠️ Build operations will go through Docker (planned)

### Concurrency

Read-only tools can run concurrently. Write tools run sequentially. This is derived from the permission flags in the manifest — if a tool has `fs:write`, the scheduler serializes it.

---

## Manifest System (`cosmo.manifest.json`)

### Schema

```jsonc
{
  "name": "code-analyzer",
  "version": "1.0.0",
  "description": "Analyzes codebase structure and quality",
  "entry": "index.js",
  "functions": [
    {
      "name": "analyzeFile",
      "description": "Analyze a single file for code quality",
      "params": {
        "filePath": { "type": "string", "required": true },
        "depth": { "type": "number", "default": 1 }
      },
      "returns": { "type": "object", "description": "Analysis result with metrics" }
    }
  ],
  "permissions": {
    "fs:read:~/.config/**": "allow",
    "fs:read:./src/**": "allow",
    "fs:write": "deny",
    "net:http": "request_once",
    "docker:run": "request_always"
  },
  "timeout": "30s"
}
```

### Permission Modes

| Mode | Behavior |
|---|---|
| `allow` | Always granted silently |
| `deny` | Always blocked silently |
| `request_once` | Prompt user once per project, remember the decision |
| `request_always` | Prompt user every time, with optional timeout and default |

Permission keys support glob patterns with `~` for home directory (e.g., `fs:read:~/Documents/**`).

### Permission Request UI ⚠️

**Status:** Fully implemented

- ✅ Full UI flow implemented (inline prompt, y/n input, timeout handling)
- ✅ Channel-based blocking between core and UI
- ✅ Policy persistence for `request_once` grants
- ✅ All tools evaluated against manifest rules via `ToolManifestProvider` interface
- ✅ Rate-limited permission prompts (5s deduplication window)

When `request_once` or `request_always` triggers, the prompt appears **inline in the Chat page** showing what the tool is trying to do (e.g., "code-editor wants to write to `/src/main.go` — allow?"). The manifest can declare a timeout and default value (allow/deny). If no timeout and no default, it waits forever.

### Discovery and Loading

1. Built-in agents: `engine/agents/*/cosmo.manifest.json`
2. User agents: `~/.cosmos/agents/*/cosmo.manifest.json`
3. On name conflict, the user's home directory version wins
4. Agents are independent — no cross-agent dependencies
5. Shared JS libraries can live in a common folder in the agents directory
6. Manifests are validated lazily (on first load)

### Integrity

Manifests include an Ed25519 signature for permission declarations. Signing uses a private key stored outside the repo at `~/.cosmos/agents.private.key` (Giacomo local path: `/home/giacomo/.cosmos/agents.private.key`). Verification uses embedded Ed25519 public key(s) in code (including Giacomo's default key). Other users generate their own keypair during installation and add their public key to the trusted verifier set. When manifest permissions change between versions, the user must re-approve.

---

## Policy Engine (`engine/policy/`) ✅

**Status:** Fully implemented with effect types, glob matching, and audit logging

### Evaluation Rules

The policy evaluator (`evaluator.go`) implements a sophisticated permission system:

1. ✅ Check manifest for the requested permission
2. ✅ If declared: evaluate the mode (allow/deny/request_once/request_always)
3. ✅ If **not declared**: deny and log (default-deny policy)
4. ✅ Per-project overrides from `.cosmos/policy.json` take absolute precedence
5. ✅ `request_once` grants scoped per project, persisted to `.cosmos/policy.json`
6. ✅ Glob pattern matching via `doublestar/v4` with specificity-based rule selection
7. ✅ Thread-safe with `sync.Mutex`

**Effect Types:**

- `EffectAllow` - Grant silently
- `EffectDeny` - Block silently
- `EffectPromptOnce` - Prompt user, remember decision
- `EffectPromptAlways` - Prompt every time

**Decision Source Tracking:**

Every evaluation returns the source of the decision:
- `manifest` - From agent's cosmo.manifest.json
- `policy_override` - From .cosmos/policy.json team override
- `persisted_grant` - From prior `request_once` user approval
- `default_deny` - No matching rule found

### Audit Log ✅

**Status:** Fully implemented with session-scoped logging

- ✅ **Format**: JSON lines (one JSON object per line), grep-friendly
- ✅ **Location**: `.cosmos/audit-{sessionID}.jsonl` per session
- ✅ **Fields**: timestamp, sessionID, agent, tool, permission, decision, decisionSource, arguments
- ✅ **Redaction**: Automatically redacts keys containing "token", "key", "password", "secret", "credential", "auth"
- ✅ **Cleanup**: Session-based cleanup via `engine/maintenance` package (deletes after 30 days)
- ✅ **Thread-safe**: Single-threaded writes, no concurrent audit operations
- ⚠️ **UI Integration**: Agents page History view designed but currently shows mock data

### Permission Request UI Flow

When a tool requires user permission (`request_once` or `request_always`), the flow is:

1. **Core checks permission** (`core/loop.go:checkPermission()`):
   - Evaluates manifest rules via `evaluator.Evaluate()`
   - If result is `EffectPromptOnce` or `EffectPromptAlways`, proceed to step 2
   - Otherwise (Allow/Deny), return immediately

2. **Core creates response channel**:
   - `responseChan := make(chan PermissionResponse, 1)` (buffered to prevent goroutine leaks)
   - Channel type: `chan<- PermissionResponse` with fields `Allowed bool` and `Remember bool`

3. **Core emits event**:
   - `PermissionRequestEvent` sent via `notifier.Send()` with:
     - Tool metadata (name, agent, permission key)
     - Human-readable description
     - Timeout duration (default: 30s, configurable per manifest)
     - `ResponseChan` embedded in the event

4. **Adapter translates event** (`app/adapter.go`):
   - Type-switches on `PermissionRequestEvent`
   - Wraps `ResponseChan` in a `func(allowed, remember bool)` callback
   - Forwards as `ChatPermissionRequestMsg` to UI with `RespondFunc` (no `core` import in `ui`)

5. **Chat page renders prompt** (`ui/chat.go`):
   - Inline yellow warning bar with permission description
   - Prompt: `[y] Allow  [n] Deny`
   - `App` intercepts y/n keys when `permissionPending` is true, forwarding them to scaffold
   - `ChatModel` emits `PermissionDecisionMsg` which triggers the callback

6. **UI responds via callback**:
   - Calls `respondFunc(allowed, remember)` which writes to core's channel
   - Marks prompt as resolved in UI

7. **Core unblocks**:
   - `select` statement receives response from channel
   - If `Allowed==true`: execute tool normally
   - If `Allowed==false`: return error as tool result
   - For `request_once`: persist decision via `evaluator.RecordOnceDecision()` (writes to `.cosmos/policy.json`)
   - If persistence fails, emit `ErrorEvent` to notify user

8. **Timeout** (core-owned):
   - Core's `select` includes `time.After(timeout)` — single timeout owner
   - On timeout, core emits `PermissionTimeoutEvent` and applies `DefaultAllow`
   - Adapter translates to `ChatPermissionTimeoutMsg` so UI marks prompt as resolved

**Key Design Choices:**

- **Channel-based blocking**: Core synchronously waits on channel; no polling or callbacks
- **Buffered channel (size 1)**: Prevents goroutine leak if UI is torn down before responding
- **Single timeout owner**: Core owns the timeout exclusively; UI does not run its own timer
- **Context cancellation**: `ctx.Done()` case allows clean shutdown during permission prompts
- **Callback isolation**: Adapter wraps `chan<- core.PermissionResponse` in a callback, so `ui` never imports `core`
- **App-level key routing**: `App.permissionPending` routes y/n to scaffold during permission prompts, bypassing the text input

**Threading Model:**

- Core loop is single-threaded (sequential message processing)
- `checkPermission()` called synchronously from `processUserMessage()`
- `evaluator.Evaluate()` and `RecordOnceDecision()` are internally thread-safe (`sync.Mutex`)
- No concurrent permission checks (tools execute sequentially)

---

## LLM Provider Architecture

### Interface (`core/provider/`)

The `core/provider` package contains **only** the provider interface and shared types. It defines the contract that all provider implementations must satisfy:

- `Provider` interface with `Send()` and `ListModels()` methods
- Shared types: `Message`, `Request`, `StreamIterator`, `StreamChunk`, `ToolDefinition`, etc.
- Error sentinels: `ErrThrottled`, `ErrAccessDenied`, `ErrModelNotFound`, `ErrModelNotReady`

The interface supports:
- Streaming responses (token-by-token delivery to the chat)
- Tool use protocol (parse tool calls from responses, return tool results)
- Model listing (for `/model` command with tab-completion)
- Token counting and pricing per model

### Implementations (`providers/`)

Each provider lives in its own package under `providers/`:

**`providers/bedrock/`** - AWS Bedrock implementation:
- `bedrock.go` - Implements `provider.Provider` using Bedrock's ConverseStream API
- `convert.go` - Message/type conversion to Bedrock formats
- `stream.go` - Streaming response iterator
- `pricing.go` - Dynamic pricing fetch from AWS Pricing API
- `bedrock_test.go`, `pricing_test.go` - Implementation tests

Future providers will follow the same pattern:
- `providers/openai/` - OpenAI implementation
- `providers/anthropic/` - Anthropic direct implementation

Each provider is **independent** - no coupling between implementations. The `core` package only depends on the interface.

### Commands

| Command | Behavior |
|---|---|
| `/model <name>` | Switch model. Tab triggers fetch of available models for selection |
| `/clear` | Clear conversation, start fresh |
| `/compact` | Summarize conversation to reduce token usage |
| `/context` | Show current context usage (tokens used / limit) |
| `/restore <session>` | Restore a saved session, with tab-completion |

### Auto-Compaction

- At **50%** context usage: suggest `/compact` to the user
- At **90%** context usage: force compaction automatically
- Compaction summarizes the conversation history and replaces it with the summary

---

## Session Management (`core/session.go`) ⚠️

**Status:** Lifecycle managed, persistence not implemented

**Current Implementation:**

- ✅ Session creation with UUID (`NewSession`)
- ✅ Message history tracking
- ✅ Start/Stop lifecycle methods with goroutine-based message loop
- ✅ Cleanup of old session data (30 days) via `engine/maintenance` package
- ✅ Session ID used in audit logs and file paths
- ❌ **No save-to-disk implementation**
- ❌ **No session restore functionality**
- ❌ **`/restore` command not implemented**

**Planned Design:**

- Storage location: `~/.cosmos/sessions/`
- Filename format: `<project-path-dotted>-<timestamp>.json` (e.g., `home.gmilani.myproject-20260222T103000.json`)
- Each session will track: messages, token usage, cost, agents invoked, files modified
- Session description: user's last prompt
- `/restore <session>` with tab-autocomplete to resume past sessions

---

## Cost Tracking (`core/pricing.go`, `core/currency.go`)

- Tracked per project: cumulative tokens (input/output) and cost
- Price adapted per model (pricing from AWS API)
- Status bar shows live counters: `▲<input tokens> ▼<output tokens>` and `$ <cost>`
- Display currency configurable via `currency` in config (ISO 4217 code, default `"USD"`)
- Exchange rate fetched once at startup from Frankfurter API and cached for the session
- Currency wiring is handled in `app/bootstrap.go` phase 2 (`setupCurrencyFormatter`): reads `config.Currency`, fetches rate if non-USD, builds `CurrencyFormatter`, passes to `NewTracker`.

### Token Estimation

Token counts use a heuristic of **1.2 characters per token** (`core/loop.go:estimateTokenCount`). This is intentionally conservative — it over-reports, which is safer for context-limit tracking. When the provider API reports actual token counts (e.g., Bedrock `ConverseStream` includes `inputTokens`/`outputTokens` in its metadata), those values are used directly via the `Tracker`. The 1.2 heuristic is only a fallback for pre-send estimation (e.g., deciding when to auto-compact).

### Model Info Caching

Model metadata (context window size, pricing) is cached per session via `sync.Once`. This is intentional — model metadata does not change during a session. No cache invalidation or TTL is needed.

---

## Chat Page Rendering

**Current Implementation:**

- ✅ **Markdown rendering** in assistant messages via `glamour` with "dark" style
- ✅ **Syntax highlighting** in code blocks via Chroma (integrated in glamour)
- ✅ **Spinner** during tool execution (visible in Chat and Agents pages)
- ✅ **Message streaming** with token-by-token accumulation
- ✅ **Permission prompts** inline with yellow warning bar
- ✅ **Text wrapping** with newline preservation
- ✅ **Graceful fallback** to plain text if markdown rendering fails

**Future Enhancements (decided, not yet implemented):**

- **Inline diffs** when agents modify files: `+`/`-` lines with syntax highlighting
- **Copy to clipboard** for code blocks and messages
- **Hotkey menu** on messages: retry, edit, copy, delete
- **Notifications** for events (agent finished, errors) appear inline in the chat window
- **Tool usage indicator**: Use `⚒` icon in chat when displaying tool invocations

---

## Changelog & VFS ⚠️

**Status:** UI complete, snapshotting not integrated

**Current Implementation:**

- ✅ Changelog page UI fully functional with expandable entries and restore buttons
- ✅ FS APIs (fs.read, fs.write, fs.unlink) callable from V8
- ✅ Permission checks integrated into file operations
- ❌ **File snapshotting not wired** (Changelog shows mock data)
- ❌ **Restore functionality not connected to actual files**
- ❌ **No interaction grouping for multi-file edits**

**Planned Design:**

The VFS (virtual filesystem) layer will wrap all file operations exposed to V8. Before any destructive operation (write, truncate, unlink), the VFS will snapshot the original file to `.cosmos/snapshots/<session-id>/<hash>`. The Changelog page will read these snapshots and present them as restorable entries grouped by interaction. Restoring will revert all files in that group to their pre-modification state.

---

## Testing Strategy

- Place `*_test.go` files alongside source files in their respective packages
- Use standard `go test ./...`
- Test UI page models by simulating messages and verifying `View()` output
- Test manifest parsing and policy evaluation with unit tests (no V8 needed)
- Test V8 runtime integration separately with small JS fixtures

---

## Key Security Principles

1. **No host execution**: Tools never spawn processes on the host. Build goes through Docker.
2. **Default deny**: Any permission not declared in the manifest is denied and logged.
3. **Isolated V8**: One isolate per tool. No shared state between tools.
4. **Manifest signing**: Ed25519 signature verification prevents tampering with permission declarations.
5. **Audit everything**: Every permission check is logged with redaction for sensitive arguments.
6. **VFS snapshots**: Every file write is reversible via the changelog.
7. **User in the loop**: `request_once` and `request_always` put the user in control with visible context about what the tool wants to do.

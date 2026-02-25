# CLAUDE.md

This file is the authoritative design reference for Cosmos. It guides both human developers and Claude Code when working on this codebase.

## What is Cosmos

Cosmos is a secure coding agent with a TUI (Terminal User Interface) built in Go. Its core differentiator is **security and control**: tools and agents are written in JavaScript and executed inside V8 isolates, with no direct host access. All interaction with the outside world goes through permission-controlled Go-side APIs.

The LLM orchestrates agents and tools. Tools run sandboxed in V8. Every permission is declared in a manifest, evaluated by a policy engine, and logged in an audit trail. The user sees everything in a tabbed terminal interface and approves sensitive operations inline in the chat.

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
│   ├── tools/                 # Tool execution layer (stub implementation)
│   │   └── stub.go            # Temporary executor with canned responses
│   ├── runtime.go             # V8 isolate management, Go-side API injection (planned)
│   ├── loader.go              # Discover, validate, and load agents from disk (planned)
│   ├── manifest/              # (planned)
│   │   ├── schema.go          # Manifest parsing & validation
│   │   └── manifest_test.go
│   ├── policy/                # (planned)
│   │   ├── evaluator.go       # Permission checks (allow/deny/request_once/request_always)
│   │   ├── audit.go           # JSON-lines audit logging with redaction
│   │   └── policy_test.go
│   └── agents/                # Bundled default agents (planned)
│       └── <agent-name>/
│           ├── index.js
│           └── cosmo.manifest.json
│
└── config/                    # Global defaults (planned)
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

### Tool Execution Layer (`engine/tools/`)

The `engine/tools` package provides the tool execution abstraction. Currently contains a **stub implementation** that will be replaced when V8 runtime lands.

**Current: Stub Executor (`stub.go`)**

- `StubExecutor` - Temporary implementation returning canned responses
  - `get_weather` - Returns mock JSON weather data
  - `read_file` - Simulates permission error after 5s delay
- `StubToolDefinitions()` - Returns hardcoded tool definitions

This stub exists solely to:
- Exercise the tool-use loop end-to-end during development
- Test the `core/loop.go` orchestration without V8 dependencies
- Provide working tools for UI/UX development

**Future: Real Executor (when V8 lands)**

The real tool executor will:
- Load tools from disk (`engine/agents/*/index.js` and `~/.cosmos/agents/`)
- Execute tools in V8 isolates (one isolate per tool for isolation)
- Enforce permissions via manifest (`engine/policy/evaluator.go`)
- Log all executions to audit trail (`engine/policy/audit.go`)
- Support hot reload when JS files change
- Apply per-tool timeouts from manifest

The `ToolExecutor` interface is defined in `core/loop.go`:

```go
type ToolExecutor interface {
    Execute(ctx context.Context, name string, input map[string]any) (string, error)
}
```

This interface decouples core orchestration from execution mechanism, making it easy to swap stub → V8 implementation without touching core logic.

### Runtime Design (planned)

- **Binding**: `rogchap.com/v8go` — real V8, cgo-based, chosen for full isolation and performance
- **Isolation**: One V8 isolate per tool. Truly sandboxed — a misbehaving tool cannot corrupt another tool's heap
- **Loading**: Lazy. Tools are loaded into V8 on first invocation, not at startup
- **Hot reload**: When a `.js` file changes on disk, the tool is reloaded automatically
- **Timeout**: Per-tool timeout declared in manifest. Global maximum of 5 minutes enforced regardless

### APIs Injected into V8

Tools have zero direct host access. All capabilities come from Go-side APIs injected into the V8 context:

| API | Description | Notes |
|---|---|---|
| `fs.*` | VFS — read, write, list, stat, unlink | Scoped per-tool via glob patterns in manifest. The VFS layer snapshots files before writes (for changelog restore). |
| `http.*` | HTTP client | Future: SSH, SQL connectors |
| `storage.*` | Persistent key-value store | Scoped per-tool, like localStorage |
| `ui.emit()` | Send messages to the chat window | For progress updates, user prompts |
| `docker.*` | Docker API for build/run | Used by build agents. No direct host process execution — this is a core security goal |

**Explicitly excluded**: No shell/process execution on the host. Build operations go through Docker via a dedicated agent. No inter-tool communication.

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

### Permission Request UI

When `request_once` or `request_always` triggers, the prompt appears **inline in the Chat page** showing what the tool is trying to do (e.g., "code-editor wants to write to `/src/main.go` — allow?"). The manifest can declare a timeout and default value (allow/deny). If no timeout and no default, it waits forever.

### Discovery and Loading

1. Built-in agents: `engine/agents/*/cosmo.manifest.json`
2. User agents: `~/.cosmos/agents/*/cosmo.manifest.json`
3. On name conflict, the user's home directory version wins
4. Agents are independent — no cross-agent dependencies
5. Shared JS libraries can live in a common folder in the agents directory
6. Manifests are validated lazily (on first load)

### Integrity

Manifests include an HMAC signature for permission declarations. The project embeds a public verification key; the private key is stored outside the project (e.g., in `~/Documents/`). When manifest permissions change between versions, the user must re-approve.

---

## Policy Engine (`engine/policy/`)

### Evaluation Rules

1. Check manifest for the requested permission
2. If declared: evaluate the mode (allow/deny/request)
3. If **not declared**: deny and log (default-deny policy)
4. No global user overrides — per-project `.cosmos/policy.json` can override manifest defaults for team enforcement
5. `request_once` grants are scoped per project (persisted to `.cosmos/policy.json`)

### Audit Log

- **Format**: JSON lines (one JSON object per line), grep-friendly
- **Location**: `.cosmos/audit.jsonl` in the project directory
- **Fields**: timestamp, agent, tool, permission, decision (allowed/denied/user-approved/user-denied), arguments (redacted for sensitive data)
- **Retention**: Rotate every 30 days or 10 MB, whichever comes first
- **UI**: The Agents page History view reads directly from this file

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

## Session Management (`core/session.go`)

- Stored at `~/.cosmos/sessions/`
- Filename format: `<project-path-dotted>-<timestamp>.json` (e.g., `home.gmilani.myproject-20260222T103000.json`)
- Each session tracks: messages, token usage, cost, agents invoked, files modified
- Session description: the user's last prompt
- `/restore <session>` with tab-autocomplete to resume past sessions

---

## Cost Tracking (`core/pricing.go`, `core/currency.go`)

- Tracked per project: cumulative tokens (input/output) and cost
- Price adapted per model (pricing from AWS API)
- Status bar shows live counters: `▲<input tokens> ▼<output tokens>` and `$ <cost>`
- Display currency configurable via `currency` in config (ISO 4217 code, default `"USD"`)
- Exchange rate fetched once at startup from Frankfurter API and cached for the session
- Currency wiring is handled in `app/bootstrap.go` phase 2 (`setupCurrencyFormatter`): reads `config.Currency`, fetches rate if non-USD, builds `CurrencyFormatter`, passes to `NewTracker`.

---

## Chat Page Rendering

Future enhancements (decided, not yet implemented):

- **Markdown rendering** in assistant messages via `glamour`
- **Syntax highlighting** in code blocks via `chroma` (github.com/alecthomas/chroma)
- **Inline diffs** when agents modify files: `+`/`-` lines with chroma highlighting
- **Copy to clipboard** for code blocks and messages
- **Hotkey menu** on messages: retry, edit, copy, delete
- **Spinner** during tool execution (visible in both Chat and Agents pages)
- **Notifications** for events (agent finished, errors) appear inline in the chat window
- **Tool usage indicator**: Use `⚒` icon in chat when displaying tool invocations

---

## Changelog & VFS

The VFS (virtual filesystem) layer wraps all file operations exposed to V8. Before any destructive operation (write, truncate, unlink), the VFS snapshots the original file. The Changelog page reads these snapshots and presents them as restorable entries grouped by interaction. Restoring reverts all files in that group to their pre-modification state.

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
4. **Manifest signing**: HMAC verification prevents tampering with permission declarations.
5. **Audit everything**: Every permission check is logged with redaction for sensitive arguments.
6. **VFS snapshots**: Every file write is reversible via the changelog.
7. **User in the loop**: `request_once` and `request_always` put the user in control with visible context about what the tool wants to do.

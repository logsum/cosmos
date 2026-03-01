# Cosmos

Cosmos is a security-first coding agent with a terminal UI, built in Go.

It is designed so AI tools run in isolated runtimes with explicit permissions, policy checks, and audit logging as core primitives (not add-ons).

## Why Cosmos

- **Security-first architecture**: V8 sandboxing, manifest-based permissions, default-deny policy
- **Terminal-native**: Tabbed TUI with Chat, Agents (history/tools), and Changelog pages
- **Real V8 isolation**: JavaScript tools run in separate isolates with zero host access
- **Manifest-driven**: Tools declare permissions (allow/deny/request_once/request_always) with Ed25519 signatures
- **Audit trail**: JSON-lines logging of all permission checks with sensitive data redaction
- **Provider abstraction**: AWS Bedrock with streaming responses and dynamic pricing
- **Cost tracking**: Token counting, currency conversion, context usage monitoring
- **Markdown rendering**: Code blocks with syntax highlighting via Glamour + Chroma

## Current Status

**Phase 1-3: Core foundation, policy engine, and V8 runtime are operational.**

### ✅ **What's Working:**

- **LLM Orchestration**: Multi-turn conversation loop with streaming responses
- **Provider**: AWS Bedrock integration with dynamic pricing and model listing
- **V8 Runtime**: Sandboxed JavaScript execution with isolates, hot reload, and timeouts
- **Manifest System**: JSON-based agent manifests with Ed25519 signature verification
- **Policy Engine**: Permission evaluation with glob patterns, default-deny, and audit logging
- **Agent Loading**: Discovery from `engine/agents/` and `~/.cosmos/agents/`
- **UI**: Tabbed TUI (Chat, Agents, Changelog) with markdown rendering and inline permission prompts
- **Tracking**: Token counting, cost tracking, currency conversion, context usage monitoring
- **APIs**: fs (read/write/list/stat/unlink), http (get/post), storage (get/set), ui.emit

### ⚠️ **In Development:**

- **Permission wiring**: Real agent permission checks (currently test-mode only)
- **VFS snapshots**: File snapshotting for changelog restore
- **Session persistence**: Save/restore session state to disk

See `TODO.md` for detailed implementation status and `CLAUDE.md` for architecture.

## Quick Start

```bash
go mod download
go run main.go
# or
go build -o cosmos && ./cosmos
```

**Requirements:**
- Go 1.23+
- AWS credentials configured (for Bedrock)
- CGo enabled (for V8 runtime)

**Configuration:**

Cosmos creates `~/.cosmos/` on first run with:
- `config.toml` - Settings (AWS region, model, currency, directories)
- `agents/` - User-created agents (manifests + JavaScript)
- `sessions/` - Session state (planned)
- `.cosmos/` in project directory - Audit logs, policy overrides, snapshots

See `config/defaults.go` for all configuration options.

## Roadmap (Next Steps)

1. ✅ ~~Manifest + policy engine~~ **DONE**
2. ✅ ~~Sandboxed V8 tool runtime and agent loader~~ **DONE**
3. **Wire permission checks to real agents** (currently test-mode only)
4. **VFS-backed changelog and restore** (UI ready, snapshotting not integrated)
5. **Session persistence** (lifecycle works, save/restore not implemented)
6. **Slash commands** (`/compact` works, `/model`, `/clear`, `/context`, `/restore` planned)
7. **Agent creation flow** (LLM-driven agent generation)
8. **Security hardening** (see TODO.md Phase 7.3 for specific items)

---

Design reference: `CLAUDE.md`

# Cosmos

Cosmos is a security-first coding agent with a terminal UI, built in Go.

It is designed so AI tools run in isolated runtimes with explicit permissions, policy checks, and audit logging as core primitives (not add-ons).

## Why Cosmos

- Terminal-native developer experience (tabbed TUI)
- Streaming LLM chat loop with tool-call handling
- Provider abstraction with AWS Bedrock implementation
- Session token and cost tracking with model-aware pricing
- Architecture built for secure, permissioned tool execution

## Current Status

Phase 1 (core foundation) is complete.

Today, the project includes the working core loop, Bedrock provider integration, UI wiring, and cost/context tracking. Security engine pieces (manifest parser, policy evaluator, V8 runtime, changelog/VFS) are planned next.

See `TODO.md` for the implementation roadmap.

## Quick Start

```bash
go mod download
go run main.go
# or
go build -o cosmos && ./cosmos
```

## Roadmap (Short)

1. Manifest + policy engine
2. Sandboxed V8 tool runtime and agent loader
3. VFS-backed changelog and restore
4. Session persistence and slash commands
5. Agent creation flow and hardening

---

Design reference: `CLAUDE.md`

package tools

import (
	"context"
	"cosmos/core/provider"
	"fmt"
	"time"
)

// StubExecutor is a throwaway ToolExecutor that returns canned responses.
// It exists solely to exercise the tool-use loop end-to-end and will be
// replaced by real manifest-loaded tools when V8 runtime lands.
//
// This is a temporary implementation. The real tool executor will:
// - Load tools from disk (engine/agents/*/index.js)
// - Execute tools in V8 isolates
// - Enforce permissions via manifest (engine/policy/)
// - Log all executions to audit trail (engine/policy/audit.go)
type StubExecutor struct{}

// NewStubExecutor returns a new StubExecutor.
func NewStubExecutor() *StubExecutor {
	return &StubExecutor{}
}

// Execute returns mock results based on tool name.
// The real implementation will invoke V8 isolates.
func (e *StubExecutor) Execute(_ context.Context, name string, input map[string]any) (string, error) {
	switch name {
	case "get_weather":
		return `{"temperature": "22°C", "condition": "sunny"}`, nil
	case "read_file":
		time.Sleep(5 * time.Second)
		path, _ := input["path"].(string)
		return "", fmt.Errorf("permission denied: cannot read %s", path)
	case "mock_permission_tool":
		content, _ := input["content"].(string)
		return fmt.Sprintf("Successfully wrote %d bytes to ./test.txt", len(content)), nil
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// StubToolDefinitions returns tool definitions for the stub tools.
// The real implementation will discover tools from disk at
// engine/agents/*/cosmo.manifest.json and user's ~/.cosmos/agents/
func StubToolDefinitions() []provider.ToolDefinition {
	return []provider.ToolDefinition{
		{
			Name:        "get_weather",
			Description: "Get the current weather for a location",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{
						"type":        "string",
						"description": "The city or location to get weather for",
					},
				},
				"required": []string{"location"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read the contents of a file",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "The file path to read",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "mock_permission_tool",
			Description: "Test tool that requires user permission (request_once: fs:write:./test.txt)",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "Content to write to test file",
					},
				},
				"required": []string{"content"},
			},
		},
	}
}

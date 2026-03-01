// Package loader discovers agents from disk, parses their manifests,
// registers their JS tools into a V8Executor, and produces ToolDefinition
// lists for the LLM.
package loader

import (
	"cosmos/core/provider"
	"cosmos/engine/manifest"
	"cosmos/engine/policy"
	"cosmos/engine/runtime"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadResult contains everything the caller needs after agent discovery.
type LoadResult struct {
	Executor *runtime.V8Executor
	Tools    []provider.ToolDefinition
	Agents   []AgentInfo  // metadata for UI (Agents page)
	Errors   []AgentError // non-fatal load errors (logged, not fatal)
}

// AgentInfo describes a loaded agent for UI display.
type AgentInfo struct {
	Name        string
	Version     string
	Description string
	Source      string // "builtin" or "user"
	Functions   []string
}

// AgentError describes a non-fatal agent loading error.
type AgentError struct {
	Dir string
	Err error
}

// agentEntry tracks a discovered manifest path and its source.
type agentEntry struct {
	manifestPath string
	source       string // "builtin" or "user"
}

// agentData holds parsed agent info ready for registration.
type agentData struct {
	manifest     manifest.Manifest
	entryPath    string
	source       string
	manifestPath string
}

// Load discovers agents, creates a V8Executor, registers all tools,
// and builds the ToolDefinition list for the LLM.
//
// builtinDir: e.g., "engine/agents" (may not exist)
// userDir:    e.g., ~/.cosmos/agents (may not exist)
// storageDir: e.g., .cosmos/storage/ (for per-agent KV)
// evaluator:  policy evaluator (nil = allow all)
// uiEmit:     callback for ui.emit() (nil = no-op)
func Load(builtinDir, userDir, storageDir string,
	evaluator *policy.Evaluator, uiEmit runtime.UIEmitFunc) (*LoadResult, error) {

	// 1. Discover agents from both locations.
	agents := discoverAgents(builtinDir, "builtin")

	// 2. User agents override builtin on name conflict.
	maps.Copy(agents, discoverAgents(userDir, "user"))

	// 3. Sort agent names for deterministic ordering.
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)

	// 4. Parse each agent and collect tool specs.
	type toolEntry struct {
		spec runtime.ToolSpec
		def  provider.ToolDefinition
	}
	var (
		toolEntries []toolEntry
		agentInfo   []AgentInfo
		errors      []AgentError
	)

	for _, name := range names {
		entry := agents[name]
		data, err := loadAgent(entry.manifestPath)
		if err != nil {
			errors = append(errors, AgentError{Dir: filepath.Dir(entry.manifestPath), Err: err})
			continue
		}
		data.source = entry.source

		// Build tool specs and definitions for each function.
		funcNames := make([]string, 0, len(data.manifest.Functions))
		for _, fn := range data.manifest.Functions {
			toolEntries = append(toolEntries, toolEntry{
				spec: runtime.ToolSpec{
					AgentName:    data.manifest.Name,
					FunctionName: fn.Name,
					SourcePath:   data.entryPath,
					Manifest:     data.manifest,
				},
				def: functionToToolDef(fn),
			})
			funcNames = append(funcNames, fn.Name)
		}

		agentInfo = append(agentInfo, AgentInfo{
			Name:        data.manifest.Name,
			Version:     data.manifest.Version,
			Description: data.manifest.Description,
			Source:      data.source,
			Functions:   funcNames,
		})
	}

	// 5. Create V8Executor and register tools.
	// Only advertise tools that register successfully.
	executor := runtime.NewV8Executor(nil, evaluator, storageDir, uiEmit)
	var toolDefs []provider.ToolDefinition
	for _, te := range toolEntries {
		if err := executor.RegisterTool(te.spec); err != nil {
			errors = append(errors, AgentError{
				Dir: filepath.Dir(te.spec.SourcePath),
				Err: fmt.Errorf("register tool %s: %w", te.spec.FunctionName, err),
			})
			continue
		}
		toolDefs = append(toolDefs, te.def)
	}

	return &LoadResult{
		Executor: executor,
		Tools:    toolDefs,
		Agents:   agentInfo,
		Errors:   errors,
	}, nil
}

// discoverAgents globs for cosmo.manifest.json files in dir/*/cosmo.manifest.json.
// Returns a map from agent name (directory basename) to entry.
// If dir does not exist or is empty, returns an empty map.
func discoverAgents(dir, source string) map[string]agentEntry {
	result := make(map[string]agentEntry)
	if dir == "" {
		return result
	}

	pattern := filepath.Join(dir, "*", "cosmo.manifest.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		// filepath.Glob only returns ErrBadPattern; dir doesn't exist = no matches.
		return result
	}

	for _, m := range matches {
		// Agent name is the parent directory name.
		name := filepath.Base(filepath.Dir(m))
		result[name] = agentEntry{manifestPath: m, source: source}
	}
	return result
}

// loadAgent parses a single agent manifest and resolves its entry file.
func loadAgent(manifestPath string) (*agentData, error) {
	m, err := manifest.ParseManifestFile(manifestPath, manifest.VerifyConfig{})
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	agentDir := filepath.Dir(manifestPath)
	entryPath := filepath.Clean(filepath.Join(agentDir, m.Entry))

	// Guard against path traversal: entry must resolve within agentDir.
	if !strings.HasPrefix(entryPath, filepath.Clean(agentDir)+string(filepath.Separator)) {
		return nil, fmt.Errorf("entry file %q escapes agent directory", m.Entry)
	}

	if _, err := os.Stat(entryPath); err != nil {
		return nil, fmt.Errorf("entry file %s: %w", m.Entry, err)
	}

	return &agentData{
		manifest:     m,
		entryPath:    entryPath,
		manifestPath: manifestPath,
	}, nil
}

// functionToToolDef converts a manifest FunctionDef into a provider.ToolDefinition
// with a JSON Schema InputSchema for the LLM.
func functionToToolDef(f manifest.FunctionDef) provider.ToolDefinition {
	properties := make(map[string]any, len(f.Params))
	var required []string

	for name, param := range f.Params {
		prop := map[string]any{"type": param.Type}
		if param.Description != "" {
			prop["description"] = param.Description
		}
		properties[name] = prop

		if param.Required {
			required = append(required, name)
		}
	}

	// Sort required for determinism.
	sort.Strings(required)

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	return provider.ToolDefinition{
		Name:        f.Name,
		Description: f.Description,
		InputSchema: schema,
	}
}

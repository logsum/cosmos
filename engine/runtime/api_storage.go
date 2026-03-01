package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	v8 "rogchap.com/v8go"
)

// injectStorageAPI registers storage.get and storage.set on the global template.
// Storage is project-scoped at .cosmos/storage/<agent-name>.json.
func injectStorageAPI(iso *v8.Isolate, global *v8.ObjectTemplate, ctx *ToolContext) error {
	storage := v8.NewObjectTemplate(iso)

	// storage.get(key) → any | null
	getFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		key, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "storage.get: "+err.Error())
		}

		if err := checkPermission(ctx, "storage:read"); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		data, err := readStorageFile(ctx)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("storage.get: %s", err))
		}

		val, exists := data[key]
		if !exists {
			return v8.Null(v8iso)
		}

		jsVal, err := toJSValue(v8iso, v8ctx, val)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("storage.get: create value: %s", err))
		}
		return jsVal
	})
	if err := storage.Set("get", getFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set storage.get: %w", err)
	}

	// storage.set(key, value) → undefined
	setFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		key, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "storage.set: "+err.Error())
		}

		if err := checkPermission(ctx, "storage:write"); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		// Extract value via JSON roundtrip from JS.
		args := info.Args()
		if len(args) < 2 {
			return throwJSError(v8iso, v8ctx, "storage.set: argument 1 (value) is required")
		}

		goVal, err := jsValueToGoValue(v8ctx, args[1])
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("storage.set: %s", err))
		}

		data, err := readStorageFile(ctx)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("storage.set: %s", err))
		}

		data[key] = goVal

		if err := writeStorageFile(ctx, data); err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("storage.set: %s", err))
		}

		return v8.Undefined(v8iso)
	})
	if err := storage.Set("set", setFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set storage.set: %w", err)
	}

	if err := global.Set("storage", storage, v8.ReadOnly); err != nil {
		return fmt.Errorf("set storage namespace: %w", err)
	}
	return nil
}

// storageFilePath returns the path to an agent's storage file.
// It sanitizes the agent name to prevent path traversal.
func storageFilePath(ctx *ToolContext) (string, error) {
	name := filepath.Base(ctx.AgentName)
	if name != ctx.AgentName || name == "." || name == ".." {
		return "", fmt.Errorf("invalid agent name for storage: %q", ctx.AgentName)
	}
	return filepath.Join(ctx.StorageDir, name+".json"), nil
}

// readStorageFile reads the agent's storage file, returning an empty map
// if the file does not exist.
func readStorageFile(ctx *ToolContext) (map[string]any, error) {
	path, err := storageFilePath(ctx)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(map[string]any), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read storage: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse storage: %w", err)
	}
	return result, nil
}

// writeStorageFile atomically writes the agent's storage data.
func writeStorageFile(ctx *ToolContext, data map[string]any) error {
	path, err := storageFilePath(ctx)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal storage: %w", err)
	}
	jsonData = append(jsonData, '\n')

	// Atomic write via temp file + rename.
	tmp, err := os.CreateTemp(dir, ".storage-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(jsonData); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename storage file: %w", err)
	}
	return nil
}

// jsValueToGoValue converts a V8 value to a Go value via JSON roundtrip.
func jsValueToGoValue(v8ctx *v8.Context, val *v8.Value) (any, error) {
	if val.IsUndefined() || val.IsNull() {
		return nil, nil
	}
	if val.IsString() {
		return val.String(), nil
	}
	if val.IsBoolean() {
		return val.Boolean(), nil
	}
	if val.IsNumber() {
		return val.Number(), nil
	}

	// For objects/arrays, use v8.JSONStringify (no RunScript, no global pollution).
	jsonStr, err := v8.JSONStringify(v8ctx, val)
	if err != nil {
		return nil, fmt.Errorf("stringify value: %w", err)
	}

	var result any
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("parse JSON value: %w", err)
	}
	return result, nil
}

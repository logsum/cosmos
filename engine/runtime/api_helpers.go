package runtime

import (
	"encoding/json"
	"fmt"

	"cosmos/engine/manifest"
	"cosmos/engine/policy"

	v8 "rogchap.com/v8go"
)

// checkPermission evaluates a permission key against the tool's manifest rules.
// Returns nil if allowed, or an error if denied.
// If ctx.Evaluator is nil, all permissions are allowed (test/stub mode).
func checkPermission(ctx *ToolContext, permKey string) error {
	if ctx.Evaluator == nil {
		return nil
	}

	parsed, err := manifest.ParsePermissionKey(permKey)
	if err != nil {
		return fmt.Errorf("invalid permission key %q: %w", permKey, err)
	}

	decision := ctx.Evaluator.Evaluate(ctx.AgentName, parsed, ctx.Manifest.ParsedPermissions)

	switch decision.Effect {
	case policy.EffectAllow:
		return nil
	case policy.EffectDeny:
		return fmt.Errorf("permission denied: %s", permKey)
	case policy.EffectPromptOnce, policy.EffectPromptAlways:
		// Cannot prompt from synchronous V8 callback.
		// Core loop handles prompting at tool-dispatch level.
		return fmt.Errorf("permission denied: %s (requires user approval)", permKey)
	default:
		return fmt.Errorf("permission denied: %s", permKey)
	}
}

// throwJSError schedules a JS exception on the isolate and returns the
// exception value. When returned from a FunctionCallback, V8 propagates
// the pending exception to the caller.
func throwJSError(iso *v8.Isolate, _ *v8.Context, msg string) *v8.Value {
	val, _ := v8.NewValue(iso, msg)
	return iso.ThrowException(val)
}

// argString extracts a string argument at the given index.
// Returns an error if the index is out of bounds or the value is not a string.
func argString(info *v8.FunctionCallbackInfo, idx int) (string, error) {
	args := info.Args()
	if idx >= len(args) {
		return "", fmt.Errorf("argument %d is required", idx)
	}
	if !args[idx].IsString() {
		return "", fmt.Errorf("argument %d must be a string", idx)
	}
	return args[idx].String(), nil
}

// toJSObject converts a Go map[string]any to a V8 Object value via JSON roundtrip.
func toJSObject(iso *v8.Isolate, ctx *v8.Context, data map[string]any) (*v8.Value, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal to JSON: %w", err)
	}
	script := fmt.Sprintf("JSON.parse(%s)", escapeJSString(string(jsonBytes)))
	val, err := ctx.RunScript(script, "to_js_object")
	if err != nil {
		return nil, fmt.Errorf("parse JSON in V8: %w", err)
	}
	return val, nil
}

// toJSValue converts a Go value to a V8 Value.
// Supports string, float64, int, bool, nil, and complex types via JSON roundtrip.
func toJSValue(iso *v8.Isolate, ctx *v8.Context, val any) (*v8.Value, error) {
	if val == nil {
		return v8.Null(iso), nil
	}

	switch v := val.(type) {
	case string:
		return v8.NewValue(iso, v)
	case float64:
		return v8.NewValue(iso, v)
	case int:
		return v8.NewValue(iso, int32(v))
	case int64:
		return v8.NewValue(iso, float64(v))
	case bool:
		return v8.NewValue(iso, v)
	case map[string]any:
		return toJSObject(iso, ctx, v)
	default:
		// Fall back to JSON roundtrip for complex types (arrays, nested).
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal value: %w", err)
		}
		script := fmt.Sprintf("JSON.parse(%s)", escapeJSString(string(jsonBytes)))
		return ctx.RunScript(script, "to_js_value")
	}
}

// jsValueToStringMap extracts a JS object as a Go map[string]string via v8.JSONStringify.
// Returns nil if val is undefined or null.
func jsValueToStringMap(ctx *v8.Context, val *v8.Value) (map[string]string, error) {
	if val.IsUndefined() || val.IsNull() {
		return nil, nil
	}
	if !val.IsObject() {
		return nil, fmt.Errorf("expected object, got %s", val.String())
	}

	jsonStr, err := v8.JSONStringify(ctx, val)
	if err != nil {
		return nil, fmt.Errorf("stringify object: %w", err)
	}

	var result map[string]string
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("parse object JSON: %w", err)
	}
	return result, nil
}

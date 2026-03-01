package runtime

import (
	"fmt"

	v8 "rogchap.com/v8go"
)

// injectUiAPI registers ui.emit on the global template.
// No permission check — tools should always be able to communicate progress.
func injectUiAPI(iso *v8.Isolate, global *v8.ObjectTemplate, ctx *ToolContext) error {
	ui := v8.NewObjectTemplate(iso)

	// ui.emit(message) → undefined
	emitFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		message, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "ui.emit: "+err.Error())
		}

		if ctx.UIEmit != nil {
			ctx.UIEmit(ctx.AgentName, message)
		}

		return v8.Undefined(v8iso)
	})
	if err := ui.Set("emit", emitFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set ui.emit: %w", err)
	}

	if err := global.Set("ui", ui, v8.ReadOnly); err != nil {
		return fmt.Errorf("set ui namespace: %w", err)
	}
	return nil
}

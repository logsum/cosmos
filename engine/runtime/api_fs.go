package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	v8 "rogchap.com/v8go"
)

// injectFsAPI registers fs.read, fs.write, fs.list, fs.stat, fs.unlink
// on the global template. Each callback captures ctx for permission checks.
func injectFsAPI(iso *v8.Isolate, global *v8.ObjectTemplate, ctx *ToolContext) error {
	fs := v8.NewObjectTemplate(iso)

	// fs.read(path) → string
	readFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		path, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "fs.read: "+err.Error())
		}
		path, err = canonicalizePath(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.read: resolve path: %s", err))
		}

		if err := checkPermission(ctx, "fs:read:"+path); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.read: %s", err))
		}

		val, err := v8.NewValue(v8iso, string(data))
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.read: create value: %s", err))
		}
		return val
	})
	if err := fs.Set("read", readFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set fs.read: %w", err)
	}

	// fs.write(path, content) → undefined
	writeFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		path, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "fs.write: "+err.Error())
		}
		path, err = canonicalizePath(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.write: resolve path: %s", err))
		}

		content, err := argString(info, 1)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "fs.write: "+err.Error())
		}

		if err := checkPermission(ctx, "fs:write:"+path); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		// Ensure parent directory exists.
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.write: mkdir: %s", err))
		}

		// Re-resolve parent after MkdirAll to detect TOCTOU symlink swaps.
		path, err = canonicalizePath(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.write: re-resolve path: %s", err))
		}

		// Re-check permission on the re-resolved path.
		if err := checkPermission(ctx, "fs:write:"+path); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		// Open with O_NOFOLLOW: if the final component is a symlink, open
		// fails with ELOOP, preventing symlink-based path escapes.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.write: %s", err))
		}
		_, writeErr := f.WriteString(content)
		closeErr := f.Close()
		if writeErr != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.write: %s", writeErr))
		}
		if closeErr != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.write: close: %s", closeErr))
		}

		return v8.Undefined(v8iso)
	})
	if err := fs.Set("write", writeFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set fs.write: %w", err)
	}

	// fs.list(path) → array<{name, isDir, size}>
	listFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		path, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "fs.list: "+err.Error())
		}
		path, err = canonicalizePath(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.list: resolve path: %s", err))
		}

		if err := checkPermission(ctx, "fs:read:"+path); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		entries, err := os.ReadDir(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.list: %s", err))
		}

		result := make([]any, 0, len(entries))
		for _, entry := range entries {
			fi, err := entry.Info()
			if err != nil {
				continue
			}
			result = append(result, map[string]any{
				"name":  entry.Name(),
				"isDir": entry.IsDir(),
				"size":  fi.Size(),
			})
		}

		val, err := toJSValue(v8iso, v8ctx, result)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.list: create value: %s", err))
		}
		return val
	})
	if err := fs.Set("list", listFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set fs.list: %w", err)
	}

	// fs.stat(path) → {name, size, isDir, modTime}
	statFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		path, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "fs.stat: "+err.Error())
		}
		path, err = canonicalizePath(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.stat: resolve path: %s", err))
		}

		if err := checkPermission(ctx, "fs:read:"+path); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		fi, err := os.Stat(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.stat: %s", err))
		}

		result := map[string]any{
			"name":    fi.Name(),
			"size":    fi.Size(),
			"isDir":   fi.IsDir(),
			"modTime": fi.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		}

		val, err := toJSObject(v8iso, v8ctx, result)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.stat: create value: %s", err))
		}
		return val
	})
	if err := fs.Set("stat", statFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set fs.stat: %w", err)
	}

	// fs.unlink(path) → undefined
	unlinkFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		path, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "fs.unlink: "+err.Error())
		}
		path, err = canonicalizePath(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.unlink: resolve path: %s", err))
		}

		// Destructive — uses write permission.
		if err := checkPermission(ctx, "fs:write:"+path); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		// Use Lstat to check the target is not a symlink before removing.
		fi, err := os.Lstat(path)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.unlink: %s", err))
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.unlink: refusing to remove symlink %s", path))
		}

		if err := os.Remove(path); err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("fs.unlink: %s", err))
		}

		return v8.Undefined(v8iso)
	})
	if err := fs.Set("unlink", unlinkFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set fs.unlink: %w", err)
	}

	if err := global.Set("fs", fs, v8.ReadOnly); err != nil {
		return fmt.Errorf("set fs namespace: %w", err)
	}
	return nil
}

// canonicalizePath cleans and resolves a path to its absolute, symlink-free
// form so that permission checks operate on the real filesystem path.
//
// For existing paths, filepath.EvalSymlinks resolves the entire chain.
// For new files (not yet on disk), the parent directory is resolved and the
// base name is appended. The result is always absolute.
func canonicalizePath(path string) (string, error) {
	path = filepath.Clean(path)

	// Make relative paths absolute against cwd.
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
		path = filepath.Join(cwd, path)
	}

	// Try full resolution (works for existing files).
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	// File doesn't exist yet — resolve the parent directory.
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

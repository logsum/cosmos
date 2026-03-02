package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cosmos/engine/manifest"
)

func TestHttpGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "test-value")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"message":"hello"}`))
	}))
	defer ts.Close()

	permissions := map[string]manifest.PermissionMode{
		"net:http": manifest.PermissionAllow,
	}
	toolCtx := testToolContext(t, "http-agent", permissions)

	e := NewV8Executor(toolCtx.Evaluator, "", nil, nil)
	e.allowLoopback = true // test server is on localhost
	defer e.Close()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "index.js")
	src := `function httpGet(input) { return http.get(input.url); }`
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := e.RegisterTool(ToolSpec{
		AgentName: "http-agent", FunctionName: "httpGet",
		SourcePath: srcPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	result, err := e.Execute(context.Background(), "httpGet", map[string]any{"url": ts.URL})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, result)
	}

	if status, ok := parsed["status"].(float64); !ok || status != 200 {
		t.Errorf("status = %v, want 200", parsed["status"])
	}
	if body, ok := parsed["body"].(string); !ok || body != `{"message":"hello"}` {
		t.Errorf("body = %v, want JSON message", parsed["body"])
	}
	if headers, ok := parsed["headers"].(map[string]any); ok {
		if headers["x-custom"] != "test-value" {
			t.Errorf("x-custom header = %v, want %q", headers["x-custom"], "test-value")
		}
	} else {
		t.Error("headers not present in response")
	}
}

func TestHttpPost(t *testing.T) {
	var receivedBody string
	var receivedContentType string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	defer ts.Close()

	permissions := map[string]manifest.PermissionMode{
		"net:http": manifest.PermissionAllow,
	}
	toolCtx := testToolContext(t, "http-agent", permissions)

	e := NewV8Executor(toolCtx.Evaluator, "", nil, nil)
	e.allowLoopback = true // test server is on localhost
	defer e.Close()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "index.js")
	// Pass headers as a JSON object literal to http.post
	src := `function httpPost(input) { return http.post(input.url, input.body, {"Content-Type": "application/json"}); }`
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := e.RegisterTool(ToolSpec{
		AgentName: "http-agent", FunctionName: "httpPost",
		SourcePath: srcPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	result, err := e.Execute(context.Background(), "httpPost", map[string]any{
		"url":  ts.URL,
		"body": `{"name":"cosmos"}`,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, result)
	}

	if status, ok := parsed["status"].(float64); !ok || status != 201 {
		t.Errorf("status = %v, want 201", parsed["status"])
	}

	if receivedBody != `{"name":"cosmos"}` {
		t.Errorf("received body = %q, want JSON", receivedBody)
	}
	if receivedContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", receivedContentType)
	}
}

func TestHttp_RedirectBlocked(t *testing.T) {
	// Target server (simulates an internal/disallowed service).
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"secret":"leaked"}`))
	}))
	defer target.Close()

	// Redirector: allowed domain that redirects to the disallowed target.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// Only allow the redirector's URL, not the target's.
	permissions := map[string]manifest.PermissionMode{
		"net:http:" + redirector.URL + "/**": manifest.PermissionAllow,
	}
	toolCtx := testToolContext(t, "http-agent", permissions)

	e := NewV8Executor(toolCtx.Evaluator, "", nil, nil)
	e.allowLoopback = true // both servers are on localhost
	defer e.Close()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "index.js")
	src := `function httpRedirect(input) { return http.get(input.url); }`
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := e.RegisterTool(ToolSpec{
		AgentName: "http-agent", FunctionName: "httpRedirect",
		SourcePath: srcPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// The initial request is allowed, but the redirect target is not.
	_, err := e.Execute(context.Background(), "httpRedirect", map[string]any{"url": redirector.URL + "/start"})
	if err == nil {
		t.Fatal("expected error due to blocked redirect")
	}
	if !strings.Contains(err.Error(), "redirect") && !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want redirect blocked or permission denied", err.Error())
	}
}

func TestHttp_LoopbackBlocked(t *testing.T) {
	permissions := map[string]manifest.PermissionMode{
		"net:http": manifest.PermissionAllow,
	}
	toolCtx := testToolContext(t, "http-agent", permissions)
	// AllowLoopback NOT set — default false.

	e := NewV8Executor(toolCtx.Evaluator, "", nil, nil)
	defer e.Close()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "index.js")
	src := `function httpLoopback(input) { return http.get(input.url); }`
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := e.RegisterTool(ToolSpec{
		AgentName: "http-agent", FunctionName: "httpLoopback",
		SourcePath: srcPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	tests := []struct {
		name string
		url  string
	}{
		{"loopback IPv4", "http://127.0.0.1:8080/secret"},
		{"loopback IPv6", "http://[::1]:8080/secret"},
		{"private 10.x", "http://10.0.0.1/internal"},
		{"link-local", "http://169.254.169.254/latest/meta-data/"},
		{"file scheme", "file:///etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.Execute(context.Background(), "httpLoopback", map[string]any{"url": tt.url})
			if err == nil {
				t.Fatalf("expected error for %s", tt.url)
			}
			if !strings.Contains(err.Error(), "blocked") && !strings.Contains(err.Error(), "unsupported URL scheme") {
				t.Errorf("error = %q, want blocked or unsupported scheme", err.Error())
			}
		})
	}
}

func TestHttp_PermissionDenied(t *testing.T) {
	permissions := map[string]manifest.PermissionMode{
		"net:http": manifest.PermissionDeny,
	}
	toolCtx := testToolContext(t, "http-agent", permissions)

	e := NewV8Executor(toolCtx.Evaluator, "", nil, nil)
	defer e.Close()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "index.js")
	src := `function httpDenied(input) { return http.get(input.url); }`
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := e.RegisterTool(ToolSpec{
		AgentName: "http-agent", FunctionName: "httpDenied",
		SourcePath: srcPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := e.Execute(context.Background(), "httpDenied", map[string]any{"url": "http://example.com"})
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want permission denied", err.Error())
	}
}

package runtime

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	v8 "rogchap.com/v8go"
)

const httpRequestTimeout = 30 * time.Second
const maxResponseBytes = 10 << 20 // 10 MB

// injectHttpAPI registers http.get and http.post on the global template.
func injectHttpAPI(iso *v8.Isolate, global *v8.ObjectTemplate, ctx *ToolContext) error {
	httpNs := v8.NewObjectTemplate(iso)

	// http.get(url, headers?) → {status, body, headers}
	getFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		url, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "http.get: "+err.Error())
		}

		if err := checkPermission(ctx, "net:http:"+url); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		// Extract optional headers.
		headers := extractHeaders(info, v8ctx)

		result, err := doHTTPRequest("GET", url, "", headers, ctx)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("http.get: %s", err))
		}

		val, err := toJSObject(v8iso, v8ctx, result)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("http.get: create value: %s", err))
		}
		return val
	})
	if err := httpNs.Set("get", getFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set http.get: %w", err)
	}

	// http.post(url, body, headers?) → {status, body, headers}
	postFn := v8.NewFunctionTemplate(iso, func(info *v8.FunctionCallbackInfo) *v8.Value {
		v8ctx := info.Context()
		v8iso := v8ctx.Isolate()

		url, err := argString(info, 0)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "http.post: "+err.Error())
		}

		body, err := argString(info, 1)
		if err != nil {
			return throwJSError(v8iso, v8ctx, "http.post: "+err.Error())
		}

		if err := checkPermission(ctx, "net:http:"+url); err != nil {
			return throwJSError(v8iso, v8ctx, err.Error())
		}

		// Extract optional headers.
		headers := extractHeaders(info, v8ctx)

		result, err := doHTTPRequest("POST", url, body, headers, ctx)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("http.post: %s", err))
		}

		val, err := toJSObject(v8iso, v8ctx, result)
		if err != nil {
			return throwJSError(v8iso, v8ctx, fmt.Sprintf("http.post: create value: %s", err))
		}
		return val
	})
	if err := httpNs.Set("post", postFn, v8.ReadOnly); err != nil {
		return fmt.Errorf("set http.post: %w", err)
	}

	if err := global.Set("http", httpNs, v8.ReadOnly); err != nil {
		return fmt.Errorf("set http namespace: %w", err)
	}
	return nil
}

// extractHeaders extracts optional headers from the last JS argument.
// For http.get, headers is arg[1]; for http.post, headers is arg[2].
// Returns nil if no headers argument is provided.
func extractHeaders(info *v8.FunctionCallbackInfo, v8ctx *v8.Context) map[string]string {
	args := info.Args()
	// Find the last argument that's an object (not string, not undefined/null).
	for i := len(args) - 1; i >= 1; i-- {
		v := args[i]
		if v.IsUndefined() || v.IsNull() || v.IsString() {
			continue
		}
		if v.IsObject() {
			headers, err := jsValueToStringMap(v8ctx, v)
			if err == nil {
				return headers
			}
		}
	}
	return nil
}

// validateURL rejects non-http(s) schemes and private/loopback/link-local destinations
// to prevent SSRF attacks (e.g., file://, http://169.254.169.254, http://127.0.0.1).
// Set allowLoopback to skip the IP check (for testing with local servers).
func validateURL(rawURL string, allowLoopback bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
		// allowed
	default:
		return fmt.Errorf("unsupported URL scheme %q (only http and https allowed)", u.Scheme)
	}
	if !allowLoopback {
		host := u.Hostname()
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				return fmt.Errorf("requests to private/loopback/link-local address %s blocked", ip)
			}
		}
	}
	return nil
}

// doHTTPRequest performs an HTTP request and returns the response as a map.
// It checks permissions on every redirect target to prevent open-redirect bypasses.
func doHTTPRequest(method, rawURL, body string, headers map[string]string, ctx *ToolContext) (map[string]any, error) {
	if err := validateURL(rawURL, ctx.AllowLoopback); err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, rawURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: httpRequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			target := req.URL.String()
			if err := validateURL(target, ctx.AllowLoopback); err != nil {
				return fmt.Errorf("redirect to %s blocked: %w", target, err)
			}
			if err := checkPermission(ctx, "net:http:"+target); err != nil {
				return fmt.Errorf("redirect to %s blocked: %w", target, err)
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	respHeaders := make(map[string]any)
	for k, v := range resp.Header {
		if len(v) == 1 {
			respHeaders[strings.ToLower(k)] = v[0]
		} else {
			respHeaders[strings.ToLower(k)] = strings.Join(v, ", ")
		}
	}

	return map[string]any{
		"status":  resp.StatusCode,
		"body":    string(respBody),
		"headers": respHeaders,
	}, nil
}

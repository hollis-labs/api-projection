// Package interpreter is api-projection's Stage B: a parameterized runtime
// that turns one loaded manifest into a set of registered go-mcp tools,
// executes each call against the projected upstream API, and maps every
// response through the manifest's allow-list before it becomes a tool
// result.
//
// Per adr_api_to_mcp_projection (Tesseract, project/atlas/knowledge/adr),
// this is deliberately generic/reflection-shaped code applying an
// allow-list — the same pattern Cerberus's docs/adr/0003-connector-response-dtos.md
// distrusts for a security boundary. The mitigation named there is carried
// here structurally: the manifest's response fields are validated at load
// time to be flat literal paths only (see manifest.Response.validate), so
// this package's mapping function has nothing to interpret beyond "copy
// this fixed key path if present" — no wildcards, no dynamic traversal, no
// runtime-decided scope. See the adversarial tests in this package for the
// "a populated secret does not survive the allow-list" property ADR 0003
// requires per connector, run here against manifests, not just the
// happy-path pilot manifest.
//
// This package never imports api-projection/credential and never touches
// the credential store: the resolved credential value, if any, is read
// once from the environment variable the manifest names
// (Manifest.Credential.Env) — handed to this process by whatever spawned
// it (apps/station, per the ADR's host-mediated resolution design).
package interpreter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/api-projection/manifest"
	gmcpserver "github.com/hollis-labs/go-mcp/server"
)

// Interpreter serves one loaded manifest's tools.
type Interpreter struct {
	manifest *manifest.Manifest
	client   *http.Client
	getenv   func(string) string
}

// Option customizes an Interpreter. Exposed for tests.
type Option func(*Interpreter)

// WithHTTPClient overrides the HTTP client used for upstream calls.
func WithHTTPClient(c *http.Client) Option {
	return func(ip *Interpreter) { ip.client = c }
}

// WithGetenv overrides how the credential env var is read. Exposed for
// tests; production callers should leave this at the default (os.Getenv).
func WithGetenv(fn func(string) string) Option {
	return func(ip *Interpreter) { ip.getenv = fn }
}

// New returns an Interpreter for m.
func New(m *manifest.Manifest, opts ...Option) *Interpreter {
	ip := &Interpreter{
		manifest: m,
		client:   &http.Client{Timeout: 30 * time.Second},
		getenv:   os.Getenv,
	}
	for _, opt := range opts {
		opt(ip)
	}
	return ip
}

// Register registers every manifest tool on srv.
func (ip *Interpreter) Register(srv *gmcpserver.Server) {
	for _, t := range ip.manifest.Tools {
		srv.RegisterTool(ip.buildTool(t))
	}
}

func (ip *Interpreter) buildTool(t manifest.Tool) gmcpserver.Tool {
	props := make([]gmcpserver.Prop, 0, len(t.Inputs))
	for _, in := range t.Inputs {
		props = append(props, inputProp(in))
	}
	return gmcpserver.Tool{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: gmcpserver.InputSchema(props...),

		ReadOnlyHint:    t.Annotations.ReadOnlyHint,
		DestructiveHint: t.Annotations.DestructiveHint,
		IdempotentHint:  t.Annotations.IdempotentHint,
		OpenWorldHint:   t.Annotations.OpenWorldHint,

		Handler: ip.handlerFor(t),
	}
}

func inputProp(in manifest.Input) gmcpserver.Prop {
	switch in.Type {
	case "integer":
		return gmcpserver.IntegerProp(in.Name, in.Description, in.Required)
	case "number":
		return gmcpserver.NumberProp(in.Name, in.Description, in.Required)
	case "boolean":
		return gmcpserver.BooleanProp(in.Name, in.Description, in.Required)
	default:
		return gmcpserver.StringProp(in.Name, in.Description, in.Required)
	}
}

// handlerFor closes over one manifest.Tool and returns the go-mcp handler
// that executes it: build the request from allow-listed inputs only,
// inject the credential (if any), call upstream, map the response through
// the allow-list.
func (ip *Interpreter) handlerFor(t manifest.Tool) gmcpserver.ToolHandler {
	return func(ctx context.Context, args map[string]any) (any, error) {
		path, query, err := ip.resolveFields(t, args)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, err)
		}

		reqURL := strings.TrimRight(ip.manifest.BaseURL, "/") + path
		if len(query) > 0 {
			reqURL += "?" + query.Encode()
		}

		req, err := http.NewRequestWithContext(ctx, strings.ToUpper(t.Operation.Method), reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("%s: build request: %w", t.Name, err)
		}
		for k, v := range t.Operation.FixedHeaders {
			req.Header.Set(k, v)
		}

		if cred := ip.manifest.Credential; cred != nil {
			value := ip.getenv(cred.Env)
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%s: credential_missing: environment variable %s is not set", t.Name, cred.Env)
			}
			if cred.Format != "" {
				value = fmt.Sprintf(cred.Format, value)
			}
			req.Header.Set(cred.Header, value)
		}

		resp, err := ip.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s: upstream request: %w", t.Name, err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("%s: read upstream response: %w", t.Name, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("%s: upstream_error: status %d", t.Name, resp.StatusCode)
		}

		return mapResponse(t.Response, body)
	}
}

// resolveFields builds the path (placeholders substituted) and query
// values for one call, from allow-listed inputs (validated against args)
// and pinned fixed values. Any field not declared as an input or pinned
// value in the manifest is structurally unreachable here — there is no
// code path that copies an arbitrary caller-supplied key onto the
// upstream request.
func (ip *Interpreter) resolveFields(t manifest.Tool, args map[string]any) (path string, query url.Values, err error) {
	path = t.Operation.Path
	query = url.Values{}

	for _, in := range t.Inputs {
		raw, present := args[in.Name]
		if !present {
			if in.Required {
				return "", nil, fmt.Errorf("missing required input %q", in.Name)
			}
			continue
		}
		strVal, err := stringifyValue(raw, in.Type)
		if err != nil {
			return "", nil, fmt.Errorf("input %q: %w", in.Name, err)
		}
		switch in.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+in.Name+"}", url.PathEscape(strVal))
		case "query":
			query.Set(in.Name, strVal)
		}
	}

	for _, p := range t.Pinned {
		switch p.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(p.Value))
		case "query":
			query.Set(p.Name, p.Value)
		}
	}

	return path, query, nil
}

func stringifyValue(v any, typ string) (string, error) {
	switch typ {
	case "integer":
		n, ok := v.(float64)
		if !ok {
			return "", fmt.Errorf("expected a number, got %T", v)
		}
		return strconv.FormatInt(int64(n), 10), nil
	case "number":
		n, ok := v.(float64)
		if !ok {
			return "", fmt.Errorf("expected a number, got %T", v)
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			return "", fmt.Errorf("expected a boolean, got %T", v)
		}
		return strconv.FormatBool(b), nil
	default:
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("expected a string, got %T", v)
		}
		return s, nil
	}
}

// mapResponse copies only spec's allow-listed field paths out of the raw
// upstream JSON body. This is the structural security boundary the ADR
// requires: a field the manifest does not list is never reachable here,
// regardless of what the upstream body contains.
func mapResponse(spec manifest.Response, body []byte) (any, error) {
	if spec.Array {
		var items []map[string]any
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("decode upstream array response: %w", err)
		}
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			out = append(out, extractFields(item, spec.Fields))
		}
		return out, nil
	}

	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("decode upstream response: %w", err)
	}
	return extractFields(obj, spec.Fields), nil
}

// extractFields copies each allow-listed flat/dotted field path from src
// into a fresh map, preserving nesting shape for dotted paths. A path
// absent from src (at any level) is silently omitted — this function only
// ever copies what is both listed and present, never fabricates a value
// and never falls back to anything wider than the exact listed path.
func extractFields(src map[string]any, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, field := range fields {
		segs := strings.Split(field, ".")
		val, ok := lookupPath(src, segs)
		if !ok {
			continue
		}
		setPath(out, segs, val)
	}
	return out
}

func lookupPath(m map[string]any, segs []string) (any, bool) {
	cur := any(m)
	for _, seg := range segs {
		curMap, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		val, present := curMap[seg]
		if !present {
			return nil, false
		}
		cur = val
	}
	return cur, true
}

func setPath(m map[string]any, segs []string, val any) {
	cur := m
	for i, seg := range segs {
		if i == len(segs)-1 {
			cur[seg] = val
			return
		}
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = make(map[string]any)
			cur[seg] = next
		}
		cur = next
	}
}

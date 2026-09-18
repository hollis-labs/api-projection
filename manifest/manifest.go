// Package manifest defines the api-projection manifest: the versioned,
// git-tracked, PR-reviewed artifact a Stage A compiler emits and a Stage B
// interpreter loads. It is the sole reviewed security boundary for one
// projected API, per adr_api_to_mcp_projection (Tesseract,
// project/atlas/knowledge/adr).
//
// A manifest never contains a literal credential — only a credential
// reference (keychain://…, helper://…) — and every input/response field it
// does not explicitly allow-list is unreachable at runtime by construction,
// not by convention.
package manifest

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest projects one upstream API's approved operations as MCP tools.
type Manifest struct {
	// APIName identifies the projected API (e.g. "github"). Informational —
	// not used to select behavior at runtime.
	APIName string `yaml:"api_name"`
	// BaseURL is the upstream API's base URL; each tool's operation path is
	// appended to it verbatim.
	BaseURL string `yaml:"base_url"`
	// Credential names exactly one credential-capability reference this
	// manifest's tools may use. A manifest with tools that need a
	// credential must declare one; a manifest whose tools need none may
	// omit this entirely.
	Credential *Credential `yaml:"credential,omitempty"`
	// Tools is the explicit, allow-listed set of projected operations.
	// Never "all" — each entry names exactly one upstream operation.
	Tools []Tool `yaml:"tools"`
}

// Credential is exactly one declared credential-capability reference. Ref
// must be a keychain:// or helper:// reference — never a literal secret;
// Load refuses a manifest that violates this.
type Credential struct {
	// Ref is the credential-capability reference, e.g.
	// "keychain://api-projection/github-pilot". Resolved by the host
	// (apps/station), never by the interpreter itself.
	Ref string `yaml:"ref"`
	// Env is the name of the environment variable the interpreter reads
	// the resolved credential value from. The interpreter never resolves
	// Ref itself — it only reads this variable.
	Env string `yaml:"env"`
	// Header is the HTTP header the resolved credential is sent in (e.g.
	// "Authorization").
	Header string `yaml:"header"`
	// Format is an fmt-style template with exactly one %s, applied to the
	// resolved credential value before it is placed in Header (e.g.
	// "Bearer %s"). Empty means the raw value is sent unformatted.
	Format string `yaml:"format,omitempty"`
}

// Tool is one allow-listed, projected MCP tool.
type Tool struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Operation   Operation   `yaml:"operation"`
	Inputs      []Input     `yaml:"inputs,omitempty"`
	Pinned      []Pinned    `yaml:"pinned,omitempty"`
	Annotations Annotations `yaml:"annotations"`
	Response    Response    `yaml:"response"`
}

// Operation names the single upstream HTTP operation a tool projects.
type Operation struct {
	Method string `yaml:"method"`
	// Path is the upstream path template, e.g. "/repos/{owner}/{repo}/releases".
	// {name} placeholders must each match exactly one Input or Pinned entry
	// with In: "path".
	Path string `yaml:"path"`
	// FixedHeaders are constant, manifest-authored request headers (API
	// version pins, Accept headers) — never caller-influenced, and
	// distinct from Credential.Header.
	FixedHeaders map[string]string `yaml:"fixed_headers,omitempty"`
}

// Input is one allow-listed, caller-settable field. A field not listed
// here (and not in Pinned) is not caller-settable, full stop — there is no
// passthrough for anything else the upstream operation accepts.
type Input struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	// In is "path" or "query".
	In string `yaml:"in"`
	// Type is a JSON Schema primitive type: "string", "integer", "boolean",
	// or "number".
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
}

// Pinned is a manifest-fixed value for a field the upstream operation
// accepts (or requires) that is deliberately not caller-settable. Per the
// ADR: a required-but-unlisted field gets a pinned fixed value rather than
// being left as a caller-controlled passthrough; a manifest may also pin an
// optional field to bound behavior (e.g. a small fixed page size) even when
// the upstream operation does not strictly require it.
type Pinned struct {
	Name  string `yaml:"name"`
	In    string `yaml:"in"`
	Value string `yaml:"value"`
}

// Annotations is the complete go-mcp four-hint set. Never optional here —
// a manifest's tool sets every hint explicitly, matching go-mcp's own
// required-annotation contract (server.Tool).
type Annotations struct {
	ReadOnlyHint    bool `yaml:"read_only_hint"`
	DestructiveHint bool `yaml:"destructive_hint"`
	IdempotentHint  bool `yaml:"idempotent_hint"`
	OpenWorldHint   bool `yaml:"open_world_hint"`
}

// Response is the tool's response allow-list: the interpreter copies only
// these fields from the upstream JSON response, structurally — never the
// raw upstream body.
type Response struct {
	// Array reports whether the upstream response body is a JSON array;
	// when true, Fields is applied to every element and the tool result is
	// itself an array of allow-listed objects.
	Array bool `yaml:"array"`
	// Fields lists flat, literal field paths only — a bare top-level key
	// ("tag_name") or a fixed dotted path into a nested object
	// ("author.login"). No wildcards, no array indexing, no dynamic
	// traversal: Validate refuses anything else.
	Fields []string `yaml:"fields"`
}

// Load reads, parses and validates a manifest file.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("manifest: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse parses manifest YAML bytes and validates the result.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: parse: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

var validInTypes = map[string]bool{"path": true, "query": true}
var validFieldTypes = map[string]bool{"string": true, "integer": true, "boolean": true, "number": true}
var validMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// Validate checks structural correctness and the manifest's core security
// invariants: an allow-listed operation (never wildcarded), a credential
// reference (never a literal secret), and flat literal response paths
// (never wildcards or dynamic traversal).
func (m *Manifest) Validate() error {
	if strings.TrimSpace(m.APIName) == "" {
		return fmt.Errorf("manifest: api_name is required")
	}
	if strings.TrimSpace(m.BaseURL) == "" {
		return fmt.Errorf("manifest: base_url is required")
	}
	if len(m.Tools) == 0 {
		return fmt.Errorf("manifest: at least one tool is required")
	}
	if m.Credential != nil {
		if err := m.Credential.validate(); err != nil {
			return fmt.Errorf("manifest: credential: %w", err)
		}
	}

	seen := make(map[string]struct{}, len(m.Tools))
	for i, t := range m.Tools {
		if err := t.validate(); err != nil {
			return fmt.Errorf("manifest: tools[%d] (%s): %w", i, t.Name, err)
		}
		if _, dup := seen[t.Name]; dup {
			return fmt.Errorf("manifest: tools[%d]: duplicate tool name %q", i, t.Name)
		}
		seen[t.Name] = struct{}{}
	}
	return nil
}

func (c *Credential) validate() error {
	if !IsRef(c.Ref) {
		return fmt.Errorf("ref must be a keychain:// or helper:// reference, never a literal secret: %q", c.Ref)
	}
	if strings.TrimSpace(c.Env) == "" {
		return fmt.Errorf("env is required")
	}
	if strings.TrimSpace(c.Header) == "" {
		return fmt.Errorf("header is required")
	}
	if c.Format != "" && strings.Count(c.Format, "%s") != 1 {
		return fmt.Errorf("format must contain exactly one %%s, got %q", c.Format)
	}
	return nil
}

// IsRef reports whether value is a credential-capability reference
// (keychain:// or helper://) rather than a literal value.
func IsRef(value string) bool {
	return strings.HasPrefix(value, "keychain://") || strings.HasPrefix(value, "helper://")
}

func (t *Tool) validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(t.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if err := t.Operation.validate(); err != nil {
		return fmt.Errorf("operation: %w", err)
	}

	fields := make(map[string]struct{})
	for i, in := range t.Inputs {
		if err := in.validate(); err != nil {
			return fmt.Errorf("inputs[%d]: %w", i, err)
		}
		key := in.In + ":" + in.Name
		if _, dup := fields[key]; dup {
			return fmt.Errorf("inputs[%d]: duplicate field %s %q", i, in.In, in.Name)
		}
		fields[key] = struct{}{}
	}
	for i, p := range t.Pinned {
		if err := p.validate(); err != nil {
			return fmt.Errorf("pinned[%d]: %w", i, err)
		}
		key := p.In + ":" + p.Name
		if _, dup := fields[key]; dup {
			return fmt.Errorf("pinned[%d]: field %s %q is already declared as an input — a field is caller-settable or pinned, never both", i, p.In, p.Name)
		}
		fields[key] = struct{}{}
	}

	// Every {placeholder} in the path must resolve to exactly one
	// allow-listed (input or pinned) path field, and vice versa: no
	// unresolved placeholder, and no path-scoped field the template never
	// references.
	placeholders := t.Operation.pathPlaceholders()
	pathFields := make(map[string]struct{})
	for _, in := range t.Inputs {
		if in.In == "path" {
			pathFields[in.Name] = struct{}{}
		}
	}
	for _, p := range t.Pinned {
		if p.In == "path" {
			pathFields[p.Name] = struct{}{}
		}
	}
	for name := range placeholders {
		if _, ok := pathFields[name]; !ok {
			return fmt.Errorf("operation.path references {%s}, which is not declared as an input or pinned field", name)
		}
	}
	for name := range pathFields {
		if _, ok := placeholders[name]; !ok {
			return fmt.Errorf("field %q is declared with in: path but operation.path has no {%s} placeholder", name, name)
		}
	}

	if err := t.Response.validate(); err != nil {
		return fmt.Errorf("response: %w", err)
	}
	return nil
}

func (o *Operation) validate() error {
	if !validMethods[strings.ToUpper(o.Method)] {
		return fmt.Errorf("method must be one of GET/POST/PUT/PATCH/DELETE, got %q", o.Method)
	}
	if !strings.HasPrefix(o.Path, "/") {
		return fmt.Errorf("path must start with \"/\", got %q", o.Path)
	}
	return nil
}

// pathPlaceholders returns the set of {name} placeholders in the operation
// path.
func (o *Operation) pathPlaceholders() map[string]struct{} {
	out := make(map[string]struct{})
	rest := o.Path
	for {
		start := strings.IndexByte(rest, '{')
		if start < 0 {
			break
		}
		end := strings.IndexByte(rest[start:], '}')
		if end < 0 {
			break
		}
		out[rest[start+1:start+end]] = struct{}{}
		rest = rest[start+end+1:]
	}
	return out
}

func (in *Input) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if !validInTypes[in.In] {
		return fmt.Errorf("in must be \"path\" or \"query\", got %q", in.In)
	}
	if !validFieldTypes[in.Type] {
		return fmt.Errorf("type must be one of string/integer/boolean/number, got %q", in.Type)
	}
	return nil
}

func (p *Pinned) validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if !validInTypes[p.In] {
		return fmt.Errorf("in must be \"path\" or \"query\", got %q", p.In)
	}
	return nil
}

func (r *Response) validate() error {
	if len(r.Fields) == 0 {
		return fmt.Errorf("fields: at least one allow-listed field is required")
	}
	seen := make(map[string]struct{}, len(r.Fields))
	for i, f := range r.Fields {
		if err := validateFlatFieldPath(f); err != nil {
			return fmt.Errorf("fields[%d]: %w", i, err)
		}
		if _, dup := seen[f]; dup {
			return fmt.Errorf("fields[%d]: duplicate field %q", i, f)
		}
		seen[f] = struct{}{}
	}
	return nil
}

// validateFlatFieldPath refuses anything that isn't a fixed, literal
// dot-separated key path: no wildcards, no array indexing, no empty
// segments. This is the ADR's "flat literal field paths only" invariant,
// enforced structurally rather than left to interpreter discipline.
func validateFlatFieldPath(path string) error {
	if path == "" {
		return fmt.Errorf("empty field path")
	}
	if strings.ContainsAny(path, "*[]") {
		return fmt.Errorf("field path %q must not contain wildcards or array indexing", path)
	}
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			return fmt.Errorf("field path %q has an empty segment", path)
		}
	}
	return nil
}

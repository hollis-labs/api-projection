// Package compiler is api-projection's Stage A: an offline, tool-assisted
// compiler that consumes an approved API definition (an OpenAPI 3.x
// fragment — the relevant operations only, not necessarily a full spec)
// plus a small human-authored selection naming exactly what to allow-list,
// and emits a manifest for Stage B to load.
//
// Per adr_api_to_mcp_projection, this is "offline, tool-assisted,
// human-reviewed" — Compile does not decide what to project; it validates
// that a human's selection is consistent with the real API definition
// (every named field actually exists, every required upstream field is
// either allow-listed or explicitly pinned) and derives the mechanical
// parts (JSON Schema types, heuristic go-mcp annotation hints) a human
// shouldn't have to hand-write. The emitted manifest is the artifact that
// gets reviewed and committed, not this package's output trusted blindly.
package compiler

// Document is the minimal OpenAPI 3.x subset this compiler understands:
// enough to describe one or a few operations precisely, not a general
// OpenAPI parser. A real portfolio spec (GitHub's, Cloudflare's, ...) is
// trimmed to the relevant paths before being fed in here — see
// pilots/github-releases for a worked example.
type Document struct {
	OpenAPI string              `yaml:"openapi" json:"openapi"`
	Paths   map[string]PathItem `yaml:"paths" json:"paths"`
}

// PathItem maps an HTTP method (lowercase: "get", "post", ...) to the
// operation defined for it at that path.
type PathItem map[string]Operation

// Operation is one HTTP operation.
type Operation struct {
	OperationID string              `yaml:"operationId" json:"operationId"`
	Summary     string              `yaml:"summary,omitempty" json:"summary,omitempty"`
	Parameters  []Parameter         `yaml:"parameters,omitempty" json:"parameters,omitempty"`
	Responses   map[string]Response `yaml:"responses" json:"responses"`
}

// Parameter is one path or query parameter.
type Parameter struct {
	Name        string `yaml:"name" json:"name"`
	In          string `yaml:"in" json:"in"`
	Required    bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Schema      Schema `yaml:"schema" json:"schema"`
}

// Response is one status-code response entry.
type Response struct {
	Description string               `yaml:"description,omitempty" json:"description,omitempty"`
	Content     map[string]MediaType `yaml:"content,omitempty" json:"content,omitempty"`
}

// MediaType is one content-type entry within a response.
type MediaType struct {
	Schema Schema `yaml:"schema" json:"schema"`
}

// Schema is a JSON Schema fragment, trimmed to what this compiler reads:
// primitive type, array item schema, and object properties.
type Schema struct {
	Type       string            `yaml:"type,omitempty" json:"type,omitempty"`
	Items      *Schema           `yaml:"items,omitempty" json:"items,omitempty"`
	Properties map[string]Schema `yaml:"properties,omitempty" json:"properties,omitempty"`
}

// findOperation returns the method, path, and operation matching
// operationID, and whether it was found.
func (d *Document) findOperation(operationID string) (method, path string, op Operation, ok bool) {
	for p, item := range d.Paths {
		for m, o := range item {
			if o.OperationID == operationID {
				return m, p, o, true
			}
		}
	}
	return "", "", Operation{}, false
}

// responseSchema returns the 200 response's JSON schema, if declared.
func (o *Operation) responseSchema() (Schema, bool) {
	resp, ok := o.Responses["200"]
	if !ok {
		return Schema{}, false
	}
	media, ok := resp.Content["application/json"]
	if !ok {
		return Schema{}, false
	}
	return media.Schema, true
}

// findParameter returns the parameter named name, if declared.
func (o *Operation) findParameter(name string) (Parameter, bool) {
	for _, p := range o.Parameters {
		if p.Name == name {
			return p, true
		}
	}
	return Parameter{}, false
}

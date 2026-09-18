package compiler

import (
	"fmt"
	"os"
	"strings"

	"github.com/hollis-labs/api-projection/manifest"
	"gopkg.in/yaml.v3"
)

// Selection is the small, human-authored input naming exactly what to
// project from an OpenAPI Document — the allow-list decision Compile
// checks for consistency against the real API definition rather than
// making itself.
type Selection struct {
	APIName    string               `yaml:"api_name"`
	BaseURL    string               `yaml:"base_url"`
	Credential *manifest.Credential `yaml:"credential,omitempty"`
	Tools      []ToolSelection      `yaml:"tools"`
}

// ToolSelection names one operation to project and exactly which of its
// fields to allow-list.
type ToolSelection struct {
	Name         string                `yaml:"name"`
	Description  string                `yaml:"description"`
	OperationID  string                `yaml:"operation_id"`
	Fields       []FieldSelection      `yaml:"fields,omitempty"`
	FixedHeaders map[string]string     `yaml:"fixed_headers,omitempty"`
	Response     ResponseSelection     `yaml:"response"`
	Annotations  *manifest.Annotations `yaml:"annotations,omitempty"`
}

// FieldSelection allow-lists one of the operation's declared parameters.
// Exactly one of leaving Pin empty (caller-settable) or setting it
// (manifest-pinned, never caller-settable) applies.
type FieldSelection struct {
	Name     string `yaml:"name"`
	Pin      string `yaml:"pin,omitempty"`
	Required *bool  `yaml:"required,omitempty"`
}

// ResponseSelection allow-lists the response fields to project.
type ResponseSelection struct {
	Fields []string `yaml:"fields"`
}

// LoadSelection reads and parses a selection YAML file.
func LoadSelection(path string) (*Selection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("compiler: read selection %s: %w", path, err)
	}
	var sel Selection
	if err := yaml.Unmarshal(data, &sel); err != nil {
		return nil, fmt.Errorf("compiler: parse selection: %w", err)
	}
	return &sel, nil
}

// LoadDocument reads and parses an OpenAPI document fragment (YAML or
// JSON — yaml.Unmarshal reads both).
func LoadDocument(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("compiler: read OpenAPI document %s: %w", path, err)
	}
	var doc Document
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("compiler: parse OpenAPI document: %w", err)
	}
	return &doc, nil
}

// Compile resolves sel against doc and emits a manifest.Manifest.
//
// It refuses, rather than guesses, whenever the selection is inconsistent
// with the real API definition: an unknown operationId, a field name that
// is not a declared parameter, or an upstream-required parameter that the
// selection neither allow-lists nor pins. This is the compiler's actual
// job per the ADR — mechanical derivation plus validation, with every
// allow-list decision itself remaining the human author's, made in the
// selection file.
func Compile(doc *Document, sel *Selection) (*manifest.Manifest, error) {
	if strings.TrimSpace(sel.APIName) == "" {
		return nil, fmt.Errorf("compiler: selection: api_name is required")
	}
	if strings.TrimSpace(sel.BaseURL) == "" {
		return nil, fmt.Errorf("compiler: selection: base_url is required")
	}
	if len(sel.Tools) == 0 {
		return nil, fmt.Errorf("compiler: selection: at least one tool is required")
	}

	m := &manifest.Manifest{
		APIName:    sel.APIName,
		BaseURL:    sel.BaseURL,
		Credential: sel.Credential,
	}

	for i, ts := range sel.Tools {
		tool, err := compileTool(doc, ts)
		if err != nil {
			return nil, fmt.Errorf("compiler: tools[%d] (%s): %w", i, ts.Name, err)
		}
		m.Tools = append(m.Tools, tool)
	}

	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("compiler: emitted manifest failed validation: %w", err)
	}
	return m, nil
}

func compileTool(doc *Document, ts ToolSelection) (manifest.Tool, error) {
	if strings.TrimSpace(ts.Name) == "" {
		return manifest.Tool{}, fmt.Errorf("name is required")
	}
	method, path, op, ok := doc.findOperation(ts.OperationID)
	if !ok {
		return manifest.Tool{}, fmt.Errorf("operation_id %q not found in the API definition", ts.OperationID)
	}

	tool := manifest.Tool{
		Name:        ts.Name,
		Description: ts.Description,
		Operation: manifest.Operation{
			Method:       strings.ToUpper(method),
			Path:         path,
			FixedHeaders: ts.FixedHeaders,
		},
	}
	if tool.Description == "" {
		tool.Description = op.Summary
	}

	if ts.Annotations != nil {
		tool.Annotations = *ts.Annotations
	} else {
		tool.Annotations = defaultAnnotations(method)
	}

	selected := make(map[string]bool, len(ts.Fields))
	for _, fs := range ts.Fields {
		param, ok := op.findParameter(fs.Name)
		if !ok {
			return manifest.Tool{}, fmt.Errorf("field %q is not a declared parameter of operation %q", fs.Name, ts.OperationID)
		}
		selected[fs.Name] = true

		if fs.Pin != "" {
			tool.Pinned = append(tool.Pinned, manifest.Pinned{
				Name:  param.Name,
				In:    param.In,
				Value: fs.Pin,
			})
			continue
		}

		required := param.Required
		if fs.Required != nil {
			required = *fs.Required
		}
		tool.Inputs = append(tool.Inputs, manifest.Input{
			Name:        param.Name,
			Description: param.Description,
			In:          param.In,
			Type:        mapSchemaType(param.Schema),
			Required:    required,
		})
	}

	// Every upstream-required parameter must be either allow-listed
	// (caller-settable) or pinned (manifest-fixed) — never silently
	// dropped, which would otherwise make the call fail at runtime with
	// no explanation traceable back to a reviewable decision.
	for _, p := range op.Parameters {
		if p.Required && !selected[p.Name] {
			return manifest.Tool{}, fmt.Errorf(
				"operation %q requires parameter %q, which the selection neither allow-lists nor pins", ts.OperationID, p.Name)
		}
	}

	if len(ts.Response.Fields) == 0 {
		return manifest.Tool{}, fmt.Errorf("response.fields: at least one allow-listed field is required")
	}
	schema, hasSchema := op.responseSchema()
	isArray := hasSchema && schema.Type == "array"
	itemSchema := schema
	if isArray && schema.Items != nil {
		itemSchema = *schema.Items
	}
	for _, field := range ts.Response.Fields {
		if hasSchema {
			if err := validateFieldAgainstSchema(field, itemSchema); err != nil {
				return manifest.Tool{}, fmt.Errorf("response field %q: %w", field, err)
			}
		}
	}
	tool.Response = manifest.Response{Array: isArray, Fields: ts.Response.Fields}

	return tool, nil
}

// defaultAnnotations heuristically defaults the four go-mcp hints from
// HTTP method semantics, per the ADR's "heuristically defaulted from HTTP
// semantics, always reviewable and overridable" design. A selection may
// override this entirely via ToolSelection.Annotations.
func defaultAnnotations(method string) manifest.Annotations {
	switch strings.ToUpper(method) {
	case "GET", "HEAD":
		return manifest.Annotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: true}
	case "PUT":
		return manifest.Annotations{ReadOnlyHint: false, DestructiveHint: true, IdempotentHint: true, OpenWorldHint: true}
	case "DELETE":
		return manifest.Annotations{ReadOnlyHint: false, DestructiveHint: true, IdempotentHint: true, OpenWorldHint: true}
	case "PATCH":
		return manifest.Annotations{ReadOnlyHint: false, DestructiveHint: true, IdempotentHint: false, OpenWorldHint: true}
	default: // POST and anything else unrecognized
		return manifest.Annotations{ReadOnlyHint: false, DestructiveHint: false, IdempotentHint: false, OpenWorldHint: true}
	}
}

func mapSchemaType(s Schema) string {
	switch s.Type {
	case "integer", "boolean", "number":
		return s.Type
	default:
		return "string"
	}
}

// validateFieldAgainstSchema checks that field's first segment is a
// declared property of schema. Deeper segments are checked only as long as
// the schema keeps declaring nested properties for them; a schema that
// doesn't enumerate a nested object's properties (common in a trimmed
// fragment) ends the check there rather than refusing a plausible field —
// this compiler validates what the definition actually asserts, not more.
func validateFieldAgainstSchema(field string, schema Schema) error {
	if len(schema.Properties) == 0 {
		// The definition doesn't describe this object's properties at all
		// (e.g. a bare "object" schema) — nothing to check against.
		return nil
	}
	segs := strings.Split(field, ".")
	cur := schema
	for i, seg := range segs {
		prop, ok := cur.Properties[seg]
		if !ok {
			return fmt.Errorf("%q is not declared in the response schema", strings.Join(segs[:i+1], "."))
		}
		if len(prop.Properties) == 0 {
			return nil
		}
		cur = prop
	}
	return nil
}

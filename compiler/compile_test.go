package compiler

import (
	"testing"

	"github.com/hollis-labs/api-projection/manifest"
)

func testDoc() *Document {
	return &Document{
		OpenAPI: "3.0.3",
		Paths: map[string]PathItem{
			"/repos/{owner}/{repo}/releases": {
				"get": Operation{
					OperationID: "repos/list-releases",
					Summary:     "List releases",
					Parameters: []Parameter{
						{Name: "owner", In: "path", Required: true, Schema: Schema{Type: "string"}},
						{Name: "repo", In: "path", Required: true, Schema: Schema{Type: "string"}},
						{Name: "per_page", In: "query", Required: false, Schema: Schema{Type: "integer"}},
					},
					Responses: map[string]Response{
						"200": {
							Content: map[string]MediaType{
								"application/json": {
									Schema: Schema{
										Type: "array",
										Items: &Schema{
											Type: "object",
											Properties: map[string]Schema{
												"tag_name":     {Type: "string"},
												"name":         {Type: "string"},
												"published_at": {Type: "string"},
												"html_url":     {Type: "string"},
												"draft":        {Type: "boolean"},
												"author": {
													Type: "object",
													Properties: map[string]Schema{
														"login": {Type: "string"},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func testSelection() *Selection {
	return &Selection{
		APIName: "github",
		BaseURL: "https://api.github.com",
		Credential: &manifest.Credential{
			Ref:    "keychain://api-projection/github-pilot",
			Env:    "GITHUB_TOKEN",
			Header: "Authorization",
			Format: "Bearer %s",
		},
		Tools: []ToolSelection{
			{
				Name:        "list_releases",
				Description: "List releases for a repo.",
				OperationID: "repos/list-releases",
				Fields: []FieldSelection{
					{Name: "owner"},
					{Name: "repo"},
					{Name: "per_page", Pin: "5"},
				},
				Response: ResponseSelection{Fields: []string{"tag_name", "name", "published_at", "html_url"}},
			},
		},
	}
}

func TestCompile_Valid(t *testing.T) {
	m, err := Compile(testDoc(), testSelection())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(m.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(m.Tools))
	}
	tool := m.Tools[0]
	if tool.Operation.Method != "GET" || tool.Operation.Path != "/repos/{owner}/{repo}/releases" {
		t.Fatalf("unexpected operation: %+v", tool.Operation)
	}
	if len(tool.Inputs) != 2 {
		t.Fatalf("got %d inputs, want 2 (owner, repo)", len(tool.Inputs))
	}
	if len(tool.Pinned) != 1 || tool.Pinned[0].Name != "per_page" || tool.Pinned[0].Value != "5" {
		t.Fatalf("unexpected pinned fields: %+v", tool.Pinned)
	}
	if !tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint {
		t.Fatalf("GET should default to read-only, non-destructive: %+v", tool.Annotations)
	}
	if !tool.Response.Array {
		t.Fatal("response should be detected as an array")
	}
}

func TestCompile_UnknownOperationID(t *testing.T) {
	sel := testSelection()
	sel.Tools[0].OperationID = "repos/does-not-exist"
	if _, err := Compile(testDoc(), sel); err == nil {
		t.Fatal("Compile should refuse an unknown operation_id")
	}
}

func TestCompile_UnknownFieldName(t *testing.T) {
	sel := testSelection()
	sel.Tools[0].Fields = append(sel.Tools[0].Fields, FieldSelection{Name: "does_not_exist"})
	if _, err := Compile(testDoc(), sel); err == nil {
		t.Fatal("Compile should refuse a field that is not a declared parameter")
	}
}

func TestCompile_MissingRequiredParameterNeitherAllowedNorPinned(t *testing.T) {
	sel := testSelection()
	// Drop "repo" entirely — it's required by the operation.
	sel.Tools[0].Fields = []FieldSelection{{Name: "owner"}, {Name: "per_page", Pin: "5"}}
	if _, err := Compile(testDoc(), sel); err == nil {
		t.Fatal("Compile should refuse when a required upstream parameter is neither allow-listed nor pinned")
	}
}

func TestCompile_UnknownResponseField(t *testing.T) {
	sel := testSelection()
	sel.Tools[0].Response.Fields = append(sel.Tools[0].Response.Fields, "definitely_not_in_the_schema")
	if _, err := Compile(testDoc(), sel); err == nil {
		t.Fatal("Compile should refuse a response field not declared in the schema")
	}
}

func TestCompile_NestedResponseFieldValidated(t *testing.T) {
	sel := testSelection()
	sel.Tools[0].Response.Fields = append(sel.Tools[0].Response.Fields, "author.login")
	m, err := Compile(testDoc(), sel)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	found := false
	for _, f := range m.Tools[0].Response.Fields {
		if f == "author.login" {
			found = true
		}
	}
	if !found {
		t.Fatal("author.login should be accepted as a declared nested field")
	}
}

func TestCompile_UnknownNestedResponseField(t *testing.T) {
	sel := testSelection()
	sel.Tools[0].Response.Fields = append(sel.Tools[0].Response.Fields, "author.email")
	if _, err := Compile(testDoc(), sel); err == nil {
		t.Fatal("Compile should refuse author.email: the schema only declares author.login")
	}
}

func TestCompile_AnnotationsOverride(t *testing.T) {
	sel := testSelection()
	sel.Tools[0].Annotations = &manifest.Annotations{ReadOnlyHint: false, DestructiveHint: true, IdempotentHint: false, OpenWorldHint: true}
	m, err := Compile(testDoc(), sel)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !m.Tools[0].Annotations.DestructiveHint {
		t.Fatal("explicit annotation override should be honored")
	}
}

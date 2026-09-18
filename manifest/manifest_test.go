package manifest

import "testing"

func validManifest() *Manifest {
	return &Manifest{
		APIName: "github",
		BaseURL: "https://api.github.com",
		Credential: &Credential{
			Ref:    "keychain://api-projection/github-pilot",
			Env:    "GITHUB_TOKEN",
			Header: "Authorization",
			Format: "Bearer %s",
		},
		Tools: []Tool{
			{
				Name:        "list_releases",
				Description: "List releases for a repo.",
				Operation:   Operation{Method: "GET", Path: "/repos/{owner}/{repo}/releases"},
				Inputs: []Input{
					{Name: "owner", In: "path", Type: "string", Required: true},
					{Name: "repo", In: "path", Type: "string", Required: true},
				},
				Pinned: []Pinned{
					{Name: "per_page", In: "query", Value: "5"},
				},
				Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: true},
				Response:    Response{Array: true, Fields: []string{"tag_name", "name", "published_at", "html_url"}},
			},
		},
	}
}

func TestValidate_Valid(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidate_CredentialMustBeReference(t *testing.T) {
	m := validManifest()
	m.Credential.Ref = "ghp_literalsecretvalue"
	if err := m.Validate(); err == nil {
		t.Fatal("Validate() should refuse a literal credential value")
	}
}

func TestValidate_UnknownPathPlaceholder(t *testing.T) {
	m := validManifest()
	m.Tools[0].Operation.Path = "/repos/{owner}/{repo}/{unknown}/releases"
	if err := m.Validate(); err == nil {
		t.Fatal("Validate() should refuse a path placeholder with no matching field")
	}
}

func TestValidate_PathFieldWithNoPlaceholder(t *testing.T) {
	m := validManifest()
	m.Tools[0].Inputs = append(m.Tools[0].Inputs, Input{Name: "extra", In: "path", Type: "string"})
	if err := m.Validate(); err == nil {
		t.Fatal("Validate() should refuse a path field with no matching placeholder")
	}
}

func TestValidate_ResponseFieldRejectsWildcard(t *testing.T) {
	m := validManifest()
	m.Tools[0].Response.Fields = []string{"assets[*].url"}
	if err := m.Validate(); err == nil {
		t.Fatal("Validate() should refuse a wildcarded response field path")
	}
}

func TestValidate_ResponseFieldRejectsEmptySegment(t *testing.T) {
	m := validManifest()
	m.Tools[0].Response.Fields = []string{"author..login"}
	if err := m.Validate(); err == nil {
		t.Fatal("Validate() should refuse a response field path with an empty segment")
	}
}

func TestValidate_NoOperationAllowsAll(t *testing.T) {
	// There is no way to express "all operations" in the schema at all —
	// Operation names exactly one method+path per tool. This test exists
	// to document that invariant rather than exercise a failure path.
	m := validManifest()
	if m.Tools[0].Operation.Method == "" || m.Tools[0].Operation.Path == "" {
		t.Fatal("a compiled tool must always name exactly one operation")
	}
}

func TestValidate_FieldCannotBeBothInputAndPinned(t *testing.T) {
	m := validManifest()
	m.Tools[0].Pinned = append(m.Tools[0].Pinned, Pinned{Name: "owner", In: "path", Value: "hollis-labs"})
	if err := m.Validate(); err == nil {
		t.Fatal("Validate() should refuse a field declared as both input and pinned")
	}
}

func TestValidate_DuplicateResponseField(t *testing.T) {
	m := validManifest()
	m.Tools[0].Response.Fields = append(m.Tools[0].Response.Fields, "tag_name")
	if err := m.Validate(); err == nil {
		t.Fatal("Validate() should refuse a duplicate response field")
	}
}

func TestParse_RoundTrip(t *testing.T) {
	m, err := Parse([]byte(`
api_name: github
base_url: https://api.github.com
credential:
  ref: keychain://api-projection/github-pilot
  env: GITHUB_TOKEN
  header: Authorization
  format: "Bearer %s"
tools:
  - name: list_releases
    description: List releases for a repo.
    operation:
      method: GET
      path: /repos/{owner}/{repo}/releases
    inputs:
      - name: owner
        in: path
        type: string
        required: true
      - name: repo
        in: path
        type: string
        required: true
    pinned:
      - name: per_page
        in: query
        value: "5"
    annotations:
      read_only_hint: true
      idempotent_hint: true
      open_world_hint: true
    response:
      array: true
      fields: [tag_name, name, published_at, html_url]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.APIName != "github" || len(m.Tools) != 1 {
		t.Fatalf("unexpected parse result: %+v", m)
	}
}

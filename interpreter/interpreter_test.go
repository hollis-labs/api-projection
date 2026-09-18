package interpreter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/api-projection/manifest"
	gmcpserver "github.com/hollis-labs/go-mcp/server"
)

func releasesManifest(baseURL string) *manifest.Manifest {
	return &manifest.Manifest{
		APIName: "github",
		BaseURL: baseURL,
		Credential: &manifest.Credential{
			Ref:    "keychain://api-projection/github-pilot",
			Env:    "GITHUB_TOKEN",
			Header: "Authorization",
			Format: "Bearer %s",
		},
		Tools: []manifest.Tool{
			{
				Name:        "list_releases",
				Description: "List releases for a repo.",
				Operation: manifest.Operation{
					Method:       "GET",
					Path:         "/repos/{owner}/{repo}/releases",
					FixedHeaders: map[string]string{"Accept": "application/vnd.github+json"},
				},
				Inputs: []manifest.Input{
					{Name: "owner", In: "path", Type: "string", Required: true},
					{Name: "repo", In: "path", Type: "string", Required: true},
				},
				Pinned: []manifest.Pinned{
					{Name: "per_page", In: "query", Value: "5"},
				},
				Annotations: manifest.Annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: true},
				Response:    manifest.Response{Array: true, Fields: []string{"tag_name", "name", "published_at", "html_url"}},
			},
		},
	}
}

func TestHandler_BuildsRequestAndMapsResponse(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":"v0.2.0","name":"v0.2.0","published_at":"2026-09-18T21:53:23Z","html_url":"https://github.com/hollis-labs/mcp-host/releases/tag/v0.2.0","id":391806144,"url":"https://api.github.com/internal","author":{"login":"chrispian"}}]`))
	}))
	defer srv.Close()

	m := releasesManifest(srv.URL)
	ip := New(m, WithGetenv(func(k string) string {
		if k == "GITHUB_TOKEN" {
			return "ghp_realtoken"
		}
		return ""
	}))

	gmcp := gmcpserver.NewServer("test", "0.0.0")
	ip.Register(gmcp)

	result, err := gmcp.CallTool(context.Background(), "list_releases", map[string]any{
		"owner": "hollis-labs",
		"repo":  "mcp-host",
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if gotPath != "/repos/hollis-labs/mcp-host/releases" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "per_page=5" {
		t.Fatalf("query = %q, want per_page=5 (pinned, not caller-settable)", gotQuery)
	}
	if gotAuth != "Bearer ghp_realtoken" {
		t.Fatalf("Authorization header = %q", gotAuth)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Fatalf("Accept header = %q", gotAccept)
	}

	items, ok := result.([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected result shape: %#v", result)
	}
	item := items[0]
	if item["tag_name"] != "v0.2.0" || item["html_url"] == nil {
		t.Fatalf("unexpected mapped item: %#v", item)
	}
	if _, present := item["id"]; present {
		t.Fatal("id is not allow-listed and must not appear in the result")
	}
	if _, present := item["url"]; present {
		t.Fatal("url is not allow-listed and must not appear in the result")
	}
	if _, present := item["author"]; present {
		t.Fatal("author is not allow-listed and must not appear in the result")
	}
}

func TestHandler_CredentialMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should never be called when the credential is missing")
	}))
	defer srv.Close()

	m := releasesManifest(srv.URL)
	ip := New(m, WithGetenv(func(string) string { return "" }))
	gmcp := gmcpserver.NewServer("test", "0.0.0")
	ip.Register(gmcp)

	_, err := gmcp.CallTool(context.Background(), "list_releases", map[string]any{"owner": "a", "repo": "b"})
	if err == nil || !strings.Contains(err.Error(), "credential_missing") {
		t.Fatalf("expected a credential_missing error, got %v", err)
	}
}

func TestHandler_MissingRequiredInput(t *testing.T) {
	m := releasesManifest("http://example.invalid")
	ip := New(m)
	gmcp := gmcpserver.NewServer("test", "0.0.0")
	ip.Register(gmcp)

	_, err := gmcp.CallTool(context.Background(), "list_releases", map[string]any{"owner": "a"})
	if err == nil {
		t.Fatal("expected an error for a missing required input")
	}
}

func TestHandler_UpstreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	m := releasesManifest(srv.URL)
	ip := New(m, WithGetenv(func(string) string { return "ghp_realtoken" }))
	gmcp := gmcpserver.NewServer("test", "0.0.0")
	ip.Register(gmcp)

	_, err := gmcp.CallTool(context.Background(), "list_releases", map[string]any{"owner": "a", "repo": "b"})
	if err == nil || !strings.Contains(err.Error(), "upstream_error") {
		t.Fatalf("expected an upstream_error, got %v", err)
	}
}

func TestMapResponse_ObjectAndDottedPaths(t *testing.T) {
	body := []byte(`{"tag_name":"v1","author":{"login":"chrispian","email":"secret@example.com"}}`)
	got, err := mapResponse(manifest.Response{Fields: []string{"tag_name", "author.login"}}, body)
	if err != nil {
		t.Fatalf("mapResponse: %v", err)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "secret@example.com") {
		t.Fatalf("unlisted nested field leaked into result: %s", data)
	}
	if !strings.Contains(string(data), "chrispian") {
		t.Fatalf("listed nested field missing from result: %s", data)
	}
}

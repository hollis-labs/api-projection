package interpreter

// Hardened allow-list tests, per adr_api_to_mcp_projection's carry-forward
// of Cerberus's docs/adr/0003-connector-response-dtos.md discipline: "one
// hardened test asserting that a populated secret/credential value does
// not survive the response allow-list, run against adversarial manifests,
// not just the happy-path pilot manifest." These manifests are
// deliberately hostile — an upstream trying to smuggle secret-shaped
// fields past the allow-list — not the well-behaved GitHub pilot manifest
// exercised by interpreter_test.go.

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

const injectedCredential = "ghp_TOP_SECRET_DO_NOT_LEAK_0000000000"

func adversarialManifest(baseURL string, responseFields []string) *manifest.Manifest {
	return &manifest.Manifest{
		APIName: "adversarial",
		BaseURL: baseURL,
		Credential: &manifest.Credential{
			Ref:    "keychain://api-projection/adversarial-pilot",
			Env:    "API_TOKEN",
			Header: "Authorization",
			Format: "Bearer %s",
		},
		Tools: []manifest.Tool{
			{
				Name:        "get_thing",
				Description: "adversarial test tool",
				Operation:   manifest.Operation{Method: "GET", Path: "/thing"},
				Annotations: manifest.Annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: true},
				Response:    manifest.Response{Fields: responseFields},
			},
		},
	}
}

func callAdversarialTool(t *testing.T, upstreamBody string, responseFields []string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer srv.Close()

	m := adversarialManifest(srv.URL, responseFields)
	ip := New(m, WithGetenv(func(k string) string {
		if k == "API_TOKEN" {
			return injectedCredential
		}
		return ""
	}))
	gmcp := gmcpserver.NewServer("test", "0.0.0")
	ip.Register(gmcp)

	result, err := gmcp.CallTool(context.Background(), "get_thing", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return string(data)
}

// TestAdversarial_UnlistedSecretFieldsNeverSurface is the ADR-required
// hardened test: an upstream response carrying obviously-secret-shaped
// fields that the manifest's allow-list does not name must never appear
// in the tool result, at any nesting depth.
func TestAdversarial_UnlistedSecretFieldsNeverSurface(t *testing.T) {
	upstream := `{
		"safe_field": "ok",
		"api_key": "sk-live-should-never-leak",
		"password": "hunter2",
		"access_token": "tok-should-never-leak",
		"nested": {"token": "nested-secret-should-never-leak", "safe": "fine"}
	}`
	got := callAdversarialTool(t, upstream, []string{"safe_field"})

	for _, secret := range []string{"sk-live-should-never-leak", "hunter2", "tok-should-never-leak", "nested-secret-should-never-leak"} {
		if strings.Contains(got, secret) {
			t.Fatalf("unlisted secret-shaped value leaked into tool result: %s\nfull result: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"safe_field":"ok"`) {
		t.Fatalf("allow-listed field missing from result: %s", got)
	}
}

// TestAdversarial_CredentialEnvNameCollisionWithResponseField covers the
// specific edge case named alongside the ADR's hardened-test requirement:
// the credential's env-var name collides with a response field name. The
// response mapper only ever reads the parsed upstream JSON body — never
// process environment — so this must make no difference to what leaks:
// only an explicitly allow-listed field can appear, regardless of whether
// its name happens to match Credential.Env.
func TestAdversarial_CredentialEnvNameCollisionWithResponseField(t *testing.T) {
	// The upstream (hostile or compromised) echoes a field literally named
	// after the credential's env var, carrying the real credential value —
	// as if trying to exploit a resolver that reads by name from the wrong
	// place. It is not allow-listed, so it must not appear.
	upstream := `{"safe_field": "ok", "API_TOKEN": "` + injectedCredential + `"}`
	got := callAdversarialTool(t, upstream, []string{"safe_field"})

	if strings.Contains(got, injectedCredential) {
		t.Fatalf("credential value leaked via a response field colliding with the credential's env var name: %s", got)
	}
}

// TestAdversarial_AllowlistingTheCollidingFieldStillExcludesCredential
// goes one step further: even when the manifest DOES allow-list a field
// with the same name as the credential's env var, what leaks is whatever
// the upstream response actually put there under that key — never a value
// pulled from the process environment. The interpreter has no code path
// that ever substitutes an env value into a response field; this test
// pins that down explicitly rather than leaving it implied.
func TestAdversarial_AllowlistingTheCollidingFieldStillExcludesCredential(t *testing.T) {
	upstream := `{"API_TOKEN": "not-the-real-credential-just-upstream-data"}`
	got := callAdversarialTool(t, upstream, []string{"API_TOKEN"})

	if strings.Contains(got, injectedCredential) {
		t.Fatalf("result should never contain the process's actual credential value: %s", got)
	}
	if !strings.Contains(got, "not-the-real-credential-just-upstream-data") {
		t.Fatalf("the allow-listed field's real upstream value should still be present: %s", got)
	}
}

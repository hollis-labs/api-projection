// Package credential resolves a manifest's declared credential-capability
// reference (keychain://…, helper://…) into a real value.
//
// This is the host-side half of the ADR's credential design
// (adr_api_to_mcp_projection, decision 4): apps/station resolves the
// reference and hands the resolved value to the spawned interpreter
// process over its env, keyed by the manifest's declared name. The
// interpreter (this repo's interpreter package) never imports this
// package and never touches the credential store itself.
//
// The reference grammar and resolution shape mirror Cerberus's
// internal/secretref (apps/cerberus) — the same keychain:// / helper://
// scheme, the same helper delegation model — so a reference reads the
// same way anywhere in the portfolio. This package uses its own OS
// keychain service, "api-projection", distinct from Cerberus's
// ("cerberus") and Tether's ("tether"): a projected API's credential is
// neither an AI provider key nor a Cerberus-managed service secret, and
// giving it a third, correctly-scoped namespace avoids account-naming
// collisions with either.
package credential

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/api-projection/manifest"
	"github.com/zalando/go-keyring"
)

// KeychainService is the macOS Keychain service name every keychain://
// reference this package resolves is stored under.
const KeychainService = "api-projection"

// IsRef reports whether value is a credential-capability reference
// (keychain:// or helper://) rather than a literal value. A host resolving
// an arbitrary config value (e.g. apps/station resolving a process-mode
// logical server's env map before spawn) uses this to decide whether a
// given entry needs resolving at all.
func IsRef(value string) bool {
	return manifest.IsRef(value)
}

// Errors returned by Resolve.
var (
	ErrUnsupportedScheme = errors.New("unsupported credential reference scheme")
	ErrEmptySecret       = errors.New("credential resolved to an empty value")
	ErrHelperNotFound    = errors.New("credential helper not found")
	ErrResolvedToRef     = errors.New("credential resolved to another reference")
	ErrEncodedSecret     = errors.New("credential is go-keyring-encoded and could not be decoded")
	ErrNotAReference     = errors.New("not a credential reference")
)

const keyringBase64Prefix = "go-keyring-base64:"

// Ref is a parsed credential reference.
type Ref struct {
	Raw    string
	Scheme string
	// Authority is the keychain key-path authority for keychain:// refs,
	// or the helper executable name for helper:// refs.
	Authority string
	// Path is the remainder after Authority.
	Path string
}

// Parse validates and decomposes a credential reference of the form
// keychain://<authority>/<path> or helper://<helper>/<authority>/<path>.
func Parse(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if !manifest.IsRef(raw) {
		return Ref{}, fmt.Errorf("%w: %q", ErrNotAReference, raw)
	}
	scheme, rest, _ := strings.Cut(raw, "://")
	authority, path, ok := strings.Cut(rest, "/")
	if !ok || authority == "" || path == "" {
		return Ref{}, fmt.Errorf("parse %q: expected %s://<authority>/<path>", raw, scheme)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			return Ref{}, fmt.Errorf("parse %q: empty path segment", raw)
		}
	}
	if scheme == "helper" && (strings.ContainsAny(authority, `/\`) || authority == "." || authority == "..") {
		return Ref{}, fmt.Errorf("parse %q: helper name must not contain a path", raw)
	}
	return Ref{Raw: raw, Scheme: scheme, Authority: authority, Path: path}, nil
}

type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, []byte, error)

// KeychainProvider reads a stored credential. The production default wraps
// zalando/go-keyring directly against the OS keychain; tests substitute an
// in-memory fake via WithKeychainProvider so credential-package tests never
// touch the real OS keychain.
type KeychainProvider interface {
	Get(service, user string) (string, error)
}

type osKeychainProvider struct{}

func (osKeychainProvider) Get(service, user string) (string, error) {
	secret, err := keyring.Get(service, user)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return secret, nil
}

// Resolver resolves credential references. The zero value is not usable;
// construct one with NewResolver.
type Resolver struct {
	keychain   KeychainProvider
	lookHelper func(string) (string, error)
	run        commandRunner
}

// Option customizes a Resolver.
type Option func(*Resolver)

// WithKeychainProvider overrides the keychain read path. Exposed for
// tests; production callers should leave this at the default (the real OS
// keychain via zalando/go-keyring).
func WithKeychainProvider(p KeychainProvider) Option {
	return func(r *Resolver) { r.keychain = p }
}

// WithHelperLookup overrides how helper names map to executable paths.
// Exposed for tests.
func WithHelperLookup(fn func(string) (string, error)) Option {
	return func(r *Resolver) { r.lookHelper = fn }
}

// WithCommandRunner overrides helper execution. Exposed for tests.
func WithCommandRunner(fn func(ctx context.Context, name string, args ...string) ([]byte, []byte, error)) Option {
	return func(r *Resolver) { r.run = fn }
}

// NewResolver returns a Resolver backed by the OS keychain for keychain://
// refs and local helper execution for helper:// refs.
func NewResolver(opts ...Option) *Resolver {
	r := &Resolver{keychain: osKeychainProvider{}, lookHelper: lookHelperPath, run: runCommand}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Resolve returns the credential value behind raw. It refuses an unknown
// scheme, an empty result, and a result that is itself another reference
// (a helper echoing the reference back rather than resolving it) — see
// ErrResolvedToRef.
//
// The resolved value is never placed in a returned error; only the
// reference itself is, which is non-sensitive by construction.
func (r *Resolver) Resolve(ctx context.Context, raw string) (string, error) {
	ref, err := Parse(raw)
	if err != nil {
		return "", err
	}

	var secret string
	switch ref.Scheme {
	case "keychain":
		secret, err = r.keychain.Get(KeychainService, ref.Authority+"/"+ref.Path)
		if err != nil {
			err = fmt.Errorf("keychain get: %w", err)
		}
	case "helper":
		secret, err = r.resolveHelper(ctx, ref)
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedScheme, ref.Scheme)
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref.Raw, err)
	}

	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", fmt.Errorf("resolve %s: %w", ref.Raw, ErrEmptySecret)
	}
	if secret, err = decodeKeyringValue(secret); err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref.Raw, err)
	}
	if manifest.IsRef(secret) {
		return "", fmt.Errorf("resolve %s: %w", ref.Raw, ErrResolvedToRef)
	}
	return secret, nil
}

// Set stores value in the OS keychain under a keychain:// reference. It
// refuses a helper:// reference — a helper is resolved, never written to
// directly by this package.
func Set(raw, value string) error {
	ref, err := Parse(raw)
	if err != nil {
		return err
	}
	if ref.Scheme != "keychain" {
		return fmt.Errorf("set %s: only keychain:// references can be set directly", ref.Raw)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("set %s: %w", ref.Raw, ErrEmptySecret)
	}
	if err := keyring.Set(KeychainService, ref.Authority+"/"+ref.Path, value); err != nil {
		return fmt.Errorf("set %s: keychain set: %w", ref.Raw, err)
	}
	return nil
}

func (r *Resolver) resolveHelper(ctx context.Context, ref Ref) (string, error) {
	path, err := r.lookHelper(ref.Authority)
	if err != nil || path == "" {
		return "", fmt.Errorf("%w: %s", ErrHelperNotFound, ref.Authority)
	}
	// Helpers speak the keychain:// scheme; the helper:// authority names
	// the helper binary, not the secret, so it is dropped from the
	// delegated reference.
	stdout, stderr, err := r.run(ctx, path, "resolve", "keychain://"+ref.Path)
	if err != nil {
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return "", fmt.Errorf("%s: %s: %w", ref.Authority, msg, err)
		}
		return "", fmt.Errorf("%s: %w", ref.Authority, err)
	}
	return string(stdout), nil
}

// decodeKeyringValue reverses zalando/go-keyring's macOS storage encoding,
// for a helper:// path where the delegated helper reads through the
// `security` CLI directly rather than through go-keyring's own reader
// (which decodes this marker itself). See Cerberus's secretref package for
// the fuller account of why this matters.
func decodeKeyringValue(value string) (string, error) {
	if !strings.HasPrefix(value, keyringBase64Prefix) {
		return value, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, keyringBase64Prefix))
	if err != nil {
		return "", ErrEncodedSecret
	}
	return strings.TrimSpace(string(decoded)), nil
}

// helperSearchDirs are the locations checked after $PATH when locating a
// helper binary — matching Cerberus's secretref, since a spawned service
// (Station included) can inherit a minimal PATH.
func helperSearchDirs() []string {
	var dirs []string
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		dirs = append(dirs, gobin)
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		dirs = append(dirs, filepath.Join(gopath, "bin"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "go", "bin"), filepath.Join(home, ".local", "bin"))
	}
	if exe, err := os.Executable(); err == nil {
		if eval, evalErr := filepath.EvalSymlinks(exe); evalErr == nil {
			exe = eval
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	return append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
}

func lookHelperPath(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil && isExecutableFile(path) {
		return path, nil
	}
	for _, dir := range helperSearchDirs() {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrHelperNotFound, name)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path) //nolint:gosec // operator-configured helper path is the intended trust boundary
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // helper path comes from LookPath on an operator-configured name
	cmd.Env = sanitizedEnviron()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// sanitizedEnviron strips reference-valued env vars before exec'ing a
// helper, so a helper that consults a conventional env var before the
// keychain never echoes back the very reference being resolved. See
// Cerberus's secretref.sanitizedEnviron for the fuller rationale.
func sanitizedEnviron() []string {
	entries := os.Environ()
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, value, ok := strings.Cut(entry, "="); ok && manifest.IsRef(value) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

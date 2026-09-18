package credential

import (
	"context"
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"valid keychain", "keychain://github/pilot", false},
		{"valid helper", "helper://mux-apikey-helper/github/pilot", false},
		{"no scheme", "github/pilot", true},
		{"empty authority", "keychain:///pilot", true},
		{"empty path", "keychain://github/", true},
		{"empty path segment", "keychain://github//pilot", true},
		{"helper empty authority", "helper:///github/pilot", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Parse(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

type fakeKeychain map[string]string

func (f fakeKeychain) Get(service, user string) (string, error) {
	v, ok := f[service+"/"+user]
	if !ok {
		return "", nil
	}
	return v, nil
}

func TestResolve_Keychain(t *testing.T) {
	fake := fakeKeychain{"api-projection/github/pilot": "ghp_realtoken"}
	r := NewResolver(WithKeychainProvider(fake))

	got, err := r.Resolve(context.Background(), "keychain://github/pilot")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "ghp_realtoken" {
		t.Fatalf("Resolve = %q, want %q", got, "ghp_realtoken")
	}
}

func TestResolve_Keychain_Empty(t *testing.T) {
	r := NewResolver(WithKeychainProvider(fakeKeychain{}))
	_, err := r.Resolve(context.Background(), "keychain://github/missing")
	if !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("Resolve error = %v, want ErrEmptySecret", err)
	}
}

func TestResolve_Keychain_EncodedValue(t *testing.T) {
	// go-keyring-base64:<base64 of "ghp_realtoken">
	fake := fakeKeychain{"api-projection/github/pilot": "go-keyring-base64:Z2hwX3JlYWx0b2tlbg=="}
	r := NewResolver(WithKeychainProvider(fake))
	got, err := r.Resolve(context.Background(), "keychain://github/pilot")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "ghp_realtoken" {
		t.Fatalf("Resolve = %q, want %q", got, "ghp_realtoken")
	}
}

func TestResolve_Helper(t *testing.T) {
	r := NewResolver(
		WithHelperLookup(func(name string) (string, error) {
			if name != "mux-apikey-helper" {
				t.Fatalf("unexpected helper name %q", name)
			}
			return "/usr/local/bin/mux-apikey-helper", nil
		}),
		WithCommandRunner(func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			if len(args) != 2 || args[0] != "resolve" || args[1] != "keychain://openai/work" {
				t.Fatalf("unexpected helper invocation: %v", args)
			}
			return []byte("delegated-secret\n"), nil, nil
		}),
	)

	got, err := r.Resolve(context.Background(), "helper://mux-apikey-helper/openai/work")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "delegated-secret" {
		t.Fatalf("Resolve = %q, want %q", got, "delegated-secret")
	}
}

func TestResolve_Helper_EchoesReference(t *testing.T) {
	// A helper that echoes the reference back (the exact failure mode
	// sanitizedEnviron exists to prevent) must be caught, not trusted.
	r := NewResolver(
		WithHelperLookup(func(string) (string, error) { return "/bin/true", nil }),
		WithCommandRunner(func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
			return []byte("keychain://openai/work\n"), nil, nil
		}),
	)
	_, err := r.Resolve(context.Background(), "helper://mux-apikey-helper/openai/work")
	if !errors.Is(err, ErrResolvedToRef) {
		t.Fatalf("Resolve error = %v, want ErrResolvedToRef", err)
	}
}

func TestResolve_UnsupportedScheme(t *testing.T) {
	r := NewResolver()
	_, err := r.Resolve(context.Background(), "ftp://a/b")
	if !errors.Is(err, ErrNotAReference) {
		t.Fatalf("Resolve error = %v, want ErrNotAReference", err)
	}
}

func TestSet_RefusesHelperScheme(t *testing.T) {
	if err := Set("helper://mux-apikey-helper/openai/work", "value"); err == nil {
		t.Fatal("Set with a helper:// reference should be refused")
	}
}

func TestSet_RefusesEmptyValue(t *testing.T) {
	if err := Set("keychain://github/pilot", "  "); err == nil {
		t.Fatal("Set with an empty value should be refused")
	}
}

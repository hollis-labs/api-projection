// Command api-projection-cred provisions and verifies keychain:// entries
// in the "api-projection" OS keychain service — the ahead-of-time,
// operator-run counterpart to the credential package's runtime Resolve.
// UX deliberately mirrors Tether's mux-apikey-helper (apps/tether/cmd/
// mux-apikey-helper) so provisioning a credential reads the same way
// anywhere in the portfolio.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hollis-labs/api-projection/credential"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "api-projection-cred: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "resolve":
		if len(args) != 2 {
			return fmt.Errorf("resolve expects exactly 1 argument: <ref>")
		}
		secret, err := credential.NewResolver().Resolve(context.Background(), args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, secret)
		return err
	case "set":
		if len(args) != 2 {
			return fmt.Errorf("set expects exactly 1 argument: <ref> (secret is read from stdin)")
		}
		secret, err := readSecretFromStdin(stdin)
		if err != nil {
			return fmt.Errorf("read secret from stdin: %w", err)
		}
		return credential.Set(args[1], secret)
	default:
		return usageError()
	}
}

func readSecretFromStdin(stdin io.Reader) (string, error) {
	data, err := io.ReadAll(bufio.NewReader(stdin))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func usageError() error {
	return fmt.Errorf("usage:\n  api-projection-cred set <ref>       (secret read from stdin)\n  api-projection-cred resolve <ref>")
}

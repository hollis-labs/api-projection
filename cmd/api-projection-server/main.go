// Command api-projection-server is api-projection's Stage B process: a
// real, standalone MCP server (process-mode, matching mcp-host's
// process-mode contract exactly — see libs/mcp-host's echo-server example)
// that loads one manifest and serves its tools over stdio.
//
// It never touches the credential store. If the manifest declares a
// credential, this process expects the resolved value to already be
// present in its own environment, under the variable name the manifest
// names — handed to it by whatever spawned it (apps/station, per
// adr_api_to_mcp_projection's host-mediated credential resolution design).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	gmcpserver "github.com/hollis-labs/go-mcp/server"

	"github.com/hollis-labs/api-projection/interpreter"
	"github.com/hollis-labs/api-projection/manifest"
)

// version is the reported server version; overridable at build time via
// -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "api-projection-server: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("api-projection-server", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "", "path to the manifest YAML file (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" {
		return fmt.Errorf("-manifest is required")
	}

	m, err := manifest.Load(*manifestPath)
	if err != nil {
		return err
	}

	srv := gmcpserver.NewServer("api-projection:"+m.APIName, version)
	interpreter.New(m).Register(srv)

	return srv.Run(context.Background())
}

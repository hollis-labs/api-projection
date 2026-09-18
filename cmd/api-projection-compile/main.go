// Command api-projection-compile is api-projection's Stage A CLI: it
// resolves a human-authored selection against an OpenAPI document fragment
// and writes the resulting manifest as YAML.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hollis-labs/api-projection/compiler"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "api-projection-compile: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("api-projection-compile", flag.ContinueOnError)
	docPath := fs.String("doc", "", "path to the OpenAPI document fragment (required)")
	selPath := fs.String("selection", "", "path to the selection YAML file (required)")
	outPath := fs.String("out", "", "path to write the compiled manifest YAML (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *docPath == "" || *selPath == "" {
		return fmt.Errorf("both -doc and -selection are required")
	}

	doc, err := compiler.LoadDocument(*docPath)
	if err != nil {
		return err
	}
	sel, err := compiler.LoadSelection(*selPath)
	if err != nil {
		return err
	}
	m, err := compiler.Compile(doc, sel)
	if err != nil {
		return err
	}

	data, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	if *outPath == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(*outPath, data, 0o644)
}

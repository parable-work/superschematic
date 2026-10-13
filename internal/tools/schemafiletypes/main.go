// Command schemafiletypes writes the schema-file data form into the
// @superschematic/schema-ir package, and the core JSON Schema into the
// engine's Rust schema reader:
//
//	ir/typescript/schema-file.json  the JSON Schema `superschematic json-schema`
//	                                prints with the core registry and the
//	                                default naming
//	ir/typescript/schema-file.d.ts  TypeScript types for the same documents,
//	                                with the parts a registry closes left open
//	runtime/schema/rust-ir/src/schemafile/schema-file.json
//	                                the same JSON Schema, the Rust strict
//	                                loader's default meta-schema, inside its
//	                                crate (superschematic-schema-ir)
//
// They come from the reflection of the IR structs the JSON and YAML readers
// validate against (internal/loader/schemafile). ir/typescript's
// schema-file.json is the copy TestDefinitionGolden compares against; the
// Rust crate's is byte for byte the same.
//
// Run from the repository root: go run ./internal/tools/schemafiletypes
// With -check the command exits 1 when a committed file differs from what it
// would write, which is how CI keeps them in step with the IR.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
)

const (
	schemaPath     = "ir/typescript/schema-file.json"
	typesPath      = "ir/typescript/schema-file.d.ts"
	rustSchemaPath = "runtime/schema/rust-ir/src/schemafile/schema-file.json"
)

func main() {
	check := flag.Bool("check", false, "exit 1 when a committed file differs from the generated one")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	outputs, err := render()
	if err != nil {
		fail(err)
	}
	paths := make([]string, 0, len(outputs))
	for rel := range outputs {
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	failed := false
	for _, rel := range paths {
		path := filepath.Join(root, rel)
		if *check {
			have, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(have, outputs[rel]) {
				fmt.Fprintf(os.Stderr, "%s is out of date; run: go run ./internal/tools/schemafiletypes\n", rel)
				failed = true
			}
			continue
		}
		if err := os.WriteFile(path, outputs[rel], 0o644); err != nil {
			fail(err)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// render returns each file's content keyed by its path from the repository
// root.
func render() (map[string][]byte, error) {
	definition, err := schemafile.Definition()
	if err != nil {
		return nil, fmt.Errorf("schema-file JSON Schema: %w", err)
	}
	types, err := schemafile.TypeScriptDeclarations()
	if err != nil {
		return nil, fmt.Errorf("schema-file TypeScript types: %w", err)
	}
	return map[string][]byte{schemaPath: definition, typesPath: types, rustSchemaPath: definition}, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

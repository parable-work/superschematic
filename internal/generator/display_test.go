package generator

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// D48: no generator renders @display, as none renders a field's @docs
// title. The DB, API and General fixtures build with every type language,
// every SDK and the Go, TypeScript and Rust servers twice, as loaded and
// with a display on every type, and the two trees hold the same files,
// byte for byte, but for the one output that copies the IR: the Rust
// types crate's schemas/<Type>.json, an IR document per @jsonField
// payload, which carries each type's display as it carries its fields'
// titles, and nothing else besides.
func TestDisplayChangesNoGeneratedFile(t *testing.T) {
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	every := map[string]any{
		"go":         map[string]any{"enabled": true},
		"typescript": map[string]any{"enabled": true},
		"python":     map[string]any{"enabled": true},
		"rust":       map[string]any{"enabled": true},
	}
	build := func(display bool) (string, int) {
		root := t.TempDir()
		displays := 0
		load := func(service string) (*ir.Schema, error) {
			schema, err := loader.LoadService(filepath.Join(tsFixtures, service))
			if err == nil && display {
				displays += addDisplays(schema)
			}
			return schema, err
		}
		for _, run := range []struct{ service, api string }{
			{"fixture-db", ""},
			{"fixture-general", ""},
			{"fixture-api", APILanguageGo},
			{"fixture-api", APILanguageTypeScript},
			{"fixture-api", APILanguageRust},
		} {
			schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, run.service))
			if err != nil {
				t.Fatalf("load %s: %v", run.service, err)
			}
			if display {
				displays += addDisplays(schema)
			}
			cfg.Outputs["types"] = every
			out := root
			if run.api != "" {
				cfg.Outputs["sdk"] = every
				cfg.Outputs["api"] = map[string]any{"enabled": true, "language": run.api}
				// The servers share api/<service>; the TypeScript one
				// refuses an encrypted operation it does not hand over.
				out = filepath.Join(root, run.api)
				manualEncryptedOperations(schema)
			}
			// The Rust server's Cargo.toml points at the HTTP runtime's
			// crate by a path, the same from both trees.
			paths := naming.LocalPaths{HTTPRuntimeRust: filepath.Join(root, "runtime", "http", "rust")}
			if _, err := Run(schema, cfg, Options{OutputRoot: out, Naming: naming.Default(), Paths: paths, Clock: clock, LoadDependency: load}); err != nil {
				t.Fatalf("run %s (api %q, display %v): %v", run.service, run.api, display, err)
			}
		}
		return root, displays
	}

	without, _ := build(false)
	with, displays := build(true)
	if displays == 0 {
		t.Fatal("no type took a display")
	}
	files := func(root string) map[string][]byte {
		out := map[string][]byte{}
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			out[rel] = data
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	want, got := files(without), files(with)
	if len(want) == 0 {
		t.Fatal("the fixtures generated nothing")
	}
	copies := 0
	for rel, data := range want {
		other, ok := got[rel]
		switch {
		case !ok:
			t.Errorf("%s is missing with displays", rel)
		case irCopy.MatchString(filepath.ToSlash(rel)):
			copies++
			if !bytes.Equal(data, withoutDisplays(t, rel, other)) {
				t.Errorf("%s changes with displays by more than the types' display", rel)
			}
		case !bytes.Equal(data, other):
			t.Errorf("%s changes with displays", rel)
		}
	}
	if copies == 0 {
		t.Error("no Rust crate wrote an IR copy of a payload type")
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			t.Errorf("%s is written only with displays", rel)
		}
	}
}

// irCopy matches the files that copy the IR: the Rust types crate's IR
// document of each @jsonField payload type.
var irCopy = regexp.MustCompile(`^types/rust/[^/]+/schemas/[^/]+\.json$`)

// withoutDisplays is an IR copy with the display of each of its types
// taken out, as the generator writes it.
func withoutDisplays(t *testing.T, rel string, data []byte) []byte {
	t.Helper()
	var doc ir.Schema
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	found := false
	for _, td := range doc.Types {
		found = found || td.Display != nil
		td.Display = nil
	}
	if !found {
		t.Errorf("%s carries no display", rel)
	}
	out, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return append(out, '\n')
}

// addDisplays gives every type of the schema a display with every member
// a type without behaviors takes: the nouns, a create label, its first
// string field as the title and its first field as the summary. It returns
// how many types took one.
func addDisplays(schema *ir.Schema) int {
	n := 0
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for name, td := range types {
			display := &ir.TypeDisplay{Noun: name, Plural: name + "s", CreateLabel: "New " + name}
			for _, f := range td.Fields {
				if display.SummaryFields == nil {
					display.SummaryFields = []string{f.Name}
				}
				if display.TitleField == "" && f.TypeRef.Name == "string" && !f.TypeRef.IsArray && !f.TypeRef.IsMap {
					display.TitleField = f.Name
				}
			}
			td.Display = display
			n++
		}
	}
	return n
}

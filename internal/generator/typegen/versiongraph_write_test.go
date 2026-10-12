package typegen

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestWriteTypesRemovesOnlyStaleDescriptors writes a types module over a
// versiongraph/ directory that already holds a stale descriptor and files
// the generator never writes. A schema's writes remove each *.json no graph
// of the schema writes and keep everything else; a schema with no graph
// removes the directory only when nothing else is left in it.
func TestWriteTypesRemovesOnlyStaleDescriptors(t *testing.T) {
	cases := []struct {
		name    string
		service string
		before  []string
		want    []string // the directory's files after the write; nil: no directory
	}{
		{
			name:    "a graph's descriptor replaces a stale one",
			service: "fixture-version-graph-db",
			before:  []string{"dropped.json", "notes.txt", "vendor/extra.json"},
			want:    []string{"notes.txt", "recipe.json", "vendor/extra.json"},
		},
		{
			name:    "no graph keeps what is not a descriptor",
			service: "fixture-db",
			before:  []string{"recipe.json", "notes.txt"},
			want:    []string{"notes.txt"},
		},
		{
			name:    "no graph removes a directory left empty",
			service: "fixture-db",
			before:  []string{"recipe.json"},
		},
		{
			name:    "no graph and no directory writes none",
			service: "fixture-db",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, tc.service))
			if err != nil {
				t.Fatalf("load %s: %v", tc.service, err)
			}
			output, err := Generate(schema, Options{
				SchemaName: tc.service,
				ModulePath: "example.com/schemas/types/go/" + tc.service,
				Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			outDir := t.TempDir()
			dir := filepath.Join(outDir, "versiongraph")
			for _, name := range tc.before {
				path := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			if err := WriteTypes(output, outDir); err != nil {
				t.Fatalf("write types: %v", err)
			}

			var got []string
			err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, err := filepath.Rel(dir, path)
				got = append(got, filepath.ToSlash(rel))
				return err
			})
			if os.IsNotExist(err) {
				got = nil
			} else if err != nil {
				t.Fatalf("list %s: %v", dir, err)
			} else if got == nil {
				got = []string{}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("after the write versiongraph/ is %s, want %s", describeDir(got), describeDir(tc.want))
			}
		})
	}
}

// TestGenerateRefusesAGraphColumnNoValueClassReads adds to a graph member a
// Geo.Location, a JSON object the sql generator stores as POINT, shaped as
// the catalog's row hydrates it. No value class reads POINT, so the graph
// has no descriptor, and Generate fails with the descriptor's error, naming
// the field, the JSON object it holds and the SQL type, rather than writing
// versiongraph/ without it.
func TestGenerateRefusesAGraphColumnNoValueClassReads(t *testing.T) {
	const svc = "fixture-version-graph-db"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, svc))
	if err != nil {
		t.Fatalf("load %s: %v", svc, err)
	}
	schema.Scalars["Geo.Location"] = &ir.ScalarDef{
		Name: "Geo.Location", LanguagePrimitive: ir.LanguageString, Primitive: "String", HasCustomParse: true,
		TypeMappings: map[string]string{"json_schema": ir.JSONSchemaObjectType, "sql": "POINT"},
	}
	schema.Types["Utensil"].Fields = append(schema.Types["Utensil"].Fields, &ir.FieldDef{
		Name: "shelf", TypeRef: ir.TypeRef{Name: "Geo.Location"}, Required: true,
	})

	_, err = Generate(schema, Options{
		SchemaName: svc,
		ModulePath: "example.com/schemas/types/go/" + svc,
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err == nil || !strings.HasPrefix(err.Error(), "graphdesc: version graph Recipe: Utensil.shelf: ") ||
		!strings.Contains(err.Error(), "Geo.Location holds a JSON object but is stored as POINT,") {
		t.Fatalf("Generate = %v, want the descriptor's refusal of Utensil.shelf, a JSON object stored as POINT", err)
	}
}

func describeDir(files []string) string {
	if files == nil {
		return "absent"
	}
	return fmt.Sprintf("a directory holding %q", files)
}

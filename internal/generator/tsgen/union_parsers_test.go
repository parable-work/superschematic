package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// The union-parsers services cover parse<Union>FromJSON. union-parsers-shapes
// declares Shape (Circle | Square), whose Circle has a date-time field.
// union-parsers imports them and declares three unions of its own: Figure
// (Circle | Square | Stamp), whose imported members and @strictJSON Stamp
// are picked by their keys; Marker (Pin | Flag), which every member's kind
// discriminates; and Contact (Email | Sms), two @strictJSON members with the
// same fields told apart by kind's default, a tag. Drawing holds them as a
// field, an optional field, a list, a list of lists, a map and a map of
// lists. It is JSON-authored so the real loader runs, and local to this
// test, so no other generator's goldens read it.
const (
	unionParsersShapesService = "union-parsers-shapes"
	unionParsersService       = "union-parsers"
)

var unionParsersFiles = map[string]map[string]string{
	unionParsersShapesService: {
		"schema.config.json": `{
  "name": "union-parsers-shapes",
  "kind": "General",
  "outputs": { "types": { "typescript": { "enabled": true } } }
}`,
		"src/shapes.schema.json": `{
  "scalars": {
    "Temporal.DateTime": { "name": "Temporal.DateTime", "languagePrimitive": "string" }
  },
  "types": {
    "Circle": {
      "name": "Circle",
      "role": "EmbeddedStruct",
      "fields": [
        { "name": "radius", "typeRef": { "name": "number" }, "required": true },
        { "name": "drawnAt", "typeRef": { "name": "Temporal.DateTime" } }
      ]
    },
    "Square": {
      "name": "Square",
      "role": "EmbeddedStruct",
      "fields": [
        { "name": "side", "typeRef": { "name": "number" }, "required": true }
      ]
    }
  },
  "unions": {
    "Shape": { "name": "Shape", "types": ["Circle", "Square"] }
  }
}`,
	},
	unionParsersService: {
		"schema.config.json": `{
  "name": "union-parsers",
  "kind": "General",
  "dependencies": [{ "name": "union-parsers-shapes", "kind": "General" }],
  "outputs": { "types": { "typescript": { "enabled": true } } }
}`,
		"src/drawing.schema.json": `{
  "imports": [{ "package": "@schemas/union-parsers-shapes", "types": ["Circle", "Square", "Shape"] }],
  "scalars": {
    "Temporal.DateTime": { "name": "Temporal.DateTime", "languagePrimitive": "string" }
  },
  "types": {
    "Stamp": {
      "name": "Stamp",
      "role": "EmbeddedStruct",
      "strictJSON": true,
      "fields": [
        { "name": "text", "typeRef": { "name": "string" }, "required": true, "validateMaxLength": 8 },
        { "name": "at", "typeRef": { "name": "Temporal.DateTime" }, "required": true }
      ]
    },
    "Pin": {
      "name": "Pin",
      "role": "EmbeddedStruct",
      "strictJSON": true,
      "fields": [
        { "name": "kind", "typeRef": { "name": "string" }, "required": true, "internalMetadata": true, "default": "pin" },
        { "name": "label", "typeRef": { "name": "string" }, "required": true }
      ]
    },
    "Flag": {
      "name": "Flag",
      "role": "EmbeddedStruct",
      "fields": [
        { "name": "kind", "typeRef": { "name": "string" }, "required": true, "internalMetadata": true, "default": "flag" },
        { "name": "raisedAt", "typeRef": { "name": "Temporal.DateTime" }, "required": true }
      ]
    },
    "Email": {
      "name": "Email",
      "role": "EmbeddedStruct",
      "strictJSON": true,
      "fields": [
        { "name": "kind", "typeRef": { "name": "string" }, "required": true, "default": "email" },
        { "name": "address", "typeRef": { "name": "string" }, "required": true }
      ]
    },
    "Sms": {
      "name": "Sms",
      "role": "EmbeddedStruct",
      "strictJSON": true,
      "fields": [
        { "name": "kind", "typeRef": { "name": "string" }, "required": true, "default": "sms" },
        { "name": "address", "typeRef": { "name": "string" }, "required": true }
      ]
    },
    "Drawing": {
      "name": "Drawing",
      "role": "EmbeddedStruct",
      "fields": [
        { "name": "shape", "typeRef": { "name": "Shape" }, "required": true },
        { "name": "figure", "typeRef": { "name": "Figure" } },
        { "name": "figures", "typeRef": { "name": "Figure", "isArray": true }, "required": true },
        { "name": "rows", "typeRef": { "name": "Figure", "isArray": true, "isArrayOfArrays": true } },
        { "name": "byName", "typeRef": { "name": "Figure", "isMap": true } },
        { "name": "listsByName", "typeRef": { "name": "Figure", "isArray": true, "isMap": true } },
        { "name": "marker", "typeRef": { "name": "Marker" } },
        { "name": "contacts", "typeRef": { "name": "Contact", "isArray": true } }
      ]
    }
  },
  "unions": {
    "Figure": { "name": "Figure", "types": ["Circle", "Square", "Stamp"] },
    "Marker": { "name": "Marker", "types": ["Pin", "Flag"] },
    "Contact": { "name": "Contact", "types": ["Email", "Sms"] }
  }
}`,
	},
}

// loadUnionParsers stages the union-parsers services and returns the
// package cases for them, the shapes package first.
func loadUnionParsers(t *testing.T) []tsPackageCase {
	t.Helper()
	root := t.TempDir()
	schemas := map[string]*ir.Schema{}
	for _, service := range []string{unionParsersShapesService, unionParsersService} {
		dir := filepath.Join(root, service)
		for rel, contents := range unionParsersFiles[service] {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		schema, err := loader.LoadService(dir)
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		schemas[service] = schema
	}
	return []tsPackageCase{
		{name: unionParsersShapesService, schema: schemas[unionParsersShapesService]},
		{
			name:   unionParsersService,
			schema: schemas[unionParsersService],
			deps:   map[string]*ir.Schema{unionParsersShapesService: schemas[unionParsersShapesService]},
		},
	}
}

// TestUnionParsersRendering pins what the union parsers and a model's
// parser of union fields are made of. It needs no TypeScript toolchain;
// TestUnionParsersBehavior runs the same output.
func TestUnionParsersRendering(t *testing.T) {
	cases := loadUnionParsers(t)
	read := func(tc tsPackageCase) func(rel string) string {
		output, err := Generate(tc.schema, Options{
			SchemaName:   tc.name,
			Dependencies: tc.deps,
			Clock:        codegen.FixedClock(time.Unix(0, 0).UTC()),
		})
		if err != nil {
			t.Fatalf("generate %s: %v", tc.name, err)
		}
		outDir := filepath.Join(t.TempDir(), tc.name)
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write types %s: %v", tc.name, err)
		}
		return func(rel string) string {
			t.Helper()
			data, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			return string(data)
		}
	}
	shapesPackage := naming.Default().NpmTypesPackage(unionParsersShapesService)
	checks := []struct {
		read  func(string) string
		rel   string
		wants []string
	}{
		{read(cases[0]), "validators/index.ts", []string{"export * from './unions';"}},
		{read(cases[0]), "validators/unions.ts", []string{
			"import type { Shape } from '../types';",
			"import { parseCircleFromJSON, parseCircleJsonNonStrict } from './types/circle';",
			"export function parseShapeFromJSON(input: unknown): Shape {",
			"export function validateShape(value: Shape | null | undefined): ValidationResult {",
			"export function parseShapeJson(input: unknown): unknown {",
			`unionPayloadKeysWithin(payload, ["radius", "drawnAt"]) &&`,
			// A member reads the payload only when it holds radius.
			`unionMemberReads([parseCircleFromJSON, parseCircleJsonNonStrict], payload, ["radius"])`,
			"throw new ParseError('parseShape: no member of Shape reads the payload', UNION_MISMATCH);",
		}},
		{read(cases[1]), "validators/unions.ts", []string{
			"import type { Contact, Figure, Marker } from '../types';",
			// Imported members come from the package that declares them.
			"import { parseCircleFromJSON, parseCircleJsonNonStrict, parseSquareFromJSON, parseSquareJsonNonStrict } from '" + shapesPackage + "/validators';",
			"import { parseStampFromJSON, parseStampJsonNonStrict } from './types/stamp';",
			// Marker dispatches by its discriminator.
			`switch (payload["kind"]) {`,
			`case "flag":` + "\n      return [parseFlagFromJSON, parseFlagJsonNonStrict];",
			`"parseMarker: discriminator kind is " + stated + ", not one of \"pin\", \"flag\"",`,
			// Contact's members differ by a tag.
			`unionPayloadTagsAllow(payload, [["kind", "sms"]])`,
		}},
		{read(cases[1]), "validators/types/drawing.ts", []string{
			"import type { Drawing, Shape, Figure, Marker, Contact } from '../../types';",
			"import { parseShapeFromJSON, validateShape } from '" + shapesPackage + "/validators';",
			"import { parseContactFromJSON, validateContact, parseFigureFromJSON, validateFigure, parseMarkerFromJSON, validateMarker } from '../unions';",
			// A union field is validated as its member, nested under its path.
			"const memberErrors = validateFigure(member as Figure);",
			"value.figures.forEach((item, index) => validateUnion(item, `figures[${index}]`));",
			"? parseShapeFromJSON(obj.shape)",
			"(obj.figures as unknown[]).map((item) => parseFigureFromJSON(item))",
			"Array.isArray(row) ? row.map((item) => parseFigureFromJSON(item)) : (row as Figure[])",
			"[k, v != null ? parseFigureFromJSON(v) : null]",
			"[k, Array.isArray(v) ? (v as unknown[]).map((item) => parseFigureFromJSON(item)) : (v as unknown as Figure[])]",
			"? parseMarkerFromJSON(obj.marker)",
			"(obj.contacts as unknown[]).map((item) => parseContactFromJSON(item))",
		}},
	}
	for _, check := range checks {
		source := check.read(check.rel)
		for _, want := range check.wants {
			if !strings.Contains(source, want) {
				t.Errorf("%s missing %q:\n%s", check.rel, want, source)
			}
		}
	}
}

// TestUnionParsersBehavior type-checks the union-parsers packages (tsc) and
// runs testdata/union_parsers_runtime.test.ts in union-parsers under bun:
// each union reads the member the Go and Rust decoders pick, with its
// date-time fields as Date and its @strictJSON checks, in every place
// Drawing holds one.
func TestUnionParsersBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-parser check in -short mode")
	}
	typesRoot, bunPath := buildTSPackages(t, loadUnionParsers(t))
	if t.Failed() {
		return
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	const name = "union_parsers_runtime.test.ts"
	source, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(typesRoot, unionParsersService)
	// Staged after tsc ran, which does not know bun:test.
	staged := filepath.Join(dir, name)
	if err := os.WriteFile(staged, source, 0o644); err != nil {
		t.Fatal(err)
	}
	run := exec.Command(bunPath, "test", staged)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	if !strings.Contains(string(out), " 0 fail") {
		t.Fatalf("unexpected bun test summary for %s:\n%s", name, out)
	}
	if testing.Verbose() {
		t.Logf("%s:\n%s", name, out)
	}
}

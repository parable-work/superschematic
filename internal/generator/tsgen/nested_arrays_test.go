package tsgen

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixture-nested-arrays services cover T[][] of primitives, enums and
// object types. nested-arrays-edges reaches the validator branches they
// leave out: a @strictJSON object inside T[][] (validated per element), a
// secret inside it (masked per element), a DateTime-like scalar parsed from
// JSON, a validated scalar, an enum imported from another schema, and
// @validate rules checked on each innermost element while the list bounds
// stay on the outer list. It is JSON-authored so the real loader runs.
const (
	nestedArraysEdgesService      = "nested-arrays-edges"
	nestedArraysEdgesEnumsService = "nested-arrays-edges-enums"
)

var nestedArraysEdgesFiles = map[string]map[string]string{
	nestedArraysEdgesEnumsService: {
		"schema.config.json": `{
  "name": "nested-arrays-edges-enums",
  "kind": "General",
  "outputs": { "types": { "typescript": { "enabled": true } } }
}`,
		"src/tone.schema.json": `{
  "name": "Tone",
  "kind": "Enum",
  "values": [
    { "name": "Warm", "serializedAs": "warm" },
    { "name": "Cool", "serializedAs": "cool" }
  ]
}`,
	},
	nestedArraysEdgesService: {
		"schema.config.json": `{
  "name": "nested-arrays-edges",
  "kind": "General",
  "dependencies": [{ "name": "nested-arrays-edges-enums", "kind": "General" }],
  "outputs": { "types": { "typescript": { "enabled": true } } }
}`,
		"src/sheet.schema.json": `{
  "imports": [{ "package": "@schemas/nested-arrays-edges-enums", "types": ["Tone"] }],
  "scalars": {
    "Identity.UUID": { "name": "Identity.UUID", "languagePrimitive": "string" },
    "Temporal.DateTime": { "name": "Temporal.DateTime", "languagePrimitive": "string" }
  },
  "types": {
    "Cell": {
      "name": "Cell",
      "role": "EmbeddedStruct",
      "strictJSON": true,
      "fields": [
        { "name": "label", "typeRef": { "name": "string" }, "required": true },
        { "name": "token", "typeRef": { "name": "string" }, "required": true, "secret": true }
      ]
    },
    "Sheet": {
      "name": "Sheet",
      "role": "EmbeddedStruct",
      "strictJSON": true,
      "fields": [
        { "name": "cells", "typeRef": { "name": "Cell", "isArray": true, "isArrayOfArrays": true }, "required": true },
        { "name": "ids", "typeRef": { "name": "Identity.UUID", "isArray": true, "isArrayOfArrays": true }, "required": true },
        { "name": "stamps", "typeRef": { "name": "Temporal.DateTime", "isArray": true, "isArrayOfArrays": true } },
        { "name": "tones", "typeRef": { "name": "Tone", "isArray": true, "isArrayOfArrays": true } },
        {
          "name": "codes",
          "typeRef": { "name": "string", "isArray": true, "isArrayOfArrays": true },
          "validateMaxLength": 3,
          "validatePattern": "^[a-z]+$",
          "validateListMin": 1,
          "validateListMax": 2
        },
        {
          "name": "weights",
          "typeRef": { "name": "number", "isArray": true, "isArrayOfArrays": true },
          "validateMin": 0,
          "validateMax": 1
        }
      ]
    }
  }
}`,
	},
}

// loadNestedArraysEdges stages the nested-arrays-edges services and returns
// the package cases for them, the imported enums package first.
func loadNestedArraysEdges(t *testing.T) []tsPackageCase {
	t.Helper()
	root := t.TempDir()
	schemas := map[string]*ir.Schema{}
	for _, service := range []string{nestedArraysEdgesEnumsService, nestedArraysEdgesService} {
		dir := filepath.Join(root, service)
		for rel, contents := range nestedArraysEdgesFiles[service] {
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
		{name: nestedArraysEdgesEnumsService, schema: schemas[nestedArraysEdgesEnumsService]},
		{
			name:   nestedArraysEdgesService,
			schema: schemas[nestedArraysEdgesService],
			deps:   map[string]*ir.Schema{nestedArraysEdgesEnumsService: schemas[nestedArraysEdgesEnumsService]},
		},
	}
}

// TestNestedArraysEdgesRendering pins the T[][] branches of the validator,
// parser and mask templates that the fixture goldens do not reach. It needs
// no TypeScript toolchain; TestNestedArraysValidatorBehavior runs the same
// output.
func TestNestedArraysEdgesRendering(t *testing.T) {
	cases := loadNestedArraysEdges(t)
	edges := cases[1]
	output, err := Generate(edges.schema, Options{
		SchemaName:   edges.name,
		Dependencies: edges.deps,
		Clock:        codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	outDir := filepath.Join(t.TempDir(), edges.name)
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(data)
	}

	enumsPackage := naming.Default().NpmTypesPackage(nestedArraysEdgesEnumsService)
	checks := map[string][]string{
		"types/types.ts": {
			"cells: Cell[][];",
			"ids: string[][];",
			"stamps?: JSDate[][] | null;",
			"tones?: Tone[][] | null;",
			"codes?: string[][] | null;",
			"weights?: number[][] | null;",
		},
		"validators/types/sheet.ts": {
			// The imported enum's validator comes from its own package.
			"import { validateToneRequired, validateTone } from '" + enumsPackage + "/validators/enums';",
			"const [valid, fieldErrors] = validateTone(item);",
			"setFieldErrors(errors, `tones[${rowIndex}][${index}]`, fieldErrors);",
			"const [valid, fieldErrors] = validateIdentityUUIDRequired(item);",
			"addFieldError(errors, `ids[${rowIndex}]`, \"required\", \"inner list must be an array\");",
			// @strictJSON objects are validated at every innermost element.
			"validateNested(item, `cells[${rowIndex}][${index}]`)",
			// List bounds on the outer list, element rules on each element.
			"if (fieldValue.length < 1) {",
			"if (fieldValue.length > 2) {",
			"const path = `codes[${rowIndex}][${index}]`;",
			"addFieldError(errors, path, \"maxLength\",",
			"addFieldError(errors, path, \"pattern\",",
			"const path = `weights[${rowIndex}][${index}]`;",
			"addFieldError(errors, path, \"min\",",
			"addFieldError(errors, path, \"max\",",
			// JSON parsing reaches the innermost elements.
			"row.map((item) => parseTemporalDateTimeFromLib(item as string) as JSDate)",
			"row.map((item) => parseCellFromJSON(item))",
		},
		"mask/types/sheet.ts": {
			"row.map((item: Cell) => maskSecretsCell(item))",
		},
	}
	for rel, wants := range checks {
		source := read(rel)
		for _, want := range wants {
			if !strings.Contains(source, want) {
				t.Errorf("%s missing %q:\n%s", rel, want, source)
			}
		}
	}
}

// nestedArraysDriver validates each vector with the generated validator the
// vector names and reports, per vector, every failing path with its sorted
// validator names. For the edges package it also parses and masks one
// value, to show both reach the innermost elements.
const nestedArraysDriver = `import { readFileSync, writeFileSync } from 'node:fs';
import * as pkg from './index';

type Vector = { type: string; payload: unknown };
type Validate = (value: unknown) => true | Record<string, { validator: string }[]>;

const vectors = JSON.parse(readFileSync(process.env.NESTED_VECTORS as string, 'utf8')) as Record<string, Vector>;
const exports = pkg as unknown as Record<string, unknown>;
const verdicts: Record<string, Record<string, string[]>> = {};
for (const [name, vector] of Object.entries(vectors)) {
  const result = (exports['validate' + vector.type] as Validate)(vector.payload);
  const fields: Record<string, string[]> = {};
  if (result !== true) {
    for (const [path, errs] of Object.entries(result)) {
      fields[path] = errs.map((e) => e.validator).sort();
    }
  }
  verdicts[name] = fields;
}

const extras: Record<string, unknown> = {};
if (typeof exports.parseSheetFromJSON === 'function') {
  const parse = exports.parseSheetFromJSON as (input: unknown) => any;
  const mask = exports.maskSecretsSheet as (value: unknown) => any;
  const sheet = parse({
    cells: [[{ label: 'a', token: 's1' }], [], [{ label: 'b', token: 's2' }, { label: 'c', token: 's3' }]],
    ids: [['0Wk2bUe4TBlrdAhXhGAEJ1']],
    stamps: [[], ['2026-01-02T03:04:05Z']],
  });
  const masked = mask(sheet);
  extras.stampIsDate = sheet.stamps[1][0] instanceof Date;
  extras.cellLabel = sheet.cells[2][1].label;
  extras.maskedTokens = masked.cells.map((row: { token: string }[]) => row.map((cell) => cell.token));
  extras.sourceToken = sheet.cells[2][1].token;
}

writeFileSync(process.env.NESTED_RESULTS as string, JSON.stringify({ verdicts, extras }, null, 2));
`

type nestedVector struct {
	name    string
	typ     string
	payload string
	want    map[string][]string
}

// drawingVectors run against fixture-nested-arrays' validateDrawing.
var drawingVectors = []nestedVector{
	{
		name:    "ragged",
		typ:     "Drawing",
		payload: `{"labels": [["a", "b", "c"], ["d"]], "shades": [["light"], ["dark", "light", "dark"]], "polygons": [[{"x": 0, "y": 0}], [{"x": 1, "y": 1}, {"x": 2, "y": 2}]], "samples": [[1, 2, 3], [4]]}`,
		want:    map[string][]string{},
	},
	{
		name:    "empty_outer",
		typ:     "Drawing",
		payload: `{"labels": [], "shades": [], "polygons": [], "samples": []}`,
		want:    map[string][]string{},
	},
	{
		name:    "empty_inner",
		typ:     "Drawing",
		payload: `{"labels": [[]], "shades": [[], []], "polygons": [[]], "samples": [[]]}`,
		want:    map[string][]string{},
	},
	{
		name:    "optional_absent",
		typ:     "Drawing",
		payload: `{"labels": [], "shades": [], "polygons": []}`,
		want:    map[string][]string{},
	},
	{
		name:    "optional_null",
		typ:     "Drawing",
		payload: `{"labels": [], "shades": [], "polygons": [], "samples": null}`,
		want:    map[string][]string{},
	},
	{
		name:    "required_absent",
		typ:     "Drawing",
		payload: `{"shades": [], "polygons": []}`,
		want:    map[string][]string{"labels": {"required"}},
	},
	{
		name:    "null_inner_list",
		typ:     "Drawing",
		payload: `{"labels": [["a"], null], "shades": [null], "polygons": [[], null], "samples": [null]}`,
		want: map[string][]string{
			"labels[1]":   {"required"},
			"shades[0]":   {"required"},
			"polygons[1]": {"required"},
			"samples[0]":  {"required"},
		},
	},
	{
		name:    "inner_not_a_list",
		typ:     "Drawing",
		payload: `{"labels": ["a"], "shades": [["light"], "dark"], "polygons": []}`,
		want: map[string][]string{
			"labels[0]": {"required"},
			"shades[1]": {"required"},
		},
	},
	{
		name:    "bad_element",
		typ:     "Drawing",
		payload: `{"labels": [], "shades": [["light"], ["dark", "purple"]], "polygons": []}`,
		want:    map[string][]string{"shades[1][1]": {"enum"}},
	},
	{
		// listMax 64 bounds the outer list only: 65 rows fail, one row of
		// 100 samples passes.
		name:    "outer_list_bound",
		typ:     "Drawing",
		payload: `{"labels": [], "shades": [], "polygons": [], "samples": [` + strings.TrimSuffix(strings.Repeat(`[1],`, 65), ",") + `]}`,
		want:    map[string][]string{"samples": {"listMax"}},
	},
	{
		name:    "inner_list_unbounded",
		typ:     "Drawing",
		payload: `{"labels": [], "shades": [], "polygons": [], "samples": [[` + strings.TrimSuffix(strings.Repeat(`1,`, 100), ",") + `]]}`,
		want:    map[string][]string{},
	},
}

const validSheet = `"cells": [[{"label": "a", "token": "t"}], []], "ids": [["0Wk2bUe4TBlrdAhXhGAEJ1"], []]`

// sheetVectors run against nested-arrays-edges' validateSheet.
var sheetVectors = []nestedVector{
	{
		name:    "sheet_valid",
		typ:     "Sheet",
		payload: `{` + validSheet + `, "stamps": [["2026-01-02T03:04:05Z"]], "tones": [["warm"], []], "codes": [["ab", "c"]], "weights": [[0, 0.5], [1]]}`,
		want:    map[string][]string{},
	},
	{
		name:    "sheet_bad_scalar_element",
		typ:     "Sheet",
		payload: `{"cells": [], "ids": [[], ["0Wk2bUe4TBlrdAhXhGAEJ1", "not a uuid!"]]}`,
		want:    map[string][]string{"ids[1][1]": {"pattern"}},
	},
	{
		name:    "sheet_imported_enum_element",
		typ:     "Sheet",
		payload: `{` + validSheet + `, "tones": [["warm", "cool"], ["hot"]]}`,
		want:    map[string][]string{"tones[1][0]": {"enum"}},
	},
	{
		name:    "sheet_strict_nested_object",
		typ:     "Sheet",
		payload: `{"cells": [[{"label": "a", "token": "t"}], [{"label": "b", "token": "t", "extra": 1}]], "ids": []}`,
		want:    map[string][]string{"cells[1][0]": {"object"}},
	},
	{
		name:    "sheet_null_inner_list",
		typ:     "Sheet",
		payload: `{"cells": [null], "ids": [[], null]}`,
		want:    map[string][]string{"cells[0]": {"required"}, "ids[1]": {"required"}},
	},
	{
		name:    "sheet_element_rules",
		typ:     "Sheet",
		payload: `{` + validSheet + `, "codes": [["abcd"], ["ok", "A1"]], "weights": [[0.5], [-1, 2]]}`,
		want: map[string][]string{
			"codes[0][0]":   {"maxLength"},
			"codes[1][1]":   {"pattern"},
			"weights[1][0]": {"min"},
			"weights[1][1]": {"max"},
		},
	},
	{
		// listMin 1 and listMax 2 bound the outer list; inner lists of any
		// length pass.
		name:    "sheet_outer_bounds",
		typ:     "Sheet",
		payload: `{` + validSheet + `, "codes": [["a", "b", "c", "d", "e"], ["f"], ["g"]]}`,
		want:    map[string][]string{"codes": {"listMax"}},
	},
	{
		name:    "sheet_outer_empty",
		typ:     "Sheet",
		payload: `{` + validSheet + `, "codes": []}`,
		want:    map[string][]string{"codes": {"listMin"}},
	},
}

// TestNestedArraysValidatorBehavior runs the generated validators, JSON
// parser and mask helper for T[][] fields under bun: ragged, empty outer
// and empty inner lists pass, a null inner list is rejected at field[i],
// and a bad element is reported at field[i][j].
func TestNestedArraysValidatorBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-validator check in -short mode")
	}

	drawing, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays"))
	if err != nil {
		t.Fatalf("load fixture-nested-arrays: %v", err)
	}
	cases := append([]tsPackageCase{{name: "fixture-nested-arrays", schema: drawing}}, loadNestedArraysEdges(t)...)
	tempRoot, bunPath := buildTSPackages(t, cases)
	if t.Failed() {
		return
	}

	run := func(pkg string, vectors []nestedVector) (map[string]map[string][]string, map[string]json.RawMessage) {
		t.Helper()
		dir := filepath.Join(tempRoot, pkg)
		payloads := map[string]any{}
		for _, v := range vectors {
			payloads[v.name] = map[string]any{"type": v.typ, "payload": json.RawMessage(v.payload)}
		}
		data, err := json.Marshal(payloads)
		if err != nil {
			t.Fatalf("marshal vectors: %v", err)
		}
		vectorsPath := filepath.Join(dir, "vectors.json")
		resultsPath := filepath.Join(dir, "results.json")
		if err := os.WriteFile(vectorsPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nested_driver.ts"), []byte(nestedArraysDriver), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bunPath, "run", "nested_driver.ts")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "NESTED_VECTORS="+vectorsPath, "NESTED_RESULTS="+resultsPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s driver failed: %v\n%s", pkg, err, out)
		}
		raw, err := os.ReadFile(resultsPath)
		if err != nil {
			t.Fatalf("read results: %v", err)
		}
		var results struct {
			Verdicts map[string]map[string][]string `json:"verdicts"`
			Extras   map[string]json.RawMessage     `json:"extras"`
		}
		if err := json.Unmarshal(raw, &results); err != nil {
			t.Fatalf("decode results: %v", err)
		}
		for _, v := range vectors {
			got := results.Verdicts[v.name]
			if got == nil {
				got = map[string][]string{}
			}
			for _, validators := range got {
				sort.Strings(validators)
			}
			if !reflect.DeepEqual(got, v.want) {
				t.Errorf("%s: vector %s\n  payload: %s\n  want: %v\n  got:  %v", pkg, v.name, v.payload, v.want, got)
			}
		}
		return results.Verdicts, results.Extras
	}

	run("fixture-nested-arrays", drawingVectors)
	_, extras := run(nestedArraysEdgesService, sheetVectors)

	wantExtras := map[string]string{
		"stampIsDate":  `true`,
		"cellLabel":    `"c"`,
		"maskedTokens": `[[""],[],["",""]]`,
		"sourceToken":  `"s3"`,
	}
	for key, want := range wantExtras {
		var compact bytes.Buffer
		if err := json.Compact(&compact, extras[key]); err != nil {
			t.Errorf("extra %s: %v (raw %s)", key, err, extras[key])
			continue
		}
		if compact.String() != want {
			t.Errorf("extra %s = %s, want %s", key, compact.String(), want)
		}
	}
}

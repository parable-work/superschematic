package tsgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

const primitiveTypesService = "primitive-types"

// Every shape a builtin primitive field takes: single, T[], T[][], a map
// value and a map value's list element, with the rules each primitive has.
// The schema runtimes do not walk maps, so the parity matrix has no map
// fields; this schema covers the map shapes.
const primitiveTypesSchemaJSON = `{
  "types": {
    "Sample": {
      "name": "Sample",
      "role": "EmbeddedStruct",
      "fields": [
        { "name": "name", "typeRef": { "name": "string" }, "required": true, "validateMaxLength": 5, "validatePattern": "^[a-z]+$" },
        { "name": "count", "typeRef": { "name": "number" }, "validateMin": 1, "validateMax": 10 },
        { "name": "flag", "typeRef": { "name": "boolean" } },
        { "name": "tags", "typeRef": { "name": "string", "isArray": true }, "validateMaxLength": 3 },
        { "name": "scores", "typeRef": { "name": "number", "isArray": true, "isArrayOfArrays": true }, "validateMin": 0, "validateMax": 1 },
        { "name": "labels", "typeRef": { "name": "string", "isMap": true }, "validateMaxLength": 3 },
        { "name": "limits", "typeRef": { "name": "number", "isArray": true, "isMap": true }, "validateMax": 10 },
        { "name": "switches", "typeRef": { "name": "boolean", "isMap": true } }
      ]
    }
  }
}`

const primitiveTypesDriver = `import { readFileSync, writeFileSync } from 'node:fs';
import { validateSample } from './validators/types/sample';

const vectors = JSON.parse(readFileSync(process.env.PRIMITIVE_VECTORS as string, 'utf8')) as Record<string, unknown>;
const results: Record<string, Record<string, string[]>> = {};
for (const [name, payload] of Object.entries(vectors)) {
  const result = validateSample(payload as never);
  const fields: Record<string, string[]> = {};
  if (result !== true) {
    for (const [path, errs] of Object.entries(result)) {
      fields[path] = (errs as { validator: string }[]).map((e) => e.validator).sort();
    }
  }
  results[name] = fields;
}
writeFileSync(process.env.PRIMITIVE_RESULTS as string, JSON.stringify(results, null, 2));
`

// TestPrimitiveFieldTypes type-checks and runs validateSample: a value of
// the wrong JSON type in a builtin primitive field is one "type" error at
// its path, and the field's length, pattern and range rules do not check
// it. A missing required string is "required" alone.
func TestPrimitiveFieldTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-validator check in -short mode")
	}
	dir := t.TempDir()
	for rel, contents := range map[string]string{
		"schema.config.json":     `{"name": "` + primitiveTypesService + `", "kind": "General", "outputs": {"types": {"typescript": {"enabled": true}}}}`,
		"src/sample.schema.json": primitiveTypesSchemaJSON,
	} {
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
		t.Fatalf("load: %v", err)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{name: primitiveTypesService, schema: schema}})
	if t.Failed() {
		return
	}

	vectors := map[string]struct {
		payload string
		want    map[string][]string
	}{
		"valid": {
			payload: `{"name": "abc", "count": 5, "flag": false, "tags": ["a"], "scores": [[0, 0.5], []],
				"labels": {"k": "abc"}, "limits": {"k": [1, 10]}, "switches": {"k": true}}`,
			want: map[string][]string{},
		},
		"absent_and_null": {
			payload: `{"name": "abc", "count": null, "flag": null, "labels": {"k": null}, "switches": {"k": null}}`,
			want:    map[string][]string{},
		},
		"required_string_absent": {
			payload: `{}`,
			want:    map[string][]string{"name": {"required"}},
		},
		"rules_on_the_right_type": {
			payload: `{"name": "ABCDEF", "count": 11, "tags": ["long"], "scores": [[-1]], "labels": {"k": "long"}, "limits": {"k": [11]}}`,
			want: map[string][]string{
				"name":         {"maxLength", "pattern"},
				"count":        {"max"},
				"tags[0]":      {"maxLength"},
				"scores[0][0]": {"min"},
				"labels.k":     {"maxLength"},
				"limits.k[0]":  {"max"},
			},
		},
		"wrong_types": {
			payload: `{"name": 1234567, "count": "50", "flag": "true", "tags": ["a", 1234], "scores": [[0.5, "2"]],
				"labels": {"k": 12345}, "limits": {"k": [1, "20"]}, "switches": {"k": "false"}}`,
			want: map[string][]string{
				"name":         {"type"},
				"count":        {"type"},
				"flag":         {"type"},
				"tags[1]":      {"type"},
				"scores[0][1]": {"type"},
				"labels.k":     {"type"},
				"limits.k[1]":  {"type"},
				"switches.k":   {"type"},
			},
		},
		"null_element": {
			payload: `{"name": "abc", "tags": [null], "scores": [[null]]}`,
			want:    map[string][]string{"tags[0]": {"required"}, "scores[0][0]": {"required"}},
		},
	}
	payloads := map[string]json.RawMessage{}
	for name, v := range vectors {
		payloads[name] = json.RawMessage(v.payload)
	}
	data, err := json.Marshal(payloads)
	if err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(typesRoot, primitiveTypesService)
	vectorsPath := filepath.Join(pkgDir, "vectors.json")
	resultsPath := filepath.Join(pkgDir, "results.json")
	if err := os.WriteFile(vectorsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "primitive_driver.ts"), []byte(primitiveTypesDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bunPath, "run", "primitive_driver.ts")
	cmd.Dir = pkgDir
	cmd.Env = append(os.Environ(), "PRIMITIVE_VECTORS="+vectorsPath, "PRIMITIVE_RESULTS="+resultsPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("driver failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	var results map[string]map[string][]string
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	for name, v := range vectors {
		got := results[name]
		if got == nil {
			got = map[string][]string{}
		}
		if !reflect.DeepEqual(got, v.want) {
			t.Errorf("vector %s\n  payload: %s\n  want: %v\n  got:  %v", name, v.payload, v.want, got)
		}
	}
}

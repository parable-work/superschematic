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

const nestedObjectsService = "nested-objects"

const nestedObjectsSchemaJSON = `{
  "types": {
    "Leaf": {
      "name": "Leaf",
      "role": "EmbeddedStruct",
      "fields": [
        { "name": "name", "typeRef": { "name": "string" }, "required": true },
        { "name": "code", "typeRef": { "name": "string" }, "validateMaxLength": 3 }
      ]
    },
    "Node": {
      "name": "Node",
      "role": "EmbeddedStruct",
      "fields": [
        { "name": "label", "typeRef": { "name": "string" }, "required": true },
        { "name": "child", "typeRef": { "name": "Node" } },
        { "name": "children", "typeRef": { "name": "Node", "isArray": true } },
        { "name": "leaf", "typeRef": { "name": "Leaf" } },
        { "name": "leafGrid", "typeRef": { "name": "Leaf", "isArray": true, "isArrayOfArrays": true } },
        { "name": "leafMap", "typeRef": { "name": "Leaf", "isMap": true } }
      ]
    }
  }
}`

// nestedObjectsDriver validates each payload as a Node and flattens nested
// errors to dotted paths with sorted validator names.
const nestedObjectsDriver = `import { readFileSync, writeFileSync } from 'node:fs';
import { validateNode } from './validators/types/node';

type Errors = { [key: string]: { validator: string }[] | Errors };

function flatten(errors: Errors, prefix: string, out: Record<string, string[]>): void {
  for (const [key, value] of Object.entries(errors)) {
    const path = prefix ? prefix + '.' + key : key;
    if (Array.isArray(value)) {
      out[path] = value.map((e) => e.validator).sort();
    } else {
      flatten(value, path, out);
    }
  }
}

const vectors = JSON.parse(readFileSync(process.env.NESTED_VECTORS as string, 'utf8')) as Record<string, unknown>;
const results: Record<string, Record<string, string[]>> = {};
for (const [name, payload] of Object.entries(vectors)) {
  const result = validateNode(payload as never);
  const fields: Record<string, string[]> = {};
  if (result !== true) {
    flatten(result as Errors, '', fields);
  }
  results[name] = fields;
}
writeFileSync(process.env.NESTED_RESULTS as string, JSON.stringify(results, null, 2));
`

// TestNestedObjectsValidatedForEveryType runs validateNode for a type that
// is not @strictJSON: a nested object is validated as its own type, as a
// field, a list or list-of-lists element, a map value and through the type
// itself, with its errors at dotted paths. A null list element is
// "required"; any other value that is not an object is left alone, as the
// schema runtimes leave it.
func TestNestedObjectsValidatedForEveryType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-validator check in -short mode")
	}
	dir := t.TempDir()
	for rel, contents := range map[string]string{
		"schema.config.json":   `{"name": "` + nestedObjectsService + `", "kind": "General", "outputs": {"types": {"typescript": {"enabled": true}}}}`,
		"src/node.schema.json": nestedObjectsSchemaJSON,
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
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{name: nestedObjectsService, schema: schema}})
	if t.Failed() {
		return
	}

	vectors := map[string]struct {
		payload string
		want    map[string][]string
	}{
		"valid": {
			payload: `{"label": "a", "child": {"label": "b"}, "children": [{"label": "c"}], "leaf": {"name": "x"},
				"leafGrid": [[{"name": "y"}], []], "leafMap": {"k": {"name": "z"}}}`,
			want: map[string][]string{},
		},
		"self_field": {
			payload: `{"label": "a", "child": {"label": "b", "child": {}}}`,
			want:    map[string][]string{"child.child.label": {"required"}},
		},
		"self_list_element": {
			payload: `{"label": "a", "children": [{"label": "b"}, {"leaf": {"name": "x", "code": "long"}}]}`,
			want:    map[string][]string{"children[1].label": {"required"}, "children[1].leaf.code": {"maxLength"}},
		},
		"field": {
			payload: `{"label": "a", "leaf": {}}`,
			want:    map[string][]string{"leaf.name": {"required"}},
		},
		"grid_element": {
			payload: `{"label": "a", "leafGrid": [[{"name": "ok"}, {"name": "x", "code": "long"}]]}`,
			want:    map[string][]string{"leafGrid[0][1].code": {"maxLength"}},
		},
		"map_value": {
			payload: `{"label": "a", "leafMap": {"k": {}}}`,
			want:    map[string][]string{"leafMap.k.name": {"required"}},
		},
		"null_element": {
			payload: `{"label": "a", "children": [null, {"label": "b"}], "leafGrid": [[{"name": "x"}, null]]}`,
			want:    map[string][]string{"children[0]": {"required"}, "leafGrid[0][1]": {"required"}},
		},
		"not_an_object": {
			payload: `{"label": "a", "leaf": "text", "children": [7]}`,
			want:    map[string][]string{},
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
	pkgDir := filepath.Join(typesRoot, nestedObjectsService)
	vectorsPath := filepath.Join(pkgDir, "vectors.json")
	resultsPath := filepath.Join(pkgDir, "results.json")
	if err := os.WriteFile(vectorsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "nested_objects_driver.ts"), []byte(nestedObjectsDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bunPath, "run", "nested_objects_driver.ts")
	cmd.Dir = pkgDir
	cmd.Env = append(os.Environ(), "NESTED_VECTORS="+vectorsPath, "NESTED_RESULTS="+resultsPath)
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

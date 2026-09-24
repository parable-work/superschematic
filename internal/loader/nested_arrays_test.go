package loader

import (
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

const nestedArraysJSON = `{
	"enums": {
		"Shade": {"name": "Shade", "values": [{"name": "Light", "serializedAs": "light"}, {"name": "Dark", "serializedAs": "dark"}]}
	},
	"types": {
		"Point": {
			"name": "Point",
			"role": "EmbeddedStruct",
			"fields": [
				{"name": "x", "typeRef": {"name": "number"}, "required": true},
				{"name": "y", "typeRef": {"name": "number"}, "required": true}
			]
		},
		"Drawing": {
			"name": "Drawing",
			"role": "EmbeddedStruct",
			"fields": [
				{"name": "grid", "typeRef": {"name": "number", "isArray": true, "isArrayOfArrays": true}, "required": true},
				{"name": "shades", "typeRef": {"name": "Shade", "isArray": true, "isArrayOfArrays": true}, "required": true},
				{"name": "polygons", "typeRef": {"name": "Point", "isArray": true, "isArrayOfArrays": true}}
			]
		}
	}
}`

const nestedArraysYAML = `enums:
  Shade:
    name: Shade
    values:
      - name: Light
        serializedAs: light
      - name: Dark
        serializedAs: dark
types:
  Point:
    name: Point
    role: EmbeddedStruct
    fields:
      - name: x
        typeRef: { name: number }
        required: true
      - name: y
        typeRef: { name: number }
        required: true
  Drawing:
    name: Drawing
    role: EmbeddedStruct
    fields:
      - name: grid
        typeRef: { name: number, isArray: true, isArrayOfArrays: true }
        required: true
      - name: shades
        typeRef: { name: Shade, isArray: true, isArrayOfArrays: true }
        required: true
      - name: polygons
        typeRef: { name: Point, isArray: true, isArrayOfArrays: true }
`

// TestLoadServiceArraysOfArraysDataForms: the JSON and YAML forms write
// T[][] as typeRef { name, isArray: true, isArrayOfArrays: true }.
func TestLoadServiceArraysOfArraysDataForms(t *testing.T) {
	for _, form := range []struct{ file, body string }{
		{"src/drawing.schema.json", nestedArraysJSON},
		{"src/drawing.schema.yaml", nestedArraysYAML},
	} {
		t.Run(filepath.Ext(form.file), func(t *testing.T) {
			dir := writeService(t, map[string]string{"schema.config.json": minimalConfig, form.file: form.body})
			schema, err := LoadService(dir)
			if err != nil {
				t.Fatalf("LoadService: %v", err)
			}
			drawing := schema.Types["Drawing"]
			for i, name := range []string{"number", "Shade", "Point"} {
				want := ir.TypeRef{Name: name, IsArray: true, IsArrayOfArrays: true}
				if got := drawing.Fields[i].TypeRef; got != want {
					t.Errorf("%s TypeRef = %+v, want %+v", drawing.Fields[i].Name, got, want)
				}
			}
			if drawing.Fields[2].Required {
				t.Error("polygons should be optional")
			}
		})
	}
}

// TestLoadServiceArraysOfArraysDataFormInvariants: the data forms have no
// compiler, so ir.Schema.Validate enforces the TypeRef invariants.
func TestLoadServiceArraysOfArraysDataFormInvariants(t *testing.T) {
	cases := []struct {
		typeRef string
		want    string
	}{
		{`{"name": "number", "isArrayOfArrays": true}`, "Drawing.grid: isArrayOfArrays requires isArray"},
		{`{"name": "number", "isArray": true, "isArrayOfArrays": true, "isMap": true}`, "Drawing.grid: a map value cannot be an array of arrays"},
	}
	for _, tc := range cases {
		dir := writeService(t, map[string]string{
			"schema.config.json": minimalConfig,
			"src/drawing.schema.json": `{"types": {"Drawing": {"name": "Drawing", "role": "EmbeddedStruct", "fields": [
				{"name": "grid", "typeRef": ` + tc.typeRef + `, "required": true}
			]}}}`,
		})
		_, err := LoadService(dir)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("typeRef %s: error = %v, want %q", tc.typeRef, err, tc.want)
		}
	}
}

// TestLoadServiceArraysOfArraysVerifyRuns: the context rules run for data
// forms through the loader, not only in unit tests of the verify pass.
func TestLoadServiceArraysOfArraysVerifyRuns(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/config.schema.json": `{"types": {"Config": {"name": "Config", "role": "EmbeddedStruct", "envVars": true, "fields": [
			{"name": "HOSTS", "typeRef": {"name": "string", "isArray": true, "isArrayOfArrays": true}, "required": true}
		]}}}`,
	})
	_, err := LoadService(dir)
	if err == nil || !strings.Contains(err.Error(), "Config.HOSTS: env config fields cannot be arrays of arrays") {
		t.Fatalf("error = %v", err)
	}
}

// TestLoadServiceCompositeDefaultArraysOfArrays: a platform default gives a
// T[][] field a list of lists. Inner lists may be empty and are never null.
func TestLoadServiceCompositeDefaultArraysOfArrays(t *testing.T) {
	schemaJSON := `{"types": {"GridDefaults": {"name": "GridDefaults", "role": "EmbeddedStruct", "fields": [
		{"name": "rows", "typeRef": {"name": "number", "isArray": true, "isArrayOfArrays": true}, "required": true, "validateListMax": 3}
	]}}}`
	service := func(value string) string {
		return writeService(t, map[string]string{
			"schema.config.json":                      minimalConfig,
			"src/grid.schema.json":                    schemaJSON,
			"src/grid-defaults.platform-default.json": `{"type": "GridDefaults", "value": ` + value + `}`,
		})
	}

	schema, err := LoadService(service(`{"rows": [[1, 2], [], [3]]}`))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if got, want := schema.CompositeDefaults["GridDefaults"].CanonicalJSON, `{"rows":[[1,2],[],[3]]}`; got != want {
		t.Fatalf("CanonicalJSON = %s, want %s", got, want)
	}

	for value, want := range map[string]string{
		`{"rows": [[1], null]}`:        "GridDefaults.rows[1] must be an array",
		`{"rows": [1]}`:                "GridDefaults.rows[0] must be an array",
		`{"rows": [[1, "2"]]}`:         "GridDefaults.rows[0][1] must be a number",
		`{"rows": [[], [], [], [1]]}`:  "GridDefaults.rows must contain at most 3 items",
		`{"rows": [[1, 2, 3, 4, 5]]}`:  "",
		`{"rows": [[[1]]]}`:            "GridDefaults.rows[0][0] must be a number",
		`{"rows": {"a": [1]}}`:         "GridDefaults.rows must be an array",
		`{"rows": [[1], [2], [3, 4]]}`: "",
	} {
		_, err := LoadService(service(value))
		switch {
		case want == "" && err != nil:
			t.Errorf("value %s: %v", value, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("value %s: error = %v, want %q", value, err, want)
		}
	}
}

// TestLoadServiceArraysOfArraysTypeScript: the TypeScript fixtures load
// through the full loader, verify pass included, and a nested query
// parameter is refused with the operation named.
func TestLoadServiceArraysOfArraysTypeScript(t *testing.T) {
	for _, svc := range []string{"fixture-nested-arrays", "fixture-nested-arrays-db", "fixture-nested-arrays-api"} {
		schema, err := LoadService(filepath.Join("tsreader", "testdata", "services", svc))
		if err != nil {
			t.Fatalf("LoadService(%s): %v", svc, err)
		}
		if _, found := schema.FindArrayOfArrays(); !found {
			t.Errorf("%s declares no array of arrays", svc)
		}
	}

	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-nested-arrays-query"))
	if err == nil || !strings.Contains(err.Error(), `GridQueries.listGrids: query parameter "rows" cannot be an array of arrays`) {
		t.Fatalf("error = %v", err)
	}
}

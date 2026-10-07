package typegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestGeneratedJSONObjectScalarsCheckTheirJSON builds a module whose type
// holds Geo.Location in every field shape, and a Generic.StringMap, against
// the real superscalar Go binding, and runs a test inside it. A JSON-object
// scalar's values are checked by superscalar wherever they appear; what
// encoding/json drops (an unknown, missing or duplicate key) is checked on
// the JSON at decode and reported by Validate under the core's name; a
// required Geo.Location the JSON left absent or null is "required", while
// {"lat":0,"lon":0}, decoded or built in Go, is a value.
func TestGeneratedJSONObjectScalarsCheckTheirJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module build in -short mode")
	}
	const service = "json-object-scalars"
	dir := filepath.Join(t.TempDir(), service)
	field := func(name, scalar string, required bool, shape map[string]any) map[string]any {
		typeRef := map[string]any{"name": scalar}
		for key, value := range shape {
			typeRef[key] = value
		}
		return map[string]any{"name": name, "typeRef": typeRef, "required": required}
	}
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    service,
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "places.schema.json"): map[string]any{
			"scalars": map[string]any{
				"Geo.Location":      map[string]any{"name": "Geo.Location", "languagePrimitive": "string"},
				"Generic.StringMap": map[string]any{"name": "Generic.StringMap", "languagePrimitive": "string"},
			},
			"types": map[string]any{
				"Place": map[string]any{
					"name": "Place",
					"role": "EmbeddedStruct",
					"fields": []any{
						field("at", "Geo.Location", true, nil),
						field("near", "Geo.Location", false, nil),
						field("route", "Geo.Location", false, map[string]any{"isArray": true}),
						field("grid", "Geo.Location", false, map[string]any{"isArray": true, "isArrayOfArrays": true}),
						field("named", "Geo.Location", false, map[string]any{"isMap": true}),
						field("tags", "Generic.StringMap", true, nil),
					},
				},
			},
		},
	}
	for rel, doc := range files {
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	output, err := Generate(schema, Options{
		SchemaName: service,
		ModulePath: "example.com/schemas/types/go/" + service,
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), service)
	if err := SetReplacePaths(output, testpaths.Local(t), outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	runtimeTest := strings.Replace(jsonObjectScalarsRuntimeTest, "PACKAGE", output.PackageName, 1)
	if err := os.WriteFile(filepath.Join(outDir, "json_object_runtime_test.go"), []byte(runtimeTest), 0o644); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = outDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	run := exec.Command("go", "test", "-count=1", "./...")
	run.Dir = outDir
	if out, err := run.CombinedOutput(); err != nil {
		t.Errorf("generated JSON-object scalar module failed: %v\n%s", err, out)
	}
}

// jsonObjectScalarsRuntimeTest runs inside the generated module; PACKAGE
// is its package name.
const jsonObjectScalarsRuntimeTest = `package PACKAGE

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// verdicts decodes payload into a Place and returns Validate's validator
// names by field path, or "$decode" when json.Unmarshal refuses it.
func verdicts(t *testing.T, payload string) map[string][]string {
	t.Helper()
	var place Place
	if err := json.Unmarshal([]byte(payload), &place); err != nil {
		return map[string][]string{"$decode": {"rejected"}}
	}
	got := map[string][]string{}
	for path, errs := range place.Validate() {
		for _, e := range errs.([]ValidationError) {
			got[path] = append(got[path], e.Validator)
		}
		sort.Strings(got[path])
	}
	return got
}

func TestJSONObjectScalars(t *testing.T) {
	const tags = ` + "`" + `"tags": {"k": "v"}` + "`" + `
	for _, tc := range []struct {
		payload string
		want    map[string][]string
	}{
		{` + "`" + `{"at": {"lat": 37.7749, "lon": -122.4194}, ` + "`" + ` + tags + "}", map[string][]string{}},
		{` + "`" + `{"at": {"lon": 0, "lat": 0}, ` + "`" + ` + tags + "}", map[string][]string{}},
		{"{" + tags + "}", map[string][]string{"at": {"required"}}},
		{` + "`" + `{"at": null, ` + "`" + ` + tags + "}", map[string][]string{"at": {"required"}}},
		{` + "`" + `{"at": {"lat": 0, "lon": 0}}` + "`" + `, map[string][]string{"tags": {"required"}}},
		{` + "`" + `{"at": {"lat": 91, "lon": 0}, ` + "`" + ` + tags + "}", map[string][]string{"at": {"range"}}},
		{` + "`" + `{"at": {"lat": 1, "lon": 2, "alt": 3}, ` + "`" + ` + tags + "}", map[string][]string{"at": {"custom"}}},
		{` + "`" + `{"at": {"lat": 1}, ` + "`" + ` + tags + "}", map[string][]string{"at": {"custom"}}},
		{` + "`" + `{"at": {"lat": 1, "lat": 2, "lon": 3}, ` + "`" + ` + tags + "}", map[string][]string{"at": {"custom"}}},
		{` + "`" + `{"at": {"lat": "1", "lon": 2}, ` + "`" + ` + tags + "}", map[string][]string{"$decode": {"rejected"}}},
		{` + "`" + `{"at": "37.7749,-122.4194", ` + "`" + ` + tags + "}", map[string][]string{"$decode": {"rejected"}}},
		{` + "`" + `{"at": {"lat": 0, "lon": 0}, "near": {"lat": 1, "lon": 2, "x": 1}, "route": [{"lat": 0, "lon": 0}, {"lon": 5}, {"lat": 0, "lon": 181}], "grid": [[{"lat": -90.5, "lon": 0}], [{"lat": 1, "lon": 1, "z": 0}]], "named": {"home": {"lat": 1}, "work": {"lat": 1, "lon": 1}}, ` + "`" + ` + tags + "}", map[string][]string{
			"near":       {"custom"},
			"route[1]":   {"custom"},
			"route[2]":   {"range"},
			"grid[0][0]": {"range"},
			"grid[1][0]": {"custom"},
			"named[home]": {"custom"},
		}},
	} {
		if got := verdicts(t, tc.payload); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s\n  got  %v\n  want %v", tc.payload, got, tc.want)
		}
	}
}

func TestJSONObjectDecodeRecordFollowsTheField(t *testing.T) {
	// A field set after decoding is checked as set.
	var place Place
	if err := json.Unmarshal([]byte(` + "`" + `{"at": {"lat": 1, "lon": 2, "alt": 3}, "tags": {}}` + "`" + `), &place); err != nil {
		t.Fatal(err)
	}
	place.At = GeoLocation{Lat: 5, Lon: 6}
	if errs := place.Validate(); errs.HasErrors() {
		t.Errorf("a location set after decoding kept the decoded JSON's verdict: %v", errs)
	}
	var missing Place
	if err := json.Unmarshal([]byte(` + "`" + `{"tags": {}}` + "`" + `), &missing); err != nil {
		t.Fatal(err)
	}
	missing.At = GeoLocation{Lat: 5, Lon: 6}
	if errs := missing.Validate(); errs.HasErrors() {
		t.Errorf("a location set after an absent one was decoded is still missing: %v", errs)
	}

	// {0, 0} built in Go is a value, out of range is "range", and a decoded
	// valid value equals the one built in Go.
	built := Place{At: GeoLocation{Lat: 0, Lon: 0}, Tags: GenericStringMap{}}
	if errs := built.Validate(); errs.HasErrors() {
		t.Errorf("a {0, 0} location built in Go was refused: %v", errs)
	}
	outOfRange := Place{At: GeoLocation{Lat: 0, Lon: 200}, Tags: GenericStringMap{}}
	if errs := outOfRange.Validate(); len(errs.GetFieldErrors("at")) != 1 || errs.GetFieldErrors("at")[0].Validator != "range" {
		t.Errorf("an out-of-range location built in Go: %v", errs)
	}
	var decoded Place
	if err := json.Unmarshal([]byte(` + "`" + `{"at": {"lat": 0, "lon": 0}, "tags": {}}` + "`" + `), &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, built) {
		t.Errorf("a decoded location differs from the same one built in Go: %#v vs %#v", decoded, built)
	}
	encoded, err := json.Marshal(&decoded)
	if err != nil || string(encoded) != ` + "`" + `{"at":{"lat":0,"lon":0},"tags":{}}` + "`" + ` {
		t.Errorf("Place marshals as %s (%v)", encoded, err)
	}
}
`

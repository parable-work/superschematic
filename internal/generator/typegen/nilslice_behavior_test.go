package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// optionalListBehaviorTest runs inside the generated fixture-general module
// and pins decode/validate semantics for optional (Nullable) list fields with
// list constraints: an absent or null list must stay nil and skip listMin,
// while an explicit [] must stay non-nil and fail it. This matches the
// generated TypeScript and Python validators. Regression test for the
// normalizeNilSlices bug that turned absent optional lists into empty non-nil
// slices, making every decode-then-validate of an omitted list fail listMin.
// Encoding keeps the same distinction: nil is left out and [] is written.
// It also pins that normalizeNilSlices leaves non-wire fields alone.
const optionalListBehaviorTest = `package types

import (
	"encoding/json"
	"testing"
)

func TestOptionalListDecodeValidate(t *testing.T) {
	cases := []struct {
		name         string
		payload      string
		wantNil      bool
		wantListMin  bool
		wantMarshalKey bool
	}{
		{name: "absent", payload: ` + "`" + `{"kind": "x"}` + "`" + `, wantNil: true, wantListMin: false, wantMarshalKey: false},
		{name: "explicit null", payload: ` + "`" + `{"kind": "x", "values": null}` + "`" + `, wantNil: true, wantListMin: false, wantMarshalKey: false},
		// omitzero leaves out only a nil list, so an explicit [] is written back.
		{name: "explicit empty", payload: ` + "`" + `{"kind": "x", "values": []}` + "`" + `, wantNil: false, wantListMin: true, wantMarshalKey: true},
		{name: "one item", payload: ` + "`" + `{"kind": "x", "values": ["a"]}` + "`" + `, wantNil: false, wantListMin: false, wantMarshalKey: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f FixtureFilter
			if err := json.Unmarshal([]byte(tc.payload), &f); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if gotNil := f.Values == nil; gotNil != tc.wantNil {
				t.Errorf("Values nil-ness = %v, want %v", gotNil, tc.wantNil)
			}
			errs := f.Validate()
			gotListMin := false
			for _, fe := range errs.GetFieldErrors("values") {
				if fe.Validator == "listMin" {
					gotListMin = true
				}
			}
			if gotListMin != tc.wantListMin {
				t.Errorf("listMin error = %v, want %v (errors: %v)", gotListMin, tc.wantListMin, errs)
			}

			out, err := json.Marshal(&f)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var round map[string]any
			if err := json.Unmarshal(out, &round); err != nil {
				t.Fatalf("re-decode marshaled output: %v", err)
			}
			if _, present := round["values"]; present != tc.wantMarshalKey {
				t.Errorf("marshaled values key present = %v, want %v (out: %s)", present, tc.wantMarshalKey, out)
			}
			var decoded FixtureFilter
			if err := json.Unmarshal(out, &decoded); err != nil {
				t.Fatalf("decode marshaled output: %v", err)
			}
			if (decoded.Values == nil) != tc.wantNil {
				t.Errorf("round trip changed list presence (out: %s)", out)
			}
		})
	}
}

type listMetadata struct {
	Imports []string ` + "`" + `json:"-"` + "`" + `
	Wire    []string ` + "`" + `json:"wire"` + "`" + `
}

// normalizeNilSlices fills only list fields that go on the wire. A field
// tagged json:"-" and an unexported field are left nil, at any depth; a
// field named "-" (json:"-,") is on the wire.
func TestNormalizeNilSlicesSkipsNonWireFields(t *testing.T) {
	value := struct {
		Meta     listMetadata
		Required []string
		Ignored  []string ` + "`" + `json:"-"` + "`" + `
		Dash     []string ` + "`" + `json:"-,"` + "`" + `
		private  []string
		hidden   *listMetadata
	}{hidden: &listMetadata{}}
	normalizeNilSlices(&value)
	if value.Required == nil || value.Dash == nil || value.Meta.Wire == nil {
		t.Fatal("a nil wire list was not filled")
	}
	if value.Ignored != nil || value.Meta.Imports != nil {
		t.Fatal("a json:\"-\" list was filled")
	}
	if value.private != nil || value.hidden.Wire != nil || value.hidden.Imports != nil {
		t.Fatal("an unexported field was filled")
	}
}

// A required list without listMin: absent and null stay nil and fail
// required, an explicit [] stays non-nil and is valid, through every decoder.
func TestRequiredReplacementDecodeValidate(t *testing.T) {
	cases := []struct {
		payload string
		wantNil bool
	}{
		{payload: ` + "`" + `{"expectedAggregateRevision": 1}` + "`" + `, wantNil: true},
		{payload: ` + "`" + `{"expectedAggregateRevision": 1, "assignments": null}` + "`" + `, wantNil: true},
		{payload: ` + "`" + `{"expectedAggregateRevision": 1, "assignments": []}` + "`" + `, wantNil: false},
		{payload: ` + "`" + `{"expectedAggregateRevision": 1, "assignments": [{"kind": "owner"}]}` + "`" + `, wantNil: false},
	}
	for _, decoder := range []string{"json", "map", "strict-map"} {
		for _, tc := range cases {
			t.Run(decoder+tc.payload, func(t *testing.T) {
				var input RequiredReplacement
				var err error
				if decoder == "json" {
					err = json.Unmarshal([]byte(tc.payload), &input)
				} else {
					var value map[string]any
					if err := json.Unmarshal([]byte(tc.payload), &value); err != nil {
						t.Fatal(err)
					}
					if decoder == "map" {
						err = input.FromMap(value)
					} else {
						err = input.FromMapStrict(value)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if (input.Assignments == nil) != tc.wantNil {
					t.Fatalf("assignments presence lost: %#v", input.Assignments)
				}
				errs := input.Validate().GetFieldErrors("assignments")
				if tc.wantNil {
					if len(errs) != 1 || errs[0].Validator != "required" {
						t.Fatalf("want required, got %v", errs)
					}
				} else if len(errs) != 0 {
					t.Fatalf("explicit replacement rejected: %v", errs)
				}
			})
		}
	}
}
`

// TestOptionalListRuntimeBehavior generates the fixture-general module, drops
// a behavior test into it, and runs go test there. Same setup shape as
// TestGeneratedModulesCompile.
func TestOptionalListRuntimeBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}

	paths := testpaths.Local(t)

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}

	required, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-required-arrays"))
	if err != nil {
		t.Fatalf("load fixture-required-arrays: %v", err)
	}
	for name, definition := range required.Types {
		schema.Types[name] = definition
	}
	assignments := schema.Types["RequiredReplacement"].Fields[1]
	if assignments.Name != "assignments" || !assignments.Required || !assignments.TypeRef.IsArray || assignments.ValidateListMin != nil {
		t.Fatalf("loader lost the plain required-array contract: %+v", assignments)
	}

	output, err := Generate(schema, Options{
		SchemaName: "fixture-general",
		ModulePath: "example.com/schemas/types/go/fixture-general",
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate fixture-general: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "fixture-general")
	if err := SetReplacePaths(output, paths, outDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write fixture-general: %v", err)
	}

	if err := os.WriteFile(filepath.Join(outDir, "behavior_test.go"), []byte(optionalListBehaviorTest), 0o644); err != nil {
		t.Fatalf("write behavior test: %v", err)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = outDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}

	run := exec.Command("go", "test", "./...")
	run.Dir = outDir
	if out, err := run.CombinedOutput(); err != nil {
		t.Errorf("generated module behavior test failed: %v\n%s", err, out)
	}
}

package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

// optionalListBehaviorTest runs inside the generated fixture-general module
// and pins decode/validate semantics for optional (Nullable) list fields with
// list constraints: an absent or null list must stay nil and skip listMin,
// while an explicit [] must stay non-nil and fail it. This matches the
// generated TypeScript and Python validators. Regression test for the
// normalizeNilSlices bug that turned absent optional lists into empty non-nil
// slices, making every decode-then-validate of an omitted list fail listMin.
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
		// omitempty drops the empty list on marshal even though it decodes non-nil.
		{name: "explicit empty", payload: ` + "`" + `{"kind": "x", "values": []}` + "`" + `, wantNil: false, wantListMin: true, wantMarshalKey: false},
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
		})
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

	scalarLib, err := filepath.Abs("../../../../parable-scalars")
	if err != nil {
		t.Fatalf("resolve scalar-lib path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scalarLib, "go")); err != nil {
		t.Skipf("scalar-lib runtime not available: %v", err)
	}

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}

	output, err := Generate(schema, Options{
		SchemaName: "fixture-general",
		ModulePath: "github.com/parable-platform/platform-schemas/types/go/fixture-general",
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate fixture-general: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "fixture-general")
	if err := SetReplacePaths(output, scalarLib, outDir); err != nil {
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

package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestGeneratedVariantFieldDecodes generates fixture-variant-field-db's
// module, drops a test into it and runs it (D149): Step decodes its result
// as the member its kind names, though a review result that also holds
// passed is a VerifyResult by its shape; a note decodes none; a result
// while the kind names no member is kept as it was read, encodes back
// unchanged and is "variant" to Validate; and a Step built in Go whose
// result is another member than its kind names is "variant" too.
func TestGeneratedVariantFieldDecodes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-variant-field-db"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-variant-field-db", ModulePath: "example.com/schemas/types/go/fixture-variant-field-db"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "fixture-variant-field-db")
	if err := SetReplacePaths(output, testpaths.Local(t), dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	const code = `package types

import (
	"encoding/json"
	"testing"
)

func decode(t *testing.T, text string) Step {
	t.Helper()
	var step Step
	if err := json.Unmarshal([]byte(text), &step); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}
	return step
}

func rules(step Step) []string {
	var names []string
	for _, e := range step.Validate().GetFieldErrors("result") {
		names = append(names, e.Validator)
	}
	return names
}

func TestVariantField(t *testing.T) {
	review := decode(t, ` + "`" + `{"title":"t","kind":"review","result":{"verdict":"approved","passed":true}}` + "`" + `)
	if result, ok := review.Result.(ReviewResult); !ok || result.Verdict != Verdict_Approved {
		t.Fatalf("a review step's result = %#v, want its ReviewResult", review.Result)
	}
	if verify := decode(t, ` + "`" + `{"title":"t","kind":"verify","result":{"passed":true}}` + "`" + `); verify.Result == nil {
		t.Fatal("a verify step's result is missing")
	} else if _, ok := verify.Result.(VerifyResult); !ok {
		t.Fatalf("a verify step's result = %#v", verify.Result)
	}
	if note := decode(t, ` + "`" + `{"title":"t","kind":"note"}` + "`" + `); note.Result != nil || len(rules(note)) != 0 {
		t.Fatalf("a note = %#v, %v", note.Result, rules(note))
	}

	const stray = ` + "`" + `{"title":"t","kind":"note","result":{"passed":true}}` + "`" + `
	step := decode(t, stray)
	if got := rules(step); len(got) != 1 || got[0] != "variant" {
		t.Fatalf("a note with a result: %v, want variant", got)
	}
	encoded, err := json.Marshal(&step)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal(encoded, &got)
	_ = json.Unmarshal([]byte(stray), &want)
	if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
		t.Fatalf("a note with a result encodes as %s", encoded)
	}

	built := Step{Title: "t", Kind: StepKind_Review, Result: VerifyResult{Passed: true}}
	if got := rules(built); len(got) != 1 || got[0] != "variant" {
		t.Fatalf("a review step holding a VerifyResult: %v, want variant", got)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
`
	if err := os.WriteFile(filepath.Join(dir, "variant_field_test.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	command := exec.Command("go", "test", "-count=1", ".")
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated variant field tests failed: %v\n%s", err, out)
	}
}

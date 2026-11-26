package rustgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedVariantFieldDecodesTheMemberItsSiblingNames builds the crate
// of fixture-variant-field-db and runs a test in it (D149): Step reads its
// result through StepWire as the member its kind names, though a review
// result that also holds passed reads as a VerifyResult by its shape alone;
// a note reads none; a result that does not decode as the member its kind
// names is a decode error; a result while the kind names no member is read
// as its shape picks it, which validate_step refuses as "variant"; and a
// step encodes as it was read. The API's crate, built beside it, reads its
// view through the database's union and checks its input with the
// database's validate_step_result_by.
func TestGeneratedVariantFieldDecodesTheMemberItsSiblingNames(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compiled variant check in short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled variant check")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-variant-field-db"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-variant-field-db",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	root := testpaths.TempDir(t)
	outDir := filepath.Join(root, "fixture-variant-field-db")
	if err := SetLocalPaths(output, testpaths.Local(t), outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	// The API's crate, beside the database's, which its views' union and
	// validators come from.
	api, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-variant-field-api"))
	if err != nil {
		t.Fatal(err)
	}
	apiOutput, err := Generate(api, Options{
		SchemaName:   "fixture-variant-field-api",
		Dependencies: map[string]*ir.Schema{"fixture-variant-field-db": schema},
		Clock:        codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	apiDir := filepath.Join(root, "fixture-variant-field-api")
	if err := SetLocalPaths(apiOutput, testpaths.Local(t), apiDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(apiOutput, apiDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(apiDir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	apiCrate := strings.ReplaceAll(apiOutput.CrateName, "-", "_")
	apiTest := `use ` + apiCrate + `::{validators, StepResult, StepView};

#[test]
fn a_view_reads_the_member_its_kind_names() {
    let view: StepView = serde_json::from_str(r#"{"id":"00000000-0000-4000-8000-000000000001","title":"t","kind":"review","result":{"verdict":"approved","passed":true}}"#).unwrap();
    assert!(matches!(view.result, Some(StepResult::ReviewResult(_))), "{view:?}");
    let errors = validators::validate_create_step_input(&serde_json::json!({"title":"t","kind":"note","result":{"passed":true}}));
    assert!(format!("{:?}", errors.flatten()).contains("variant"), "{:?}", errors.flatten());
}
`
	if err := os.WriteFile(filepath.Join(apiDir, "tests", "variant_view.rs"), []byte(apiTest), 0o644); err != nil {
		t.Fatal(err)
	}
	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	decodeTest := `use ` + crate + `::{validators, Step, StepResult};

fn step(text: &str) -> Step {
    serde_json::from_str(text).unwrap_or_else(|err| panic!("decode {text}: {err}"))
}

#[test]
fn the_kind_picks_the_member() {
    let verify = step(r#"{"title":"t","kind":"verify","result":{"passed":true}}"#);
    assert!(matches!(verify.result, Some(StepResult::VerifyResult(_))), "{verify:?}");
    // By its shape this would be a VerifyResult, the first member it reads as.
    let review = step(r#"{"title":"t","kind":"review","result":{"verdict":"approved","passed":true}}"#);
    assert!(matches!(review.result, Some(StepResult::ReviewResult(_))), "{review:?}");
    let note = step(r#"{"title":"t","kind":"note"}"#);
    assert!(note.result.is_none(), "{note:?}");
    let null = step(r#"{"title":"t","kind":"verify","result":null}"#);
    assert!(null.result.is_none(), "{null:?}");
}

#[test]
fn a_result_that_is_not_its_member_is_refused() {
    let err = serde_json::from_str::<Step>(r#"{"title":"t","kind":"verify","result":{"verdict":"approved"}}"#).unwrap_err();
    assert!(err.to_string().contains("passed"), "{err}");
}

#[test]
fn a_result_its_kind_names_no_member_for_is_variant() {
    let text = r#"{"title":"t","kind":"note","result":{"passed":true}}"#;
    assert!(matches!(step(text).result, Some(StepResult::VerifyResult(_))));
    let errors = validators::validate_step(&serde_json::from_str(text).unwrap());
    let flat = format!("{:?}", errors.flatten());
    assert!(flat.contains("variant"), "{flat}");
}

#[test]
fn a_step_round_trips() {
    let text = r#"{"title":"t","kind":"review","result":{"verdict":"changes_requested","notes":"redo"}}"#;
    let decoded = step(text);
    let encoded = serde_json::to_value(&decoded).unwrap();
    assert_eq!(encoded, serde_json::from_str::<serde_json::Value>(text).unwrap());
    let again: Step = serde_json::from_value(encoded).unwrap();
    assert_eq!(again, decoded);
}
`
	if err := os.WriteFile(filepath.Join(testsDir, "variant_field.rs"), []byte(decodeTest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{outDir, apiDir} {
		cmd := exec.Command(cargoPath, "test", "--quiet")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(root, "target"))
		if combined, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo test in %s failed: %v\n%s", filepath.Base(dir), err, combined)
		}
	}
}

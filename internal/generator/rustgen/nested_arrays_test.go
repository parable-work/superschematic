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

// listsOfUnionsAndJSONSchema is a strict type whose lists of lists hold a
// discriminated union and a JSON scalar, the two element kinds whose serde
// decoding differs from a plain struct or primitive.
func listsOfUnionsAndJSONSchema() *ir.Schema {
	schema := paymentUnionSchema()
	schema.Name = "nested-elements"
	schema.Scalars["Generic.JSON"] = &ir.ScalarDef{
		Name:              "Generic.JSON",
		LanguagePrimitive: ir.LanguageObject,
	}
	schema.Types["Ledger"] = &ir.TypeDef{
		Name:              "Ledger",
		Role:              ir.RoleAPIView,
		DenyUnknownFields: true,
		Fields: []*ir.FieldDef{
			{Name: "payments", TypeRef: ir.TypeRef{Name: "Payment", IsArray: true, IsArrayOfArrays: true}, Required: true},
			{Name: "cells", TypeRef: ir.TypeRef{Name: "Generic.JSON", IsArray: true, IsArrayOfArrays: true}, Required: true},
		},
	}
	return schema
}

// TestListsOfUnionsAndJSONRender: a list of lists of a union or a JSON
// scalar renders as Vec<Vec<T>> and pulls in the same imports and crates
// as a single list of it.
func TestListsOfUnionsAndJSONRender(t *testing.T) {
	output, err := Generate(listsOfUnionsAndJSONSchema(), Options{
		SchemaName: "nested-elements",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !output.UsesUnions {
		t.Fatal("a list of lists of a union must import crate::unions")
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	typesRs, err := os.ReadFile(filepath.Join(outDir, "src", "types.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"use crate::unions::*;",
		"#[serde(deny_unknown_fields)]\npub struct Ledger {",
		"    pub payments: Vec<Vec<Payment>>,\n",
		"    pub cells: Vec<Vec<GenericJSON>>,\n",
	} {
		if !strings.Contains(string(typesRs), want) {
			t.Errorf("types.rs missing %q:\n%s", want, typesRs)
		}
	}
	cargoToml, err := os.ReadFile(filepath.Join(outDir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cargoToml), "serde_json = ") {
		t.Errorf("Cargo.toml misses serde_json for the JSON scalar:\n%s", cargoToml)
	}
}

// TestListsOfListsSerde builds the generated crates and checks with serde
// that a list of lists decodes and encodes like Vec<Vec<T>>: ragged and
// empty lists round-trip, an optional one may be absent or null, and a null
// inner list, a null element and a flat list are refused. Union elements
// keep their tag, JSON elements keep every value including null, and
// deny_unknown_fields still applies to the enclosing type.
func TestListsOfListsSerde(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compiled serde checks in short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled serde checks")
	}
	// Both crates build the same serde dependencies, so they share one
	// target directory.
	targetDir := filepath.Join(t.TempDir(), "target")

	t.Run("fixture-nested-arrays", func(t *testing.T) {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays"))
		if err != nil {
			t.Fatalf("load fixture-nested-arrays: %v", err)
		}
		output, err := Generate(schema, Options{
			SchemaName: "fixture-nested-arrays",
			Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
		})
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		cargoTestGeneratedCrate(t, cargoPath, output, targetDir, "lists_of_lists", fixtureListsOfListsTest(output.CrateName))
	})

	t.Run("unions-and-json", func(t *testing.T) {
		output, err := Generate(listsOfUnionsAndJSONSchema(), Options{
			SchemaName: "nested-elements",
			Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
		})
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		cargoTestGeneratedCrate(t, cargoPath, output, targetDir, "lists_of_unions_and_json", unionsAndJSONListsOfListsTest(output.CrateName))
	})
}

// cargoTestGeneratedCrate writes output as a crate, adds serde_json as a
// dev-dependency and source as tests/<name>.rs, and runs cargo test on it.
// A crate that uses the scalar runtime crate resolves it from the
// superscalar checkout scripts/superscalar-dep.sh stands up, since the crate
// is not on crates.io yet; without the checkout the test is skipped.
func cargoTestGeneratedCrate(t *testing.T, cargoPath string, output *ModuleOutput, targetDir, name, source string) {
	t.Helper()
	extraToml := "\n[dev-dependencies]\nserde_json = \"1.0\"\n"
	if output.UsesScalarLib {
		paths := testpaths.Local(t)
		extraToml += "\n[patch.crates-io]\n" + output.Naming.ScalarRustCrate + " = { path = \"" + filepath.ToSlash(paths.ScalarRust) + "\" }\n"
	}
	outDir := filepath.Join(t.TempDir(), output.CrateName)
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	cargoTomlPath := filepath.Join(outDir, "Cargo.toml")
	cargoToml, err := os.ReadFile(cargoTomlPath)
	if err != nil {
		t.Fatalf("read Cargo.toml: %v", err)
	}
	cargoToml = append(cargoToml, []byte(extraToml)...)
	if err := os.WriteFile(cargoTomlPath, cargoToml, 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatalf("create tests directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(testsDir, name+".rs"), []byte(source), 0o644); err != nil {
		t.Fatalf("write %s test: %v", name, err)
	}
	cmd := exec.Command(cargoPath, "test", "--quiet")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+targetDir)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo test failed: %v\n%s", err, combined)
	}
}

func fixtureListsOfListsTest(crateName string) string {
	return `use ` + strings.ReplaceAll(crateName, "-", "_") + `::{Drawing, Point, Shade};

#[test]
fn ragged_lists_round_trip_byte_identically() {
    let drawing = Drawing {
        labels: vec![
            vec!["a".to_string(), "b".to_string(), "c".to_string()],
            vec![],
            vec!["d".to_string()],
        ],
        shades: vec![vec![Shade::Light], vec![Shade::Dark, Shade::Light]],
        polygons: vec![vec![Point { x: 0.0, y: 0.0 }, Point { x: 1.0, y: 0.5 }], vec![]],
        samples: Some(vec![vec![1.5], vec![], vec![2.0, 3.25]]),
    };
    let encoded = serde_json::to_string(&drawing).expect("encode");
    assert_eq!(
        encoded,
        r#"{"labels":[["a","b","c"],[],["d"]],"shades":[["light"],["dark","light"]],"polygons":[[{"x":0.0,"y":0.0},{"x":1.0,"y":0.5}],[]],"samples":[[1.5],[],[2.0,3.25]]}"#
    );
    let decoded: Drawing = serde_json::from_str(&encoded).expect("decode");
    assert_eq!(decoded, drawing);
    assert_eq!(serde_json::to_string(&decoded).expect("re-encode"), encoded);
}

#[test]
fn empty_outer_and_inner_lists_round_trip() {
    let payload = r#"{"labels":[],"shades":[[]],"polygons":[[],[]]}"#;
    let drawing: Drawing = serde_json::from_str(payload).expect("decode");
    assert!(drawing.labels.is_empty());
    assert_eq!(drawing.shades, vec![Vec::<Shade>::new()]);
    assert_eq!(drawing.polygons, vec![Vec::<Point>::new(), Vec::new()]);
    assert_eq!(drawing.samples, None);
    assert_eq!(serde_json::to_string(&drawing).expect("encode"), payload);
}

#[test]
fn optional_list_of_lists_may_be_absent_or_null() {
    let drawing: Drawing =
        serde_json::from_str(r#"{"labels":[],"shades":[],"polygons":[],"samples":null}"#).expect("null");
    assert_eq!(drawing.samples, None);
    let drawing: Drawing =
        serde_json::from_str(r#"{"labels":[],"shades":[],"polygons":[],"samples":[[]]}"#).expect("empty inner");
    assert_eq!(drawing.samples, Some(vec![vec![]]));
}

#[test]
fn null_inner_list_is_rejected() {
    for payload in [
        r#"{"labels":[null],"shades":[],"polygons":[]}"#,
        r#"{"labels":[],"shades":[[],null],"polygons":[]}"#,
        r#"{"labels":[],"shades":[],"polygons":[null]}"#,
        r#"{"labels":[],"shades":[],"polygons":[],"samples":[null]}"#,
    ] {
        assert!(serde_json::from_str::<Drawing>(payload).is_err(), "accepted {payload}");
    }
}

#[test]
fn bad_elements_and_flat_lists_are_rejected() {
    for payload in [
        r#"{"labels":[["a",null]],"shades":[],"polygons":[]}"#,
        r#"{"labels":[],"shades":[["grey"]],"polygons":[]}"#,
        r#"{"labels":[],"shades":[],"polygons":[[{"x":1.0}]]}"#,
        r#"{"labels":["a"],"shades":[],"polygons":[]}"#,
    ] {
        assert!(serde_json::from_str::<Drawing>(payload).is_err(), "accepted {payload}");
    }
}
`
}

func unionsAndJSONListsOfListsTest(crateName string) string {
	return `use ` + strings.ReplaceAll(crateName, "-", "_") + `::{CardPayment, Ledger, Payment, WirePayment};

#[test]
fn union_and_json_elements_round_trip() {
    let payload: serde_json::Value = serde_json::from_str(
        r#"{"payments":[[{"kind":"card","last4":"4242"},{"kind":"wire","iban":"DE89"}],[]],"cells":[[{"a":[1,2]},null,"text",1.5,true,[[]]],[]]}"#,
    )
    .expect("payload");
    let ledger: Ledger = serde_json::from_value(payload.clone()).expect("decode");
    assert_eq!(
        ledger.payments,
        vec![
            vec![
                Payment::CardPayment(CardPayment { kind: "card".to_string(), last4: "4242".to_string() }),
                Payment::WirePayment(WirePayment { kind: "wire".to_string(), iban: "DE89".to_string() }),
            ],
            vec![],
        ]
    );
    assert!(ledger.cells[0][1].is_null());
    assert_eq!(serde_json::to_value(&ledger).expect("encode"), payload);
}

#[test]
fn strict_type_and_inner_lists_are_enforced() {
    for payload in [
        r#"{"payments":[],"cells":[],"extra":1}"#,
        r#"{"payments":[null],"cells":[]}"#,
        r#"{"payments":[],"cells":[null]}"#,
        r#"{"payments":[[{"kind":"cash"}]],"cells":[]}"#,
    ] {
        assert!(serde_json::from_str::<Ledger>(payload).is_err(), "accepted {payload}");
    }
}
`
}

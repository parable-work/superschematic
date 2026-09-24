package rustgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTaggedUnionDecodesFloatsUnderArbitraryPrecision builds a crate whose
// discriminated union has a member with a floating-point field and decodes
// it with serde_json's arbitrary_precision feature on. serde's derive for
// an internally tagged enum buffers the input in its own Content type, where
// an arbitrary-precision number is a private map that f64 cannot read, so
// the derived union failed to decode. Cargo unifies features across a
// build, so any crate in the graph can turn the feature on.
func TestTaggedUnionDecodesFloatsUnderArbitraryPrecision(t *testing.T) {
	schema := paymentUnionSchema()
	wire := schema.Types["WirePayment"]
	wire.Fields = append(wire.Fields, &ir.FieldDef{Name: "amount", TypeRef: ir.TypeRef{Name: "number"}, Required: true})

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	cargoToml, err := os.ReadFile(filepath.Join(outDir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cargoToml), "serde_json") {
		t.Fatalf("a crate with a discriminated union must depend on serde_json:\n%s", cargoToml)
	}

	if testing.Short() {
		t.Skip("skipping compiled decode check in short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled decode check")
	}
	cargoToml = append(cargoToml, []byte("\n[dev-dependencies]\nserde_json = { version = \"1.0\", features = [\"arbitrary_precision\"] }\n")...)
	if err := os.WriteFile(filepath.Join(outDir, "Cargo.toml"), cargoToml, 0o644); err != nil {
		t.Fatal(err)
	}
	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	decodeTest := `use ` + crate + `::{Payment, WirePayment};

#[test]
fn tagged_union_with_a_float_decodes_and_round_trips() {
    let text = r#"{"kind":"wire","iban":"DE89370400440532013000","amount":12.5}"#;
    let decoded: Payment = serde_json::from_str(text).expect("decode the wire variant");
    let Payment::WirePayment(WirePayment { amount, .. }) = &decoded else {
        panic!("decoded {decoded:?}");
    };
    assert_eq!(*amount, 12.5);
    let again: Payment = serde_json::from_slice(&serde_json::to_vec(&decoded).unwrap()).unwrap();
    assert_eq!(again, decoded);
}

#[test]
fn tagged_union_rejects_a_missing_or_unknown_tag() {
    let missing = serde_json::from_str::<Payment>(r#"{"iban":"x","amount":1}"#).unwrap_err();
    assert!(missing.to_string().contains("discriminator kind"), "{missing}");
    let unknown = serde_json::from_str::<Payment>(r#"{"kind":"cash"}"#).unwrap_err();
    assert!(unknown.to_string().contains("\"cash\""), "{unknown}");
}
`
	if err := os.WriteFile(filepath.Join(testsDir, "tagged_union.rs"), []byte(decodeTest), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cargoPath, "test", "--quiet")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(outDir, "target"))
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo test failed: %v\n%s", err, combined)
	}
}

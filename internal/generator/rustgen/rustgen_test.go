package rustgen

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

// TestWriteTypesGolden generates the Rust types crate for fixture services
// and compares every emitted file against its golden copy. Regenerate with:
// go test ./internal/generator/rustgen -run TestWriteTypesGolden -update
func TestWriteTypesGolden(t *testing.T) {
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	cases := []struct {
		service string
		deps    []string
	}{
		{service: "fixture-db"},
		{service: "fixture-api", deps: []string{"fixture-db"}},
		{service: "fixture-general"},
	}

	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, tc.service))
			if err != nil {
				t.Fatalf("load %s: %v", tc.service, err)
			}

			deps := map[string]*ir.Schema{}
			for _, dep := range tc.deps {
				depSchema, err := loader.LoadService(filepath.Join(fixturesDir, dep))
				if err != nil {
					t.Fatalf("load dependency %s: %v", dep, err)
				}
				deps[dep] = depSchema
			}

			output, err := Generate(schema, Options{
				SchemaName:   tc.service,
				Dependencies: deps,
				Clock:        fixedClock,
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}

			outDir := filepath.Join(t.TempDir(), tc.service)
			if err := WriteTypes(output, outDir); err != nil {
				t.Fatalf("write types: %v", err)
			}

			if tc.service == "fixture-general" {
				generated, err := os.ReadFile(filepath.Join(outDir, "src", "types.rs"))
				if err != nil {
					t.Fatalf("read generated types.rs: %v", err)
				}
				const accessor = `pub fn default_fixture_config() -> FixtureConfig {
    serde_json::from_str(`
				if !strings.Contains(string(generated), accessor) {
					t.Fatalf("types.rs missing fresh composite default accessor:\n%s", generated)
				}
			}

			compareWithGolden(t, outDir, filepath.Join("testdata", "golden", tc.service))
		})
	}
}

func TestGenerateTypedRecordFields(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-maps"))
	if err != nil {
		t.Fatalf("load fixture-maps: %v", err)
	}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-maps",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	var fields []FieldInfo
	for _, typeInfo := range output.Types {
		if typeInfo.Name == "MapContainer" {
			fields = typeInfo.Fields
			break
		}
	}
	if len(fields) != 2 {
		t.Fatalf("MapContainer fields = %d, want 2", len(fields))
	}
	if fields[0].RustType != "HashMap<String, String>" {
		t.Fatalf("strings RustType = %q", fields[0].RustType)
	}
	if fields[1].RustType != "HashMap<String, MapValue>" {
		t.Fatalf("nested RustType = %q", fields[1].RustType)
	}
}

func TestGenerateIntegerScalarWithoutRustMapping(t *testing.T) {
	schema := ir.NewSchema("fixture", ir.SchemaKindGeneral)
	schema.Scalars["Temporal.Hours"] = &ir.ScalarDef{
		Name:              "Temporal.Hours",
		LanguagePrimitive: ir.LanguageNumber,
		Primitive:         "Int",
		TypeMappings: map[string]string{
			"sql": "BIGINT",
		},
	}
	schema.Types["Retention"] = &ir.TypeDef{
		Name: "Retention",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{
				Name:     "hours",
				TypeRef:  ir.TypeRef{Name: "Temporal.Hours"},
				Required: true,
			},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "fixture",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if output.Scalars[0].RustType != "i64" {
		t.Fatalf("scalar RustType = %q, want i64", output.Scalars[0].RustType)
	}
	if output.Types[0].Fields[0].RustType != "TemporalHours" {
		t.Fatalf("field RustType = %q, want TemporalHours", output.Types[0].Fields[0].RustType)
	}
}

// compareWithGolden compares every file in gotDir against goldenDir,
// rewriting the goldens when -update is set.
func compareWithGolden(t *testing.T, gotDir, goldenDir string) {
	t.Helper()

	gotFiles := listFiles(t, gotDir)

	if *update {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatalf("clear golden dir: %v", err)
		}
		for _, rel := range gotFiles {
			data, err := os.ReadFile(filepath.Join(gotDir, rel))
			if err != nil {
				t.Fatalf("read generated %s: %v", rel, err)
			}
			dest := filepath.Join(goldenDir, rel)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", rel, err)
			}
		}
		return
	}

	goldenFiles := listFiles(t, goldenDir)

	gotSet := map[string]bool{}
	for _, rel := range gotFiles {
		gotSet[rel] = true
	}
	for _, rel := range goldenFiles {
		if !gotSet[rel] {
			t.Errorf("golden file %s was not generated", rel)
		}
	}

	goldenSet := map[string]bool{}
	for _, rel := range goldenFiles {
		goldenSet[rel] = true
	}

	for _, rel := range gotFiles {
		if !goldenSet[rel] {
			t.Errorf("generated unexpected file %s (run with -update to accept)", rel)
			continue
		}
		got, err := os.ReadFile(filepath.Join(gotDir, rel))
		if err != nil {
			t.Fatalf("read generated %s: %v", rel, err)
		}
		want, err := os.ReadFile(filepath.Join(goldenDir, rel))
		if err != nil {
			t.Fatalf("read golden %s: %v", rel, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n--- got ---\n%s", rel, got)
		}
	}
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return files
}

// TestGenerateUnionsAndDefaults exercises discriminated unions and @default
// literal emission on a hand-built IR (the fixtures do not declare unions).
func TestGenerateUnionsAndDefaults(t *testing.T) {
	schema := paymentUnionSchema()
	enabledDefault := "true"
	limitDefault := "25"
	labelDefault := "pending"

	schema.Types["Settings"] = &ir.TypeDef{
		Name: "Settings",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "enabled", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true, Default: &enabledDefault},
			{Name: "limit", TypeRef: ir.TypeRef{Name: "number"}, Required: true, Default: &limitDefault},
			{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Default: &labelDefault},
			{Name: "payment", TypeRef: ir.TypeRef{Name: "Payment"}, Required: false},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if len(output.Unions) != 1 {
		t.Fatalf("expected 1 union, got %d", len(output.Unions))
	}
	union := output.Unions[0]
	if union.Discriminator != "kind" {
		t.Errorf("expected discriminator \"kind\", got %q", union.Discriminator)
	}
	if len(union.Members) != 2 || union.Members[0].TagValue != "card" || union.Members[1].TagValue != "wire" {
		t.Errorf("unexpected union members: %+v", union.Members)
	}

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}

	for _, name := range []string{
		"Cargo.toml",
		"README.md",
		filepath.Join("src", "lib.rs"),
		filepath.Join("src", "types.rs"),
		filepath.Join("src", "unions.rs"),
	} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("expected output file %s: %v", name, err)
		}
	}

	typesRs, err := os.ReadFile(filepath.Join(outDir, "src", "types.rs"))
	if err != nil {
		t.Fatalf("read types.rs: %v", err)
	}
	for _, want := range []string{
		"fn default_settings_enabled() -> bool {\n    true\n}",
		"fn default_settings_limit() -> f64 {\n    25_f64\n}",
		"fn default_settings_label() -> String {\n    \"pending\".to_string()\n}",
		"fn default_card_payment_kind() -> String {\n    \"card\".to_string()\n}",
		"#[serde(default = \"default_settings_enabled\")]",
		"pub payment: Option<Payment>,",
		"impl Default for Settings {",
		// Discriminator fields on union members must not serialize: serde's
		// internally tagged enum already writes the tag key, so a member
		// serializing its own copy would emit a duplicate key. On the way in
		// serde consumes the tag, so the member fills it from the default.
		"#[serde(default = \"default_card_payment_kind\", skip_serializing)]",
		"#[serde(default = \"default_wire_payment_kind\", skip_serializing)]",
	} {
		if !strings.Contains(string(typesRs), want) {
			t.Errorf("types.rs missing %q:\n%s", want, typesRs)
		}
	}

	unionsRs, err := os.ReadFile(filepath.Join(outDir, "src", "unions.rs"))
	if err != nil {
		t.Fatalf("read unions.rs: %v", err)
	}
	for _, want := range []string{
		"#[serde(tag = \"kind\")]",
		"#[serde(rename = \"card\")]",
		"CardPayment(CardPayment),",
		"#[serde(rename = \"wire\")]",
		"WirePayment(WirePayment),",
	} {
		if !strings.Contains(string(unionsRs), want) {
			t.Errorf("unions.rs missing %q:\n%s", want, unionsRs)
		}
	}
}

func TestGeneratedCardAndWireUnionRoundTripGolden(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compiled round-trip golden in short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled round-trip golden")
	}

	output, err := Generate(paymentUnionSchema(), Options{
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
	cargoTomlPath := filepath.Join(outDir, "Cargo.toml")
	cargoToml, err := os.ReadFile(cargoTomlPath)
	if err != nil {
		t.Fatalf("read Cargo.toml: %v", err)
	}
	cargoToml = append(cargoToml, []byte("\n[dev-dependencies]\nserde_json = \"1.0\"\n")...)
	if err := os.WriteFile(cargoTomlPath, cargoToml, 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}

	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatalf("create tests directory: %v", err)
	}
	const roundTripTest = `use schemas_synthetic_types::{CardPayment, Payment, WirePayment};

#[test]
fn card_and_wire_union_variants_round_trip_byte_identically() {
    let variants = [
        Payment::CardPayment(CardPayment {
            kind: "card".to_string(),
            last4: "4242".to_string(),
        }),
        Payment::WirePayment(WirePayment {
            kind: "wire".to_string(),
            iban: "DE89370400440532013000".to_string(),
        }),
    ];

    for variant in variants {
        let serialized = serde_json::to_vec(&variant).expect("serialize union variant");
        let decoded: Payment =
            serde_json::from_slice(&serialized).expect("deserialize union variant");
        let reserialized = serde_json::to_vec(&decoded).expect("reserialize union variant");

        assert_eq!(decoded, variant);
        assert_eq!(reserialized, serialized);
    }
}
`
	if err := os.WriteFile(filepath.Join(testsDir, "union_round_trip.rs"), []byte(roundTripTest), 0o644); err != nil {
		t.Fatalf("write round-trip test: %v", err)
	}

	cmd := exec.Command(cargoPath, "test", "--quiet")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(outDir, "target"))
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo test failed: %v\n%s", err, combined)
	}
}

func paymentUnionSchema() *ir.Schema {
	schema := ir.NewSchema("synthetic", ir.SchemaKindGeneral)
	cardDefault := "card"
	wireDefault := "wire"
	schema.Types["CardPayment"] = &ir.TypeDef{
		Name: "CardPayment",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &cardDefault},
			{Name: "last4", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Types["WirePayment"] = &ir.TypeDef{
		Name: "WirePayment",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &wireDefault},
			{Name: "iban", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Unions["Payment"] = &ir.UnionDef{
		Name:  "Payment",
		Types: []string{"CardPayment", "WirePayment"},
	}
	return schema
}

func TestGenerateRejectsUnionDiscriminatorWithoutDefault(t *testing.T) {
	schema := paymentUnionSchema()
	schema.Types["WirePayment"].Fields[0].Default = nil

	_, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err == nil {
		t.Fatal("Generate accepted a union discriminator without @default")
	}
	const want = `union "Payment" member "WirePayment" discriminator "kind" must declare @default`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Generate error = %q, want substring %q", err, want)
	}
}

func TestGenerateRejectsUnionDiscriminatorWithUnresolvableEnumDefault(t *testing.T) {
	schema := paymentUnionSchema()
	schema.Enums["PaymentKindEnum"] = &ir.EnumDef{
		Name: "PaymentKindEnum",
		Values: []ir.EnumValueDef{
			{Name: "CARD", SerializedAs: "card"},
			{Name: "WIRE", SerializedAs: "wire"},
		},
	}
	schema.Types["CardPayment"].Fields[0].TypeRef = ir.TypeRef{Name: "PaymentKindEnum"}
	schema.Types["WirePayment"].Fields[0].TypeRef = ir.TypeRef{Name: "PaymentKindEnum"}
	unresolvableDefault := "NOT_A_REAL_VARIANT"
	schema.Types["WirePayment"].Fields[0].Default = &unresolvableDefault

	_, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err == nil {
		t.Fatal("Generate accepted a union discriminator with an unresolvable enum @default")
	}
	const want = `union "Payment" member "WirePayment" discriminator "kind" @default "NOT_A_REAL_VARIANT" cannot be rendered as Rust`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Generate error = %q, want substring %q", err, want)
	}
}

// TestGenerateUnionDiscriminatorDefaultFromImportedEnum verifies that a
// union member whose @internalMetadata discriminator field is typed by an
// enum imported from a dependency schema still gets a serde default. Without
// the default, Rust's internally-tagged deserialization fails with
// "missing field" because serde consumes the tag before deserializing the
// member struct (the desktop-agent DesktopActivitySample case).
func TestGenerateUnionDiscriminatorDefaultFromImportedEnum(t *testing.T) {
	enumsSchema := ir.NewSchema("enums", ir.SchemaKindGeneral)
	enumsSchema.Enums["SampleKindEnum"] = &ir.EnumDef{
		Name: "SampleKindEnum",
		Values: []ir.EnumValueDef{
			{Name: "FOCUS_SEGMENT", SerializedAs: "focus_segment"},
			{Name: "COVERAGE_GAP", SerializedAs: "coverage_gap"},
		},
	}

	schema := ir.NewSchema("agent", ir.SchemaKindGeneral)
	schema.Imports = []ir.Import{
		{Package: "@schemas/enums", Types: []string{"SampleKindEnum"}},
	}
	focusDefault := "focus_segment"
	gapDefault := "coverage_gap"
	schema.Types["FocusSample"] = &ir.TypeDef{
		Name: "FocusSample",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "SampleKindEnum"}, Required: true, InternalMetadata: true, Default: &focusDefault},
			{Name: "appId", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Types["GapSample"] = &ir.TypeDef{
		Name: "GapSample",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "SampleKindEnum"}, Required: true, InternalMetadata: true, Default: &gapDefault},
			{Name: "reason", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Unions["Sample"] = &ir.UnionDef{
		Name:  "Sample",
		Types: []string{"FocusSample", "GapSample"},
	}

	output, err := Generate(schema, Options{
		SchemaName:   "agent",
		Clock:        codegen.FixedClock(time.Unix(0, 0).UTC()),
		Dependencies: map[string]*ir.Schema{"enums": enumsSchema},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	typesRs, err := os.ReadFile(filepath.Join(outDir, "src", "types.rs"))
	if err != nil {
		t.Fatalf("read types.rs: %v", err)
	}
	// The discriminator must not be serialized by the member struct: the
	// internally-tagged enum already writes the tag, and a second `kind`
	// key breaks round-tripping ("duplicate field" on deserialize).
	for _, want := range []string{
		"fn default_focus_sample_kind() -> SampleKindEnum {\n    SampleKindEnum::FocusSegment\n}",
		"fn default_gap_sample_kind() -> SampleKindEnum {\n    SampleKindEnum::CoverageGap\n}",
		"#[serde(default = \"default_focus_sample_kind\", skip_serializing)]",
		"#[serde(default = \"default_gap_sample_kind\", skip_serializing)]",
	} {
		if !strings.Contains(string(typesRs), want) {
			t.Errorf("types.rs missing %q:\n%s", want, typesRs)
		}
	}
}

// TestRustFieldNames verifies snake_case conversion and Rust keyword
// escaping for field names.
func TestRustFieldNames(t *testing.T) {
	cases := map[string]string{
		"userName": "user_name",
		"id":       "id",
		"type":     "r#type",
		"match":    "r#match",
		"self":     "self_",
		"1stPlace": "_1st_place",
	}
	for in, want := range cases {
		if got := toRustFieldName(in); got != want {
			t.Errorf("toRustFieldName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGeneratedEnumsExposeAllAndStableNames(t *testing.T) {
	schema := ir.NewSchema("fixture", ir.SchemaKindGeneral)
	schema.Enums["EntityKind"] = &ir.EnumDef{
		Name: "EntityKind",
		Values: []ir.EnumValueDef{
			{Name: "PAGE", SerializedAs: "page"},
			{Name: "PERCEPTION_PIECE", SerializedAs: "perception_piece"},
		},
	}
	output, err := Generate(schema, Options{
		SchemaName: "fixture",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write: %v", err)
	}
	generated, err := os.ReadFile(filepath.Join(outDir, "src", "enums.rs"))
	if err != nil {
		t.Fatalf("read enums.rs: %v", err)
	}
	source := string(generated)
	for _, want := range []string{
		"#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]",
		"pub const ALL: &'static [Self] = &[Self::Page, Self::PerceptionPiece, ];",
		"pub const fn as_str(&self) -> &'static str",
		"Self::PerceptionPiece => \"perception_piece\"",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("generated enum missing %q:\n%s", want, source)
		}
	}
}

// TestRustEnumVariants verifies enum value to Rust variant conversion.
func TestRustEnumVariants(t *testing.T) {
	cases := map[string]string{
		"ACTIVE":        "Active",
		"in_progress":   "InProgress",
		"on-hold":       "OnHold",
		"self":          "Self_",
		"SELF":          "Self_",
		"v2.beta":       "V2Beta",
		"404_NOT_FOUND": "N404NotFound",
	}
	for in, want := range cases {
		if got := toRustEnumVariant(in); got != want {
			t.Errorf("toRustEnumVariant(%q) = %q, want %q", in, got, want)
		}
	}
}

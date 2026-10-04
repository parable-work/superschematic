package rustgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// recipesSchema declares a versioned table (Recipe) next to a table that is
// not versioned (Step).
func recipesSchema() *ir.Schema {
	schema := ir.NewSchema("recipes", ir.SchemaKindDB)
	schema.Types["Recipe"] = &ir.TypeDef{Name: "Recipe", Role: ir.RoleDBTable, Versioned: true, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "notes", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "vegetarian", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true},
	}}
	schema.Types["Step"] = &ir.TypeDef{Name: "Step", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	return schema
}

// TestGeneratedHistoryRecord builds the crate for a versioned table and
// checks with serde that it reads and writes the JSON Go's
// HistoryRecord[Recipe] writes: the record's wire names, RFC 3339 recordedAt
// with nanoseconds, and _version on the value. A value without _version
// reads as version 0, as Go decodes it, and a table that is not versioned
// has no version field.
func TestGeneratedHistoryRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compiled history record check in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled history record check")
	}
	output, err := Generate(recipesSchema(), Options{SchemaName: "recipes"})
	if err != nil {
		t.Fatal(err)
	}
	outDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := SetLocalPaths(output, testpaths.Local(t), outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	cargoTomlPath := filepath.Join(outDir, "Cargo.toml")
	cargoToml, err := os.ReadFile(cargoTomlPath)
	if err != nil {
		t.Fatal(err)
	}
	cargoToml = append(cargoToml, []byte("\n[dev-dependencies]\nserde_json = \"1.0\"\n")...)
	if err := os.WriteFile(cargoTomlPath, cargoToml, 0o644); err != nil {
		t.Fatal(err)
	}
	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	test := `use ` + crate + `::{HistoryRecord, Recipe, Step};

// Written by json.Marshal of the Go HistoryRecord[Recipe]: fields in
// declaration order, time.Time as RFC 3339 with nanoseconds, an absent
// optional field left out.
const GO_RECORD: &str = r#"{"version":3,"operation":"UPDATE","recordedAt":"2026-01-02T03:04:05.123456789Z","value":{"id":"r1","name":"Soup","vegetarian":true,"_version":3}}"#;

#[test]
fn decodes_and_re_encodes_the_go_history_record() {
    let record: HistoryRecord<Recipe> = serde_json::from_str(GO_RECORD).expect("decode");
    assert_eq!(record.version, 3);
    assert_eq!(record.operation, "UPDATE");
    assert_eq!(record.value.name, "Soup");
    assert_eq!(record.value.notes, None);
    assert_eq!(record.value.version, 3);
    assert_eq!(serde_json::to_string(&record).expect("encode"), GO_RECORD);
}

#[test]
fn missing_version_reads_as_zero_and_is_always_written() {
    let recipe: Recipe = serde_json::from_str(r#"{"id":"r1","name":"Soup","vegetarian":false}"#).expect("decode");
    assert_eq!(recipe.version, 0);
    let encoded = serde_json::to_value(&recipe).expect("encode");
    assert_eq!(encoded["_version"], 0);
    assert!(encoded.get("version").is_none());
}

#[test]
fn record_fields_are_required() {
    for key in ["version", "operation", "recordedAt", "value"] {
        let mut value: serde_json::Value = serde_json::from_str(GO_RECORD).unwrap();
        value.as_object_mut().unwrap().remove(key);
        assert!(serde_json::from_value::<HistoryRecord<Recipe>>(value).is_err(), "missing {key}");
    }
}

#[test]
fn unversioned_table_has_no_version_field() {
    let step = Step { id: "s1".to_string(), title: "Chop".to_string() };
    assert_eq!(serde_json::to_string(&step).expect("encode"), r#"{"id":"s1","title":"Chop"}"#);
}
`
	if err := os.WriteFile(filepath.Join(testsDir, "history_record.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cargo, "test", "--quiet")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(outDir, "target"))
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated Rust tests: %v\n%s", err, data)
	}
}

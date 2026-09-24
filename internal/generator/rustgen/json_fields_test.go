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

// jsonFieldsSchema declares Generic.JSON in every field shape (direct,
// optional, list, map, map of lists, nested in a list of structs) next to
// non-JSON fields, and in a member of a tagged and an untagged union.
func jsonFieldsSchema() *ir.Schema {
	schema := ir.NewSchema("json-fields", ir.SchemaKindGeneral)
	schema.Scalars["Generic.JSON"] = &ir.ScalarDef{
		Name: "Generic.JSON", LanguagePrimitive: ir.LanguageObject,
		TypeMappings: map[string]string{"rust": "serde_json::Value"},
	}
	schema.Scalars["Contact.Email"] = &ir.ScalarDef{
		Name: "Contact.Email", LanguagePrimitive: ir.LanguageString,
		TypeMappings: map[string]string{"rust": "String"},
	}
	schema.Types["Child"] = &ir.TypeDef{Name: "Child", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "payload", TypeRef: ir.TypeRef{Name: "Generic.JSON"}, Required: true},
	}}
	jsonTag, plainTag := "json", "plain"
	schema.Types["Child"].Fields = append(schema.Types["Child"].Fields, &ir.FieldDef{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &jsonTag})
	schema.Types["TaggedPlain"] = &ir.TypeDef{Name: "TaggedPlain", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &plainTag},
		{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	schema.Types["Plain"] = &ir.TypeDef{Name: "Plain", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	schema.Unions["TaggedJson"] = &ir.UnionDef{Name: "TaggedJson", Types: []string{"Child", "TaggedPlain"}}
	schema.Unions["UntaggedJson"] = &ir.UnionDef{Name: "UntaggedJson", Types: []string{"Child", "Plain"}}
	schema.Types["JsonFields"] = &ir.TypeDef{Name: "JsonFields", Role: ir.RoleEmbeddedStruct, StrictJSON: true, Fields: []*ir.FieldDef{
		{Name: "value", TypeRef: ir.TypeRef{Name: "Generic.JSON"}, Required: true},
		{Name: "optionalValue", TypeRef: ir.TypeRef{Name: "Generic.JSON"}},
		{Name: "values", TypeRef: ir.TypeRef{Name: "Generic.JSON", IsArray: true}, Required: true},
		{Name: "optionalValues", TypeRef: ir.TypeRef{Name: "Generic.JSON", IsArray: true}},
		{Name: "mapping", TypeRef: ir.TypeRef{Name: "Generic.JSON", IsMap: true}, Required: true},
		{Name: "optionalMapping", TypeRef: ir.TypeRef{Name: "Generic.JSON", IsMap: true}},
		{Name: "mapOfArrays", TypeRef: ir.TypeRef{Name: "Generic.JSON", IsMap: true, IsArray: true}, Required: true},
		{Name: "children", TypeRef: ir.TypeRef{Name: "Child", IsArray: true}, Required: true},
		{Name: "email", TypeRef: ir.TypeRef{Name: "Contact.Email"}, Required: true},
		{Name: "count", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
		{Name: "enabled", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true},
	}}
	return schema
}

// TestGenericJSONFieldAdapters pins which fields decode through the lossless
// adapter: every Generic.JSON field and nothing else, with the adapter path
// taken from the naming file's scalar crate.
func TestGenericJSONFieldAdapters(t *testing.T) {
	output, err := Generate(jsonFieldsSchema(), Options{SchemaName: "json-fields"})
	if err != nil {
		t.Fatal(err)
	}
	if !output.UsesScalarLib {
		t.Fatal("a crate with a Generic.JSON field depends on the scalar crate for its adapter")
	}
	for _, typ := range output.Types {
		for _, field := range typ.Fields {
			if got, want := field.PreserveJSON, field.Type == "Generic.JSON"; got != want {
				t.Errorf("%s.%s adapter=%v, want %v", typ.Name, field.Name, got, want)
			}
		}
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "src", "types.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if output.JSONFieldAdapter() != "superscalar::scalars::json_scalar::serde::deserialize" {
		t.Fatalf("adapter path = %q", output.JSONFieldAdapter())
	}
	if got := strings.Count(string(data), `#[serde(deserialize_with = "superscalar::scalars::json_scalar::serde::deserialize")]`); got != 8 {
		t.Fatalf("got %d JSON adapter attributes, want 8", got)
	}
}

// TestGenericJSONUnionImportsAndRecursion pins that a union whose member is
// imported from a dependency, and reaches Generic.JSON through a recursive
// type, still reads its buffer through the adapter.
func TestGenericJSONUnionImportsAndRecursion(t *testing.T) {
	dep := jsonFieldsSchema()
	dep.Name = "json-dep"
	dep.Types["Child"].Fields = append(dep.Types["Child"].Fields, &ir.FieldDef{Name: "next", TypeRef: ir.TypeRef{Name: "Child"}})
	root := ir.NewSchema("json-import", ir.SchemaKindGeneral)
	root.Imports = []ir.Import{{Package: "@schemas/json-dep", Types: []string{"Child"}}}
	root.Types["Plain"] = &ir.TypeDef{Name: "Plain", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
	root.Unions["ImportedUnion"] = &ir.UnionDef{Name: "ImportedUnion", Types: []string{"Child", "Plain"}}
	output, err := Generate(root, Options{SchemaName: root.Name, Dependencies: map[string]*ir.Schema{dep.Name: dep}})
	if err != nil {
		t.Fatal(err)
	}
	if !output.UsesScalarLib || !output.Unions[0].PreserveJSON {
		t.Fatal("a union with an imported member that reaches Generic.JSON must read through the adapter")
	}
	for _, dependency := range output.ExternalCrateDeps {
		if dependency.Name == "serde_json" {
			return
		}
	}
	t.Fatal("an untagged union that reads through the adapter needs a direct serde_json dependency")
}

// TestGeneratedGenericJSONFields builds the crate against the real scalar
// crate and runs Rust tests: Generic.JSON keeps exact number digits and
// objects whose keys look like serde_json's private markers, in every field
// shape and through both union forms, from text and from a Value; the other
// fields and the missing, null and empty cases behave as before.
func TestGeneratedGenericJSONFields(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compiled generated JSON field contract in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled generated JSON field contract")
	}
	output, err := Generate(jsonFieldsSchema(), Options{SchemaName: "json-fields"})
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	outDir, err = filepath.EvalSymlinks(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetScalarLibPath(output, testpaths.Local(t), outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	test := `use ` + crate + `::{JsonFields, TaggedJson, UntaggedJson};
use serde_json::{json, Value};

fn evidence() -> Value {
    json!({"nested":[{"$serde_json::private::Number":"1"},{"$serde_json::private::RawValue":"true"},{"$serde_json::private::Number":"literal text","extra":[]}],"n":"12345678901234567890.12345678901234567890123456789".parse::<serde_json::Number>().unwrap()})
}

#[test]
fn generated_fields_preserve_json_and_other_types() {
    let expected = evidence();
    let input = json!({"value":expected,"optionalValue":expected,"values":[expected,null],"optionalValues":[expected],"mapping":{"$serde_json::private::Number":expected},"optionalMapping":{"a":expected},"mapOfArrays":{"a":[expected]},"children":[{"payload":expected}],"email":"test@example.com","count":1.25,"enabled":true});
    for value in [serde_json::from_str::<JsonFields>(&input.to_string()).unwrap(), serde_json::from_value::<JsonFields>(input).unwrap()] {
        assert_eq!(value.value, expected);
        assert_eq!(value.optional_value, Some(expected.clone()));
        assert_eq!(value.values, vec![expected.clone(),Value::Null]);
        assert_eq!(value.optional_values, Some(vec![expected.clone()]));
        assert_eq!(value.mapping["$serde_json::private::Number"], expected);
        assert_eq!(value.optional_mapping.as_ref().unwrap()["a"], Some(expected.clone()));
        assert_eq!(value.map_of_arrays["a"], vec![expected.clone()]);
        assert_eq!(value.children[0].payload, expected);
        assert_eq!(value.email, "test@example.com");
        assert_eq!(value.count, 1.25);
        assert!(value.enabled);
        let round_trip: JsonFields = serde_json::from_value(serde_json::to_value(&value).unwrap()).unwrap();
        assert_eq!(round_trip, value);
    }
}

#[test]
fn generated_union_buffering_keeps_json_literals_and_precision() {
    let expected = evidence();
    let tagged = json!({"kind":"json","payload":expected});
    for value in [serde_json::from_str::<TaggedJson>(&tagged.to_string()).unwrap(), serde_json::from_value::<TaggedJson>(tagged).unwrap()] {
        let TaggedJson::Child(child) = value else { panic!("wrong variant") };
        assert_eq!(child.payload, expected);
    }
    let untagged = json!({"payload":expected});
    for value in [serde_json::from_str::<UntaggedJson>(&untagged.to_string()).unwrap(), serde_json::from_value::<UntaggedJson>(untagged).unwrap()] {
        let UntaggedJson::Child(child) = value else { panic!("wrong variant") };
        assert_eq!(child.payload, expected);
    }
    assert!(serde_json::from_str::<TaggedJson>(r#"{"kind":"other","payload":null}"#).is_err());
    assert!(serde_json::from_str::<UntaggedJson>(r#"{"unrelated":true}"#).is_err());
}

#[test]
fn generated_missing_null_empty_and_non_json_semantics() {
    let input = json!({"value":null,"values":[],"mapping":{},"mapOfArrays":{},"children":[],"email":"test@example.com","count":0.0,"enabled":false});
    let value: JsonFields = serde_json::from_value(input.clone()).unwrap();
    assert!(value.value.is_null());
    assert!(value.optional_value.is_none() && value.optional_values.is_none() && value.optional_mapping.is_none());
    assert!(value.values.is_empty() && value.mapping.is_empty() && value.map_of_arrays.is_empty());
    for optional in ["optionalValue","optionalValues","optionalMapping"] {
        let mut changed = input.clone(); changed[optional] = Value::Null;
        assert_eq!(serde_json::from_value::<JsonFields>(changed).unwrap(), value);
    }
    for (optional, empty) in [("optionalValue",json!({})),("optionalValues",json!([])),("optionalMapping",json!({}))] {
        let mut changed = input.clone(); changed[optional] = empty.clone();
        let decoded: JsonFields = serde_json::from_value(changed).unwrap();
        assert_eq!(serde_json::to_value(decoded).unwrap()[optional], empty);
    }
    for required in ["value","values","mapping","mapOfArrays","children","email","count","enabled"] {
        let mut missing = input.clone(); missing.as_object_mut().unwrap().remove(required);
        assert!(serde_json::from_value::<JsonFields>(missing).is_err(), "missing {required}");
    }
    for (field, wrong) in [("values",Value::Null),("mapping",Value::Null),("mapOfArrays",json!([])),("optionalValues",json!({})),("optionalMapping",json!([])),("email",json!(1)),("count",json!("0")),("enabled",json!(0)),("unknown",json!(true))] {
        let mut changed = input.clone(); changed[field] = wrong;
        assert!(serde_json::from_value::<JsonFields>(changed).is_err(), "bad {field}");
    }
}
`
	if err := os.WriteFile(filepath.Join(testsDir, "json_fields.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cargo, "test", "--quiet")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(outDir, "target"))
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated Rust tests: %v\n%s", err, data)
	}
}

package rustgen

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// validatorCasesSchema covers what the parity corpus does not: map fields
// of a primitive with rules and of a scalar, an enum list, a nested
// @strictJSON type inside a type that is not one and inside one that is, a
// type that holds itself, @default values, a scalar with reserved words, and
// a pattern that reads \w.
func validatorCasesSchema() *ir.Schema {
	schema := ir.NewSchema("validator-cases", ir.SchemaKindGeneral)
	minHandle, maxHandle := 2, 8
	minCount, maxCount := int64(0), int64(10)
	schema.Scalars["Test.Handle"] = &ir.ScalarDef{
		Name: "Test.Handle", LanguagePrimitive: ir.LanguageString, Primitive: "String",
		MinLength: minHandle, MaxLength: maxHandle, Pattern: "^[a-z]+$",
		ReservedWords: []string{"Admin"}, ReservedWordsCaseInsensitive: true,
	}
	schema.Scalars["Test.Count"] = &ir.ScalarDef{
		Name: "Test.Count", LanguagePrimitive: ir.LanguageNumber, Primitive: "Int",
		Minimum: &minCount, Maximum: &maxCount,
	}
	schema.Enums["Tier"] = &ir.EnumDef{Name: "Tier", Values: []ir.EnumValueDef{{Name: "FREE", SerializedAs: "free"}, {Name: "PRO", SerializedAs: "pro"}}}

	port, tier := "8080", "PRO"
	schema.Types["Settings"] = &ir.TypeDef{Name: "Settings", Role: ir.RoleEmbeddedStruct, StrictJSON: true, Fields: []*ir.FieldDef{
		{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "port", TypeRef: ir.TypeRef{Name: "number"}, Required: true, Default: &port},
		{Name: "tier", TypeRef: ir.TypeRef{Name: "Tier"}, Required: true, Default: &tier},
		{Name: "handle", TypeRef: ir.TypeRef{Name: "Test.Handle"}},
	}}
	maxLabel, maxLabels, minNicknames := 3, 2, 1
	schema.Types["Account"] = &ir.TypeDef{Name: "Account", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "handle", TypeRef: ir.TypeRef{Name: "Test.Handle"}, Required: true},
		{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}, Required: true, ValidateMaxLength: &maxLabel, ValidateListMax: &maxLabels},
		{Name: "quotas", TypeRef: ir.TypeRef{Name: "Test.Count", IsMap: true}},
		{Name: "tiers", TypeRef: ir.TypeRef{Name: "Tier", IsArray: true}},
		{Name: "settings", TypeRef: ir.TypeRef{Name: "Settings"}},
		{Name: "parent", TypeRef: ir.TypeRef{Name: "Account"}},
		{Name: "nicknames", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, ValidatePattern: `^\w+$`, ValidateListMin: &minNicknames},
	}}
	schema.Types["Bundle"] = &ir.TypeDef{Name: "Bundle", Role: ir.RoleEmbeddedStruct, StrictJSON: true, Fields: []*ir.FieldDef{
		{Name: "settings", TypeRef: ir.TypeRef{Name: "Settings"}, Required: true},
		{Name: "extras", TypeRef: ir.TypeRef{Name: "Settings", IsMap: true}},
	}}
	return schema
}

// TestGeneratedValidatorsCoverMapsStrictTypesAndDefaults runs the generated
// validators on the cases above. Each expected verdict is what tsgen's
// validator_*.tmpl reports for the same payload.
func TestGeneratedValidatorsCoverMapsStrictTypesAndDefaults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo test in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled validator check")
	}
	output, err := Generate(validatorCasesSchema(), Options{
		SchemaName: "validator-cases",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	cargoTestGeneratedCrate(t, cargoPath, output, t.TempDir(), "validators", strings.ReplaceAll(validatorCasesTest, "TYPES_CRATE", crate))
}

const validatorCasesTest = `use std::collections::BTreeMap;

use serde_json::{json, Value};
use superschematic_schema_runtime::{ParseError, UnknownFields, ValidationErrors};
use TYPES_CRATE::validators;

fn verdicts(errors: &ValidationErrors) -> BTreeMap<String, Vec<String>> {
    errors
        .flatten()
        .into_iter()
        .map(|(path, errs)| (path, errs.into_iter().map(|e| e.validator).collect()))
        .collect()
}

fn expect(errors: &ValidationErrors, want: Value) {
    let want: BTreeMap<String, Vec<String>> = serde_json::from_value(want).unwrap();
    assert_eq!(verdicts(errors), want);
}

#[test]
fn maps_enums_nested_objects_and_rules() {
    let account = json!({
        "handle": "Admin",
        "labels": {"a": "abcd", "b": "x", "c": "y"},
        "quotas": {"q": 11, "r": "x"},
        "tiers": ["pro", "gold", null],
        "settings": {"port": "x"},
        "parent": {"labels": {}},
        "nicknames": ["é", 1]
    });
    expect(&validators::validate_account(&account), json!({
        "handle": ["pattern", "reservedWord"],
        "labels": ["listMax"],
        "labels.a": ["maxLength"],
        "quotas.q": ["max"],
        "quotas.r": ["type"],
        "tiers[1]": ["enum"],
        "tiers[2]": ["required"],
        "settings.name": ["required"],
        "settings.port": ["type"],
        "settings.tier": ["required"],
        "parent.handle": ["required"],
        "nicknames[0]": ["pattern"],
        "nicknames[1]": ["type"]
    }));
}

#[test]
fn a_missing_required_map_is_one_required() {
    expect(&validators::validate_account(&json!({"handle": "ok"})), json!({"labels": ["required"]}));
    expect(&validators::validate_account(&json!({"handle": "ok", "labels": "x"})), json!({"labels": ["required"]}));
    expect(&validators::validate_account(&json!({"handle": "ok", "labels": {}, "nicknames": []})), json!({"nicknames": ["listMin"]}));
}

#[test]
fn a_strict_type_refuses_undeclared_keys_and_reports_nested_objects_as_one_error() {
    let bundle = json!({
        "settings": {"name": "n", "port": 1, "tier": "free", "extra": 1},
        "extras": {"a": {"name": "m", "port": 2, "tier": "pro"}, "b": 3},
        "junk": true
    });
    let errors = validators::validate_bundle(&bundle);
    expect(&errors, json!({"junk": ["unknown"], "settings": ["object"], "extras.b": ["object"]}));
    let message = &errors.flatten()["settings"][0].message;
    assert_eq!(message, r#"{"extra":[{"validator":"unknown","message":"unknown field"}]}"#);
    expect(
        &validators::validate_bundle(&json!({"settings": {"name": "n", "port": 1, "tier": "free"}, "extras": []})),
        json!({"extras": ["object"]}),
    );
    expect(&validators::validate_bundle(&json!([1])), json!({"$": ["object"]}));
    expect(&validators::validate_bundle(&json!({})), json!({"settings": ["required"]}));
}

#[test]
fn parse_fills_defaults_refuses_unknown_keys_and_decodes() {
    let settings = validators::parse_settings(json!({"name": "n"}), UnknownFields::Refuse).unwrap();
    assert_eq!(settings.port, 8080.0);
    assert_eq!(settings.tier.as_str(), "pro");
    match validators::parse_settings(json!({"name": "n", "bogus": 1}), UnknownFields::Refuse) {
        Err(ParseError::UnknownFields(keys)) => assert_eq!(keys, ["bogus"]),
        other => panic!("unexpected {other:?}"),
    }
    assert!(matches!(validators::parse_settings(json!([]), UnknownFields::Allow), Err(ParseError::NotAnObject)));
    // A strict type keeps an explicit null, which its required field refuses.
    match validators::parse_settings(json!({"name": "n", "port": null}), UnknownFields::Allow) {
        Err(ParseError::Invalid(errors)) => expect(&errors, json!({"port": ["required"]})),
        other => panic!("unexpected {other:?}"),
    }
    let account = validators::parse_account(json!({"handle": "ok", "labels": {"a": "b"}, "extra": 1}), UnknownFields::Allow).unwrap();
    assert_eq!(account.handle, "ok");
}

#[test]
fn scalar_and_enum_validators() {
    use validators::scalars::{validate_test_handle, validate_test_handle_required};
    assert_eq!(validate_test_handle(None), Ok(()));
    assert_eq!(validate_test_handle(Some(&json!("ok"))), Ok(()));
    assert_eq!(validate_test_handle_required(Some(&json!(""))).unwrap_err()[0].validator, "required");
    let names = |value: Value| -> Vec<String> {
        validate_test_handle(Some(&value)).unwrap_err().into_iter().map(|e| e.validator).collect()
    };
    assert_eq!(names(json!("a")), ["minLength"]);
    assert_eq!(names(json!("abcdefghi")), ["maxLength"]);
    assert_eq!(names(json!(7)), ["type"]);
    assert_eq!(validators::validate_tier(Some(&json!("free"))), Ok(()));
    assert_eq!(validators::validate_tier(Some(&json!(1))).unwrap_err()[0].validator, "type");
    assert_eq!(validators::validate_tier_required(Some(&Value::Null)).unwrap_err()[0].validator, "required");
}
`

func TestValidatorHelpers(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:         `"plain"`,
		`a"b\c`:         `"a\"b\\c"`,
		"tab\tnl\n":     `"tab\tnl\n"`,
		"bell\x07":      `"bell\u{7}"`,
		`^\w+@é$`:       `"^\\w+@é$"`,
		"nul\x00":       `"nul\0"`,
		"del\x7f":       `"del\u{7f}"`,
		`{"k":["v"]}`:   `"{\"k\":[\"v\"]}"`,
		"emoji 😀 stays": `"emoji 😀 stays"`,
	} {
		if got := rustString(in); got != want {
			t.Errorf("rustString(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"ParityMatrix": "PARITY_MATRIX",
		"reqGrid":      "REQ_GRID",
		"_version":     "VERSION",
		"9lives":       "F_9LIVES",
	} {
		if got := constName(in); got != want {
			t.Errorf("constName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{1: "1_f64", 0.5: "0.5_f64", -2: "-2_f64", 1e21: "1000000000000000000000_f64"} {
		if got := f64Literal(in); got != want {
			t.Errorf("f64Literal(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestScalarValidatorsTakeTypeScriptsShape pins the scalar fields that come
// from tsgen's view of a scalar: an empty string is a missing value only for
// a scalar TypeScript holds as a plain string or a JSON object or array, and
// a date-time's type error names a date-time string.
func TestScalarValidatorsTakeTypeScriptsShape(t *testing.T) {
	output, err := Generate(validatorCasesSchema(), Options{SchemaName: "validator-cases"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ScalarValidator{}
	for _, s := range output.Validators.Scalars {
		byName[s.Canonical] = s
	}
	handle := byName["Test.Handle"]
	if handle.Kind != scalarString || !handle.EmptyIsMissing || handle.PatternStatic != "TEST_HANDLE_PATTERN" || len(handle.ReservedWords) != 1 || handle.ReservedWords[0] != "admin" {
		t.Errorf("Test.Handle validator = %+v", handle)
	}
	count := byName["Test.Count"]
	if count.Kind != scalarNumber || count.EmptyIsMissing || count.Minimum != "0" || count.MaximumF64 != "10_f64" {
		t.Errorf("Test.Count validator = %+v", count)
	}
	if output.Validators.CallsCore || output.Validators.ParsesCore || output.Validators.ValidatesJSON || output.UsesScalarLib {
		t.Errorf("no scalar here asks the core, but the crate does: %+v", output.Validators)
	}

	dateTime := buildScalarValidator(codegen.ScalarInfo{Name: "Temporal.DateTime", Primitive: ir.LanguageString, Traits: codegen.ScalarTraits{IsDateTimeLike: true}}, nil)
	if dateTime.EmptyIsMissing || dateTime.TypeMessage != "expected a date-time string" {
		t.Errorf("Temporal.DateTime validator = %+v", dateTime)
	}
	stringMap := buildScalarValidator(codegen.ScalarInfo{Name: "Generic.StringMap", Primitive: ir.LanguageString, HasCustomParse: true, Traits: codegen.ScalarTraits{IsJSONLike: true, StructuredJSON: "object"}}, nil)
	if stringMap.Kind != scalarStructured || !stringMap.EmptyIsMissing || stringMap.CoreParse {
		t.Errorf("Generic.StringMap validator = %+v", stringMap)
	}
	anyJSON := buildScalarValidator(codegen.ScalarInfo{Name: "Generic.JSON", Primitive: ir.LanguageString, HasCustomValidate: true, Traits: codegen.ScalarTraits{IsJSONLike: true, IsAnyJSON: true}}, nil)
	if anyJSON.Kind != scalarOpaque || anyJSON.EmptyIsMissing || !anyJSON.CoreValidateJSON || anyJSON.CoreValidate {
		t.Errorf("Generic.JSON validator = %+v", anyJSON)
	}
}

package envgen

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func TestGenerateWithDependencies_ImportedEnumMetadata(t *testing.T) {
	defaultValue := "bar"
	schema := &ir.Schema{
		Name: "consumer",
		Kind: ir.SchemaKindGeneral,
		Imports: []ir.Import{
			{Package: "@schemas/shared", Types: []string{"ImportedEnvironment"}},
		},
		Scalars: map[string]*ir.ScalarDef{},
		Types: map[string]*ir.TypeDef{
			"ConsumerConfig": {
				Name:    "ConsumerConfig",
				EnvVars: true,
				Fields: []*ir.FieldDef{
					{
						Name:     "IMPORTED_ENV",
						TypeRef:  ir.TypeRef{Name: "ImportedEnvironment"},
						Required: false,
						Default:  &defaultValue,
					},
				},
			},
		},
		Enums: map[string]*ir.EnumDef{},
	}
	dependencies := map[string]*ir.Schema{
		"shared": {
			Name:    "shared",
			Kind:    ir.SchemaKindGeneral,
			Scalars: map[string]*ir.ScalarDef{},
			Types:   map[string]*ir.TypeDef{},
			Enums: map[string]*ir.EnumDef{
				"ImportedEnvironment": {
					Name: "ImportedEnvironment",
					Values: []ir.EnumValueDef{
						{Name: "Foo", SerializedAs: "foo"},
						{Name: "Bar", SerializedAs: "bar"},
					},
				},
			},
		},
	}

	output, err := GenerateWithDependencies(schema, "consumer", dependencies)
	if err != nil {
		t.Fatalf("GenerateWithDependencies: %v", err)
	}
	if output == nil || len(output.Fields) != 1 {
		t.Fatalf("expected one generated env field, got %#v", output)
	}

	field := output.Fields[0]
	if !field.IsEnum {
		t.Fatal("imported enum field should be marked isEnum")
	}
	if !containsString(field.EnumValues, "foo") || !containsString(field.EnumValues, "bar") {
		t.Fatalf("unexpected imported enum values: %v", field.EnumValues)
	}
	if field.DefaultValue != "bar" {
		t.Fatalf("default = %q, want bar", field.DefaultValue)
	}
	if loader := rustLoaderExpr(field); !strings.Contains(loader, `enum_or_default("IMPORTED_ENV", &["foo", "bar"], "bar")?`) {
		t.Fatalf("unexpected Rust loader expression: %s", loader)
	}
}

func TestRustFloatScalarUsesFloatLoader(t *testing.T) {
	field := ConfigField{
		Key:             "MATCH_THRESHOLD",
		Required:        false,
		DefaultValue:    "0.88",
		HasDefault:      true,
		IsScalar:        true,
		ScalarPrimitive: "number",
		ScalarKind:      "Float",
	}

	if got := rustType(field); got != "f64" {
		t.Fatalf("rustType = %q, want f64", got)
	}
	if got := rustLoaderExpr(field); got != `f64_or_default("MATCH_THRESHOLD", 0.88)?` {
		t.Fatalf("rustLoaderExpr = %q", got)
	}
	if !usesRustFloat([]ConfigField{field}) {
		t.Fatal("float scalar should emit Rust float loader helpers")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestDerivedFields: with Derived set, an API gets a config field per
// edge, named by the naming file's [derived_fields], beside its settings;
// a setting that collides with one is refused, and so is an @envVars type
// named EnvConfig.
func TestDerivedFields(t *testing.T) {
	schema := &ir.Schema{
		Name:   "orders",
		Kind:   ir.SchemaKindAPI,
		AuthDB: "shop-db",
		Calls:  []ir.ServiceRef{{Name: "shop-api", Kind: ir.SchemaKindAPI}},
		Types: map[string]*ir.TypeDef{
			"OrdersConfig": {Name: "OrdersConfig", EnvVars: true, Fields: []*ir.FieldDef{
				{Name: "LOG_LEVEL", TypeRef: ir.TypeRef{Name: "string"}},
			}},
		},
	}
	names := naming.Default()
	names.DerivedFields.Service = "{SERVICE}_ENDPOINT"
	output, err := GenerateWithOptions(schema, Options{SchemaName: "orders", Naming: names, Derived: true})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range output.Derived {
		keys = append(keys, f.Key+":"+f.RuntimeType()+":"+f.From)
	}
	if got, want := strings.Join(keys, ","), "SHOP_DB_DATABASE:Database:authDb,SHOP_API_ENDPOINT:Service:calls"; got != want {
		t.Errorf("derived = %s, want %s", got, want)
	}
	if !output.EnvConfig || output.TypeName != "OrdersConfig" {
		t.Errorf("EnvConfig = %v, TypeName = %s", output.EnvConfig, output.TypeName)
	}

	without, err := GenerateWithOptions(schema, Options{SchemaName: "orders"})
	if err != nil {
		t.Fatal(err)
	}
	if without.EnvConfig || len(without.Derived) != 0 {
		t.Errorf("without Derived: EnvConfig = %v, derived = %v", without.EnvConfig, without.Derived)
	}

	schema.Types["OrdersConfig"].Fields = append(schema.Types["OrdersConfig"].Fields, &ir.FieldDef{Name: "SHOP_DB_DATABASE_URL", TypeRef: ir.TypeRef{Name: "string"}})
	if _, err := GenerateWithOptions(schema, Options{SchemaName: "orders", Derived: true}); err == nil ||
		!strings.Contains(err.Error(), "@envVars field SHOP_DB_DATABASE_URL of OrdersConfig collides with SHOP_DB_DATABASE, the config field its authDb shop-db derives") {
		t.Errorf("a colliding setting: %v", err)
	}

	schema.Types = map[string]*ir.TypeDef{"EnvConfig": {Name: "EnvConfig", EnvVars: true}}
	if _, err := GenerateWithOptions(schema, Options{SchemaName: "orders", Derived: true}); err == nil || !strings.Contains(err.Error(), "is named EnvConfig") {
		t.Errorf("an @envVars type named EnvConfig: %v", err)
	}

	schema.Types = nil
	onlyDerived, err := GenerateWithOptions(schema, Options{SchemaName: "orders", Derived: true})
	if err != nil || onlyDerived == nil || onlyDerived.TypeName != "" || len(onlyDerived.Derived) != 2 {
		t.Errorf("an API without @envVars: %+v, %v", onlyDerived, err)
	}
}

package envgen

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func TestGenerateWithDependencies_ImportedEnumMetadata(t *testing.T) {
	defaultValue := "bar"
	schema := &ir.Schema{
		Name: "consumer",
		Kind: ir.SchemaKindGeneral,
		Imports: []ir.Import{
			{Package: "@parable-platform/shared", Types: []string{"ImportedEnvironment"}},
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

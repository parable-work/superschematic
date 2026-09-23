package apigen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestOpenAPIArrayLengthConstraints pins that listMin/listMax on an array
// field become minItems/maxItems, and that an undeclared bound is not
// emitted: a required array without listMin accepts [].
func TestOpenAPIArrayLengthConstraints(t *testing.T) {
	zero, one, three := 0, 1, 3
	for _, tc := range []struct {
		name string
		min  *int
		max  *int
	}{
		{"unconstrained", nil, nil},
		{"explicit empty allowed", &zero, nil},
		{"bounded selection", &one, &three},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := ir.NewSchema("array-contract", ir.SchemaKindGeneral)
			def := &ir.TypeDef{Name: "Input", Fields: []*ir.FieldDef{{
				Name: "values", TypeRef: ir.TypeRef{Name: "string", IsArray: true},
				Required: true, ValidateListMin: tc.min, ValidateListMax: tc.max,
			}}}
			result := openAPITypeSchema(def, map[string]interface{}{}, schema, nil, nil, nil, nil, map[string]bool{})
			field := result["properties"].(map[string]interface{})["values"].(map[string]interface{})
			for name, limit := range map[string]*int{"minItems": tc.min, "maxItems": tc.max} {
				got, present := field[name]
				if limit == nil {
					if present {
						t.Fatalf("undeclared %s was emitted: %v", name, got)
					}
				} else if got != *limit {
					t.Fatalf("%s = %v, want %d", name, got, *limit)
				}
			}
		})
	}
}

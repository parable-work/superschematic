package ir

import (
	"errors"
	"strings"
	"testing"
)

// envVarsSchema is an API schema of types, built in the order given.
func envVarsSchema(types ...*TypeDef) *Schema {
	s := NewSchema("finances-api", SchemaKindAPI)
	for _, td := range types {
		s.Types[td.Name] = td
	}
	return s
}

func envVarsClass(name, extends string) *TypeDef {
	return &TypeDef{Name: name, Owner: "src/" + name + ".schema.ts", EnvVars: true, Extends: extends}
}

func plainClass(name, extends string) *TypeDef {
	return &TypeDef{Name: name, Owner: "src/" + name + ".schema.ts", Extends: extends}
}

// permutations lists every order of types.
func permutations(types []*TypeDef) [][]*TypeDef {
	if len(types) <= 1 {
		return [][]*TypeDef{types}
	}
	var out [][]*TypeDef
	for i := range types {
		rest := append(append([]*TypeDef{}, types[:i]...), types[i+1:]...)
		for _, p := range permutations(rest) {
			out = append(out, append([]*TypeDef{types[i]}, p...))
		}
	}
	return out
}

// TestTheEnvVarsTypeIsTheOneNoOtherExtends: a schema's config is the
// @envVars class no other @envVars class extends, directly or through a
// plain class between them, whatever order the schema's types are built in
// and walked in; two or more such classes are refused, naming them (D94).
func TestTheEnvVarsTypeIsTheOneNoOtherExtends(t *testing.T) {
	for _, tc := range []struct {
		name  string
		types []*TypeDef
		want  string
		err   string
	}{
		{"none", []*TypeDef{plainClass("Category", "")}, "", ""},
		{"one", []*TypeDef{envVarsClass("FinancesConfig", ""), plainClass("Category", "")}, "FinancesConfig", ""},
		{"the documented pair", []*TypeDef{envVarsClass("FieldKeys", ""), envVarsClass("FinancesConfig", "FieldKeys")}, "FinancesConfig", ""},
		{"a chain", []*TypeDef{envVarsClass("FieldKeys", ""), envVarsClass("PlaidKeys", "FieldKeys"), envVarsClass("FinancesConfig", "PlaidKeys")}, "FinancesConfig", ""},
		{"through a plain class", []*TypeDef{envVarsClass("FieldKeys", ""), plainClass("Secrets", "FieldKeys"), envVarsClass("FinancesConfig", "Secrets")}, "FinancesConfig", ""},
		{"a base from another service", []*TypeDef{envVarsClass("FinancesConfig", "SharedKeys")}, "FinancesConfig", ""},
		{
			"two unrelated",
			[]*TypeDef{envVarsClass("PlaidConfig", ""), envVarsClass("FinancesConfig", "")},
			"",
			"finances-api has two @envVars classes, FinancesConfig (src/FinancesConfig.schema.ts) and PlaidConfig (src/PlaidConfig.schema.ts), and neither extends the other; a service's config is the one @envVars class no other extends, which holds the fields of every class it extends, so make one of them extend the other, or remove @envVars from one (D94)",
		},
		{
			"two on one base",
			[]*TypeDef{envVarsClass("FieldKeys", ""), envVarsClass("PlaidConfig", "FieldKeys"), envVarsClass("FinancesConfig", "FieldKeys")},
			"",
			"finances-api has two @envVars classes, FinancesConfig (src/FinancesConfig.schema.ts) and PlaidConfig (src/PlaidConfig.schema.ts), and neither extends the other;",
		},
		{
			"three",
			[]*TypeDef{envVarsClass("C", ""), envVarsClass("A", ""), envVarsClass("B", "")},
			"",
			"finances-api has 3 @envVars classes that no other extends, A (src/A.schema.ts), B (src/B.schema.ts) and C (src/C.schema.ts); a service's config is the one @envVars class no other extends, which holds the fields of every class it extends, so make them one chain, each extending the next, or remove @envVars from all but one (D94)",
		},
		{
			"a cycle",
			[]*TypeDef{envVarsClass("A", "B"), envVarsClass("B", "A")},
			"",
			"the @envVars classes of finances-api, A (src/A.schema.ts) and B (src/B.schema.ts), extend one another, so none is its config;",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, order := range permutations(tc.types) {
				// A fresh map per order, walked many times: each walk of a
				// Go map may take another order.
				schema := envVarsSchema(order...)
				for range 50 {
					got, err := schema.EnvVarsType()
					switch {
					case tc.err != "":
						var typesErr *EnvVarsTypesError
						if got != nil || !errors.As(err, &typesErr) || !strings.HasPrefix(err.Error(), tc.err) {
							t.Fatalf("EnvVarsType = %v, %v; want the error %q", got, err, tc.err)
						}
					case err != nil:
						t.Fatalf("EnvVarsType: %v", err)
					case tc.want == "" && got != nil:
						t.Fatalf("EnvVarsType = %s, want none", got.Name)
					case tc.want != "" && (got == nil || got.Name != tc.want):
						t.Fatalf("EnvVarsType = %v, want %s", got, tc.want)
					}
				}
			}
		})
	}
	var none *Schema
	if got, err := none.EnvVarsType(); got != nil || err != nil {
		t.Errorf("a nil schema's EnvVarsType = %v, %v", got, err)
	}
}

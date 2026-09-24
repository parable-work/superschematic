package ir

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestProjectionPredicateForms(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		name             string
		pred             *ProjectionPredicate
		literal, binding bool
	}{
		{"nil", nil, false, false},
		{"binding", &ProjectionPredicate{Column: "base.a", Setting: "app.a"}, false, true},
		{"optional guarded binding", &ProjectionPredicate{Column: "base.a", Setting: "app.a", Optional: true, When: &ProjectionCondition{Column: "base.b", Equals: "x"}}, false, true},
		{"anyOf", &ProjectionPredicate{AnyOf: []*ProjectionPredicate{{Column: "base.a", Setting: "app.a"}, {Column: "base.b", Setting: "app.b"}}}, false, false},
		{"isNull", &ProjectionPredicate{Column: "base.a", IsNull: true}, true, false},
		{"equals", &ProjectionPredicate{Column: "base.a", Equals: &ProjectionLiteral{Bool: &yes}}, true, false},
		{"function", &ProjectionPredicate{Function: "app.f", Args: []string{"base.id"}, RequiredSettings: []string{"app.a"}}, false, false},
		{"column only", &ProjectionPredicate{Column: "base.a"}, false, false},
	} {
		if got := tc.pred.IsLiteral(); got != tc.literal {
			t.Errorf("%s: IsLiteral = %v", tc.name, got)
		}
		if got := tc.pred.IsBinding(); got != tc.binding {
			t.Errorf("%s: IsBinding = %v", tc.name, got)
		}
	}
}

// TestProjectionLiteralRoundTrips: an equals literal keeps its type through
// the JSON and YAML forms of the IR.
func TestProjectionLiteralRoundTrips(t *testing.T) {
	s, n, b := "team", 1.5, false
	for _, lit := range []*ProjectionLiteral{{String: &s}, {Number: &n}, {Bool: &b}} {
		if lit.Set() != 1 {
			t.Fatalf("Set() = %d", lit.Set())
		}
		raw, err := json.Marshal(lit)
		if err != nil {
			t.Fatal(err)
		}
		var fromJSON ProjectionLiteral
		if err := json.Unmarshal(raw, &fromJSON); err != nil || fromJSON.Kind() != lit.Kind() {
			t.Fatalf("JSON %s decoded as %q: %v", raw, fromJSON.Kind(), err)
		}
		text, err := yaml.Marshal(lit)
		if err != nil {
			t.Fatal(err)
		}
		var fromYAML ProjectionLiteral
		if err := yaml.Unmarshal(text, &fromYAML); err != nil || fromYAML.Kind() != lit.Kind() {
			t.Fatalf("YAML %s decoded as %q: %v", text, fromYAML.Kind(), err)
		}
	}
	if (&ProjectionLiteral{}).Kind() != "" || (*ProjectionLiteral)(nil).Set() != 0 {
		t.Error("an empty literal has no kind")
	}
}

func TestProjectionSourceAndProjections(t *testing.T) {
	if got := (&FieldDef{Name: "slotKey"}).ProjectionSource(); got != "base.slotKey" {
		t.Errorf("default source = %q", got)
	}
	if got := (&FieldDef{Name: "handle", ProjectedFrom: "channel.handle"}).ProjectionSource(); got != "channel.handle" {
		t.Errorf("@column source = %q", got)
	}
	schema := NewSchema("s", SchemaKindDB)
	schema.Types["Zeta"] = &TypeDef{Name: "Zeta", Role: RoleProjection}
	schema.Types["Alpha"] = &TypeDef{Name: "Alpha", Role: RoleProjection}
	schema.Types["Table"] = &TypeDef{Name: "Table", Role: RoleDBTable}
	got := schema.Projections()
	if len(got) != 2 || got[0].Name != "Alpha" || got[1].Name != "Zeta" {
		t.Errorf("Projections() = %v, want Alpha, Zeta", got)
	}
}

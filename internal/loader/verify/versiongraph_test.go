package verify

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// recipeGraph returns a DB schema with a valid version graph: the Recipe
// root, a Step member ordered by position and an Ingredient member whose
// parent is a Step. The scalars carry the SQL mappings hydration gives them.
func recipeGraph() *ir.Schema {
	schema := ir.NewSchema("recipes", ir.SchemaKindDB)
	for name, sql := range map[string]string{"Identity.UUID": "UUID", "Generic.Int64": "BIGINT", "Generic.JSON": "JSONB"} {
		schema.Scalars[name] = &ir.ScalarDef{Name: name, TypeMappings: map[string]string{"sql": sql}}
	}
	key := func() *ir.FieldDef {
		return &ir.FieldDef{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true}
	}
	toRecipe := func() *ir.FieldDef {
		return &ir.FieldDef{Name: "recipe", TypeRef: ir.TypeRef{Name: "Recipe"}, Required: true, Relation: &ir.RelationDef{Type: "Recipe"}}
	}
	schema.Types["Recipe"] = &ir.TypeDef{
		Name: "Recipe", Role: ir.RoleDBTable, Owner: "src/recipe.schema.ts",
		VersionGraph: &ir.VersionGraphConfig{},
		Fields:       []*ir.FieldDef{key(), {Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
	}
	schema.Types["Step"] = &ir.TypeDef{
		Name: "Step", Role: ir.RoleDBTable, Owner: "src/recipe.schema.ts", Versioned: true,
		GraphMember: &ir.GraphMemberConfig{Graph: "Recipe", Order: "position"},
		Fields: []*ir.FieldDef{
			key(), toRecipe(),
			{Name: "position", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true},
			{Name: "timings", TypeRef: ir.TypeRef{Name: "Generic.JSON"}, Required: true, ConflictUnit: ir.ConflictUnitKeyed},
		},
	}
	schema.Types["Ingredient"] = &ir.TypeDef{
		Name: "Ingredient", Role: ir.RoleDBTable, Owner: "src/recipe.schema.ts", Versioned: true,
		GraphMember: &ir.GraphMemberConfig{Graph: "Recipe", Parent: &ir.GraphParent{Key: "stepKey", Of: "Step"}},
		Fields: []*ir.FieldDef{
			key(), toRecipe(),
			{Name: "stepKey", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "quantity", TypeRef: ir.TypeRef{Name: "string"}, Required: true, ConflictUnit: ir.ConflictUnitExcluded},
		},
	}
	return schema
}

// TestVersionGraphDeclarations runs every version graph rule once where it
// holds and once where it breaks. A case with no want must verify clean.
func TestVersionGraphDeclarations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *ir.Schema)
		want   string
	}{
		{name: "the valid graph", mutate: func(*ir.Schema) {}},

		// The root.
		{
			name:   "root on an embedded struct",
			mutate: func(s *ir.Schema) { s.Types["Recipe"].Role = ir.RoleEmbeddedStruct },
			want:   "Recipe: @versionGraph is only allowed on DB table types",
		},
		{
			name: "root with two keys",
			mutate: func(s *ir.Schema) {
				s.Types["Recipe"].Fields = append(s.Types["Recipe"].Fields, &ir.FieldDef{Name: "code", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true})
			},
			want: "Recipe: @versionGraph requires exactly one @key field, found 2",
		},
		{
			name:   "root with a string key",
			mutate: func(s *ir.Schema) { s.Types["Recipe"].Fields[0].TypeRef.Name = "string" },
			want:   "Recipe: @versionGraph requires a UUID @key, and id is string",
		},
		{
			name:   "a PascalCase graph name",
			mutate: func(s *ir.Schema) { s.Types["Recipe"].VersionGraph.Name = "Cookbook" },
		},
		{
			name:   "a graph name that is not PascalCase",
			mutate: func(s *ir.Schema) { s.Types["Recipe"].VersionGraph.Name = "cook_book" },
			want:   `Recipe: @versionGraph name "cook_book" must be a PascalCase identifier`,
		},
		{
			name:   "a positive schema epoch",
			mutate: func(s *ir.Schema) { s.Types["Recipe"].VersionGraph.SchemaEpoch = 3 },
		},
		{
			name:   "a negative schema epoch",
			mutate: func(s *ir.Schema) { s.Types["Recipe"].VersionGraph.SchemaEpoch = -1 },
			want:   "Recipe: @versionGraph schemaEpoch must not be negative",
		},
		{
			name:   "a root that is also a member",
			mutate: func(s *ir.Schema) { s.Types["Recipe"].GraphMember = &ir.GraphMemberConfig{Graph: "Recipe"} },
			want:   "Recipe: a type belongs to at most one version graph",
		},
		{
			name: "a graph with no member",
			mutate: func(s *ir.Schema) {
				delete(s.Types, "Step")
				delete(s.Types, "Ingredient")
			},
			want: "Recipe: @versionGraph needs at least one @graphMember type",
		},
		{
			name: "a generated type the schema already defines",
			mutate: func(s *ir.Schema) {
				s.Types["RecipeRef"] = &ir.TypeDef{Name: "RecipeRef", Role: ir.RoleEmbeddedStruct}
			},
			want: `Recipe: version graph "Recipe" generates RecipeRef, which the schema already defines`,
		},
		{
			name:   "a generated enum the schema already defines",
			mutate: func(s *ir.Schema) { s.Enums["RecipeEntityKind"] = &ir.EnumDef{Name: "RecipeEntityKind"} },
			want:   `generates RecipeEntityKind, which the schema already defines`,
		},
		{
			name: "a graph name that steps around a defined type",
			mutate: func(s *ir.Schema) {
				s.Types["RecipeRef"] = &ir.TypeDef{Name: "RecipeRef", Role: ir.RoleEmbeddedStruct}
				s.Types["Recipe"].VersionGraph.Name = "Cookbook"
			},
		},
		{
			name: "two graphs of one name",
			mutate: func(s *ir.Schema) {
				s.Types["Recipe"].VersionGraph.Name = "Book"
				s.Types["Menu"] = &ir.TypeDef{
					Name: "Menu", Role: ir.RoleDBTable, VersionGraph: &ir.VersionGraphConfig{Name: "Book"},
					Fields: []*ir.FieldDef{{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true}},
				}
				s.Types["Course"] = &ir.TypeDef{
					Name: "Course", Role: ir.RoleDBTable, Versioned: true, GraphMember: &ir.GraphMemberConfig{Graph: "Menu"},
					Fields: []*ir.FieldDef{
						{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true},
						{Name: "menu", TypeRef: ir.TypeRef{Name: "Menu"}, Relation: &ir.RelationDef{Type: "Menu"}},
					},
				}
			},
			want: `Recipe: version graph "Book" generates BookRef, which version graph "Book" also generates`,
		},

		// A member.
		{
			name:   "member on an embedded struct",
			mutate: func(s *ir.Schema) { s.Types["Step"].Role = ir.RoleEmbeddedStruct },
			want:   "Step: @graphMember is only allowed on DB table types",
		},
		{
			name:   "member without @versioned",
			mutate: func(s *ir.Schema) { s.Types["Step"].Versioned = false },
			want:   "Step: @graphMember requires @versioned",
		},
		{
			name:   "member without a key",
			mutate: func(s *ir.Schema) { s.Types["Step"].Fields[0].Key = false },
			want:   "Step: @graphMember requires exactly one @key field, found 0",
		},
		{
			name:   "member of a type that is not a root",
			mutate: func(s *ir.Schema) { s.Types["Ingredient"].GraphMember.Graph = "Step" },
			want:   `Ingredient: @graphMember graph "Step" is not a @versionGraph type of this schema`,
		},
		{
			name: "member with no relation to the root",
			mutate: func(s *ir.Schema) {
				s.Types["Step"].Fields = append(s.Types["Step"].Fields[:1], s.Types["Step"].Fields[2:]...)
			},
			want: "Step: @graphMember requires exactly one relation to the graph root Recipe, found 0",
		},
		{
			name: "member with two relations to the root",
			mutate: func(s *ir.Schema) {
				s.Types["Step"].Fields = append(s.Types["Step"].Fields, &ir.FieldDef{Name: "source", TypeRef: ir.TypeRef{Name: "Recipe"}})
			},
			want: "Step: @graphMember requires exactly one relation to the graph root Recipe, found 2",
		},
		{
			name: "member with deletedAt",
			mutate: func(s *ir.Schema) {
				s.Types["Step"].Fields = append(s.Types["Step"].Fields, &ir.FieldDef{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}})
			},
			want: "Step: a @graphMember cannot have deletedAt",
		},
		{
			name: "member with a field the graph adds",
			mutate: func(s *ir.Schema) {
				s.Types["Step"].Fields = append(s.Types["Step"].Fields, &ir.FieldDef{Name: "ref", TypeRef: ir.TypeRef{Name: "string"}})
			},
			want: "Step: field ref collides with the field @graphMember adds",
		},
		{
			name: "a parent of the member's own type",
			mutate: func(s *ir.Schema) {
				s.Types["Ingredient"].GraphMember.Parent.Of = "Ingredient"
			},
		},
		{
			name:   "a parent that is not a member of the graph",
			mutate: func(s *ir.Schema) { s.Types["Ingredient"].GraphMember.Parent.Of = "Recipe" },
			want:   `Ingredient: @graphMember parent.of "Recipe" is not a @graphMember type of graph Recipe`,
		},
		{
			name:   "a parent key that is not a field",
			mutate: func(s *ir.Schema) { s.Types["Ingredient"].GraphMember.Parent.Key = "stepId" },
			want:   `Ingredient: @graphMember parent.key "stepId" is not a field of Ingredient`,
		},
		{
			name:   "a parent key that is not a UUID",
			mutate: func(s *ir.Schema) { s.Types["Ingredient"].GraphMember.Parent.Key = "quantity" },
			want:   `Ingredient: @graphMember parent.key "quantity" must be a UUID field`,
		},
		{
			name:   "an order field that is not Int64",
			mutate: func(s *ir.Schema) { s.Types["Step"].Fields[2].TypeRef.Name = "string" },
			want:   `Step: @graphMember order "position" must name a Generic.Int64 field`,
		},
		{
			name:   "an order that names no field",
			mutate: func(s *ir.Schema) { s.Types["Step"].GraphMember.Order = "rank" },
			want:   `Step: @graphMember order "rank" must name a Generic.Int64 field`,
		},

		// Conflict units.
		{
			name: "jsonSchema on a @jsonField payload",
			mutate: func(s *ir.Schema) {
				s.Types["Step"].Fields[3].TypeRef.Name = "Schedule"
				s.Types["Step"].Fields[3].JsonField = true
				s.Types["Step"].Fields[3].ConflictUnit = ir.ConflictUnitJSONSchema
			},
		},
		{
			name:   "keyed on a field that is not JSON",
			mutate: func(s *ir.Schema) { s.Types["Ingredient"].Fields[3].ConflictUnit = ir.ConflictUnitKeyed },
			want:   `Ingredient.quantity: @conflictUnit("keyed") needs a JSON object field`,
		},
		{
			name:   "keyed on a list of JSON values",
			mutate: func(s *ir.Schema) { s.Types["Step"].Fields[3].TypeRef.IsArray = true },
			want:   `Step.timings: @conflictUnit("keyed") needs a JSON object field`,
		},
		{
			name:   "a strategy that does not exist",
			mutate: func(s *ir.Schema) { s.Types["Ingredient"].Fields[3].ConflictUnit = "rows" },
			want:   `Ingredient.quantity: @conflictUnit "rows" is not a strategy`,
		},
		{
			name:   "a conflict unit off a member",
			mutate: func(s *ir.Schema) { s.Types["Recipe"].Fields[1].ConflictUnit = ir.ConflictUnitAtomic },
			want:   "Recipe.title: @conflictUnit is only allowed on the fields of a @graphMember type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := recipeGraph()
			tc.mutate(schema)
			r := Run(schema, Input{})
			if tc.want == "" {
				if len(r.Errors) > 0 {
					t.Fatalf("want a clean verification, got %v", errorStrings(r))
				}
				return
			}
			if !hasError(r, tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, errorStrings(r))
			}
		})
	}
}

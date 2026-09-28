package verify

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// pantrySchema extends recipeGraph with Pantry, a @versioned table outside
// the graph, and Stock, a table that can pin Pantry's history rows by a
// relation (pantry_id), a plain UUID column (pantry_key) and an Int64
// (pantry_version).
func pantrySchema() *ir.Schema {
	schema := recipeGraph()
	schema.Scalars["Temporal.DateTime"] = &ir.ScalarDef{Name: "Temporal.DateTime", TypeMappings: map[string]string{"sql": "TIMESTAMPTZ"}}
	schema.Scalars["Identity.OtherUUID"] = &ir.ScalarDef{Name: "Identity.OtherUUID", TypeMappings: map[string]string{"sql": "uuid"}}
	schema.Types["Pantry"] = &ir.TypeDef{
		Name: "Pantry", Role: ir.RoleDBTable, Owner: "src/pantry.schema.ts", Versioned: true,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "recipe", TypeRef: ir.TypeRef{Name: "Recipe"}, Relation: &ir.RelationDef{Type: "Recipe"}},
			{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "shoppingNotes", TypeRef: ir.TypeRef{Name: "string"}},
			{Name: "updatedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}},
			{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
		},
	}
	schema.Types["Stock"] = &ir.TypeDef{
		Name: "Stock", Role: ir.RoleDBTable, Owner: "src/pantry.schema.ts",
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "pantry", TypeRef: ir.TypeRef{Name: "Pantry"}, Required: true, Relation: &ir.RelationDef{Type: "Pantry"}},
			{Name: "pantryKey", TypeRef: ir.TypeRef{Name: "Identity.OtherUUID"}, Required: true},
			{Name: "pantryVersion", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true},
			{Name: "count", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
			{Name: "tags", TypeRef: ir.TypeRef{Name: "Identity.UUID", IsArray: true}},
		},
	}
	return schema
}

// pinPantry declares one pruneKeepReferencedBy pin on Pantry.
func pinPantry(s *ir.Schema, table, keyColumn, versionColumn string) {
	days := 30
	s.Types["Pantry"].VersionedConfig = &ir.VersionedConfig{
		RetentionDays:         &days,
		PruneKeepReferencedBy: []*ir.PruneReference{{Table: table, KeyColumn: keyColumn, VersionColumn: versionColumn}},
	}
}

// excludeFrom declares @versioned({ exclude }) on a type.
func excludeFrom(s *ir.Schema, typeName string, names ...string) {
	s.Types[typeName].VersionedConfig = &ir.VersionedConfig{Exclude: names}
}

// TestVersionedExcludeOptimisticAndPins runs each rule of @versioned
// exclude, @optimistic, the _version collision and the pin columns once
// where it holds and once where it breaks. A case with no want must verify
// clean.
func TestVersionedExcludeOptimisticAndPins(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *ir.Schema)
		want   string
	}{
		{name: "the valid schema", mutate: func(*ir.Schema) {}},

		// exclude.
		{
			name:   "excluding plain and audit fields",
			mutate: func(s *ir.Schema) { excludeFrom(s, "Pantry", "shoppingNotes", "updatedBy") },
		},
		{
			name:   "excluding a field the type does not have",
			mutate: func(s *ir.Schema) { excludeFrom(s, "Pantry", "notes") },
			want:   `Pantry: @versioned exclude "notes" is not a field of Pantry`,
		},
		{
			name:   "excluding the key",
			mutate: func(s *ir.Schema) { excludeFrom(s, "Pantry", "id") },
			want:   "Pantry: @versioned exclude cannot name the key id",
		},
		{
			name:   "excluding a field twice",
			mutate: func(s *ir.Schema) { excludeFrom(s, "Pantry", "label", "label") },
			want:   "Pantry: @versioned exclude repeats label",
		},
		{
			name:   "excluding deletedAt",
			mutate: func(s *ir.Schema) { excludeFrom(s, "Pantry", "deletedAt") },
			want:   "Pantry: @versioned exclude cannot name deletedAt",
		},
		{
			name:   "excluding a relation",
			mutate: func(s *ir.Schema) { excludeFrom(s, "Pantry", "recipe") },
			want:   "Pantry: @versioned exclude cannot name the relation recipe",
		},
		{
			name: "a graph member excluding an excluded conflict unit and an audit field",
			mutate: func(s *ir.Schema) {
				s.Types["Ingredient"].Fields = append(s.Types["Ingredient"].Fields, &ir.FieldDef{Name: "createdAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true})
				excludeFrom(s, "Ingredient", "quantity", "createdAt")
			},
		},
		{
			name:   "a graph member excluding content",
			mutate: func(s *ir.Schema) { excludeFrom(s, "Step", "timings") },
			want:   "Step: a @graphMember may exclude only audit fields and fields with @conflictUnit(\"excluded\"), and timings is content",
		},

		// @optimistic.
		{
			name: "an optimistic table",
			mutate: func(s *ir.Schema) {
				s.Types["Stock"].Optimistic = true
			},
		},
		{
			name: "an optimistic table with no key",
			mutate: func(s *ir.Schema) {
				s.Types["Stock"].Optimistic = true
				s.Types["Stock"].Fields[0].Key = false
			},
		},
		{
			name: "an optimistic embedded struct",
			mutate: func(s *ir.Schema) {
				s.Types["Stock"].Optimistic = true
				s.Types["Stock"].Role = ir.RoleEmbeddedStruct
			},
			want: "Stock: @optimistic is only allowed on DB table types (this type has role EmbeddedStruct)",
		},
		{
			name:   "a versioned table that is also optimistic",
			mutate: func(s *ir.Schema) { s.Types["Pantry"].Optimistic = true },
			want:   "Pantry: @versioned implies @optimistic; declare one of them",
		},

		// The generated _version field.
		{
			name: "a versioned table declaring version",
			mutate: func(s *ir.Schema) {
				s.Types["Pantry"].Fields = append(s.Types["Pantry"].Fields, &ir.FieldDef{Name: "version", TypeRef: ir.TypeRef{Name: "string"}})
			},
			want: "Pantry.version: a @versioned type cannot declare a field named version",
		},
		{
			name: "an optimistic table declaring _version",
			mutate: func(s *ir.Schema) {
				s.Types["Stock"].Optimistic = true
				s.Types["Stock"].Fields = append(s.Types["Stock"].Fields, &ir.FieldDef{Name: "_version", TypeRef: ir.TypeRef{Name: "Generic.Int64"}})
			},
			want: "Stock._version: a @optimistic type cannot declare a field named _version",
		},
		{
			name: "a plain table declaring version",
			mutate: func(s *ir.Schema) {
				s.Types["Stock"].Fields = append(s.Types["Stock"].Fields, &ir.FieldDef{Name: "version", TypeRef: ir.TypeRef{Name: "string"}})
			},
		},

		// pruneKeepReferencedBy pins.
		{
			name:   "a pin by a relation column",
			mutate: func(s *ir.Schema) { pinPantry(s, "stock", "pantry_id", "pantry_version") },
		},
		{
			name:   "a pin by a UUID column of another UUID scalar",
			mutate: func(s *ir.Schema) { pinPantry(s, "stock", "pantry_key", "pantry_version") },
		},
		{
			name: "a pin by an optimistic table's own _version",
			mutate: func(s *ir.Schema) {
				s.Types["Stock"].Optimistic = true
				pinPantry(s, "stock", "pantry_id", "_version")
			},
		},
		{
			name:   "a pin on a table outside the schema",
			mutate: func(s *ir.Schema) { pinPantry(s, "pantry_audit", "anything", "at_all") },
		},
		{
			name:   "a pin whose key column is missing",
			mutate: func(s *ir.Schema) { pinPantry(s, "stock", "pantry", "pantry_version") },
			want:   "Pantry: @versioned pruneKeepReferencedBy stock: keyColumn pantry is not a column of Stock",
		},
		{
			name:   "a pin whose key column holds another type",
			mutate: func(s *ir.Schema) { pinPantry(s, "stock", "count", "pantry_version") },
			want:   "Pantry: @versioned pruneKeepReferencedBy stock: keyColumn count must hold the key type of Pantry (Identity.UUID)",
		},
		{
			name:   "a pin whose key column is a list",
			mutate: func(s *ir.Schema) { pinPantry(s, "stock", "tags", "pantry_version") },
			want:   "keyColumn tags must hold the key type of Pantry",
		},
		{
			name:   "a pin whose version column is missing",
			mutate: func(s *ir.Schema) { pinPantry(s, "stock", "pantry_id", "_version") },
			want:   "Pantry: @versioned pruneKeepReferencedBy stock: versionColumn _version is not a column of Stock",
		},
		{
			name:   "a pin whose version column is not an Int64",
			mutate: func(s *ir.Schema) { pinPantry(s, "stock", "pantry_id", "count") },
			want:   "Pantry: @versioned pruneKeepReferencedBy stock: versionColumn count must be a Generic.Int64 column",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := pantrySchema()
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

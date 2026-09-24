package verify

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// projectionSchema builds a DB schema with a Preference table left-joined to
// a Channel table and one projection over them, so each case can break
// exactly one rule.
func projectionSchema(mutate func(schema *ir.Schema, td *ir.TypeDef)) *ir.Schema {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	for _, name := range []string{"Identity.UUID", "Identity.Name", "Generic.JSON", "Temporal.DateTime"} {
		schema.Scalars[name] = &ir.ScalarDef{Name: name, LanguagePrimitive: ir.LanguageString}
	}
	schema.Scalars["Generic.Int64"] = &ir.ScalarDef{Name: "Generic.Int64", LanguagePrimitive: ir.LanguageNumber}
	schema.Enums["PreferenceScope"] = &ir.EnumDef{Name: "PreferenceScope", Values: []ir.EnumValueDef{
		{Name: "App", SerializedAs: "app"}, {Name: "Team", SerializedAs: "team"}, {Name: "User", SerializedAs: "user"},
	}}
	schema.Types["Channel"] = &ir.TypeDef{
		Name: "Channel", Role: ir.RoleDBTable, Owner: "src/channel.schema.ts",
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "name", TypeRef: ir.TypeRef{Name: "Identity.Name"}, Required: true},
		},
	}
	schema.Types["Preference"] = &ir.TypeDef{
		Name: "Preference", Role: ir.RoleDBTable, Owner: "src/preference.schema.ts",
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "account", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "channel", TypeRef: ir.TypeRef{Name: "Channel"}},
			{Name: "value", TypeRef: ir.TypeRef{Name: "Generic.JSON"}, Required: true},
			{Name: "scope", TypeRef: ir.TypeRef{Name: "PreferenceScope"}, Required: true},
			{Name: "hidden", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true},
			{Name: "revision", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true},
			{Name: "archivedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
			{Name: "tags", TypeRef: ir.TypeRef{Name: "Identity.Name", IsArray: true}, Required: true},
		},
	}
	td := &ir.TypeDef{
		Name: "AppPreference", Role: ir.RoleProjection, Owner: "src/app-preference.projection.schema.ts",
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "channelId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, ProjectedFrom: "base.channel"},
			{Name: "channelName", TypeRef: ir.TypeRef{Name: "Identity.Name"}, ProjectedFrom: "channel.name"},
			{Name: "value", TypeRef: ir.TypeRef{Name: "Generic.JSON"}, Required: true},
		},
		Projection: &ir.ProjectionDef{
			Pool: "app", Name: "preferences", Migration: "20260902120000", Source: "Preference",
			Joins: []*ir.ProjectionJoin{{
				Type: "Channel", Alias: "channel", Kind: "left",
				On: []*ir.ProjectionJoinKey{{Left: "channel.id", Right: "base.channel"}},
			}},
			Predicates: []*ir.ProjectionPredicate{{Column: "base.account", Setting: "app.account_id"}},
		},
	}
	schema.Types[td.Name] = td
	if mutate != nil {
		mutate(schema, td)
	}
	return schema
}

func projectionErrors(t *testing.T, schema *ir.Schema) []string {
	t.Helper()
	r := &Result{}
	checkProjections(schema, r)
	var msgs []string
	for _, d := range r.Errors {
		msgs = append(msgs, d.Msg)
	}
	return msgs
}

func containsMessage(msgs []string, want string) bool {
	for _, msg := range msgs {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

func TestProjectionValidPassesVerification(t *testing.T) {
	if errs := projectionErrors(t, projectionSchema(nil)); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

// TestProjectionWithoutRowRulesPassesVerification: the core does not
// require a row rule; a distribution that does registers a check.
func TestProjectionWithoutRowRulesPassesVerification(t *testing.T) {
	schema := projectionSchema(func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Predicates = nil })
	if errs := projectionErrors(t, schema); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestComputedProjectionColumnValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    *ir.ProjectionFunctionCall
		from    string
		outside bool
		want    string
	}{
		{"valid", &ir.ProjectionFunctionCall{Function: "app.preference_policy_key", Args: []string{"base.id"}}, "", false, ""},
		{"qualified name required", &ir.ProjectionFunctionCall{Function: "f", Args: []string{"base.id"}}, "", false, "schema.function"},
		{"function SQL injection", &ir.ProjectionFunctionCall{Function: "app.f); DROP TABLE x;--", Args: []string{"base.id"}}, "", false, "schema.function"},
		{"no arguments", &ir.ProjectionFunctionCall{Function: "app.f"}, "", false, "non-empty alias.field args"},
		{"argument SQL expression", &ir.ProjectionFunctionCall{Function: "app.f", Args: []string{"base.id OR TRUE"}}, "", false, "no field"},
		{"literal argument", &ir.ProjectionFunctionCall{Function: "app.f", Args: []string{"'value'"}}, "", false, "alias.field"},
		{"unknown alias", &ir.ProjectionFunctionCall{Function: "app.f", Args: []string{"other.id"}}, "", false, "unknown alias"},
		{"unknown field", &ir.ProjectionFunctionCall{Function: "app.f", Args: []string{"base.absent"}}, "", false, "no field"},
		{"source and function", &ir.ProjectionFunctionCall{Function: "app.f", Args: []string{"base.id"}}, "base.id", false, "not both"},
		{"outside projection", &ir.ProjectionFunctionCall{Function: "app.f", Args: []string{"base.id"}}, "", true, "only valid on a @projection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := projectionSchema(func(s *ir.Schema, td *ir.TypeDef) {
				field := &ir.FieldDef{Name: "policyKey", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, ProjectedFunction: tc.call, ProjectedFrom: tc.from}
				if tc.outside {
					s.Types["Channel"].Fields = append(s.Types["Channel"].Fields, field)
				} else {
					td.Fields = append(td.Fields, field)
				}
			})
			errs := strings.Join(projectionErrors(t, schema), "\n")
			if tc.want == "" && errs != "" || tc.want != "" && !strings.Contains(errs, tc.want) {
				t.Fatalf("want %q, got %q", tc.want, errs)
			}
		})
	}
}

func TestProjectionRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(schema *ir.Schema, td *ir.TypeDef)
		want   string
	}{
		{"setting without a dot", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Predicates[0].Setting = "account_id" }, "dotted custom Postgres setting"},
		{"unknown rule column", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Predicates[0].Column = "base.owner" }, `no field "owner"`},
		{"rule without a setting", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Predicates[0].Setting = "" }, "dotted custom Postgres setting"},
		{"unknown guard column", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Predicates[0].When = &ir.ProjectionCondition{Column: "base.kind", Equals: "x"}
		}, `when.column "base.kind"`},
		{"unknown alias", func(_ *ir.Schema, td *ir.TypeDef) { td.Fields[2].ProjectedFrom = "store.name" }, `unknown alias "store"`},
		{"type drift", func(_ *ir.Schema, td *ir.TypeDef) { td.Fields[3].TypeRef.Name = "Identity.Name" }, "keeps its source column's type"},
		{"relation exposed as object", func(_ *ir.Schema, td *ir.TypeDef) { td.Fields[1].TypeRef.Name = "Channel" }, "expose the key column"},
		{"required over a left join", func(_ *ir.Schema, td *ir.TypeDef) { td.Fields[2].Required = true }, "comes through a left join"},
		{"required over a nullable column", func(_ *ir.Schema, td *ir.TypeDef) { td.Fields[1].Required = true }, "is nullable"},
		{"table decorator on a column", func(_ *ir.Schema, td *ir.TypeDef) { td.Fields[0].Key = true }, "table decorators and wrappers are not allowed"},
		{"map column", func(_ *ir.Schema, td *ir.TypeDef) { td.Fields[3].TypeRef.IsMap = true }, "cannot be maps"},
		{"source is not a table", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Source = "Nope" }, "is not a DB table type"},
		{"join table is not a table", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Joins[0].Type = "Nope" }, `@join table "Nope"`},
		{"join alias base", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Joins[0].Alias = "base" }, "already in use"},
		{"join alias not an identifier", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Joins[0].Alias = "Channel" }, "@join alias must be a snake_case"},
		{"join kind", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Joins[0].Kind = "full" }, `kind must be "inner" or "left"`},
		{"join never touches its alias", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Joins[0].On = []*ir.ProjectionJoinKey{{Left: "base.id", Right: "base.channel"}}
		}, "never references alias"},
		{"join without a condition", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Joins[0].On = nil }, "needs at least one alias.field equality"},
		{"bad migration stamp", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Migration = "v2" }, "14-digit"},
		{"bad view name", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Name = "Preferences" }, "snake_case SQL identifier"},
		{"bad pool", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Pool = "" }, "@projection pool must be a snake_case SQL identifier"},
		{"no columns", func(_ *ir.Schema, td *ir.TypeDef) { td.Fields = nil }, "at least one column"},
		{"column decorator outside a projection", func(schema *ir.Schema, _ *ir.TypeDef) {
			schema.Types["Channel"].Fields[1].ProjectedFrom = "base.name"
		}, "only valid on a @projection class"},
		{"join without @projection", func(schema *ir.Schema, _ *ir.TypeDef) {
			schema.Types["Channel"].Projection = &ir.ProjectionDef{Joins: []*ir.ProjectionJoin{{Type: "Preference", Alias: "p"}}}
		}, "need @projection on the class"},
		{"projection without a declaration", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection = nil }, "has no @projection declaration"},
		{"projection outside a DB schema", func(schema *ir.Schema, _ *ir.TypeDef) { schema.Kind = ir.SchemaKindGeneral }, "only allowed in DB schemas"},
		{"heritage on a projection", func(_ *ir.Schema, td *ir.TypeDef) { td.Extends = "Channel" }, "cannot extend or implement"},
		{"table decorator on a projection", func(_ *ir.Schema, td *ir.TypeDef) { td.Versioned = true }, "carries only @projection and @join"},
		{"anyOf with one alternative", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Predicates = []*ir.ProjectionPredicate{{AnyOf: []*ir.ProjectionPredicate{
				{Column: "base.channel", Setting: "app.channel_id", Optional: true},
			}}}
		}, "at least two alternatives"},
		{"anyOf alternative with a guard", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Predicates = []*ir.ProjectionPredicate{{AnyOf: []*ir.ProjectionPredicate{
				{Column: "base.channel", Setting: "app.channel_id", When: &ir.ProjectionCondition{Column: "base.scope", Equals: "app"}},
				{Column: "base.account", Setting: "app.account_id"},
			}}}
		}, "carries only column, setting and optional"},
		{"anyOf alternative with an unknown column", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Predicates = []*ir.ProjectionPredicate{{AnyOf: []*ir.ProjectionPredicate{
				{Column: "base.channel", Setting: "app.channel_id"},
				{Column: "base.commit", Setting: "app.commit_id"},
			}}}
		}, `no field "commit"`},
		{"anyOf with a binding", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Predicates = []*ir.ProjectionPredicate{{Column: "base.account", AnyOf: []*ir.ProjectionPredicate{
				{Column: "base.channel", Setting: "app.channel_id"},
				{Column: "base.account", Setting: "app.account_id"},
			}}}
		}, "either anyOf or a column and setting"},
		{"function rule without required settings", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Predicates = []*ir.ProjectionPredicate{{Function: "app.visible", Args: []string{"base.id"}}}
		}, "function rule takes function, args and requiredSettings only"},
		{"function rule with a bad setting", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Predicates = []*ir.ProjectionPredicate{{Function: "app.visible", Args: []string{"base.id"}, RequiredSettings: []string{"user"}}}
		}, "required setting must be a dotted"},
		{"args without a function", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Predicates[0].Args = []string{"base.id"} }, "require function"},
		{"collapse over an unknown column", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Collapse = &ir.ProjectionCollapse{By: []string{"base.slotKey"}}
		}, `collapse.by[0] "base.slotKey"`},
		{"collapse without a key", func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Collapse = &ir.ProjectionCollapse{} }, "collapse.by must name at least one column"},
		{"collapse order with rank and direction", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Collapse = &ir.ProjectionCollapse{By: []string{"base.id"}, Order: []*ir.ProjectionOrder{
				{Column: "base.scope", Rank: []string{"user"}, Direction: "desc"},
			}}
		}, "either rank or direction/nulls"},
		{"collapse order with a bad direction", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Collapse = &ir.ProjectionCollapse{By: []string{"base.id"}, Order: []*ir.ProjectionOrder{
				{Column: "base.scope", Direction: "sideways"},
			}}
		}, "direction must be"},
		{"collapse order with bad nulls", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Collapse = &ir.ProjectionCollapse{By: []string{"base.id"}, Order: []*ir.ProjectionOrder{
				{Column: "base.scope", Nulls: "middle"},
			}}
		}, "nulls must be"},
		{"collapse order with a repeated rank", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Collapse = &ir.ProjectionCollapse{By: []string{"base.id"}, Order: []*ir.ProjectionOrder{
				{Column: "base.scope", Rank: []string{"user", "user"}},
			}}
		}, "twice"},
		{"duplicate relation", func(schema *ir.Schema, td *ir.TypeDef) {
			dup := *td
			dup.Name = "Twin"
			dup.Projection = &ir.ProjectionDef{}
			*dup.Projection = *td.Projection
			dup.Projection.Migration = "20260902120001"
			schema.Types["Twin"] = &dup
		}, "is already declared by"},
		{"duplicate migration stamp", func(schema *ir.Schema, td *ir.TypeDef) {
			dup := *td
			dup.Name = "Twin"
			dup.Projection = &ir.ProjectionDef{}
			*dup.Projection = *td.Projection
			dup.Projection.Name = "twins"
			schema.Types["Twin"] = &dup
		}, "is already used by"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := projectionErrors(t, projectionSchema(tc.mutate))
			if !containsMessage(errs, tc.want) {
				t.Errorf("expected an error containing %q, got:\n%s", tc.want, strings.Join(errs, "\n"))
			}
		})
	}
}

func literalPredicate(pred *ir.ProjectionPredicate) func(*ir.Schema, *ir.TypeDef) {
	return func(_ *ir.Schema, td *ir.TypeDef) { td.Projection.Predicates = []*ir.ProjectionPredicate{pred} }
}

func strLit(s string) *ir.ProjectionLiteral  { return &ir.ProjectionLiteral{String: &s} }
func numLit(n float64) *ir.ProjectionLiteral { return &ir.ProjectionLiteral{Number: &n} }
func boolLit(b bool) *ir.ProjectionLiteral   { return &ir.ProjectionLiteral{Bool: &b} }

// TestLiteralPredicateValidation covers the setting-free row rules: isNull,
// notNull and a typed equals. A literal must agree with its column's type,
// an enum literal must name a member, and the forms never mix with a
// setting binding or an anyOf.
func TestLiteralPredicateValidation(t *testing.T) {
	valid := []struct {
		name string
		pred *ir.ProjectionPredicate
	}{
		{"isNull", &ir.ProjectionPredicate{Column: "base.archivedAt", IsNull: true}},
		{"notNull", &ir.ProjectionPredicate{Column: "base.channel", NotNull: true}},
		{"guarded notNull", &ir.ProjectionPredicate{Column: "base.channel", NotNull: true, When: &ir.ProjectionCondition{Column: "base.scope", Equals: "user"}}},
		{"equals string", &ir.ProjectionPredicate{Column: "channel.name", Equals: strLit("stable")}},
		{"equals enum member", &ir.ProjectionPredicate{Column: "base.scope", Equals: strLit("team")}},
		{"equals boolean", &ir.ProjectionPredicate{Column: "base.hidden", Equals: boolLit(false)}},
		{"equals number", &ir.ProjectionPredicate{Column: "base.revision", Equals: numLit(1)}},
		{"equals relation key", &ir.ProjectionPredicate{Column: "base.channel", Equals: strLit("00000000-0000-0000-0000-000000000001")}},
	}
	for _, tc := range valid {
		t.Run("valid "+tc.name, func(t *testing.T) {
			if errs := projectionErrors(t, projectionSchema(literalPredicate(tc.pred))); len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
		})
	}
	refused := []struct {
		name   string
		mutate func(schema *ir.Schema, td *ir.TypeDef)
		want   string
	}{
		{"two forms", literalPredicate(&ir.ProjectionPredicate{Column: "base.archivedAt", IsNull: true, NotNull: true}), "exactly one of isNull, notNull and equals"},
		{"literal with a setting", literalPredicate(&ir.ProjectionPredicate{Column: "base.archivedAt", IsNull: true, Setting: "app.x"}), "reads no setting"},
		{"optional literal", literalPredicate(&ir.ProjectionPredicate{Column: "base.archivedAt", IsNull: true, Optional: true}), "reads no setting"},
		{"unknown column", literalPredicate(&ir.ProjectionPredicate{Column: "base.retiredAt", IsNull: true}), `no field "retiredAt"`},
		{"array column", literalPredicate(&ir.ProjectionPredicate{Column: "base.tags", IsNull: true}), "must be a scalar, enum, boolean or relation column"},
		{"enum non-member", literalPredicate(&ir.ProjectionPredicate{Column: "base.scope", Equals: strLit("everyone")}), "is not a member of enum PreferenceScope"},
		{"boolean literal on a string column", literalPredicate(&ir.ProjectionPredicate{Column: "channel.name", Equals: boolLit(true)}), "must be a string literal"},
		{"number literal on an enum column", literalPredicate(&ir.ProjectionPredicate{Column: "base.scope", Equals: numLit(2)}), "must be a string literal"},
		{"string literal on a boolean column", literalPredicate(&ir.ProjectionPredicate{Column: "base.hidden", Equals: strLit("false")}), "must be a boolean literal"},
		{"string literal on a numeric column", literalPredicate(&ir.ProjectionPredicate{Column: "base.revision", Equals: strLit("1")}), "must be a number literal"},
		{"empty equals", literalPredicate(&ir.ProjectionPredicate{Column: "base.hidden", Equals: &ir.ProjectionLiteral{}}), "exactly one string, number or boolean literal"},
		{"literal inside anyOf", func(_ *ir.Schema, td *ir.TypeDef) {
			td.Projection.Predicates = []*ir.ProjectionPredicate{{AnyOf: []*ir.ProjectionPredicate{
				{Column: "base.channel", Setting: "app.channel_id"},
				{Column: "base.archivedAt", IsNull: true},
			}}}
		}, "carries only column, setting and optional"},
	}
	for _, tc := range refused {
		t.Run("refused "+tc.name, func(t *testing.T) {
			errs := projectionErrors(t, projectionSchema(tc.mutate))
			if !containsMessage(errs, tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, errs)
			}
		})
	}
}

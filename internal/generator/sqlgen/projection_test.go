package sqlgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

func loadProjectionFixture(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-projection"))
	if err != nil {
		t.Fatalf("load fixture-projection: %v", err)
	}
	return schema
}

// TestWriteProjectionsGolden generates the fixture-projection service (a
// view over one table with an inner and a left join, setting bindings,
// optional alternatives, literal rules and a collapse) and compares
// create.sql, drop.sql, the migration pair, the Arrow schema and the docs
// file against their golden copies. Regenerate with:
// go test ./internal/generator/sqlgen -run TestWriteProjectionsGolden -update
func TestWriteProjectionsGolden(t *testing.T) {
	schema := loadProjectionFixture(t)
	output, err := Generate(schema, Options{
		SchemaName: "fixture-projection",
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(output.Projections) != 1 {
		t.Fatalf("expected one projection, got %d", len(output.Projections))
	}
	view := output.Projections[0]
	if view.Relation != "app.preferences" {
		t.Errorf("relation = %q", view.Relation)
	}
	if want := "app.account_id,app.channel_id,app.user_id,app.branch_id,app.commit_id"; strings.Join(view.Settings, ",") != want {
		t.Errorf("settings = %v, want %s", view.Settings, want)
	}

	outDir := t.TempDir()
	migrationsDir := filepath.Join(outDir, "migrations")
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	if err := WriteProjections(schema, output, outDir, migrationsDir); err != nil {
		t.Fatalf("write projections: %v", err)
	}

	goldenDir := filepath.Join("testdata", "golden", "fixture-projection")
	files := map[string]string{
		"create.sql":                 filepath.Join(outDir, "create.sql"),
		"drop.sql":                   filepath.Join(outDir, "drop.sql"),
		view.UpFileName:              filepath.Join(migrationsDir, view.UpFileName),
		view.DownFileName:            filepath.Join(migrationsDir, view.DownFileName),
		"app.preferences.arrow.json": filepath.Join(outDir, ProjectionsSubdir, "app.preferences.arrow.json"),
		"app.preferences.docs.json":  filepath.Join(outDir, ProjectionsSubdir, "app.preferences.docs.json"),
	}
	for name, path := range files {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", name, err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n--- got ---\n%s", name, got)
		}
	}

	// The Arrow document has the shape arrow-rs's serde form expects: fields
	// with the six Field keys and schema-level metadata.
	arrow, err := os.ReadFile(files["app.preferences.arrow.json"])
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Fields []map[string]any  `json:"fields"`
		Meta   map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(arrow, &doc); err != nil {
		t.Fatalf("arrow json: %v", err)
	}
	if len(doc.Fields) != len(view.Columns) {
		t.Errorf("arrow fields = %d, columns = %d", len(doc.Fields), len(view.Columns))
	}
	for _, f := range doc.Fields {
		for _, key := range []string{"name", "data_type", "nullable", "dict_id", "dict_is_ordered", "metadata"} {
			if _, ok := f[key]; !ok {
				t.Errorf("arrow field %v lacks %q", f["name"], key)
			}
		}
	}
	keys := NewMetadataKeys(DefaultMetadataKeyPrefix)
	if got := doc.Meta[keys.ProjectionSetting]; got != "app.account_id,app.channel_id,app.user_id,app.branch_id,app.commit_id" {
		t.Errorf("settings metadata = %q", got)
	}
	if got := doc.Meta[keys.ProjectionOptionalSettings]; got != "app.branch_id,app.commit_id" {
		t.Errorf("optional settings metadata = %q", got)
	}
	if got := doc.Meta[keys.ProjectionRows]; got != "one" {
		t.Errorf("rows metadata = %q, want one (the fixture collapses by slot)", got)
	}
	up, err := os.ReadFile(files[view.UpFileName])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`SELECT DISTINCT ON (base.slot_key)`,
		`ORDER BY base.slot_key ASC, CASE base.scope WHEN 'user' THEN 0 WHEN 'team' THEN 1 WHEN 'app' THEN 2 ELSE 3 END ASC, base.branch_id ASC NULLS LAST;`,
		`(base.scope IS DISTINCT FROM 'app' OR (base.branch_id = NULLIF(current_setting('app.branch_id', true), '')::uuid OR base."commit" = NULLIF(current_setting('app.commit_id', true), '')::uuid))`,
		`WHERE base.account = current_setting('app.account_id')::uuid`,
		`LEFT JOIN channel AS branch ON base.branch_id = branch.id`,
	} {
		if !strings.Contains(string(up), want) {
			t.Errorf("migration lacks %q:\n%s", want, up)
		}
	}
}

// TestMetadataKeyPrefixNamesEveryKey: every Arrow metadata key carries the
// configured prefix and nothing else, so a deployment whose readers expect
// another namespace changes one naming key.
func TestMetadataKeyPrefixNamesEveryKey(t *testing.T) {
	schema := loadProjectionFixture(t)
	output, err := Generate(schema, Options{SchemaName: "fixture-projection", MetadataKeyPrefix: "acme."})
	if err != nil {
		t.Fatal(err)
	}
	if output.MetadataKeyPrefix != "acme." {
		t.Fatalf("DDLOutput.MetadataKeyPrefix = %q", output.MetadataKeyPrefix)
	}
	arrow, err := ArrowSchemaJSON(schema, "fixture-projection", output.Projections[0], output.MetadataKeyPrefix)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Fields []struct {
			Metadata map[string]string `json:"metadata"`
		} `json:"fields"`
		Meta map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(arrow, &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	check := func(metadata map[string]string) {
		for key := range metadata {
			if !strings.HasPrefix(key, "acme.") {
				t.Errorf("metadata key %q does not carry the prefix", key)
			}
			seen[strings.TrimPrefix(key, "acme.")] = true
		}
	}
	check(doc.Meta)
	for _, f := range doc.Fields {
		check(f.Metadata)
	}
	for _, suffix := range []string{"projection.source", "projection.settings", "projection.rows", "scalar.canonical_name", "enum.values"} {
		if !seen[suffix] {
			t.Errorf("no acme.%s key in the Arrow schema", suffix)
		}
	}
	if !strings.Contains(string(arrow), `"acme.enum.values": "app,team,user"`) {
		t.Errorf("enum values metadata missing:\n%s", arrow)
	}
}

func TestSettingCast(t *testing.T) {
	cases := map[string]string{
		"UUID": "::uuid", "BIGINT": "::bigint", "INTEGER": "::integer", "BOOLEAN": "::boolean",
		"TIMESTAMPTZ": "::timestamptz", "DATE": "::date", "TEXT": "", "CITEXT": "", "VARCHAR(80)": "",
	}
	for sqlType, want := range cases {
		got, err := settingCast(sqlType)
		if err != nil || got != want {
			t.Errorf("settingCast(%s) = %q, %v; want %q", sqlType, got, err, want)
		}
	}
	if _, err := settingCast("JSONB"); err == nil {
		t.Error("a JSONB column must not be bindable to a setting")
	}
}

func TestArrowDataType(t *testing.T) {
	for sqlType, want := range map[string]string{
		"UUID": `"Utf8"`, "CITEXT": `"Utf8"`, "VARCHAR(80)": `"Utf8"`, "JSONB": `"Utf8"`, "LTREE": `"Utf8"`,
		"BIGINT": `"Int64"`, "INTEGER": `"Int32"`, "SMALLINT": `"Int16"`, "BOOLEAN": `"Boolean"`,
		"DOUBLE PRECISION": `"Float64"`, "REAL": `"Float32"`, "DATE": `"Date32"`, "BYTEA": `"Binary"`,
		"TIMESTAMPTZ": `{"Timestamp":["Microsecond","UTC"]}`,
		"TIMESTAMP":   `{"Timestamp":["Microsecond",null]}`,
		"TIME":        `{"Time64":"Microsecond"}`,
		"CITEXT[]":    `{"List":{"data_type":"Utf8","dict_id":0,"dict_is_ordered":false,"metadata":{},"name":"item","nullable":true}}`,
	} {
		v, err := arrowDataType(sqlType)
		if err != nil {
			t.Errorf("arrowDataType(%s): %v", sqlType, err)
			continue
		}
		got, _ := json.Marshal(v)
		if string(got) != want {
			t.Errorf("arrowDataType(%s) = %s, want %s", sqlType, got, want)
		}
	}
	if _, err := arrowDataType("POINT"); err == nil {
		t.Error("an unmapped SQL type must be refused, not guessed")
	}
}

// TestProjectionWithoutRowRulesGenerates: the core renders a view with no
// WHERE clause; a deployment that requires rules registers a verify rule.
func TestProjectionWithoutRowRulesGenerates(t *testing.T) {
	schema := loadProjectionFixture(t)
	def := schema.Types["AppPreference"].Projection
	def.Predicates = nil
	def.Collapse = nil
	output, err := Generate(schema, Options{SchemaName: "fixture-projection"})
	if err != nil {
		t.Fatal(err)
	}
	view := output.Projections[0]
	if len(view.Predicates) != 0 || len(view.Settings) != 0 {
		t.Fatalf("predicates %v, settings %v", view.Predicates, view.Settings)
	}
	ddl := projectionViewDDL(view)
	if strings.Contains(ddl, "WHERE") || strings.Contains(ddl, "ORDER BY") {
		t.Fatalf("a rule-free view rendered a WHERE or ORDER BY:\n%s", ddl)
	}
}

// TestProjectionUnsafeNamesAreRefusedAtGeneration: an IR that reaches the
// generator without verification (a hand-built or data-form schema) still
// cannot put an unchecked name into the DDL.
func TestProjectionUnsafeNamesAreRefusedAtGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(def *ir.ProjectionDef)
	}{
		{"pool", func(def *ir.ProjectionDef) { def.Pool = "app; DROP TABLE preference" }},
		{"view name", func(def *ir.ProjectionDef) { def.Name = `prefs"` }},
		{"migration stamp", func(def *ir.ProjectionDef) { def.Migration = "../../../etc" }},
		{"join alias", func(def *ir.ProjectionDef) { def.Joins[0].Alias = "channel; --" }},
		{"join alias base", func(def *ir.ProjectionDef) { def.Joins[0].Alias = "base" }},
		{"join kind", func(def *ir.ProjectionDef) { def.Joins[0].Kind = "cross" }},
		{"setting", func(def *ir.ProjectionDef) { def.Predicates[0].Setting = "app.account_id') OR true --" }},
		{"single anyOf", func(def *ir.ProjectionDef) { def.Predicates[3].AnyOf = def.Predicates[3].AnyOf[:1] }},
		{"guard inside anyOf", func(def *ir.ProjectionDef) {
			def.Predicates[3].AnyOf[0].When = &ir.ProjectionCondition{Column: "base.scope", Equals: "app"}
		}},
		{"rank and direction", func(def *ir.ProjectionDef) { def.Collapse.Order[0].Direction = "desc" }},
		{"bad direction", func(def *ir.ProjectionDef) { def.Collapse.Order[1].Direction = "sideways" }},
		{"bad nulls", func(def *ir.ProjectionDef) { def.Collapse.Order[1].Nulls = "middle" }},
		{"unknown collapse column", func(def *ir.ProjectionDef) { def.Collapse.By = []string{"base.missing"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := loadProjectionFixture(t)
			tc.mutate(schema.Types["AppPreference"].Projection)
			if _, err := Generate(schema, Options{SchemaName: "fixture-projection"}); err == nil {
				t.Fatal("an unsafe declaration generated")
			}
		})
	}
}

func TestTrustedFunctionProjectionPredicate(t *testing.T) {
	for _, tc := range []struct {
		name, function, arg, setting string
		refused                      bool
	}{
		{"trusted", "app.preference_visible", "base.id", "app.user_id", false},
		{"SQL injection", "app.f); DROP TABLE preference;--", "base.id", "app.user_id", true},
		{"argument expression", "app.f", "base.id OR TRUE", "app.user_id", true},
		{"unknown column", "app.f", "base.absent", "app.user_id", true},
		{"setting injection", "app.f", "base.id", "app.user_id');--", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := loadProjectionFixture(t)
			def := schema.Types["AppPreference"].Projection
			def.Predicates = []*ir.ProjectionPredicate{
				def.Predicates[0],
				{Function: tc.function, Args: []string{tc.arg}, RequiredSettings: []string{tc.setting}},
			}
			output, err := Generate(schema, Options{SchemaName: "fixture-projection"})
			if tc.refused {
				if err == nil {
					t.Fatal("unsafe rule generated")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			view := output.Projections[0]
			sql := strings.Join(view.Predicates, " AND ")
			for _, expected := range []string{`"app"."preference_visible"(base.id)`, "IS true", `NULLIF(current_setting('app.user_id'), '') IS NOT null`} {
				if !strings.Contains(sql, expected) {
					t.Fatalf("missing %q in %s", expected, sql)
				}
			}
			if strings.Join(view.Settings, ",") != "app.account_id,app.user_id" {
				t.Fatalf("required settings lost: %v", view.Settings)
			}
		})
	}
}

func TestComputedProjectionColumnGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, function    string
		args              []string
		from              string
		nullable, refused bool
	}{
		{"required", "app.preference_policy_key", []string{"base.channel"}, "", false, false},
		{"nullable", "app.preference_policy_key", []string{"base.id"}, "", true, false},
		{"function injection", "app.f); DROP TABLE preference;--", []string{"base.id"}, "", false, true},
		{"unqualified function", "f", []string{"base.id"}, "", false, true},
		{"missing args", "app.f", nil, "", false, true},
		{"argument expression", "app.f", []string{"base.id OR TRUE"}, "", false, true},
		{"literal argument", "app.f", []string{"'literal'"}, "", false, true},
		{"unknown column", "app.f", []string{"base.missing"}, "", false, true},
		{"unknown alias", "app.f", []string{"other.id"}, "", false, true},
		{"both forms", "app.f", []string{"base.id"}, "base.id", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := loadProjectionFixture(t)
			td := schema.Types["AppPreference"]
			td.Fields = append(td.Fields, &ir.FieldDef{Name: "policyKey", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: !tc.nullable, ProjectedFrom: tc.from, ProjectedFunction: &ir.ProjectionFunctionCall{Function: tc.function, Args: tc.args}})
			output, err := Generate(schema, Options{SchemaName: "fixture-projection"})
			if tc.refused {
				if err == nil {
					t.Fatal("unsafe computed column generated")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			view := output.Projections[0]
			column := view.Columns[len(view.Columns)-1]
			arg := "base.id"
			if tc.name == "required" {
				arg = "base.channel_id"
			}
			wantExpr := `"app"."preference_policy_key"(` + arg + `)`
			if column.Expr != wantExpr || column.SelfNamed || column.Type != "UUID" || column.Scalar != "Identity.UUID" || column.Nullable != tc.nullable {
				t.Fatalf("computed column drift: %+v", column)
			}
			if !strings.Contains(projectionViewDDL(view), wantExpr+" AS policy_key") {
				t.Fatalf("DDL does not select the function result:\n%s", projectionViewDDL(view))
			}
			arrow, err := ArrowSchemaJSON(schema, "fixture-projection", view, DefaultMetadataKeyPrefix)
			if err != nil {
				t.Fatal(err)
			}
			var raw struct {
				Fields []json.RawMessage `json:"fields"`
			}
			if err := json.Unmarshal(arrow, &raw); err != nil {
				t.Fatal(err)
			}
			var field struct {
				Name     string            `json:"name"`
				Type     string            `json:"data_type"`
				Nullable bool              `json:"nullable"`
				Metadata map[string]string `json:"metadata"`
			}
			if err := json.Unmarshal(raw.Fields[len(raw.Fields)-1], &field); err != nil {
				t.Fatal(err)
			}
			keys := NewMetadataKeys(DefaultMetadataKeyPrefix)
			if field.Name != "policy_key" || field.Type != "Utf8" || field.Nullable != tc.nullable ||
				field.Metadata[keys.ScalarCanonicalName] != "Identity.UUID" || field.Metadata[keys.ProjectionSource] != column.Source {
				t.Fatalf("Arrow metadata drift: %+v", field)
			}
			doc, err := DocsJSON("fixture-projection", view)
			if err != nil {
				t.Fatal(err)
			}
			var docs projectionDocs
			if err := json.Unmarshal(doc, &docs); err != nil {
				t.Fatal(err)
			}
			got := docs.Columns[len(docs.Columns)-1]
			if got.Source != tc.function+"("+strings.Join(tc.args, ", ")+")" || got.SQLType != "UUID" || got.Nullable != tc.nullable {
				t.Fatalf("docs drift: %+v", got)
			}
		})
	}
}

// TestLtreeScalarProjectionColumn: a scalar whose SQL type is ltree is
// selected as stored, without a cast, and served as Utf8 with its SQL type
// in the metadata, so a reader's text cast is the only conversion.
func TestLtreeScalarProjectionColumn(t *testing.T) {
	schema := loadProjectionFixture(t)
	schema.Scalars["Acme.Path"] = &ir.ScalarDef{Name: "Acme.Path", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "ltree"}}
	schema.Types["Preference"].Fields = append(schema.Types["Preference"].Fields, &ir.FieldDef{Name: "path", TypeRef: ir.TypeRef{Name: "Acme.Path"}, Required: true})
	projected := schema.Types["AppPreference"]
	projected.Fields = append(projected.Fields, &ir.FieldDef{Name: "path", TypeRef: ir.TypeRef{Name: "Acme.Path"}, Required: true})
	output, err := Generate(schema, Options{SchemaName: "fixture-projection"})
	if err != nil {
		t.Fatal(err)
	}
	view := output.Projections[0]
	column := view.Columns[len(view.Columns)-1]
	if column.Name != "path" || column.Type != "ltree" || column.Scalar != "Acme.Path" || column.Nullable || !column.SelfNamed || column.Expr != "base.path" {
		t.Fatalf("ltree column drift: %+v", column)
	}
	ddl := projectionViewDDL(view)
	if !strings.Contains(ddl, "\n  base.path\n") || strings.Contains(ddl, "base.path::text") {
		t.Fatalf("the view must select the ltree column as stored:\n%s", ddl)
	}
	arrow, err := ArrowSchemaJSON(schema, "fixture-projection", view, DefaultMetadataKeyPrefix)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Fields []json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(arrow, &raw); err != nil {
		t.Fatal(err)
	}
	var field struct {
		Type     string            `json:"data_type"`
		Nullable bool              `json:"nullable"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(raw.Fields[len(raw.Fields)-1], &field); err != nil {
		t.Fatal(err)
	}
	keys := NewMetadataKeys(DefaultMetadataKeyPrefix)
	if field.Type != "Utf8" || field.Nullable || field.Metadata[keys.ScalarSQLType] != "ltree" || field.Metadata[keys.ProjectionSource] != "base.path" {
		t.Fatalf("an ltree column must be a required Utf8 field with its SQL type: %+v", field)
	}
}

// TestLiteralProjectionPredicates renders the setting-free rules: IS null,
// IS NOT null and a typed equals with a string, number or boolean literal.
// None of them registers a setting.
func TestLiteralProjectionPredicates(t *testing.T) {
	str := func(s string) *ir.ProjectionLiteral { return &ir.ProjectionLiteral{String: &s} }
	num := func(n float64) *ir.ProjectionLiteral { return &ir.ProjectionLiteral{Number: &n} }
	boolean := func(b bool) *ir.ProjectionLiteral { return &ir.ProjectionLiteral{Bool: &b} }
	for _, tc := range []struct {
		name    string
		pred    *ir.ProjectionPredicate
		want    string
		refused bool
	}{
		{"isNull", &ir.ProjectionPredicate{Column: "base.commit", IsNull: true}, `base."commit" IS null`, false},
		{"notNull", &ir.ProjectionPredicate{Column: "base.userId", NotNull: true}, "base.user_id IS NOT null", false},
		{"guarded", &ir.ProjectionPredicate{Column: "base.userId", NotNull: true, When: &ir.ProjectionCondition{Column: "base.scope", Equals: "user"}}, "(base.scope IS DISTINCT FROM 'user' OR base.user_id IS NOT null)", false},
		{"equals string", &ir.ProjectionPredicate{Column: "base.scope", Equals: str("team")}, "base.scope = 'team'", false},
		{"equals quoted string", &ir.ProjectionPredicate{Column: "base.handle", Equals: str("o'brien")}, "base.handle = 'o''brien'", false},
		{"equals number", &ir.ProjectionPredicate{Column: "base.revision", Equals: num(1)}, "base.revision = 1", false},
		{"equals boolean", &ir.ProjectionPredicate{Column: "base.hidden", Equals: boolean(false)}, "base.hidden = false", false},
		{"enum non-member", &ir.ProjectionPredicate{Column: "base.scope", Equals: str("everyone")}, "", true},
		{"two forms", &ir.ProjectionPredicate{Column: "base.commit", IsNull: true, NotNull: true}, "", true},
		{"literal with setting", &ir.ProjectionPredicate{Column: "base.commit", IsNull: true, Setting: "app.x"}, "", true},
		{"empty equals", &ir.ProjectionPredicate{Column: "base.hidden", Equals: &ir.ProjectionLiteral{}}, "", true},
		{"unknown column", &ir.ProjectionPredicate{Column: "base.absent", IsNull: true}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := loadProjectionFixture(t)
			def := schema.Types["AppPreference"].Projection
			def.Predicates = []*ir.ProjectionPredicate{def.Predicates[0], tc.pred}
			output, err := Generate(schema, Options{SchemaName: "fixture-projection"})
			if tc.refused {
				if err == nil {
					t.Fatal("invalid literal rule generated")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			view := output.Projections[0]
			if len(view.Predicates) != 2 || view.Predicates[1] != tc.want {
				t.Fatalf("predicates = %v, want second %q", view.Predicates, tc.want)
			}
			if strings.Join(view.Settings, ",") != "app.account_id" {
				t.Fatalf("a literal rule must not register a setting: %v", view.Settings)
			}
		})
	}
}

// TestLiteralEqualsChecksDependencyEnums: an equals literal on a column
// whose enum lives in a dependency schema is checked against that enum's
// members, which only the generator can see.
func TestLiteralEqualsChecksDependencyEnums(t *testing.T) {
	schema := loadProjectionFixture(t)
	scope := schema.Enums["PreferenceScope"]
	delete(schema.Enums, "PreferenceScope")
	deps := map[string]*ir.Schema{"shared": {Name: "shared", Enums: map[string]*ir.EnumDef{"PreferenceScope": scope}}}
	def := schema.Types["AppPreference"].Projection
	everyone := "everyone"
	def.Predicates = []*ir.ProjectionPredicate{{Column: "base.scope", Equals: &ir.ProjectionLiteral{String: &everyone}}}
	_, err := Generate(schema, Options{SchemaName: "fixture-projection", Dependencies: deps})
	if err == nil || !strings.Contains(err.Error(), `"everyone" is not a member of enum PreferenceScope`) {
		t.Fatalf("want the dependency enum membership error, got %v", err)
	}
	team := "team"
	def.Predicates[0].Equals.String = &team
	output, err := Generate(schema, Options{SchemaName: "fixture-projection", Dependencies: deps})
	if err != nil {
		t.Fatal(err)
	}
	arrow, err := ArrowSchemaJSON(schema, "fixture-projection", output.Projections[0], DefaultMetadataKeyPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arrow), `"superschematic.enum.values": "app,team,user"`) {
		t.Fatalf("a dependency enum's values must reach the Arrow metadata:\n%s", arrow)
	}
}

// TestProjectionViewOwnerRendersSetRole checks outputs.sql.viewOwner: the
// migration creates the view under SET ROLE and resets after the last
// statement, create.sql and the down migration stay role-free, and a role
// name that is not a plain identifier is refused.
func TestProjectionViewOwnerRendersSetRole(t *testing.T) {
	schema := loadProjectionFixture(t)
	if _, err := Generate(schema, Options{SchemaName: "fixture-projection", ViewOwner: "owner; DROP ROLE postgres"}); err == nil {
		t.Fatal("an unsafe view owner generated")
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-projection", ViewOwner: "app_view_owner"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	migrations := filepath.Join(dir, "migrations")
	if err := WriteDDL(output, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjections(schema, output, dir, migrations); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile(filepath.Join(migrations, output.Projections[0].UpFileName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(up)
	setRole := strings.Index(text, "SET ROLE app_view_owner;")
	createView := strings.Index(text, "CREATE VIEW app.preferences")
	lastComment := strings.LastIndex(text, "COMMENT ON COLUMN")
	resetRole := strings.LastIndex(text, "RESET ROLE;")
	if setRole < 0 || createView < 0 || resetRole < 0 || setRole > createView || lastComment > resetRole {
		t.Fatalf("SET ROLE must precede CREATE VIEW and RESET ROLE must follow the last COMMENT:\n%s", text)
	}
	down, err := os.ReadFile(filepath.Join(migrations, output.Projections[0].DownFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(down), "SET ROLE") {
		t.Fatalf("the down migration drops as the runner:\n%s", down)
	}
	ddl, err := os.ReadFile(filepath.Join(dir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ddl), "SET ROLE") {
		t.Fatal("create.sql must stay role-free; its runner owns the DDL")
	}
	plain, err := Generate(schema, Options{SchemaName: "fixture-projection"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Projections[0].QuotedViewOwner != "" {
		t.Fatal("an unset view owner must not render a role")
	}
}

// TestSchemaWithoutProjectionsWritesNoProjectionFiles: a DB service that
// declares no view gets no projections directory and no migrations.
func TestSchemaWithoutProjectionsWritesNoProjectionFiles(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-db"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteProjections(schema, output, dir, filepath.Join(dir, "migrations")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a schema without projections wrote %v", entries)
	}
}

package tsreader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestProjectionWalk checks that the @projection / @join / @column surface
// lands on the IR as declared: the source and joins by class name, every
// row rule with its settings and guards, the collapse, and every column's
// source.
func TestProjectionWalk(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-projection"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	td := schema.Types["AppPreference"]
	if td == nil {
		t.Fatal("AppPreference not loaded")
	}
	if td.Role != ir.RoleProjection {
		t.Fatalf("role = %s, want Projection", td.Role)
	}
	p := td.Projection
	if p == nil {
		t.Fatal("no projection declaration")
	}
	if p.Pool != "app" || p.Name != "preferences" || p.Migration != "20260902120000" || p.Source != "Preference" {
		t.Errorf("declaration = %+v", *p)
	}
	if len(p.Predicates) != 7 {
		t.Fatalf("predicates = %d, want 7", len(p.Predicates))
	}
	if first := p.Predicates[0]; !first.IsBinding() || first.Column != "base.account" || first.Setting != "app.account_id" || first.Optional || first.When != nil {
		t.Errorf("first rule = %+v", first)
	}
	if guarded := p.Predicates[2]; guarded.When == nil || guarded.When.Column != "base.scope" || guarded.When.Equals != "user" {
		t.Errorf("guarded rule = %+v", guarded)
	}
	// Two optional bindings of which one must hold, guarded to app rows.
	version := p.Predicates[3]
	if len(version.AnyOf) != 2 || version.Column != "" || version.When == nil || version.When.Equals != "app" {
		t.Errorf("anyOf rule = %+v", version)
	} else if !version.AnyOf[0].Optional || version.AnyOf[0].Setting != "app.branch_id" || version.AnyOf[1].Column != "base.commit" {
		t.Errorf("anyOf alternatives = %+v %+v", version.AnyOf[0], version.AnyOf[1])
	}
	// The literal rules read no setting.
	if lit := p.Predicates[4]; !lit.IsNull || lit.Column != "base.archivedAt" || lit.Setting != "" || !lit.IsLiteral() {
		t.Errorf("isNull rule = %+v", lit)
	}
	if lit := p.Predicates[5]; lit.Equals == nil || lit.Equals.Kind() != "boolean" || *lit.Equals.Bool || lit.Column != "base.hidden" {
		t.Errorf("equals rule = %+v", lit)
	}
	if lit := p.Predicates[6]; !lit.NotNull || lit.Column != "base.userId" || lit.When == nil || lit.When.Equals != "user" {
		t.Errorf("notNull rule = %+v", lit)
	}
	if p.Collapse == nil || len(p.Collapse.By) != 1 || p.Collapse.By[0] != "base.slotKey" || len(p.Collapse.Order) != 2 {
		t.Errorf("collapse = %+v", p.Collapse)
	} else if len(p.Collapse.Order[0].Rank) != 3 || p.Collapse.Order[1].Nulls != "last" || p.Collapse.Order[1].Direction != "asc" {
		t.Errorf("collapse order = %+v %+v", p.Collapse.Order[0], p.Collapse.Order[1])
	}
	if len(p.Joins) != 2 || p.Joins[0].Alias != "channel" || p.Joins[0].Kind != "inner" || p.Joins[1].Kind != "left" {
		t.Errorf("joins = %+v", p.Joins)
	}
	if j := p.Joins[1]; j.Type != "Channel" || len(j.On) != 1 || j.On[0].Left != "branch.id" || j.On[0].Right != "base.branch" {
		t.Errorf("left join = %+v", j)
	}
	byName := map[string]*ir.FieldDef{}
	for _, fd := range td.Fields {
		byName[fd.Name] = fd
	}
	if got := byName["channelHandle"].ProjectedFrom; got != "channel.handle" {
		t.Errorf("channelHandle source = %q", got)
	}
	if got := byName["slotKey"].ProjectionSource(); got != "base.slotKey" {
		t.Errorf("slotKey source = %q", got)
	}
	if byName["branchName"].Required {
		t.Error("branchName is Nullable in the declaration")
	}
	if got := schema.Projections(); len(got) != 1 || got[0] != td {
		t.Errorf("Schema.Projections() = %v", got)
	}
}

// loadPatchedProjection copies the projection fixture into a temporary
// service next to it, applies replace to the projection file, and loads it.
func loadPatchedProjection(t *testing.T, old, replacement string) (*ir.Schema, error) {
	t.Helper()
	dir, err := os.MkdirTemp(filepath.Join("testdata", "services"), "fixture-projection-patched-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "services", "fixture-projection"))); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "src", "app-preference.projection.schema.ts")
	source, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), old) {
		t.Fatalf("fixture has no %q to replace", old)
	}
	if err := os.WriteFile(filename, []byte(strings.Replace(string(source), old, replacement, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	schema, _, err := LoadService(dir)
	return schema, err
}

// TestProjectionFunctionRuleFromTypeScript reads a function rule and a
// computed @column from source.
func TestProjectionFunctionRuleFromTypeScript(t *testing.T) {
	schema, err := loadPatchedProjection(t, "where: [",
		`where: [{ function: "app.preference_visible", args: ["base.id"], requiredSettings: ["app.user_id", "app.filter_version"] },`)
	if err != nil {
		t.Fatal(err)
	}
	p := schema.Types["AppPreference"].Projection.Predicates[0]
	if p.Function != "app.preference_visible" || len(p.Args) != 1 || p.Args[0] != "base.id" || len(p.RequiredSettings) != 2 {
		t.Fatalf("function rule lost: %+v", p)
	}
}

func TestComputedProjectionColumnFromTypeScript(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"structured call", `{ function: "app.preference_policy_key", args: ["base.id"] }`, ""},
		{"raw SQL object", `{ sql: "SELECT base.id" }`, ""},
		{"extra key", `{ function: "app.f", args: ["base.id"], sql: "TRUE" }`, ""},
		{"empty args", `{ function: "app.f", args: [] }`, "args"},
		{"literal number", `{ function: "app.f", args: [42] }`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := loadPatchedProjection(t, `@column("base.channel")`, "@column("+tc.source+")")
			if tc.name != "structured call" {
				// The compiler rejects a wrong object shape before the walker;
				// an unsafe reference that type-checks is verify's to refuse.
				if err == nil {
					t.Fatal("invalid computed column accepted")
				}
				if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected %q: %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range schema.Types["AppPreference"].Fields {
				if field.Name != "channelId" {
					continue
				}
				call := field.ProjectedFunction
				if call == nil || call.Function != "app.preference_policy_key" || len(call.Args) != 1 || call.Args[0] != "base.id" || field.ProjectedFrom != "" {
					t.Fatalf("computed column lost: %+v", field)
				}
				return
			}
			t.Fatal("computed field not found")
		})
	}
}

// TestProjectionRefusalsFromTypeScript: what the compiler and the
// decorators refuse while the walk reads a projection, each located in the
// projection file. The option set has no key for a required row rule; that
// policy belongs to an extension (Registry.RegisterCheck). Resolution of the
// names a declaration carries is verify's (internal/loader).
func TestProjectionRefusalsFromTypeScript(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement, want string }{
		{"unknown option", `pool: "app",`, `pool: "app", scope: "x",`, `'scope' does not exist in type 'ProjectionOptions'`},
		{"missing migration", `migration: "20260902120000",`, ``, `Property 'migration' is missing`},
		{"mixed rule forms", `{ column: "base.archivedAt", isNull: true }`, `{ column: "base.archivedAt", isNull: true, setting: "app.x" }`, "literal rule carries only column and when"},
		{"single anyOf", `{ column: "base.commit", setting: "app.commit_id", optional: true }`, ``, "anyOf needs at least two alternatives"},
		{"bad join kind", `{ "branch.id": "base.branch" }, "left")`, `{ "branch.id": "base.branch" }, "full" as "left")`, `@join kind must be "inner" or "left"`},
		{"type argument is not a class", `@join<Channel>("channel"`, `@join<string>("channel"`, "type argument must name a schema class"},
		{"no type argument", `@join<Channel>("channel"`, `@join("channel"`, "@join takes exactly 1 type argument(s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadPatchedProjection(t, tc.old, tc.replacement)
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error containing %q, got: %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "app-preference.projection.schema.ts") {
				t.Errorf("error does not point at the projection file: %v", err)
			}
		})
	}
}

// TestProjectionDecoratorsAreDBOnly: @projection in a General schema is
// refused by the decorator's kind gate.
func TestProjectionDecoratorsAreDBOnly(t *testing.T) {
	dir, err := os.MkdirTemp(filepath.Join("testdata", "services"), "fixture-projection-general-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "services", "fixture-projection"))); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"schema.config.ts", filepath.Join("src", "service.generated.ts")} {
		path := filepath.Join(dir, file)
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(source), "SchemaKind.DB", "SchemaKind.General")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = LoadService(dir)
	if err == nil || !strings.Contains(err.Error(), "@projection is only allowed in DB schemas") {
		t.Fatalf("want the DB-only kind gate, got: %v", err)
	}
}

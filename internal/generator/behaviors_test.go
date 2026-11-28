package generator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes/behaviortypestest"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// acmeLedger is the extension the behavior types fixture assumes: it
// declares acme.Rating, and a kind whose pipeline is the types generator
// alone, which renders behaviors, so a schema whose types compose them
// builds.
type acmeLedger struct{}

func (acmeLedger) Name() string { return "acme" }

func (acmeLedger) Register(r *registry.Registry) error {
	return errors.Join(
		r.RegisterKind(registry.KindSpec{Name: "Ledger", Extension: "acme", StructRole: ir.RoleEmbeddedStruct, Pipeline: []string{typesGenerator}}),
		r.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Declaration: json.RawMessage(behaviortypestest.Rating)}),
	)
}

// The types generator renders behaviors a type composes with @behavior
// (D16): a pipeline of it alone builds a schema whose types compose them,
// and each language writes its <T>Behaviors beside the own-field types,
// the extension's behavior under its exact name.
func TestTypesGeneratorRendersBehaviors(t *testing.T) {
	reg := extensionRegistry(t, acmeLedger{})
	schema := behaviortypestest.Schema(t, reg)
	schema.Kind = ir.SchemaKind("Ledger")
	enabled := map[string]any{"enabled": true}
	cfg := &schemaconfig.SchemaConfig{Name: "tracker", Kind: schema.Kind, Outputs: map[string]any{
		"types": map[string]any{LangGo: enabled, LangTypeScript: enabled, LangRust: enabled, LangPython: enabled},
	}}
	root := t.TempDir()
	if _, err := Run(schema, cfg, Options{OutputRoot: root, Registry: reg}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	python, err := filepath.Glob(filepath.Join(TypesDir(root, LangPython, "tracker"), "*", "behaviors.py"))
	if err != nil || len(python) != 1 {
		t.Fatalf("behaviors.py: %v, %v", python, err)
	}
	for _, tc := range []struct {
		path  string
		wants []string
	}{
		{filepath.Join(TypesDir(root, LangGo, "tracker"), "behaviors.go"), []string{"type TicketBehaviors struct {", "AcmeRating TicketAcmeRating `json:\"acme.Rating\"`", "TicketStateInProgress TicketState = \"in-progress\""}},
		{filepath.Join(TypesDir(root, LangTypeScript, "tracker"), "types", "behaviors.ts"), []string{"export interface TicketBehaviors {", "readonly 'acme.Rating': {", "export type TicketState = 'open' | 'in-progress' | 'done' | 'dropped';"}},
		{filepath.Join(TypesDir(root, LangRust, "tracker"), "src", "behaviors.rs"), []string{"pub struct TicketBehaviors {", "#[serde(rename = \"acme.Rating\")]\n    pub acme_rating: TicketAcmeRating,"}},
		{python[0], []string{"class TicketBehaviors(BaseModel):", "acme_rating: TicketAcmeRating = Field(..., alias=\"acme.Rating\""}},
	} {
		data, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.wants {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s lacks %q", tc.path, want)
			}
		}
	}
	// The own-field type is the instance's data as before.
	types, err := os.ReadFile(filepath.Join(TypesDir(root, LangGo, "tracker"), "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(types), "type Ticket struct {") || strings.Contains(string(types), "Behaviors") {
		t.Errorf("types.go's Ticket is not the own fields alone:\n%s", types)
	}
}

// A General schema's type that composes a behavior with @behavior builds,
// and its types write <T>Behaviors (D16). A DB schema's is refused before
// any generator runs, since a DB type is a table, which implements a
// behavior or carries it on a field (D103), and so is an API's own type:
// behaviors compose on DB tables.
func TestCoreGeneratorsOnATypeThatComposesBehaviors(t *testing.T) {
	enabled := map[string]any{"enabled": true}
	goTypes := map[string]any{LangGo: enabled}
	for _, tc := range []struct {
		kind    ir.SchemaKind
		outputs map[string]any
		want    string
	}{
		{ir.SchemaKindGeneral, map[string]any{"types": goTypes}, ""},
		{ir.SchemaKindDB, map[string]any{"types": goTypes},
			"generator: type Item of items composes behavior Comments with @behavior, which a DB type does not take: a DB type is a table, which implements a behavior or carries it on a field (D103)"},
		{ir.SchemaKindAPI, map[string]any{"types": goTypes, "api": enabled},
			"generator: type Item of API items composes behavior Comments, but behaviors compose on DB tables, by implements and field types, and an API serves their operations through @behaviorOperations (D103)"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			schema := ir.NewSchema("items", tc.kind)
			role := ir.RoleEmbeddedStruct
			if tc.kind == ir.SchemaKindDB {
				role = ir.RoleDBTable
			}
			schema.Types["Item"] = &ir.TypeDef{Name: "Item", Role: role, Behaviors: []ir.BehaviorRef{{Name: "Comments"}},
				Fields: []*ir.FieldDef{{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
			cfg := &schemaconfig.SchemaConfig{Name: "items", Kind: tc.kind, Outputs: tc.outputs}
			root := t.TempDir()
			_, err := Run(schema, cfg, Options{OutputRoot: root, Registry: CoreRegistry(naming.Default())})
			if tc.want != "" {
				if err == nil || err.Error() != tc.want {
					t.Fatalf("Run: err = %v, want %q", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			behaviorsGo := filepath.Join(TypesDir(root, LangGo, "items"), "behaviors.go")
			if _, err := os.Stat(behaviorsGo); err != nil {
				t.Errorf("the types write no behaviors.go: %v", err)
			}
		})
	}
}

// fieldBehaviorsDB is the DB service whose Order's status is a Workflow and
// whose Shipment has two (D103), loaded as a build loads it, with the
// fields the behaviors own added to the tables.
func fieldBehaviorsDB(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("..", "loader", "tsreader", "testdata", "services", "fixture-workflow-fields-db"), loader.WithRegistry(CoreRegistry(naming.Default())))
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// A table whose fields are behaviors stays a table (D103). sql, types and
// the Go ORM render what its behaviors add
// (GeneratorSpec.RendersFieldBehaviors), so the DB pipeline runs: the ORM
// writes rules.go and each repository's operations. The API generators
// and the SDKs render it too, and a generator that does not, an
// extension's, is refused before any generator runs, in today's words for
// a behavior it does not render.
func TestCoreGeneratorsOnATableWhoseFieldsAreBehaviors(t *testing.T) {
	schema := fieldBehaviorsDB(t)
	reg := CoreRegistry(naming.Default())
	cfg := &schemaconfig.SchemaConfig{Name: "fixture-workflow-fields-db", Kind: ir.SchemaKindDB,
		Outputs: map[string]any{"types": map[string]any{LangTypeScript: map[string]any{"enabled": true}, LangGo: map[string]any{"enabled": true}}}}
	root := t.TempDir()
	if _, err := Run(schema, cfg, Options{OutputRoot: root, Registry: reg}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ormDir := ORMDir(root, "fixture-workflow-fields-db")
	for _, name := range []string{"rules.go", "repository_order.go", "repository_shipment.go"} {
		if _, err := os.Stat(filepath.Join(ormDir, name)); err != nil {
			t.Errorf("the ORM wrote no %s: %v", name, err)
		}
	}
	if source, err := os.ReadFile(filepath.Join(ormDir, "repository_shipment.go")); err != nil || !strings.Contains(string(source), "func (r *ShipmentRepository) PaymentStatus() ShipmentPaymentStatusOperations") {
		t.Errorf("repository_shipment.go has no PaymentStatus operations (%v)", err)
	}

	var rendering []registry.GeneratorSpec
	for _, name := range []string{typesGenerator, "sql", ormGenerator, "api", "sdks", "envConfig", rulescoreGenerator} {
		spec, ok := reg.Generator(name)
		if !ok || !spec.RendersFieldBehaviors {
			t.Fatalf("generator %s: registered %v, renders field behaviors %v", name, ok, spec.RendersFieldBehaviors)
		}
		rendering = append(rendering, spec)
	}
	r := run{registry.GenerateContext{Schema: schema, Registry: reg}}
	if err := r.refuseBehaviors(rendering); err != nil {
		t.Fatalf("refuseBehaviors with the generators that render field behaviors: %v", err)
	}
	extension := registry.GeneratorSpec{Name: "acme-docs"}
	want := "generator: acme-docs does not render behaviors yet: type Order composes behavior Workflow on its field status (D103)"
	if err := r.refuseBehaviors(append(rendering, extension)); err == nil || err.Error() != want {
		t.Errorf("refuseBehaviors with an extension's generator: %v, want %q", err, want)
	}
}

// An API that imports a table of its DB service whose fields are
// behaviors, or projects one, is rendered by the API generators and the
// SDKs (D103): its types carry the owned fields, and its
// @behaviorOperations sets' operations are generated. A generator that
// does not render such a table, an extension's, is still refused before
// any generator runs, naming the generator, the table, the field and the
// behavior.
func TestAPIGeneratorsRenderAnImportedFieldBehaviorTable(t *testing.T) {
	deps := map[string]*ir.Schema{"fixture-workflow-fields-db": fieldBehaviorsDB(t)}
	load := func(name string) (*ir.Schema, error) {
		if dep := deps[name]; dep != nil {
			return dep, nil
		}
		return nil, errors.New("no such service")
	}
	schema := ir.NewSchema("orders-api", ir.SchemaKindAPI)
	schema.Imports = []ir.Import{{Package: "@schemas/fixture-workflow-fields-db", Types: []string{"Order"}}}
	cfg := &schemaconfig.SchemaConfig{Name: "orders-api", Kind: ir.SchemaKindAPI,
		Dependencies: []schemaconfig.ServiceDependency{{Name: "fixture-workflow-fields-db", Kind: ir.SchemaKindDB}}}
	reg := CoreRegistry(naming.Default())
	r := run{registry.GenerateContext{Schema: schema, Config: cfg, Registry: reg, LoadDependency: load, Options: registry.Options{LoadDependency: load}}}
	var enabled []registry.GeneratorSpec
	for _, name := range []string{typesGenerator, "api", "sdks"} {
		spec, ok := reg.Generator(name)
		if !ok || !spec.RendersFieldBehaviors {
			t.Fatalf("generator %s: registered %v, renders field behaviors %v", name, ok, spec.RendersFieldBehaviors)
		}
		enabled = append(enabled, spec)
	}
	if err := r.refuseImportedBehaviorTypes(enabled); err != nil {
		t.Fatalf("refuseImportedBehaviorTypes with types, api and sdks: %v", err)
	}
	extension := registry.GeneratorSpec{Name: "acme-docs"}
	want := "generator: acme-docs does not render behaviors yet: orders-api imports fixture-workflow-fields-db.Order, which composes behavior Workflow on its field status (D103)"
	if err := r.refuseImportedBehaviorTypes(append(enabled, extension)); err == nil || err.Error() != want {
		t.Fatalf("refuseImportedBehaviorTypes with an extension's generator: %v, want %q", err, want)
	}
}

// D27 plans a table's fields that are behaviors as any column (D103): from
// the version of Order before its status was a Workflow, the plan adds
// status, status_entered_at and @optimistic's _version with the trigger
// that bumps it, on Postgres and on SQLite.
func TestMigrationPlansTheColumnsOfFieldBehaviors(t *testing.T) {
	to := fieldBehaviorsDB(t)
	from := fieldBehaviorsDB(t)
	order := from.Types["Order"]
	order.Behaviors, order.Optimistic = nil, false
	order.Fields = slices.DeleteFunc(order.Fields, (*ir.FieldDef).Owned)
	cfg := &schemaconfig.SchemaConfig{Name: "fixture-workflow-fields-db", Kind: ir.SchemaKindDB}
	outputs, err := ParseOutputs(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []sqlmigrate.Dialect{sqlmigrate.Postgres, sqlmigrate.SQLite} {
		opts := SQLOptions(cfg, outputs, nil, naming.Default())
		before, err := sqlmigrate.BuildModel(from, opts, dialect)
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}
		after, err := sqlmigrate.BuildModel(to, opts, dialect)
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}
		plan, err := sqlmigrate.Diff(before, after, sqlmigrate.Options{})
		if err != nil {
			t.Fatalf("%s: Diff: %v", dialect, err)
		}
		statements := ""
		for _, step := range plan.Steps {
			statements += strings.Join(step.Statements, "\n") + "\n"
		}
		for _, want := range []string{"status", "status_entered_at", "_version"} {
			if !strings.Contains(statements, want) {
				t.Errorf("%s: the plan never names %s:\n%s", dialect, want, statements)
			}
		}
		if dialect == sqlmigrate.Postgres && !strings.Contains(statements, "order_bump_version") {
			t.Errorf("postgres: the plan adds no version trigger:\n%s", statements)
		}
	}
}

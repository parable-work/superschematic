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
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// stockroomExtension registers a behavior and a kind whose pipeline is two
// generators: shelfList renders behaviors; labels does not unless
// labelsRenders is set, and labelsDisabled switches it off. ran records
// which generators ran.
type stockroomExtension struct {
	ran            *[]string
	labelsRenders  bool
	labelsDisabled bool
}

func (stockroomExtension) Name() string { return "stockroom" }

func (e stockroomExtension) Register(r *registry.Registry) error {
	record := func(name string) func(registry.GenerateContext) error {
		return func(registry.GenerateContext) error {
			*e.ran = append(*e.ran, name)
			return nil
		}
	}
	return errors.Join(
		r.RegisterKind(registry.KindSpec{Name: "Stockroom", Extension: "stockroom", StructRole: ir.RoleEmbeddedStruct, Pipeline: []string{"shelfList", "labels"}}),
		r.RegisterGenerator(registry.GeneratorSpec{Name: "shelfList", Extension: "stockroom", RendersBehaviors: true, Generate: record("shelfList")}),
		r.RegisterGenerator(registry.GeneratorSpec{
			Name: "labels", Extension: "stockroom", RendersBehaviors: e.labelsRenders, Generate: record("labels"),
			Enabled: func(registry.GenerateContext) (bool, string) { return !e.labelsDisabled, "labels" },
		}),
		r.RegisterBehavior(registry.BehaviorSpec{Extension: "stockroom", Declaration: json.RawMessage(`{"name":"stockroom.Counted"}`)}),
	)
}

func stockroomService(behaviors ...ir.BehaviorRef) (*ir.Schema, *schemaconfig.SchemaConfig) {
	schema := ir.NewSchema("stock", ir.SchemaKind("Stockroom"))
	schema.Types["Plain"] = &ir.TypeDef{Name: "Plain", Role: ir.RoleEmbeddedStruct}
	schema.Types["Shelf"] = &ir.TypeDef{Name: "Shelf", Role: ir.RoleEmbeddedStruct, Behaviors: behaviors}
	return schema, &schemaconfig.SchemaConfig{Name: "stock", Kind: ir.SchemaKind("Stockroom")}
}

// Run refuses a schema with behaviors before any generator runs when an
// enabled generator does not render them, and names it; a generator that
// declares RendersBehaviors runs, and a disabled one is not asked.
func TestRunRefusesBehaviorsAGeneratorDoesNotRender(t *testing.T) {
	counted := ir.BehaviorRef{Name: "stockroom.Counted"}

	var ran []string
	reg := extensionRegistry(t, stockroomExtension{ran: &ran})
	schema, cfg := stockroomService(counted)
	_, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Registry: reg})
	if want := "generator: labels does not render behaviors yet: type Shelf composes behavior stockroom.Counted"; err == nil || err.Error() != want {
		t.Fatalf("Run: err = %v, want %q", err, want)
	}
	if len(ran) != 0 {
		t.Fatalf("generators ran before the refusal: %v", ran)
	}

	// The same pipeline on a schema without behaviors runs both.
	plain, plainCfg := stockroomService()
	if _, err := Run(plain, plainCfg, Options{OutputRoot: t.TempDir(), Registry: reg}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ran, ",") != "shelfList,labels" {
		t.Fatalf("ran = %v", ran)
	}

	for _, ext := range []stockroomExtension{{labelsRenders: true}, {labelsDisabled: true}} {
		ran = nil
		ext.ran = &ran
		reg := extensionRegistry(t, ext)
		schema, cfg := stockroomService(counted)
		if _, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Registry: reg}); err != nil {
			t.Fatalf("%+v: Run: %v", ext, err)
		}
		if ran[0] != "shelfList" {
			t.Fatalf("%+v: ran = %v", ext, ran)
		}
	}
}

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

// The types generator renders behaviors (D58): a pipeline of it alone
// builds a schema whose types compose them, and each language writes its
// <T>Behaviors beside the own-field types, the extension's behavior
// under its exact name.
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

// Every core generator of the DB and General pipelines renders behaviors
// (D58): a DB schema's type that composes one is an engine type, which sql
// and orm leave out and enginedoc writes the document of, and a General
// schema's types write <T>Behaviors. An API's own type that composes one
// is refused before any generator runs: behaviors compose on DB types.
func TestCoreGeneratorsOnATypeThatComposesBehaviors(t *testing.T) {
	enabled := map[string]any{"enabled": true}
	goTypes := map[string]any{LangGo: enabled}
	for _, tc := range []struct {
		kind    ir.SchemaKind
		outputs map[string]any
		want    string
	}{
		{ir.SchemaKindDB, map[string]any{"types": goTypes}, ""},
		{ir.SchemaKindGeneral, map[string]any{"types": goTypes}, ""},
		{ir.SchemaKindAPI, map[string]any{"types": goTypes, "api": enabled},
			"generator: type Item of API items composes behavior Comments, but behaviors compose on DB types: a DB type that composes one is an engine type, whose instances the engine keeps, and an API reaches it through its DB service (D58)"},
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
			result, err := Run(schema, cfg, Options{OutputRoot: root, Registry: CoreRegistry(naming.Default())})
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
			engineDocument := EngineDocumentPath(root, "items")
			_, statErr := os.Stat(engineDocument)
			if tc.kind == ir.SchemaKindDB {
				if statErr != nil || result.Outputs["engine"] != engineDocument {
					t.Errorf("the DB build wrote no engine document: %v (outputs %v)", statErr, result.Outputs)
				}
				// Item is the schema's only type and an engine type: no
				// table, no repository.
				if !slices.Contains(result.Skipped, "sql") || !slices.Contains(result.Skipped, "orm") {
					t.Errorf("Skipped = %v, want sql and orm", result.Skipped)
				}
			} else if statErr == nil {
				t.Errorf("a %s build wrote an engine document", tc.kind)
			}
		})
	}
}

// engineDB is a DB service whose Ticket composes Workflow, an engine type,
// beside a table, for the API refusals.
func engineDB() *ir.Schema {
	db := ir.NewSchema("desk-db", ir.SchemaKindDB)
	db.Types["Ticket"] = &ir.TypeDef{Name: "Ticket", Role: ir.RoleDBTable, Behaviors: []ir.BehaviorRef{{Name: "Workflow"}},
		Fields: []*ir.FieldDef{{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
	db.Types["Agent"] = &ir.TypeDef{Name: "Agent", Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
	return db
}

// The API generators do not serve an engine type yet (D58): an API that
// imports one, or projects one with @source, is refused before any
// generator runs, naming the first generator that does not render
// behaviors. A table of the same DB is no engine type.
func TestAPIGeneratorsRefuseAnImportedEngineType(t *testing.T) {
	enabled := map[string]any{"enabled": true}
	goTypes := map[string]any{LangGo: enabled}
	deps := map[string]*ir.Schema{"desk-db": engineDB()}
	load := func(name string) (*ir.Schema, error) {
		if dep := deps[name]; dep != nil {
			return dep, nil
		}
		return nil, errors.New("no such service")
	}
	api := func(use func(*ir.Schema)) *ir.Schema {
		schema := ir.NewSchema("desk-api", ir.SchemaKindAPI)
		use(schema)
		return schema
	}
	importType := func(name string) func(*ir.Schema) {
		return func(s *ir.Schema) { s.Imports = []ir.Import{{Package: "@schemas/desk-db", Types: []string{name}}} }
	}
	project := func(s *ir.Schema) {
		s.Types["TicketView"] = &ir.TypeDef{Name: "TicketView", Role: ir.RoleAPIView, Source: &ir.SourceRef{Target: "desk-db.Ticket"},
			Fields: []*ir.FieldDef{{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
	}
	for _, tc := range []struct {
		name    string
		schema  *ir.Schema
		outputs map[string]any
		want    string
	}{
		{"import", api(importType("Ticket")), map[string]any{"types": goTypes, "api": enabled},
			"generator: api does not render engine types yet: desk-api imports desk-db.Ticket, which composes behavior Workflow (D58)"},
		{"sdk alone", api(importType("Ticket")), map[string]any{"types": goTypes, "sdk": goTypes},
			"generator: sdks does not render engine types yet: desk-api imports desk-db.Ticket, which composes behavior Workflow (D58)"},
		{"source", api(project), map[string]any{"types": goTypes, "api": enabled},
			"generator: api does not render engine types yet: type TicketView of desk-api projects desk-db.Ticket, which composes behavior Workflow (D58)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &schemaconfig.SchemaConfig{Name: "desk-api", Kind: ir.SchemaKindAPI, Outputs: tc.outputs,
				Dependencies: []schemaconfig.ServiceDependency{{Name: "desk-db", Kind: ir.SchemaKindDB}}}
			_, err := Run(tc.schema, cfg, Options{OutputRoot: t.TempDir(), Registry: CoreRegistry(naming.Default()), LoadDependency: load})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Run: err = %v, want %q", err, tc.want)
			}
		})
	}

	// A table of the same DB is no engine type: the refusal lets the API
	// pipeline's generators run on an API that imports one.
	cfg := &schemaconfig.SchemaConfig{Name: "desk-api", Kind: ir.SchemaKindAPI, Outputs: map[string]any{"types": goTypes},
		Dependencies: []schemaconfig.ServiceDependency{{Name: "desk-db", Kind: ir.SchemaKindDB}}}
	if err := refuseImportedEngineTypesOf(t, api(importType("Agent")), cfg, load); err != nil {
		t.Fatalf("an imported table: %v", err)
	}
}

// The server generator refuses a server that serves an API which uses an
// engine type, naming the stack, the server and the API (D58).
func TestServerGeneratorRefusesAnAPIThatUsesAnEngineType(t *testing.T) {
	deps := map[string]*ir.Schema{"desk-db": engineDB()}
	load := func(name string) (*ir.Schema, error) {
		if dep := deps[name]; dep != nil {
			return dep, nil
		}
		return nil, errors.New("no such service")
	}
	reg := CoreRegistry(naming.Default())
	cfg := &schemaconfig.SchemaConfig{Name: "desk-api", Kind: ir.SchemaKindAPI,
		Dependencies: []schemaconfig.ServiceDependency{{Name: "desk-db", Kind: ir.SchemaKindDB}}}
	for name, want := range map[string]string{
		"Ticket": "generator: server does not render engine types yet: stack desk: server web serves desk-api, and desk-api imports desk-db.Ticket, which composes behavior Workflow (D58)",
		"Agent":  "",
	} {
		api := ir.NewSchema("desk-api", ir.SchemaKindAPI)
		api.Imports = []ir.Import{{Package: "@schemas/desk-db", Types: []string{name}}}
		served, _ := newRun(api, cfg, &registry.Outputs{}, Options{OutputRoot: t.TempDir(), Registry: reg, LoadDependency: load}, reg)
		err := served.refuseServedEngineTypes("desk", "web")
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
}

// refuseImportedEngineTypesOf runs the refusal alone, as runPipeline does
// with the API pipeline's generators enabled.
func refuseImportedEngineTypesOf(t *testing.T, schema *ir.Schema, cfg *schemaconfig.SchemaConfig, load func(string) (*ir.Schema, error)) error {
	t.Helper()
	reg := CoreRegistry(naming.Default())
	outputs, err := registry.ParseOutputs(cfg.Outputs, reg)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRun(schema, cfg, outputs, Options{OutputRoot: t.TempDir(), Registry: reg, LoadDependency: load}, reg)
	return r.refuseImportedEngineTypes(reg.Pipeline(string(ir.SchemaKindAPI)))
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

// A table whose fields are behaviors stays a table (D103): no engine type,
// so no engine document and nothing for refuseEngineTypes. sql and types
// render what its behaviors add (GeneratorSpec.RendersFieldBehaviors), and
// every other generator the DB pipeline enables is refused before any
// runs, in today's words for a behavior it does not render: the Go ORM's
// repository would write the owned fields and skip the core. A decorator's
// engine type is refused and served as it was.
func TestCoreGeneratorsOnATableWhoseFieldsAreBehaviors(t *testing.T) {
	schema := fieldBehaviorsDB(t)
	if engine := codegen.EngineTypes(schema); len(engine) != 0 {
		t.Fatalf("engine types = %v, want none", engine)
	}
	reg := CoreRegistry(naming.Default())
	cfg := &schemaconfig.SchemaConfig{Name: "fixture-workflow-fields-db", Kind: ir.SchemaKindDB,
		Outputs: map[string]any{"types": map[string]any{LangTypeScript: map[string]any{"enabled": true}, LangGo: map[string]any{"enabled": true}}}}
	_, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Registry: reg})
	if want := "generator: orm does not render behaviors yet: type Order composes behavior Workflow on its field status (D103)"; err == nil || err.Error() != want {
		t.Fatalf("Run: err = %v, want %q", err, want)
	}

	var rendering []registry.GeneratorSpec
	for _, name := range []string{typesGenerator, "sql", "envConfig", enginedocGenerator, projectengineGenerator} {
		spec, ok := reg.Generator(name)
		if !ok || !spec.RendersFieldBehaviors {
			t.Fatalf("generator %s: registered %v, renders field behaviors %v", name, ok, spec.RendersFieldBehaviors)
		}
		rendering = append(rendering, spec)
	}
	if err := refuseBehaviors(schema, rendering); err != nil {
		t.Fatalf("refuseBehaviors with the generators that render field behaviors: %v", err)
	}
	for _, name := range []string{ormGenerator, "api", "sdks"} {
		spec, _ := reg.Generator(name)
		if err := refuseBehaviors(schema, append(rendering, spec)); err == nil || !strings.Contains(err.Error(), "generator: "+name+" does not render behaviors yet") {
			t.Errorf("refuseBehaviors with %s: %v", name, err)
		}
	}
}

// An API that imports a table of its DB service whose fields are
// behaviors, or projects one, is refused before any generator runs (D103):
// its operations would write the owned fields as any field.
func TestAPIGeneratorsRefuseAnImportedFieldBehaviorTable(t *testing.T) {
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
		Outputs:      map[string]any{"types": map[string]any{LangGo: map[string]any{"enabled": true}}, "api": map[string]any{"enabled": true}},
		Dependencies: []schemaconfig.ServiceDependency{{Name: "fixture-workflow-fields-db", Kind: ir.SchemaKindDB}}}
	_, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Registry: CoreRegistry(naming.Default()), LoadDependency: load})
	if want := "generator: api does not render behaviors yet: orders-api imports fixture-workflow-fields-db.Order, which composes behavior Workflow on its field status (D103)"; err == nil || err.Error() != want {
		t.Fatalf("Run: err = %v, want %q", err, want)
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

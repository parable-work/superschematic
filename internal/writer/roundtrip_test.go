package writer

import (
	"encoding/json"
	"fmt"
	"github.com/parable-work/superschematic/internal/testpaths"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// roundtripFixture is one service of the round-trip corpus.
type roundtripFixture struct {
	name string

	// dir is the fixture service directory relative to this package.
	dir string

	// native is the format the fixture is authored in.
	native Format

	// skipTSReason names the IR content that has no TypeScript authoring
	// form, exempting the fixture from the TS leg. Empty means the fixture
	// is fully TS-expressible.
	skipTSReason string
}

const (
	tsFixtures   = "../loader/tsreader/testdata/services"
	dataFixtures = "../loader/testdata/services"
)

var corpus = []roundtripFixture{
	{name: "fixture-db", dir: tsFixtures + "/fixture-db", native: FormatTS},
	// Covers the TS writer emitting every onDelete action, including the
	// space-containing "NO ACTION" quoting case, across all format legs.
	{name: "fixture-relation-ondelete", dir: tsFixtures + "/fixture-relation-ondelete", native: FormatTS},
	{name: "fixture-api", dir: tsFixtures + "/fixture-api", native: FormatTS},
	{name: "fixture-general", dir: tsFixtures + "/fixture-general", native: FormatTS},
	// Type-level decode decorators: the TS writer emits both.
	{name: "fixture-deny-unknown-fields", dir: tsFixtures + "/fixture-deny-unknown-fields", native: FormatTS},
	{name: "fixture-strict-json", dir: tsFixtures + "/fixture-strict-json", native: FormatTS},
	// Operation @docs records in the data forms.
	{name: "fixture-docs", dir: tsFixtures + "/fixture-docs", native: FormatTS},
	// SQL projection views: every row rule form, joins, @column in both
	// forms of the source and the collapse.
	{name: "fixture-projection", dir: tsFixtures + "/fixture-projection", native: FormatTS},
	{name: "fixture-db-json", dir: dataFixtures + "/fixture-db-json", native: FormatJSON},
	{name: "fixture-db-yaml", dir: dataFixtures + "/fixture-db-yaml", native: FormatYAML},
	{name: "fixture-general-json", dir: dataFixtures + "/fixture-general-json", native: FormatJSON},
	{name: "fixture-general-yaml", dir: dataFixtures + "/fixture-general-yaml", native: FormatYAML},
	{
		name: "fixture-comments-yaml", dir: dataFixtures + "/fixture-comments-yaml", native: FormatYAML,
		skipTSReason: "schema-level comments and operation argument comments have no TypeScript authoring form",
	},
}

// TestRoundTrip is the closed-loop guarantee for the three formats: every
// corpus service, loaded in its native format, written to each other format,
// and re-loaded, produces equal IR -- comment metadata included. Re-loading
// runs the full reader pipeline, so every writer output also passes its
// format's validation (JSON Schema for the data forms, the compiler and walk
// for TypeScript).
//
// The TypeScript leg is carved out for fixtures whose IR carries content
// with no TypeScript authoring form (see skipTSReason); TestTSWriterRejects
// pins the loud failure for the pipeline case.
func TestRoundTrip(t *testing.T) {
	for _, fixture := range corpus {
		t.Run(fixture.name, func(t *testing.T) {
			schema, err := loader.LoadService(fixture.dir)
			if err != nil {
				t.Fatalf("loading native fixture: %v", err)
			}

			for _, target := range []Format{FormatTS, FormatJSON, FormatYAML} {
				if target == fixture.native {
					continue
				}
				t.Run(string(fixture.native)+"_to_"+string(target), func(t *testing.T) {
					if target == FormatTS && fixture.skipTSReason != "" {
						t.Skipf("TS leg carve-out: %s", fixture.skipTSReason)
					}
					// Data formats materialize @source lineage into the
					// imports block; expect that on the want side too.
					wantSchema := schema
					if target == FormatJSON || target == FormatYAML {
						wantSchema = withSourceLineageImports(schema)
					}
					want := normalizeIR(t, wantSchema)
					reloaded := writeAndReload(t, schema, target)
					got := normalizeIR(t, reloaded)
					if got != want {
						t.Errorf("IR mismatch after %s -> %s round trip\nwant:\n%s\ngot:\n%s",
							fixture.native, target, want, got)
					}
				})
			}
		})
	}
}

// TestWriteJSONSourceLineageImports: a TS-authored @source with no
// schema.Imports still writes a JSON imports entry so reload can stand
// the SourceRef without ExternalTypes.
func TestWriteJSONSourceLineageImports(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-api")
	if err != nil {
		t.Fatalf("loading fixture-api: %v", err)
	}
	for _, imp := range schema.Imports {
		if strings.Contains(imp.Package, "fixture-db") {
			t.Fatalf("precondition: fixture-db must not be in schema.Imports; got %+v", schema.Imports)
		}
	}
	if schema.Types["TenantView"] == nil || schema.Types["TenantView"].Source == nil {
		t.Fatal("precondition: TenantView.@source missing")
	}

	reloaded := writeAndReload(t, schema, FormatJSON)
	found := false
	for _, imp := range reloaded.Imports {
		if imp.Package != "@schemas/fixture-db" {
			continue
		}
		for _, name := range imp.Types {
			if name == "Tenant" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("reloaded JSON missing fixture-db.Tenant import; got %+v", reloaded.Imports)
	}
	if reloaded.Types["TenantView"] == nil || reloaded.Types["TenantView"].Source == nil {
		t.Fatal("TenantView.@source lost after JSON round trip")
	}
	if got := reloaded.Types["TenantView"].Source.Target; got != "fixture-db.Tenant" {
		t.Fatalf("Source.Target = %q, want fixture-db.Tenant", got)
	}
}

// TestRoundTripYAMLThroughJSON closes the explicit YAML -> JSON -> YAML
// path: native YAML comments survive conversion to JSON (as "comment"
// properties) and back to native YAML comments.
func TestRoundTripYAMLThroughJSON(t *testing.T) {
	schema, err := loader.LoadService(dataFixtures + "/fixture-comments-yaml")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}

	asJSON := writeAndReload(t, schema, FormatJSON)
	backToYAML := writeAndReload(t, asJSON, FormatYAML)

	want := normalizeIR(t, schema)
	got := normalizeIR(t, backToYAML)
	if got != want {
		t.Fatalf("IR mismatch after yaml -> json -> yaml\nwant:\n%s\ngot:\n%s", want, got)
	}

	// Spot-check that the comparison actually covered comment metadata at
	// every position.
	if backToYAML.Comment != "Widget catalog schema, exercising comment metadata on every node position." {
		t.Errorf("schema comment = %q", backToYAML.Comment)
	}
	widget := backToYAML.Types["Widget"]
	if widget.Comment != "A catalog widget." {
		t.Errorf("type comment = %q", widget.Comment)
	}
	if widget.Fields[0].Comment != "Primary identifier." {
		t.Errorf("field comment = %q", widget.Fields[0].Comment)
	}
	if backToYAML.Enums["WidgetStatus"].Values[1].Comment != "Hidden from the catalog." {
		t.Errorf("enum value comment = %q", backToYAML.Enums["WidgetStatus"].Values[1].Comment)
	}
	if backToYAML.Unions["CatalogEntry"].Comment != "Anything a catalog entry can point at." {
		t.Errorf("union comment = %q", backToYAML.Unions["CatalogEntry"].Comment)
	}
	if backToYAML.Scalars["Identity.UUID"].Comment != "Canonical widget identifier scalar." {
		t.Errorf("scalar comment = %q", backToYAML.Scalars["Identity.UUID"].Comment)
	}
	set := backToYAML.OperationSets[0]
	if set.Comment != "Read operations for widgets." {
		t.Errorf("operation set comment = %q", set.Comment)
	}
	if set.Operations[0].Comment != "Fetch one widget by id." {
		t.Errorf("operation comment = %q", set.Operations[0].Comment)
	}
	if set.Operations[0].Arguments[0].Comment != "The widget identifier." {
		t.Errorf("argument comment = %q", set.Operations[0].Arguments[0].Comment)
	}
}

func TestTSWriterEmitsConfiguredVersionedDecorator(t *testing.T) {
	retentionDays := 90
	schema := ir.NewSchema("fixture-db", ir.SchemaKindDB)
	schema.Types["Tenant"] = &ir.TypeDef{
		Name:      "Tenant",
		Owner:     "src/tenant.schema.ts",
		Role:      ir.RoleDBTable,
		Versioned: true,
		VersionedConfig: &ir.VersionedConfig{
			RetentionDays: &retentionDays,
			PartitionBy:   "month",
			PruneKeepReferencedBy: []*ir.PruneReference{{
				Table:         "commit_entry",
				KeyColumn:     "entity_id",
				VersionColumn: "entity_version",
			}},
		},
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
		},
	}

	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "tenant.schema.ts"))
	if err != nil {
		t.Fatalf("reading TS output: %v", err)
	}
	if !strings.Contains(string(got), `@versioned({ retentionDays: 90, partitionBy: "month", pruneKeepReferencedBy: { table: "commit_entry", keyColumn: "entity_id", versionColumn: "entity_version" } })`) {
		t.Fatalf("TS output missing configured @versioned decorator:\n%s", got)
	}
}

func TestTSWriterEmitsTemporalFormatDecorator(t *testing.T) {
	schema := ir.NewSchema("fixture-general", ir.SchemaKindGeneral)
	schema.Types["Event"] = &ir.TypeDef{
		Name:  "Event",
		Owner: "src/event.schema.ts",
		Role:  ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{{
			Name:           "occurredAt",
			TypeRef:        ir.TypeRef{Name: "Temporal.DateTime"},
			Required:       true,
			TemporalFormat: "unix_millis",
		}},
	}

	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "event.schema.ts"))
	if err != nil {
		t.Fatalf("reading TS output: %v", err)
	}
	for _, want := range []string{
		`import { temporalFormat } from "@superschematic/schema";`,
		`@temporalFormat("unix_millis")`,
	} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("TS output missing %q:\n%s", want, got)
		}
	}
}

// TestTSWriterEmitsMultiPinVersionedDecorator: a table pinned by more than one
// referencing table emits the array form. One pin keeps the object form so
// existing schema files round-trip byte-identical.
func TestTSWriterEmitsMultiPinVersionedDecorator(t *testing.T) {
	retentionDays := 90
	schema := ir.NewSchema("fixture-db", ir.SchemaKindDB)
	schema.Types["Tenant"] = &ir.TypeDef{
		Name:      "Tenant",
		Owner:     "src/tenant.schema.ts",
		Role:      ir.RoleDBTable,
		Versioned: true,
		VersionedConfig: &ir.VersionedConfig{
			RetentionDays: &retentionDays,
			PruneKeepReferencedBy: []*ir.PruneReference{
				{Table: "commit_entry", KeyColumn: "entity_id", VersionColumn: "entity_version"},
				{Table: "proposal_resolution", KeyColumn: "stash_id", VersionColumn: "stash_version"},
			},
		},
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
		},
	}

	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "tenant.schema.ts"))
	if err != nil {
		t.Fatalf("reading TS output: %v", err)
	}
	want := `@versioned({ retentionDays: 90, pruneKeepReferencedBy: [{ table: "commit_entry", keyColumn: "entity_id", versionColumn: "entity_version" }, { table: "proposal_resolution", keyColumn: "stash_id", versionColumn: "stash_version" }] })`
	if !strings.Contains(string(got), want) {
		t.Fatalf("TS output missing multi-pin @versioned decorator:\n%s", got)
	}
}

// writeAndReload writes a schema out in the target format into a temp
// service directory and loads it back through the full reader pipeline.
func writeAndReload(t *testing.T, schema *ir.Schema, target Format) *ir.Schema {
	t.Helper()
	dir := t.TempDir()
	if _, err := WriteService(schema, target, dir); err != nil {
		t.Fatalf("WriteService(%s): %v", target, err)
	}
	// Config/paths must cover SourceRef lineage imports that data formats
	// materialize into schema.Imports on reload.
	cfgSchema := withSourceLineageImports(schema)
	writeServiceConfig(t, dir, cfgSchema)
	if target == FormatTS {
		writeTSProject(t, dir, cfgSchema)
	}
	reloaded, err := loader.LoadService(dir)
	if err != nil {
		dumpService(t, dir)
		t.Fatalf("reloading %s output: %v", target, err)
	}
	return reloaded
}

// fixtureDependencyKinds maps the corpus's cross-service dependency names to
// their schema kinds, for the generated service configs (the verification
// pass requires every imported service to be a declared dependency).
var fixtureDependencyKinds = map[string]ir.SchemaKind{
	"fixture-db": ir.SchemaKindDB,
	"web-db":     ir.SchemaKindDB,
}

// writeServiceConfig writes the minimal data-form service config, declaring
// a dependency for every cross-service import the schema carries.
func writeServiceConfig(t *testing.T, dir string, schema *ir.Schema) {
	t.Helper()
	deps := ""
	for i, imp := range schema.Imports {
		service := imp.Package[strings.LastIndex(imp.Package, "/")+1:]
		kind, ok := fixtureDependencyKinds[service]
		if !ok {
			t.Fatalf("fixture imports unknown service %q; add it to fixtureDependencyKinds", service)
		}
		if i > 0 {
			deps += ", "
		}
		deps += fmt.Sprintf("{\"name\": %q, \"kind\": %q}", service, kind)
	}
	cfg := fmt.Sprintf("{\n  \"name\": %q,\n  \"kind\": %q,\n  \"dependencies\": [%s],\n  \"outputs\": {}\n}\n",
		schema.Name, schema.Kind, deps)
	if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTSProject writes the package.json and tsconfig.json a generated
// TypeScript service needs to compile: @superschematic/* toolchain packages resolve
// to this repo's authoring packages, and cross-service imports resolve to
// the original fixture services.
func writeTSProject(t *testing.T, dir string, schema *ir.Schema) {
	t.Helper()

	pkg := fmt.Sprintf("{\n  \"name\": %q,\n  \"private\": true\n}\n", "@schemas/"+schema.Name)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}

	packagesDir, err := filepath.Abs("../../packages")
	if err != nil {
		t.Fatal(err)
	}
	servicesDir, err := filepath.Abs(tsFixtures)
	if err != nil {
		t.Fatal(err)
	}

	paths := make(map[string][]string)
	for _, name := range []string{"api", "db", "schema", "schema-config"} {
		paths["@superschematic/"+name] = []string{filepath.ToSlash(filepath.Join(packagesDir, name, "src", "index.ts"))}
	}
	// superscalar is a dependency, not a packages/ member: it resolves to the
	// checkout scripts/superscalar-dep.sh stands up.
	paths["superscalar"] = []string{filepath.ToSlash(filepath.Join(testpaths.Local(t).ScalarTypeScript, "src", "index.ts"))}
	for _, imp := range schema.Imports {
		service := imp.Package[strings.LastIndex(imp.Package, "/")+1:]
		paths[imp.Package] = []string{filepath.ToSlash(filepath.Join(servicesDir, service, "src", "index.ts"))}
	}

	tsconfig := map[string]any{
		"compilerOptions": map[string]any{
			"target":                       "ES2022",
			"module":                       "ESNext",
			"moduleResolution":             "Bundler",
			"lib":                          []string{"ES2022"},
			"strict":                       true,
			"strictPropertyInitialization": false,
			"experimentalDecorators":       true,
			"noEmit":                       true,
			"skipLibCheck":                 true,
			"baseUrl":                      ".",
			"paths":                        paths,
		},
		"include": []string{"src/**/*.ts"},
	}
	data, err := json.MarshalIndent(tsconfig, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// normalizeIR renders a schema as comparison-stable JSON: Owner paths drop
// their format extension, so "src/tenant.schema.ts" and
// "src/tenant.schema.yaml" compare equal.
func normalizeIR(t *testing.T, schema *ir.Schema) string {
	t.Helper()
	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	for _, ext := range []string{".schema.ts", ".schema.json", ".schema.yaml", ".schema.yml"} {
		s = strings.ReplaceAll(s, ext, ".schema")
	}
	return s
}

// dumpService prints the generated service files to aid debugging a failed
// reload.
func dumpService(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		t.Logf("--- %s ---\n%s", rel, data)
		return nil
	})
}

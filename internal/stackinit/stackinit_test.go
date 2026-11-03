package stackinit_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stackinit"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// operation is an operation with the service clause given, or none.
func operation(name string, callers *ir.ServiceCallers) *ir.FieldDef {
	return &ir.FieldDef{Name: name, ServiceCallers: callers}
}

var (
	requireClause = &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}
	allowClause   = &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: []string{"notes-api"}}
)

// TestExpose holds the rule: an API is exposed when an end user or a third
// party can call one of its operations, one whose clause, its own or its
// set's, is none or @allowService; @publicRoute opens an operation in a
// set that requires a service.
func TestExpose(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sets   []*ir.OperationSet
		want   bool
		reason string
	}{
		{"no operations", nil, false, "it has no operations"},
		{"an empty set", []*ir.OperationSet{{Name: "Empty"}}, false, "it has no operations"},
		{"an open operation", []*ir.OperationSet{{Name: "Notes", Operations: []*ir.FieldDef{operation("getNote", nil)}}}, true, "Notes.getNote is not @requireService"},
		{"every operation requires a service", []*ir.OperationSet{{Name: "Index", Operations: []*ir.FieldDef{operation("index", requireClause), operation("reindex", requireClause)}}}, false, "every operation is @requireService"},
		{"the set requires a service", []*ir.OperationSet{{Name: "Index", ServiceCallers: requireClause, Operations: []*ir.FieldDef{operation("index", nil)}}}, false, "every operation is @requireService"},
		{"an operation allows a service", []*ir.OperationSet{{Name: "Index", Operations: []*ir.FieldDef{operation("index", requireClause), operation("release", allowClause)}}}, true, "Index.release is not @requireService"},
		{"a public route in a set that requires a service", []*ir.OperationSet{{Name: "Hooks", ServiceCallers: requireClause, Operations: []*ir.FieldDef{{Name: "ping", Public: true}}}}, true, "Hooks.ping is not @requireService"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stackinit.Expose(&ir.Schema{Name: "notes-api", Kind: ir.SchemaKindAPI, OperationSets: tc.sets})
			assert.Equal(t, stackinit.Exposure{Exposed: tc.want, Reason: tc.reason}, got)
		})
	}
}

// TestExposeForASite: an API a site calls is exposed whatever its
// operations, since D55 requires it.
func TestExposeForASite(t *testing.T) {
	internal := &ir.Schema{OperationSets: []*ir.OperationSet{{Name: "Index", ServiceCallers: requireClause, Operations: []*ir.FieldDef{operation("index", nil)}}}}
	open := &ir.Schema{OperationSets: []*ir.OperationSet{{Name: "Notes", Operations: []*ir.FieldDef{operation("getNote", nil)}}}}
	apis := map[string]*ir.Schema{"notes-api": open, "notes-index": internal, "notes-search": internal}

	exposed, reasons := stackinit.ExposeFor(apis, nil)
	assert.Equal(t, []string{"notes-api"}, exposed)
	assert.False(t, reasons["notes-search"].Exposed)

	exposed, reasons = stackinit.ExposeFor(apis, map[string][]string{"notes-web": {"notes-search"}})
	assert.Equal(t, []string{"notes-api", "notes-search"}, exposed)
	assert.Equal(t, stackinit.Exposure{Exposed: true, Reason: "site notes-web calls it"}, reasons["notes-search"])
	assert.Equal(t, "every operation is @requireService", reasons["notes-index"].Reason)
}

// TestCheckExpose refuses a name the stack does not deploy, and an expose
// list that leaves out an API a site calls.
func TestCheckExpose(t *testing.T) {
	deploy := []ir.ServiceRef{{Name: "notes-api", Kind: ir.SchemaKindAPI}, {Name: "notes-index", Kind: ir.SchemaKindAPI}, {Name: "notes-web", Kind: ir.SchemaKindSite}}
	calls := map[string][]string{"notes-web": {"notes-api"}}
	require.NoError(t, stackinit.CheckExpose([]string{"notes-api"}, deploy, calls))
	require.NoError(t, stackinit.CheckExpose([]string{"notes-api", "notes-web"}, deploy, calls))
	require.EqualError(t, stackinit.CheckExpose([]string{"notes-api", "notes-db"}, deploy, calls), "expose names notes-db, which is no API or Site service of the tree")
	require.EqualError(t, stackinit.CheckExpose([]string{"notes-index"}, deploy, calls), "site notes-web calls notes-api, so notes-api must be exposed (D55)")
	require.EqualError(t, stackinit.CheckExpose(nil, deploy, calls), "site notes-web calls notes-api, so notes-api must be exposed (D55)")
}

// TestDefaultName: the word the deployed services share, else the npm
// scope the naming file sets, else the project's directory.
func TestDefaultName(t *testing.T) {
	acme := naming.Default()
	acme.NpmScope = "@acme"
	root := filepath.Join(string(filepath.Separator), "work", "Notes App", "schemas")
	assert.Equal(t, "shop-stack", stackinit.DefaultName([]string{"shop-api", "shop-orders", "shop-web"}, acme, root))
	assert.Equal(t, "orders-stack", stackinit.DefaultName([]string{"orders"}, acme, root))
	assert.Equal(t, "acme-stack", stackinit.DefaultName([]string{"shop-api", "billing-api"}, acme, root))
	assert.Equal(t, "notes-app-stack", stackinit.DefaultName([]string{"shop-api", "billing-api"}, naming.Default(), root))
	assert.Equal(t, "stack", stackinit.DefaultName([]string{"shop-api", "billing-api"}, naming.Default(), filepath.Join(string(filepath.Separator), "schemas")))
	for _, name := range []string{"shop-stack", "notes-app-stack", "acme-stack", "stack"} {
		require.NoError(t, stackinit.CheckName(name))
	}
}

// TestCheckNames refuses a stack name that is no service directory's and an
// environment name that is no class name.
func TestCheckNames(t *testing.T) {
	for _, bad := range []string{"", "Shop", "shop_stack", "shop--stack", "-shop", "shop-", "1shop"} {
		assert.Error(t, stackinit.CheckName(bad), bad)
	}
	for _, bad := range []string{"", "staging", "Staging-1", "1Staging"} {
		assert.Error(t, stackinit.CheckEnvironment(bad), bad)
	}
	require.NoError(t, stackinit.CheckEnvironment("Staging2"))
}

// TestClassName: the stack's name without -stack, as a class, made unique
// against the handles and environments the schema declares.
func TestClassName(t *testing.T) {
	assert.Equal(t, "Shop", stackinit.ClassName("shop-stack", nil))
	assert.Equal(t, "ShopStackInit", stackinit.ClassName("shop-stack-init", nil))
	assert.Equal(t, "Stack", stackinit.ClassName("stack", nil))
	assert.Equal(t, "ShopApiStack", stackinit.ClassName("shop-api-stack", []string{"ShopApi", "Dev"}))
	assert.Equal(t, "DevStack", stackinit.ClassName("dev-stack", []string{"Dev"}))
}

// assemble is the core registry with stacktest's fake target.
func assemble(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	require.NoError(t, err)
	return reg
}

// goldenPlan is a stack over the fixture tree with both environments and
// CI, on stacktest's fake target in gcp's place.
func goldenPlan() *stackinit.Plan {
	kinds := map[string]ir.SchemaKind{"notes-api": ir.SchemaKindAPI, "notes-index": ir.SchemaKindAPI, "notes-web": ir.SchemaKindSite}
	return &stackinit.Plan{
		Name:   "notes-stack",
		Deploy: stackinit.Refs([]string{"notes-api", "notes-index", "notes-web"}, kinds),
		Expose: stackinit.Refs([]string{"notes-api"}, kinds),
		CI:     []string{"github"},
		Environments: []stackinit.Environment{
			{Name: "Dev", Target: "local", Comment: "This machine."},
			{Name: "Staging", Target: stacktest.Target, Values: map[string]any{"project": "notes-staging", "region": "us-east1"}, Comment: "The cloud."},
		},
	}
}

// TestRenderGolden holds the files of a stack with a local and a cloud
// environment and CI to testdata/golden/notes-stack: the schema the
// TypeScript writer writes, the config, the package and tsconfig a
// sibling's give, and the target's types read from its JSON Schemas. Run
// with -update to rewrite them.
func TestRenderGolden(t *testing.T) {
	reg := assemble(t)
	names := naming.Default()
	naming.SetActive(names)
	sibling := stackinit.Sibling{
		TSConfig:       []byte(`{"extends": "../../tsconfig.base.json", "include": ["schema.config.ts", "src/**/*.ts"], "exclude": ["src/**/*.test.ts"]}`),
		HasPackageJSON: true,
	}
	files, err := stackinit.Render(goldenPlan(), sibling, names, reg)
	require.NoError(t, err)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
		golden := filepath.Join("testdata", "golden", "notes-stack", filepath.FromSlash(f.Path))
		if *update {
			require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
			require.NoError(t, os.WriteFile(golden, f.Content, 0o644))
			continue
		}
		want, err := os.ReadFile(golden)
		require.NoError(t, err, "run with -update to write %s", golden)
		assert.Equal(t, string(want), string(f.Content), f.Path)
	}
	assert.Equal(t, []string{"package.json", "tsconfig.json", "schema.config.ts", "src/stack.schema.ts", "src/fake-target.ts"}, paths)
}

// TestRenderWithoutAPackage: a sibling with no package.json gives none, a
// tsconfig that extends nothing is copied as it is, and a stack with no
// cloud environment and no CI writes no target types and no outputs.
func TestRenderWithoutAPackage(t *testing.T) {
	reg := assemble(t)
	names := naming.Default()
	plan := goldenPlan()
	plan.Environments = plan.Environments[:1]
	plan.CI = nil
	tsconfig := []byte("// a comment\n{ \"compilerOptions\": {} }\n")
	files, err := stackinit.Render(plan, stackinit.Sibling{TSConfig: tsconfig}, names, reg)
	require.NoError(t, err)
	require.Len(t, files, 3)
	assert.Equal(t, stackinit.TSConfig, files[0].Path)
	assert.Equal(t, tsconfig, files[0].Content)
	assert.Contains(t, string(files[1].Content), "outputs: {}")
	assert.Equal(t, stackinit.SchemaFile, files[2].Path)
}

// TestRenderRefusesAnUnlinkedTarget: an environment on a target the
// binary does not link has no types to write.
func TestRenderRefusesAnUnlinkedTarget(t *testing.T) {
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming())
	require.NoError(t, err)
	_, err = stackinit.Render(goldenPlan(), stackinit.Sibling{TSConfig: []byte("{}")}, naming.Default(), reg)
	require.EqualError(t, err, "environment Staging names target fake, which this binary does not link")
}

// TestTargetTypesReadTheSchemas: each JSON Schema a target registers reads
// as the TypeScript type tsc checks the same values with.
func TestTargetTypesReadTheSchemas(t *testing.T) {
	reg := assemble(t)
	target, ok := reg.Target(stacktest.Target)
	require.True(t, ok)
	got, err := stackinit.TargetTypes(target, reg, naming.Default())
	require.NoError(t, err)
	for _, want := range []string{
		`declare module "@superschematic/stack" {`,
		"      values: { project: string; projectNumber?: string; region: string; production?: boolean };",
		`      database: { tier?: "small" | "large"; highAvailability?: boolean };`,
		"      server: { minInstances?: number; public?: boolean };",
	} {
		assert.Contains(t, string(got), want)
	}
}

// TestTheSchemaDeclaresInOrder: the @stack class comes first, then each
// environment in its order, which the reader numbers them by.
func TestTheSchemaDeclaresInOrder(t *testing.T) {
	reg := assemble(t)
	naming.SetActive(naming.Default())
	schema, err := stackinit.Schema(goldenPlan(), reg)
	require.NoError(t, err)
	text := string(schema)
	assert.True(t, strings.Index(text, "class Notes") < strings.Index(text, "class Dev"))
	assert.True(t, strings.Index(text, "class Dev") < strings.Index(text, "class Staging"))
	assert.Contains(t, text, `@stack({ deploy: [NotesApi, NotesIndex, NotesWeb], expose: [NotesApi] })`)
	assert.Contains(t, text, `@environment({ target: "fake", fake: { project: "notes-staging", region: "us-east1" } })`)
}

// TestTheSchemaExposesEveryApp: init exposes every @app of a deployed API
// after the services, as { of, app }, and says why (D78).
func TestTheSchemaExposesEveryApp(t *testing.T) {
	reg := assemble(t)
	naming.SetActive(naming.Default())
	plan := goldenPlan()
	apis := map[string]*ir.Schema{
		"notes-api":   {Name: "notes-api", Kind: ir.SchemaKindAPI, Apps: []*ir.App{{Name: "Backoffice"}, {Name: "Reports"}}},
		"notes-index": {Name: "notes-index", Kind: ir.SchemaKindAPI},
		"notes-web":   {Name: "notes-web", Kind: ir.SchemaKindSite, Apps: []*ir.App{{Name: "NotAnApi"}}},
	}
	plan.ExposeApps = stackinit.AppsOf(plan.Deploy, apis)
	assert.Equal(t, []stackinit.AppRef{{API: "notes-api", App: "Backoffice"}, {API: "notes-api", App: "Reports"}}, plan.ExposeApps)
	schema, err := stackinit.Schema(plan, reg)
	require.NoError(t, err)
	text := string(schema)
	assert.Contains(t, text, `expose: [NotesApi, { of: NotesApi, app: "Backoffice" }, { of: NotesApi, app: "Reports" }]`)
	assert.Contains(t, text, "It exposes every app of those APIs too")

	plan.ExposeApps = nil
	schema, err = stackinit.Schema(plan, reg)
	require.NoError(t, err)
	assert.NotContains(t, string(schema), "every app")
}

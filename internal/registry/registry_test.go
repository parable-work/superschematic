package registry

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func noopGenerate(GenerateContext) error { return nil }

type fakeExtension struct {
	name     string
	register func(r *Registry) error
}

func (e fakeExtension) Name() string               { return e.name }
func (e fakeExtension) Register(r *Registry) error { return e.register(r) }

func TestNewRegistersCoreKindsWithTodaysPipelines(t *testing.T) {
	reg := New(naming.Naming{})

	wantKinds := []string{"API", "DB", "General"}
	if got := reg.Kinds(); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("Kinds() = %v, want %v", got, wantKinds)
	}

	wantPipelines := map[string][]string{
		"DB":      {"sql", "orm", "types"},
		"API":     {"types", "api", "sdks"},
		"General": {"types", "envConfig"},
	}
	for kind, want := range wantPipelines {
		spec, ok := reg.Kind(kind)
		if !ok {
			t.Fatalf("Kind(%q) missing", kind)
		}
		if !reflect.DeepEqual(spec.Pipeline, want) {
			t.Errorf("Kind(%q).Pipeline = %v, want %v", kind, spec.Pipeline, want)
		}
	}

	db, _ := reg.Kind("DB")
	if db.StructRole != ir.RoleDBTable || db.SourceProjectionRole != "" || db.AllowsOperationSets {
		t.Errorf("DB kind authoring rules = %+v", db)
	}
	api, _ := reg.Kind("API")
	if api.StructRole != ir.RoleEmbeddedStruct || api.SourceProjectionRole != ir.RoleAPIView || !api.AllowsOperationSets {
		t.Errorf("API kind authoring rules = %+v", api)
	}
	if !api.ForbiddenPackages["@superschematic/db"] || api.ForbiddenPackages["@superschematic/api"] {
		t.Errorf("API ForbiddenPackages = %v", api.ForbiddenPackages)
	}
	general, _ := reg.Kind("General")
	if general.SourceProjectionRole != ir.RoleEmbeddedStruct || !general.DeniedReferences["DB"] || len(general.AllowedReferences) != 0 {
		t.Errorf("General kind reference rules = %+v", general)
	}
	if !reflect.DeepEqual(reg.Naming(), naming.Default()) {
		t.Errorf("Naming() should fall back to the defaults, got %+v", reg.Naming())
	}
}

func TestRegisterRejectsDuplicatesAndPostFinalizeRegistration(t *testing.T) {
	reg := New(naming.Default())

	if err := reg.RegisterKind(KindSpec{Name: "DB"}); err == nil || !strings.Contains(err.Error(), `kind "DB" is already registered`) {
		t.Fatalf("duplicate kind error = %v", err)
	}
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "types"}); err == nil || !strings.Contains(err.Error(), "no Generate function") {
		t.Fatalf("generator without Generate error = %v", err)
	}
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "types", Generate: noopGenerate}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "types", Generate: noopGenerate}); err == nil || !strings.Contains(err.Error(), `generator "types" is already registered`) {
		t.Fatalf("duplicate generator error = %v", err)
	}
	if err := reg.RegisterDocument(DocumentSpec{Name: "catalog"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDocument(DocumentSpec{Name: "catalog"}); err == nil {
		t.Fatal("expected duplicate document error")
	}
	if err := reg.RegisterDecorator(DecoratorSpec{Name: "shelf", Packages: []string{"@acme/schematic"}, Target: TargetField}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDecorator(DecoratorSpec{Name: "shelf", Packages: []string{"@acme/schematic"}, Target: TargetField}); err == nil {
		t.Fatal("expected duplicate decorator error for the same target")
	}
	if err := reg.RegisterDecorator(DecoratorSpec{Name: "shelf", Packages: []string{"@acme/schematic"}, Target: TargetType}); err != nil {
		t.Fatalf("same name on another target must register: %v", err)
	}
	if _, ok := reg.Decorator("shelf", TargetOperation); ok {
		t.Fatal("Decorator lookup must be keyed by target")
	}

	for _, name := range []string{"sql", "orm", "api", "sdks", "envConfig"} {
		if err := reg.RegisterGenerator(GeneratorSpec{Name: name, Generate: noopGenerate}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := reg.RegisterKind(KindSpec{Name: "Late"}); err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Fatalf("post-Finalize registration error = %v", err)
	}
}

func TestFinalizeChecksPipelinesAndOutputKeys(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.Finalize(); err == nil || !strings.Contains(err.Error(), `kind API pipeline names unregistered generator "types"`) {
		t.Fatalf("Finalize with no generators = %v", err)
	}

	reg = coreOutputRegistry(t)
	if err := reg.RegisterKind(KindSpec{Name: "Catalog", Pipeline: []string{"catalog"}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "catalog", Kinds: []string{"Shop"}, Generate: noopGenerate}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err == nil || !strings.Contains(err.Error(), `restricted to kinds [Shop]`) {
		t.Fatalf("Finalize with kind-restricted generator = %v", err)
	}

	reg = coreOutputRegistry(t)
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "a", OutputKey: "widget", Generate: noopGenerate}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "b", OutputKey: "widget", Generate: noopGenerate}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err == nil || !strings.Contains(err.Error(), `generators a and b both claim output key "widget"`) {
		t.Fatalf("Finalize with duplicate output key = %v", err)
	}
}

func TestUseIsFailClosed(t *testing.T) {
	reg := New(naming.Default())
	boom := errors.New("boom")
	ext := fakeExtension{name: "acme", register: func(*Registry) error { return boom }}

	err := reg.Use(ext)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "extension acme") {
		t.Fatalf("Use error = %v", err)
	}
	if err := reg.Finalize(); !errors.Is(err, boom) {
		t.Fatalf("Finalize after failed Use = %v, want the Use error", err)
	}
}

func TestPipelineAppendsKindRestrictedGeneratorsInRegistrationOrder(t *testing.T) {
	reg := coreOutputRegistry(t)
	err := reg.Use(fakeExtension{name: "acme", register: func(r *Registry) error {
		return errors.Join(
			r.RegisterKind(KindSpec{Name: "Catalog", Extension: "acme", Pipeline: []string{"types", "catalog"}}),
			r.RegisterGenerator(GeneratorSpec{Name: "catalog", Extension: "acme", Kinds: []string{"Catalog"}, OutputKey: "catalog", Generate: noopGenerate}),
			r.RegisterGenerator(GeneratorSpec{Name: "audit", Extension: "acme", Kinds: []string{"DB", "Catalog"}, OutputKey: "audit", Generate: noopGenerate}),
		)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}

	names := func(specs []GeneratorSpec) []string {
		out := make([]string, 0, len(specs))
		for _, spec := range specs {
			out = append(out, spec.Name)
		}
		return out
	}
	if got := names(reg.Pipeline("DB")); !reflect.DeepEqual(got, []string{"sql", "orm", "types", "audit"}) {
		t.Errorf("Pipeline(DB) = %v", got)
	}
	if got := names(reg.Pipeline("Catalog")); !reflect.DeepEqual(got, []string{"types", "catalog", "audit"}) {
		t.Errorf("Pipeline(Catalog) = %v", got)
	}
	if got := names(reg.Pipeline("API")); !reflect.DeepEqual(got, []string{"types", "api", "sdks"}) {
		t.Errorf("Pipeline(API) = %v", got)
	}
	if got := reg.Pipeline("Nope"); got != nil {
		t.Errorf("Pipeline(unknown) = %v, want nil", got)
	}
}

func TestOutputKeysListsCoreFirstThenExtensionsSorted(t *testing.T) {
	reg := New(naming.Default())
	core := []GeneratorSpec{
		{Name: "types", OutputKey: "types"},
		{Name: "sql"},
		{Name: "orm"},
		{Name: "api", OutputKey: "api"},
		{Name: "sdks", OutputKey: "sdk"},
		{Name: "envConfig"},
		{Name: "zeta", Extension: "acme", OutputKey: "zeta"},
		{Name: "alpha", Extension: "acme", OutputKey: "alpha"},
	}
	for _, spec := range core {
		spec.Generate = noopGenerate
		if err := reg.RegisterGenerator(spec); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"types", "api", "sdk", "alpha", "zeta"}
	if got := reg.OutputKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("OutputKeys() = %v, want %v", got, want)
	}
}

func TestBuildAllHooksKeepRegistrationOrder(t *testing.T) {
	reg := New(naming.Default())
	noop := func(context.Context, BuildAllContext) error { return nil }
	if err := reg.RegisterBuildAllHook(BuildAllHook{Name: "second", Run: noop}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterBuildAllHook(BuildAllHook{Name: "first", Run: noop}); err != nil {
		t.Fatal(err)
	}
	hooks := reg.BuildAllHooks()
	if len(hooks) != 2 || hooks[0].Name != "second" || hooks[1].Name != "first" {
		t.Fatalf("BuildAllHooks() = %+v", hooks)
	}
}

func TestRegisterBuildAllHookRejectsInvalidAndLateRegistrations(t *testing.T) {
	reg := New(naming.Default())
	noop := func(context.Context, BuildAllContext) error { return nil }
	if err := reg.RegisterBuildAllHook(BuildAllHook{Run: noop}); err == nil {
		t.Error("nameless hook: want error")
	}
	if err := reg.RegisterBuildAllHook(BuildAllHook{Name: "merge"}); err == nil {
		t.Error("hook without Run: want error")
	}
	if err := reg.RegisterBuildAllHook(BuildAllHook{Name: "merge", Run: noop}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterBuildAllHook(BuildAllHook{Name: "merge", Run: noop}); err == nil {
		t.Error("duplicate hook: want error")
	}
	for _, name := range []string{"types", "sql", "orm", "api", "sdks", "envConfig"} {
		if err := reg.RegisterGenerator(GeneratorSpec{Name: name, Generate: noopGenerate}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
	err := reg.RegisterBuildAllHook(BuildAllHook{Name: "late", Run: noop})
	if err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Fatalf("post-Finalize registration: got %v, want an after-Finalize error", err)
	}
	if hooks := reg.BuildAllHooks(); len(hooks) != 1 {
		t.Fatalf("BuildAllHooks() after a refused registration = %+v, want the one accepted hook", hooks)
	}
}

func TestExtensionConfigReadsTheNamingTable(t *testing.T) {
	n, err := naming.Parse([]byte("[extension.acme]\nregion = \"eu\"\n"), "test.toml")
	if err != nil {
		t.Fatal(err)
	}
	reg := New(n)
	if got := reg.ExtensionConfig("acme"); got["region"] != "eu" {
		t.Errorf("ExtensionConfig(acme) = %v", got)
	}
	if got := reg.ExtensionConfig("other"); got != nil {
		t.Errorf("ExtensionConfig(other) = %v, want nil", got)
	}
}

// TestPackageAllowsKindDerivesImportRulesFromDecoratorKinds: an authoring
// package is importable by a kind when no decorator is declared in it or
// when one of its decorators allows the kind; a package whose decorators are
// all restricted elsewhere is not.
func TestPackageAllowsKindDerivesImportRulesFromDecoratorKinds(t *testing.T) {
	reg := New(naming.Naming{AuthoringPackages: []string{"@acme/config"}})
	if err := reg.RegisterKind(KindSpec{Name: "Grouping", Extension: "acme"}); err != nil {
		t.Fatal(err)
	}
	apply := func(Node, []any, Site) error { return nil }
	for _, spec := range []DecoratorSpec{
		{Name: "group", Extension: "acme", Packages: []string{"@acme/grouping"}, Target: TargetType, Kinds: []string{"Grouping"}, Apply: apply},
		{Name: "note", Extension: "acme", Packages: []string{"@acme/mixed"}, Target: TargetField, Apply: apply},
		{Name: "pin", Extension: "acme", Packages: []string{"@acme/mixed"}, Target: TargetType, Kinds: []string{"Grouping"}, Apply: apply},
	} {
		if err := reg.RegisterDecorator(spec); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		pkg, kind string
		want      bool
	}{
		{"@acme/grouping", "Grouping", true},
		{"@acme/grouping", string(ir.SchemaKindDB), false},
		{"@acme/mixed", string(ir.SchemaKindDB), true},
		{"@acme/config", string(ir.SchemaKindDB), true},
		{"@superschematic/schema", string(ir.SchemaKindDB), true},
		{"@superschematic/api", string(ir.SchemaKindGeneral), true},
	}
	for _, tc := range cases {
		if got := reg.PackageAllowsKind(tc.pkg, tc.kind); got != tc.want {
			t.Errorf("PackageAllowsKind(%s, %s) = %v, want %v", tc.pkg, tc.kind, got, tc.want)
		}
	}
}

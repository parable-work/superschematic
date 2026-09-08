package verify

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/platform"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// hasError reports whether any error diagnostic contains the substring.
func hasError(r *Result, substr string) bool {
	for _, d := range r.Errors {
		if strings.Contains(d.Error(), substr) {
			return true
		}
	}
	return false
}

func hasWarning(r *Result, substr string) bool {
	for _, d := range r.Warnings {
		if strings.Contains(d.Error(), substr) {
			return true
		}
	}
	return false
}

func errorStrings(r *Result) []string {
	out := make([]string, len(r.Errors))
	for i, d := range r.Errors {
		out[i] = d.Error()
	}
	return out
}

// --- kind/import rules ---

func TestForbiddenToolchainImports(t *testing.T) {
	cases := []struct {
		kind ir.SchemaKind
		pkg  string
	}{
		{ir.SchemaKindAPI, "@superschematic/db"},
		{ir.SchemaKindDB, "@superschematic/api"},
		{ir.SchemaKindGeneral, "@superschematic/db"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind)+"_"+tc.pkg, func(t *testing.T) {
			schema := ir.NewSchema("svc", tc.kind)
			r := Run(schema, Input{ImportSites: []ImportSite{
				{Package: tc.pkg, File: "src/a.schema.ts", Line: 2, Col: 1},
			}})
			want := "a " + string(tc.kind) + " schema cannot import " + tc.pkg
			if !hasError(r, want) {
				t.Errorf("expected %q, got %v", want, errorStrings(r))
			}
			if !hasError(r, "src/a.schema.ts:2:1") {
				t.Errorf("error should carry the import site location, got %v", errorStrings(r))
			}
		})
	}
}

// TestForbiddenToolchainImportsUnderAnAlias: a distribution may re-export
// the core authoring packages under its own names and declare them in
// [package_aliases]. The kind rule is keyed on the declaring package
// (KindSpec.ForbiddenPackages, Registry.PackageAllowsKind); verify folds the
// specifier the schema wrote onto it, so both spellings of a forbidden
// package are rejected and the message names the author's spelling.
func TestForbiddenToolchainImportsUnderAnAlias(t *testing.T) {
	n := naming.Default()
	n.PackageAliases = map[string]string{
		"@acme/db":       "@superschematic/db",
		"@acme/api":      "@superschematic/api",
		"@acme/schema":   "@superschematic/schema",
		"@acme/platform": "@superschematic/platform",
	}
	reg := registry.New(n)
	if err := (platform.Extension{}).Register(reg); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		kind ir.SchemaKind
		pkg  string
	}{
		{ir.SchemaKindAPI, "@superschematic/db"},
		{ir.SchemaKindAPI, "@acme/db"},
		{ir.SchemaKindDB, "@superschematic/api"},
		{ir.SchemaKindDB, "@acme/api"},
		{ir.SchemaKindGeneral, "@superschematic/db"},
		{ir.SchemaKindGeneral, "@acme/db"},
		{ir.SchemaKindGeneral, "@superschematic/api"},
		{ir.SchemaKindGeneral, "@acme/api"},
		{ir.SchemaKindDB, "@superschematic/platform"},
		{ir.SchemaKindDB, "@acme/platform"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind)+"_"+tc.pkg, func(t *testing.T) {
			r := Run(ir.NewSchema("svc", tc.kind), Input{Registry: reg, Naming: n, ImportSites: []ImportSite{
				{Package: tc.pkg, File: "src/a.schema.ts", Line: 2, Col: 1},
			}})
			want := "src/a.schema.ts:2:1: a " + string(tc.kind) + " schema cannot import " + tc.pkg
			if !hasError(r, want) {
				t.Errorf("expected %q, got %v", want, errorStrings(r))
			}
		})
	}
	allowed := []struct {
		kind ir.SchemaKind
		pkg  string
	}{
		{ir.SchemaKindDB, "@superschematic/db"},
		{ir.SchemaKindDB, "@acme/db"},
		{ir.SchemaKindAPI, "@superschematic/api"},
		{ir.SchemaKindAPI, "@acme/api"},
		{ir.SchemaKindGeneral, "@superschematic/schema"},
		{ir.SchemaKindGeneral, "@acme/schema"},
		{ir.SchemaKind(platform.Kind), "@superschematic/platform"},
		{ir.SchemaKind(platform.Kind), "@acme/platform"},
	}
	for _, tc := range allowed {
		r := Run(ir.NewSchema("svc", tc.kind), Input{Registry: reg, Naming: n, ImportSites: []ImportSite{{Package: tc.pkg, File: "src/a.schema.ts"}}})
		if len(r.Errors) > 0 {
			t.Errorf("%s importing %s: unexpected errors %v", tc.kind, tc.pkg, errorStrings(r))
		}
	}
}

// TestKindRestrictedPackageImportIsForbiddenElsewhere: an authoring package
// whose only decorator is restricted to one kind cannot be imported by
// another kind, with the same message and site as a ForbiddenPackages hit;
// the kind it is restricted to imports it freely.
func TestKindRestrictedPackageImportIsForbiddenElsewhere(t *testing.T) {
	reg := registry.New(naming.Naming{})
	if err := reg.RegisterKind(registry.KindSpec{Name: "Grouping", Extension: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDecorator(registry.DecoratorSpec{
		Name: "group", Extension: "test", Packages: []string{"@test/grouping"},
		Target: registry.TargetType, Kinds: []string{"Grouping"},
		Apply: func(registry.Node, []any, registry.Site) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	sites := []ImportSite{{Package: "@test/grouping", File: "src/a.schema.ts", Line: 3, Col: 1}}
	r := Run(ir.NewSchema("svc", ir.SchemaKindDB), Input{Registry: reg, ImportSites: sites})
	if !hasError(r, "src/a.schema.ts:3:1: a DB schema cannot import @test/grouping") {
		t.Errorf("expected the import to be rejected, got %v", errorStrings(r))
	}
	if r := Run(ir.NewSchema("grp", ir.SchemaKind("Grouping")), Input{Registry: reg, ImportSites: sites}); len(r.Errors) > 0 {
		t.Errorf("the restricted kind should import its own package, got %v", errorStrings(r))
	}
}

func TestAllowedToolchainImports(t *testing.T) {
	cases := []struct {
		kind ir.SchemaKind
		pkg  string
	}{
		{ir.SchemaKindDB, "@superschematic/db"},
		{ir.SchemaKindAPI, "@superschematic/api"},
		{ir.SchemaKindGeneral, "@superschematic/schema"},
		{ir.SchemaKindGeneral, "superscalar"},
	}
	for _, tc := range cases {
		schema := ir.NewSchema("svc", tc.kind)
		r := Run(schema, Input{ImportSites: []ImportSite{{Package: tc.pkg, File: "src/a.schema.ts"}}})
		if len(r.Errors) > 0 {
			t.Errorf("%s importing %s: unexpected errors %v", tc.kind, tc.pkg, errorStrings(r))
		}
	}
}

// --- versioned table rules ---

func TestVersionedDBTableWithSingleKeyVerifies(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindDB)
	schema.Types["Tenant"] = &ir.TypeDef{
		Name:      "Tenant",
		Role:      ir.RoleDBTable,
		Versioned: true,
		Owner:     "src/tenant.schema.ts",
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true},
		},
	}

	r := Run(schema, Input{})
	if len(r.Errors) > 0 {
		t.Fatalf("Run returned unexpected errors: %v", errorStrings(r))
	}
}

func TestVersionedRequiresDBTableRole(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindGeneral)
	schema.Types["Widget"] = &ir.TypeDef{
		Name:      "Widget",
		Role:      ir.RoleEmbeddedStruct,
		Versioned: true,
		Owner:     "src/widget.schema.json",
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Key: true},
		},
	}

	r := Run(schema, Input{})
	if !hasError(r, "Widget: @versioned is only allowed on DB table types") {
		t.Fatalf("expected DB table role error, got %v", errorStrings(r))
	}
	if !hasError(r, "src/widget.schema.json") {
		t.Fatalf("expected error to anchor at owner, got %v", errorStrings(r))
	}
}

func TestVersionedRequiresExactlyOneKey(t *testing.T) {
	cases := []struct {
		name   string
		fields []*ir.FieldDef
		want   string
	}{
		{
			name: "no key",
			fields: []*ir.FieldDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}},
			},
			want: "Tenant: @versioned requires exactly one @key field, found 0",
		},
		{
			name: "composite key",
			fields: []*ir.FieldDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true},
				{Name: "tenantId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true},
			},
			want: "Tenant: @versioned requires exactly one @key field, found 2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := ir.NewSchema("svc", ir.SchemaKindDB)
			schema.Types["Tenant"] = &ir.TypeDef{
				Name:      "Tenant",
				Role:      ir.RoleDBTable,
				Versioned: true,
				Owner:     "src/tenant.schema.ts",
				Fields:    tc.fields,
			}

			r := Run(schema, Input{})
			if !hasError(r, tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, errorStrings(r))
			}
		})
	}
}

func TestVersionedRejectsInvalidConfig(t *testing.T) {
	zero := 0
	ninety := 90
	cases := []struct {
		name string
		cfg  *ir.VersionedConfig
		want string
	}{
		{
			name: "non-positive retention",
			cfg:  &ir.VersionedConfig{RetentionDays: &zero},
			want: "Tenant: @versioned retentionDays must be greater than 0",
		},
		{
			name: "unsupported partition",
			cfg:  &ir.VersionedConfig{PartitionBy: "week"},
			want: `Tenant: @versioned partitionBy must be "month" when set`,
		},
		{
			name: "prune keep without retention",
			cfg: &ir.VersionedConfig{
				PruneKeepReferencedBy: []*ir.PruneReference{{Table: "commit_entry", KeyColumn: "entity_id", VersionColumn: "entity_version"}},
			},
			want: "Tenant: @versioned pruneKeepReferencedBy requires retentionDays (it only shapes the generated prune function)",
		},
		{
			name: "prune keep with unsafe identifier",
			cfg: &ir.VersionedConfig{
				RetentionDays:         &ninety,
				PruneKeepReferencedBy: []*ir.PruneReference{{Table: "commit entry; DROP", KeyColumn: "entity_id", VersionColumn: "entity_version"}},
			},
			want: `Tenant: @versioned pruneKeepReferencedBy table must be a snake_case SQL identifier, got "commit entry; DROP"`,
		},
		{
			// Every entry is interpolated into DDL, so the check cannot stop
			// at the first one.
			name: "prune keep with unsafe identifier in a later entry",
			cfg: &ir.VersionedConfig{
				RetentionDays: &ninety,
				PruneKeepReferencedBy: []*ir.PruneReference{
					{Table: "commit_entry", KeyColumn: "entity_id", VersionColumn: "entity_version"},
					{Table: "resolution", KeyColumn: "stash id", VersionColumn: "stash_version"},
				},
			},
			want: `Tenant: @versioned pruneKeepReferencedBy keyColumn must be a snake_case SQL identifier, got "stash id"`,
		},
		{
			name: "prune keep repeating one reference",
			cfg: &ir.VersionedConfig{
				RetentionDays: &ninety,
				PruneKeepReferencedBy: []*ir.PruneReference{
					{Table: "commit_entry", KeyColumn: "entity_id", VersionColumn: "entity_version"},
					{Table: "commit_entry", KeyColumn: "entity_id", VersionColumn: "entity_version"},
				},
			},
			want: "Tenant: @versioned pruneKeepReferencedBy repeats commit_entry(entity_id, entity_version)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := ir.NewSchema("svc", ir.SchemaKindDB)
			schema.Types["Tenant"] = &ir.TypeDef{
				Name:            "Tenant",
				Role:            ir.RoleDBTable,
				Versioned:       true,
				VersionedConfig: tc.cfg,
				Owner:           "src/tenant.schema.ts",
				Fields: []*ir.FieldDef{
					{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true},
				},
			}

			r := Run(schema, Input{})
			if !hasError(r, tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, errorStrings(r))
			}
		})
	}
}

func TestCrossKindReferenceRules(t *testing.T) {
	cases := []struct {
		importer ir.SchemaKind
		dep      ir.SchemaKind
		allowed  bool
	}{
		{ir.SchemaKindAPI, ir.SchemaKindDB, true},
		{ir.SchemaKindAPI, ir.SchemaKindGeneral, true},
		{ir.SchemaKindAPI, ir.SchemaKindAPI, false},
		{ir.SchemaKindDB, ir.SchemaKindGeneral, true},
		{ir.SchemaKindDB, ir.SchemaKindDB, false},
		{ir.SchemaKindDB, ir.SchemaKindAPI, false},
		{ir.SchemaKindGeneral, ir.SchemaKindGeneral, true},
		{ir.SchemaKindGeneral, ir.SchemaKindDB, false},
		{ir.SchemaKindGeneral, ir.SchemaKindAPI, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.importer)+"_imports_"+string(tc.dep), func(t *testing.T) {
			schema := ir.NewSchema("svc", tc.importer)
			// Cross-kind rules apply to codegen imports (schema.Imports), not
			// authoring-only ImportSites used for @source / extends.
			schema.Imports = []ir.Import{{
				Package: "@schemas/other",
				Types:   []string{"Widget"},
			}}
			r := Run(schema, Input{
				Dependencies: map[string]ir.SchemaKind{"other": tc.dep},
				ImportSites: []ImportSite{
					{Package: "@schemas/other", File: "src/a.schema.ts", Line: 3, Col: 1},
				},
			})
			violation := hasError(r, "cannot reference types from")
			if tc.allowed && violation {
				t.Errorf("expected no cross-kind violation, got %v", errorStrings(r))
			}
			if !tc.allowed && !violation {
				t.Errorf("expected a cross-kind violation, got %v", errorStrings(r))
			}
		})
	}
}

func TestUndeclaredDependencyIsAnError(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindAPI)
	schema.Imports = []ir.Import{{
		Package: "@schemas/mystery",
		Types:   []string{"Widget"},
	}}
	r := Run(schema, Input{ImportSites: []ImportSite{
		{Package: "@schemas/mystery", File: "src/a.schema.ts", Line: 1, Col: 1},
	}})
	if !hasError(r, `declares no dependency on service "mystery"`) {
		t.Errorf("expected an undeclared-dependency error, got %v", errorStrings(r))
	}
}

// TestServiceScopeComesFromInputNaming: the service-import scope is the
// configured npm scope carried on Input, not a process-wide value. Under an
// @acme scope an @acme import is a service reference (and so needs a
// declared dependency) while a @parable-platform import is neither a
// toolchain nor a service import and passes untouched.
func TestServiceScopeComesFromInputNaming(t *testing.T) {
	acme := naming.Naming{NpmScope: "@acme"}

	schema := ir.NewSchema("svc", ir.SchemaKindAPI)
	schema.Imports = []ir.Import{{Package: "@acme/mystery", Types: []string{"Widget"}}}
	r := Run(schema, Input{
		Naming: acme,
		ImportSites: []ImportSite{
			{Package: "@acme/mystery", File: "src/a.schema.ts", Line: 1, Col: 1},
		},
	})
	if !hasError(r, `declares no dependency on service "mystery"`) {
		t.Errorf("@acme import must be a service reference under the @acme scope, got %v", errorStrings(r))
	}

	other := ir.NewSchema("svc", ir.SchemaKindAPI)
	other.Imports = []ir.Import{{Package: "@schemas/mystery", Types: []string{"Widget"}}}
	r = Run(other, Input{
		Naming: acme,
		ImportSites: []ImportSite{
			{Package: "@schemas/mystery", File: "src/a.schema.ts", Line: 1, Col: 1},
		},
	})
	if len(r.Errors) != 0 {
		t.Errorf("@parable-platform import is not a service reference under the @acme scope, got %v", errorStrings(r))
	}
}

// TestAuthoringOnlyImportNeedsNoConfigDependency: ImportSites used only for
// @source / extends (absent from schema.Imports) do not require a
// schema.config dependencies entry — TypeScript owns those package imports.
func TestAuthoringOnlyImportNeedsNoConfigDependency(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindAPI)
	r := Run(schema, Input{ImportSites: []ImportSite{
		{Package: "@schemas/web-db", File: "src/a.schema.ts", Line: 1, Col: 1},
	}})
	if len(r.Errors) > 0 {
		t.Errorf("authoring-only import must not require schema.config deps, got %v", errorStrings(r))
	}
}

// TestSiblingSentinelKindServiceImportsAreUnrestricted: a kind whose files
// import member services as sentinels (KindSpec.ImportsSiblingSentinels)
// declares membership, so the cross-kind reference rules and the
// schema.config dependency requirement do not apply to it.
func TestSiblingSentinelKindServiceImportsAreUnrestricted(t *testing.T) {
	reg := registry.New(naming.Naming{})
	if err := reg.RegisterKind(registry.KindSpec{Name: "Grouping", Extension: "test", ImportsSiblingSentinels: true}); err != nil {
		t.Fatal(err)
	}
	schema := ir.NewSchema("deployment", ir.SchemaKind("Grouping"))
	schema.Imports = []ir.Import{{Package: "@schemas/web-api", Types: []string{"WebApi"}}}
	sites := []ImportSite{{Package: "@schemas/web-api", File: "src/p.schema.ts"}}
	r := Run(schema, Input{Registry: reg, ImportSites: sites})
	if len(r.Errors) > 0 {
		t.Errorf("sentinel imports of a sibling-sentinel kind should be unrestricted, got %v", errorStrings(r))
	}
	general := Run(ir.NewSchema("deployment", ir.SchemaKindGeneral), Input{Registry: reg, ImportSites: sites})
	if len(general.Errors) != 0 {
		// A General schema with the import only in ImportSites (not in
		// schema.Imports) is authoring-only and passes too; the contrast
		// case below puts the import in schema.Imports.
		t.Errorf("authoring-only import errored: %v", errorStrings(general))
	}
	withCodegen := ir.NewSchema("deployment", ir.SchemaKindGeneral)
	withCodegen.Imports = schema.Imports
	if r := Run(withCodegen, Input{Registry: reg, ImportSites: sites}); !hasError(r, "declares no dependency on service") {
		t.Errorf("a General schema keeps the dependency rule, got %v", errorStrings(r))
	}
}

// --- @source verification ---

// sourceSchema builds an API schema with a local source table and a
// projection of it, for the @source checks.
func sourceSchema(viewFields []*ir.FieldDef) *ir.Schema {
	schema := ir.NewSchema("svc", ir.SchemaKindAPI)
	schema.Types["Tenant"] = &ir.TypeDef{
		Name: "Tenant",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "name", TypeRef: ir.TypeRef{Name: "Identity.Name"}, Required: true, SourceMustProject: true},
			{Name: "slug", TypeRef: ir.TypeRef{Name: "Identity.Slug"}, Required: true},
		},
	}
	schema.Types["TenantView"] = &ir.TypeDef{
		Name:   "TenantView",
		Owner:  "src/tenant.schema.ts",
		Role:   ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "svc.Tenant"},
		Fields: viewFields,
	}
	return schema
}

func TestSourceProjectionVerifies(t *testing.T) {
	schema := sourceSchema([]*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "name", TypeRef: ir.TypeRef{Name: "Identity.Name"}, Required: true},
		{Name: "userCount", TypeRef: ir.TypeRef{Name: "number"}, Virtual: true},
	})
	r := Run(schema, Input{})
	if len(r.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", errorStrings(r))
	}
	if len(r.Warnings) > 0 {
		t.Fatalf("unexpected warnings (name is projected)")
	}
	src := schema.Types["TenantView"].Source
	if len(src.Virtual) != 1 || src.Virtual[0] != "userCount" {
		t.Errorf("virtual = %v, want [userCount]", src.Virtual)
	}
	if len(src.OmittedFromSource) != 1 || src.OmittedFromSource[0] != "slug" {
		t.Errorf("omittedFromSource = %v, want [slug]", src.OmittedFromSource)
	}
}

func TestSourceUnknownFieldMustBeVirtual(t *testing.T) {
	schema := sourceSchema([]*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "name", TypeRef: ir.TypeRef{Name: "Identity.Name"}, Required: true},
		{Name: "mystery", TypeRef: ir.TypeRef{Name: "string"}},
	})
	r := Run(schema, Input{})
	if !hasError(r, "TenantView.mystery is not in source type svc.Tenant and is not marked @virtual") {
		t.Errorf("expected the @virtual hard error, got %v", errorStrings(r))
	}
}

func TestSourceTypeMismatchIsAnError(t *testing.T) {
	schema := sourceSchema([]*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "name", TypeRef: ir.TypeRef{Name: "Identity.Name"}, Required: true},
	})
	r := Run(schema, Input{})
	if !hasError(r, `TenantView.id type "string" is not assignable`) {
		t.Errorf("expected an assignability error, got %v", errorStrings(r))
	}
}

func TestSourceArrayMismatchIsAnError(t *testing.T) {
	schema := sourceSchema([]*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID", IsArray: true}, Required: true},
		{Name: "name", TypeRef: ir.TypeRef{Name: "Identity.Name"}, Required: true},
	})
	r := Run(schema, Input{})
	if !hasError(r, `TenantView.id type "Identity.UUID[]" is not assignable`) {
		t.Errorf("expected an array-ness error, got %v", errorStrings(r))
	}
}

func TestSourceNestedProjectionTypeIsAssignable(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindAPI)
	schema.Types["Property"] = &ir.TypeDef{
		Name: "Property",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "entityKey", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}
	schema.Types["StoragePackage"] = &ir.TypeDef{
		Name: "StoragePackage",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{
				Name:              "perceptions",
				TypeRef:           ir.TypeRef{Name: "Property", IsArray: true},
				Required:          true,
				SourceMustProject: true,
			},
		},
	}
	schema.Types["Perception"] = &ir.TypeDef{
		Name:   "Perception",
		Role:   ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "svc.Property"},
		Fields: []*ir.FieldDef{
			{Name: "entityKey", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}
	schema.Types["EffectivePackage"] = &ir.TypeDef{
		Name:   "EffectivePackage",
		Role:   ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "svc.StoragePackage"},
		Fields: []*ir.FieldDef{
			{Name: "perceptions", TypeRef: ir.TypeRef{Name: "Perception", IsArray: true}, Required: true},
		},
	}

	r := Run(schema, Input{})
	if len(r.Errors) > 0 {
		t.Fatalf("nested @source projection produced errors: %v", errorStrings(r))
	}
	if len(r.Warnings) > 0 {
		t.Fatalf("nested @source projection omitted a must-project field: %v", r.Warnings)
	}
}

func TestSourceNestedProjectionMustReachDeclaredSource(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindAPI)
	schema.Types["Property"] = &ir.TypeDef{Name: "Property", Role: ir.RoleDBTable}
	schema.Types["Page"] = &ir.TypeDef{Name: "Page", Role: ir.RoleDBTable}
	schema.Types["StoragePackage"] = &ir.TypeDef{
		Name: "StoragePackage",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "perceptions", TypeRef: ir.TypeRef{Name: "Property", IsArray: true}, Required: true},
		},
	}
	schema.Types["Perception"] = &ir.TypeDef{
		Name:   "Perception",
		Role:   ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "svc.Page"},
	}
	schema.Types["EffectivePackage"] = &ir.TypeDef{
		Name:   "EffectivePackage",
		Role:   ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "svc.StoragePackage"},
		Fields: []*ir.FieldDef{
			{Name: "perceptions", TypeRef: ir.TypeRef{Name: "Perception", IsArray: true}, Required: true},
		},
	}

	r := Run(schema, Input{})
	if !hasError(r, `EffectivePackage.perceptions type "Perception[]" is not assignable`) {
		t.Fatalf("projection targeting Page was accepted for Property[]: %v", errorStrings(r))
	}
}

func TestSourceMustProjectWarning(t *testing.T) {
	schema := sourceSchema([]*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
	})
	r := Run(schema, Input{})
	if len(r.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", errorStrings(r))
	}
	if !hasWarning(r, "TenantView omits svc.Tenant.name, which is marked @sourceMustProject") {
		t.Errorf("expected the must-project warning, got %d warnings", len(r.Warnings))
	}
}

func TestSourceCrossServiceResolvesThroughExternals(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindAPI)
	schema.Types["UserView"] = &ir.TypeDef{
		Name:   "UserView",
		Role:   ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "web-db.User"},
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "nickname", TypeRef: ir.TypeRef{Name: "string"}},
		},
	}
	r := Run(schema, Input{ExternalTypes: map[string]*ir.TypeDef{
		"web-db.User": {Name: "User", Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		}},
	}})
	if !hasError(r, "UserView.nickname is not in source type web-db.User and is not marked @virtual") {
		t.Errorf("expected the @virtual hard error through externals, got %v", errorStrings(r))
	}
}

func TestSourceUnresolvableTargetIsAnError(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindAPI)
	schema.Types["UserView"] = &ir.TypeDef{
		Name:   "UserView",
		Role:   ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "web-db.User"},
	}
	r := Run(schema, Input{})
	if !hasError(r, `UserView: cannot resolve @source target "web-db.User"`) {
		t.Errorf("expected an unresolvable-target error, got %v", errorStrings(r))
	}
}

func TestSourceImportedTargetStandsAsAuthored(t *testing.T) {
	// A data-format schema declaring the target in its imports block keeps
	// its recorded SourceRef; structural checks need the resolved type.
	schema := ir.NewSchema("svc", ir.SchemaKindAPI)
	schema.Imports = []ir.Import{{Package: "@schemas/web-db", Types: []string{"User"}}}
	schema.Types["UserView"] = &ir.TypeDef{
		Name:   "UserView",
		Role:   ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "web-db.User", Virtual: []string{"nickname"}},
		Fields: []*ir.FieldDef{
			{Name: "nickname", TypeRef: ir.TypeRef{Name: "string"}, Virtual: true},
		},
	}
	r := Run(schema, Input{Dependencies: map[string]ir.SchemaKind{"web-db": ir.SchemaKindDB}})
	if len(r.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", errorStrings(r))
	}
	if got := schema.Types["UserView"].Source.Virtual; len(got) != 1 || got[0] != "nickname" {
		t.Errorf("authored SourceRef should stand, got virtual = %v", got)
	}
}

func TestSourceProjectionAllowedInGeneralContract(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindGeneral)
	schema.Types["Storage"] = &ir.TypeDef{
		Name: "Storage",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}
	schema.Types["View"] = &ir.TypeDef{
		Name:   "View",
		Role:   ir.RoleEmbeddedStruct,
		Source: &ir.SourceRef{Target: "svc.Storage"},
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}
	r := Run(schema, Input{})
	if len(r.Errors) > 0 {
		t.Fatalf("general projection contract produced errors: %v", errorStrings(r))
	}
}

func TestSourceOutsideProjectionKindsIsAnError(t *testing.T) {
	schema := ir.NewSchema("svc", ir.SchemaKindDB)
	schema.Types["View"] = &ir.TypeDef{
		Name:   "View",
		Source: &ir.SourceRef{Target: "svc.View"},
	}
	r := Run(schema, Input{})
	if !hasError(r, "@source projections are only allowed in API or General schemas") {
		t.Errorf("expected a kind error, got %v", errorStrings(r))
	}
}

// --- trait shape checks ---

// traitSchema builds a schema with a configurable trait, a marker trait, and
// one implementer carrying the given refs.
func traitSchema(implements []ir.TraitRef) *ir.Schema {
	schema := ir.NewSchema("svc", ir.SchemaKindGeneral)
	schema.Types["Tagged"] = &ir.TypeDef{
		Name: "Tagged", Role: ir.RoleTrait, IsTrait: true,
		TraitConfig: &ir.TraitConfigSchema{Fields: []*ir.FieldDef{
			{Name: "channel", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "priority", TypeRef: ir.TypeRef{Name: "number"}},
		}},
	}
	schema.Types["Reviewed"] = &ir.TypeDef{Name: "Reviewed", Role: ir.RoleTrait, IsTrait: true}
	schema.Types["Notification"] = &ir.TypeDef{
		Name: "Notification", Owner: "src/tags.schema.ts", Role: ir.RoleEmbeddedStruct,
		Implements: implements,
	}
	return schema
}

func TestTraitConfigArgsVerify(t *testing.T) {
	r := Run(traitSchema([]ir.TraitRef{
		{Name: "Tagged", ConfigArgs: map[string]any{"channel": "email", "priority": float64(2)}},
		{Name: "Reviewed"},
	}), Input{})
	if len(r.Errors) > 0 {
		t.Errorf("unexpected errors: %v", errorStrings(r))
	}
}

func TestTraitMissingRequiredConfig(t *testing.T) {
	r := Run(traitSchema([]ir.TraitRef{
		{Name: "Tagged", ConfigArgs: map[string]any{"priority": float64(2)}},
	}), Input{})
	if !hasError(r, `Notification implements Tagged without required config "channel"`) {
		t.Errorf("expected a missing-config error, got %v", errorStrings(r))
	}
}

func TestTraitUnknownConfigKey(t *testing.T) {
	r := Run(traitSchema([]ir.TraitRef{
		{Name: "Tagged", ConfigArgs: map[string]any{"channel": "email", "color": "red"}},
	}), Input{})
	if !hasError(r, `Notification supplies unknown config "color" to trait Tagged`) {
		t.Errorf("expected an unknown-key error, got %v", errorStrings(r))
	}
}

func TestTraitConfigValueTypeMismatch(t *testing.T) {
	r := Run(traitSchema([]ir.TraitRef{
		{Name: "Tagged", ConfigArgs: map[string]any{"channel": float64(7)}},
	}), Input{})
	if !hasError(r, `Notification config "channel" for trait Tagged must be a string`) {
		t.Errorf("expected a value-type error, got %v", errorStrings(r))
	}
}

func TestMarkerTraitRejectsConfigArgs(t *testing.T) {
	r := Run(traitSchema([]ir.TraitRef{
		{Name: "Reviewed", ConfigArgs: map[string]any{"channel": "email"}},
	}), Input{})
	if !hasError(r, "trait Reviewed takes no configuration but Notification supplies config arguments") {
		t.Errorf("expected a marker-trait error, got %v", errorStrings(r))
	}
}

func TestImplementingNonTraitIsAnError(t *testing.T) {
	schema := traitSchema([]ir.TraitRef{{Name: "Plain"}})
	schema.Types["Plain"] = &ir.TypeDef{Name: "Plain", Role: ir.RoleEmbeddedStruct}
	r := Run(schema, Input{})
	if !hasError(r, "Notification implements Plain, which is not a trait") {
		t.Errorf("expected a non-trait error, got %v", errorStrings(r))
	}
}

func TestOpenTraitConfigAcceptsAnyKeys(t *testing.T) {
	schema := traitSchema(nil)
	schema.Types["Branded"] = &ir.TypeDef{
		Name: "Branded", Role: ir.RoleTrait, IsTrait: true,
		TraitConfig: &ir.TraitConfigSchema{},
	}
	schema.Types["Notification"].Implements = []ir.TraitRef{
		{Name: "Branded", ConfigArgs: map[string]any{"anything": true}},
	}
	r := Run(schema, Input{})
	if len(r.Errors) > 0 {
		t.Errorf("open config should accept any keys, got %v", errorStrings(r))
	}
}

// --- diagnostics ---

func TestDiagnosticFormatting(t *testing.T) {
	cases := []struct {
		d    Diagnostic
		want string
	}{
		{Diagnostic{File: "src/a.schema.ts", Line: 3, Col: 7, Msg: "boom"}, "src/a.schema.ts:3:7: boom"},
		{Diagnostic{File: "src/a.schema.yaml", Msg: "boom"}, "src/a.schema.yaml: boom"},
		{Diagnostic{Msg: "boom"}, "boom"},
	}
	for _, tc := range cases {
		if got := tc.d.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestResultErrJoinsErrors(t *testing.T) {
	r := &Result{}
	if r.Err() != nil {
		t.Error("clean result should have nil Err")
	}
	r.errorf("a.ts", "first")
	r.errorf("b.ts", "second")
	err := r.Err()
	if err == nil || !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Errorf("Err() should join all diagnostics, got %v", err)
	}
}

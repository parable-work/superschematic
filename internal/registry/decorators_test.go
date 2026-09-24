package registry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func TestNewRegistersCoreDecoratorsForEveryWalkerCase(t *testing.T) {
	reg := New(naming.Naming{})
	want := map[DecoratorTarget][]string{
		TargetType:         {"trait", "source", "envVars", "jsonField", "denyUnknownFields", "strictJSON", "versioned", "index"},
		TargetField:        {"key", "unique", "searchField", "jsonField", "uiHidden", "internalMetadata", "temporalFormat", "virtual", "sourceMustProject", "docs", "purpose", "icon"},
		TargetOperationSet: {"rateLimit", "bodyLimit", "timeout"},
		TargetOperation:    {"rest", "requirePermission", "requireOwnership", "auth", "encrypted", "publicRoute", "webhook", "hmacVerified", "manualRouteRegistration", "rateLimit", "bodyLimit", "timeout", "docs", "mcp", "icon"},
	}
	total := 0
	for target, names := range want {
		for _, name := range names {
			spec, ok := reg.Decorator(name, target)
			if !ok {
				t.Errorf("core decorator @%s missing for target %v", name, target)
				continue
			}
			if spec.Extension != "" || len(spec.Packages) == 0 {
				t.Errorf("@%s: core spec must have no extension and at least one package: %+v", name, spec)
			}
			total++
		}
	}
	if got := len(reg.Decorators()); got != total {
		t.Errorf("Decorators() = %d specs, want %d", got, total)
	}
	if len(reg.Extensions()) != 0 {
		t.Errorf("core registry lists extensions: %v", reg.Extensions())
	}
}

func TestIsAuthoringPackageCoversNamingAndDecoratorPackages(t *testing.T) {
	reg := New(naming.Naming{})
	for _, pkg := range []string{"@superschematic/api", "@superschematic/db", "@superschematic/schema", "@superschematic/schema-config", "superscalar", "@superschematic/deploy"} {
		if !reg.IsAuthoringPackage(pkg) {
			t.Errorf("%s must be an authoring package by default", pkg)
		}
	}
	if reg.IsAuthoringPackage("@acme/schematic") || reg.IsAuthoringPackage("") {
		t.Fatal("unregistered packages must not be authoring packages")
	}
	err := reg.RegisterDecorator(DecoratorSpec{
		Name: "shelf", Extension: "acme", Packages: []string{"@acme/schematic"}, Target: TargetField,
		Apply: func(Node, []any, Site) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reg.IsAuthoringPackage("@acme/schematic") {
		t.Error("a registered decorator's package must join the authoring set")
	}
	if got := reg.Extensions(); len(got) != 1 || got[0] != "acme" {
		t.Errorf("Extensions() = %v, want [acme]", got)
	}

	fork := New(naming.Naming{ScalarNpmPackage: "@acme/scalars"})
	if !fork.IsAuthoringPackage("@acme/scalars") {
		t.Error("a renamed scalar package must be an authoring package")
	}
}

// The core packages are authoring packages by registration, not by list:
// every core decorator declares one of the @superschematic/* names, so they
// resolve with a Naming whose list names none of them, and a distribution
// that re-exports them under its own scope adds the aliases through
// [package_aliases], which also joins the authoring set.
func TestCorePackagesAreAuthoringByRegistration(t *testing.T) {
	core := []string{pkgAPI, pkgDB, pkgSchema, pkgSchemaConfig}
	bare := New(naming.Naming{AuthoringPackages: []string{"@acme/schematic"}, ScalarNpmPackage: "@acme/scalars"})
	for _, pkg := range core {
		if !bare.IsAuthoringPackage(pkg) {
			t.Errorf("%s must be an authoring package with no list naming it", pkg)
		}
	}
	if bare.IsAuthoringPackage("@acme/db") {
		t.Error("@acme/db is authoring only through Naming.AuthoringPackages or PackageAliases")
	}
	if scope := bare.Naming().AuthoringScope(); scope != "@acme" {
		t.Errorf("AuthoringScope() = %q, want @acme", scope)
	}

	aliased := New(naming.Naming{PackageAliases: map[string]string{"@acme/db": pkgDB}})
	if !aliased.IsAuthoringPackage("@acme/db") {
		t.Error("an aliased specifier must be an authoring package")
	}

	defaults := New(naming.Default())
	for _, pkg := range append(core, "superscalar") {
		if !defaults.IsAuthoringPackage(pkg) {
			t.Errorf("%s must be an authoring package under the defaults", pkg)
		}
	}
	if scope := defaults.Naming().AuthoringScope(); scope != "@superschematic" {
		t.Errorf("AuthoringScope() = %q, want @superschematic (the list must stay single-scope)", scope)
	}
}

func TestRegisterDecoratorValidatesSpec(t *testing.T) {
	reg := New(naming.Naming{})
	noop := func(Node, []any, Site) error { return nil }
	cases := map[string]DecoratorSpec{
		"no package":         {Name: "x", Target: TargetField, Apply: noop},
		"extension no apply": {Name: "x", Extension: "acme", Packages: []string{"@acme/s"}, Target: TargetField},
		"bad args schema":    {Name: "x", Packages: []string{"@acme/s"}, Target: TargetField, Apply: noop, Args: json.RawMessage(`{"type": 12}`)},
	}
	for name, spec := range cases {
		if err := reg.RegisterDecorator(spec); err == nil {
			t.Errorf("%s: expected registration error", name)
		}
	}
}

func TestValidateArgsAndDecodeArgs(t *testing.T) {
	reg := New(naming.Naming{})
	err := reg.RegisterDecorator(DecoratorSpec{
		Name: "shelf", Extension: "acme", Packages: []string{"@acme/schematic"}, Target: TargetField,
		Args:  json.RawMessage(`{"type":"object","required":["aisle"],"properties":{"aisle":{"type":"integer","minimum":0}},"additionalProperties":false}`),
		Apply: func(Node, []any, Site) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := reg.Decorator("shelf", TargetField)
	if err := spec.ValidateArgs([]any{map[string]any{"aisle": float64(3)}}); err != nil {
		t.Errorf("valid argument rejected: %v", err)
	}
	if err := spec.ValidateArgs(nil); err == nil || !strings.Contains(err.Error(), "@shelf takes exactly one argument") {
		t.Errorf("missing argument error = %v", err)
	}
	err = spec.ValidateArgs([]any{map[string]any{"aisle": float64(-1)}})
	if err == nil || !strings.HasPrefix(err.Error(), "@shelf argument:") {
		t.Errorf("schema violation error = %v", err)
	}
	if err := spec.ValidateArgs([]any{map[string]any{"bay": "b"}}); err == nil {
		t.Error("additional property accepted")
	}

	var shelf struct {
		Aisle int `json:"aisle"`
	}
	if err := DecodeArgs([]any{map[string]any{"aisle": float64(3)}}, &shelf); err != nil || shelf.Aisle != 3 {
		t.Errorf("DecodeArgs = %v, shelf = %+v", err, shelf)
	}
	if err := DecodeArgs(nil, &shelf); err == nil {
		t.Error("DecodeArgs accepted zero arguments")
	}

	core, _ := reg.Decorator("key", TargetField)
	if err := core.ValidateArgs([]any{"anything"}); err != nil {
		t.Errorf("decorators without Args must not validate: %v", err)
	}
}

func TestKindErrorAndTargetNames(t *testing.T) {
	reg := New(naming.Naming{})
	source, _ := reg.Decorator("source", TargetType)
	if got := source.KindError("DB"); got != "@source is only allowed in API or General schemas (this service is kind DB)" {
		t.Errorf("KindError = %q", got)
	}
	if !source.AllowsKind("API") || source.AllowsKind("DB") {
		t.Error("AllowsKind disagrees with Kinds")
	}
	if source.Apply != nil {
		t.Error("@source is a frontend marker and must not have Apply")
	}
	for target, want := range map[DecoratorTarget]string{
		TargetType: "a type declaration", TargetField: "a field",
		TargetOperationSet: "an operation set", TargetOperation: "an operation",
	} {
		if target.String() != want {
			t.Errorf("%d.String() = %q, want %q", target, target.String(), want)
		}
	}
}

func TestCoreApplyBodiesReproduceWalkerBehaviour(t *testing.T) {
	reg := New(naming.Naming{})
	apply := func(name string, target DecoratorTarget, n Node, args ...any) error {
		spec, ok := reg.Decorator(name, target)
		if !ok {
			t.Fatalf("no core decorator @%s", name)
		}
		return spec.Apply(n, args, Site{})
	}

	td := &ir.TypeDef{}
	if err := apply("denyUnknownFields", TargetType, Node{Type: td}); err != nil || !td.DenyUnknownFields {
		t.Errorf("denyUnknownFields = %v, err %v", td.DenyUnknownFields, err)
	}
	if err := apply("strictJSON", TargetType, Node{Type: td}); err != nil || !td.StrictJSON {
		t.Errorf("strictJSON = %v, err %v", td.StrictJSON, err)
	}
	if err := apply("index", TargetType, Node{Type: td}, []any{"a", "b"}, map[string]any{"unique": true, "name": "digest"}); err != nil {
		t.Fatal(err)
	}
	if len(td.Indexes) != 1 || !td.Indexes[0].Unique || td.Indexes[0].Name != "digest" || len(td.Indexes[0].Keys) != 2 {
		t.Errorf("index = %+v", td.Indexes)
	}
	err := apply("index", TargetType, Node{Type: td}, []any{"a"}, map[string]any{"name": "Not Snake"})
	var argErr *ArgError
	if !errors.As(err, &argErr) || argErr.Index != 1 || argErr.Msg != `@index name "Not Snake" must be a lowercase identifier (e.g. digest)` {
		t.Errorf("index name error = %v", err)
	}

	for _, name := range []string{"envVars", "versioned", "trait", "source"} {
		if spec, _ := reg.Decorator(name, TargetType); spec.Apply != nil {
			t.Errorf("@%s must stay a marker: the walker reads it before field resolution", name)
		}
	}
	for _, name := range []string{"rateLimit", "bodyLimit", "timeout"} {
		if spec, _ := reg.Decorator(name, TargetOperation); !spec.RecordsErrors() {
			t.Errorf("@%s on an operation must record errors and keep the operation", name)
		}
	}

	fd := &ir.FieldDef{TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}}
	if err := apply("temporalFormat", TargetField, Node{Field: fd}, "unix_millis"); err != nil || fd.TemporalFormat != "unix_millis" {
		t.Errorf("temporalFormat = %q, err %v", fd.TemporalFormat, err)
	}
	if err := apply("temporalFormat", TargetField, Node{Field: fd}, "weeks"); err == nil || err.Error() != `@temporalFormat "weeks" is not a declared epoch unit (unix, unix_millis, unix_micros, unix_nanos)` {
		t.Errorf("temporalFormat error = %v", err)
	}

	set := &ir.OperationSet{}
	if err := apply("rateLimit", TargetOperationSet, Node{OperationSet: set}, map[string]any{"requestsPerMinute": float64(60)}); err != nil || *set.Middleware.RateLimit != 60 {
		t.Errorf("rateLimit = %+v, err %v", set.Middleware, err)
	}
	if err := apply("timeout", TargetOperationSet, Node{OperationSet: set}, map[string]any{"seconds": "5"}); err == nil || err.Error() != "@timeout requires a literal seconds" {
		t.Errorf("timeout error = %v", err)
	}

	op := &ir.FieldDef{}
	if err := apply("rest", TargetOperation, Node{Field: op}, "GET", "tenants/{id}"); err != nil || op.HTTPMethod != "GET" || op.RestPath != "tenants/{id}" {
		t.Errorf("rest = %+v, err %v", op, err)
	}
	if err := apply("rest", TargetOperation, Node{Field: op}, "FETCH"); err == nil || err.Error() != `unsupported HTTP method "FETCH"` {
		t.Errorf("rest error = %v", err)
	}
	if err := apply("requirePermission", TargetOperation, Node{Field: op}, []any{}); err == nil || err.Error() != "@requirePermission requires a non-empty array of permission strings" {
		t.Errorf("requirePermission error = %v", err)
	}
	if err := apply("hmacVerified", TargetOperation, Node{Field: op}, map[string]any{"provider": ""}); err == nil || err.Error() != "@hmacVerified requires a literal provider" {
		t.Errorf("hmacVerified error = %v", err)
	}
	if err := apply("bodyLimit", TargetOperation, Node{Field: op}, map[string]any{"megabytes": float64(1)}); err != nil || *op.Middleware.BodyLimit != 1 {
		t.Errorf("operation bodyLimit = %+v, err %v", op.Middleware, err)
	}
}

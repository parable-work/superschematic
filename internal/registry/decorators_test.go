package registry

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func TestNewRegistersCoreDecoratorsForEveryWalkerCase(t *testing.T) {
	reg := New(naming.Naming{})
	want := map[DecoratorTarget][]string{
		TargetType:         {"trait", "source", "envVars", "jsonField", "denyUnknownFields", "strictJSON", "versioned", "optimistic", "versionGraph", "graphMember", "index", "projection", "join", "behavior", "stack", "server", "database", "environment"},
		TargetField:        {"key", "unique", "searchField", "jsonField", "uiHidden", "internalMetadata", "temporalFormat", "conflictUnit", "virtual", "sourceMustProject", "docs", "purpose", "icon", "column"},
		TargetOperationSet: {"rateLimit", "bodyLimit", "timeout", "requireService", "allowService", "userSessions", "userAdministration"},
		TargetOperation:    {"rest", "requirePermission", "requireOwnership", "auth", "encrypted", "publicRoute", "webhook", "hmacVerified", "manualRouteRegistration", "rateLimit", "bodyLimit", "timeout", "requireService", "allowService", "docs", "mcp", "icon"},
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

	for _, name := range []string{"envVars", "versioned", "versionGraph", "graphMember", "trait", "source"} {
		if spec, _ := reg.Decorator(name, TargetType); spec.Apply != nil {
			t.Errorf("@%s must stay a marker: the walker reads it before field resolution", name)
		}
	}
	for _, name := range []string{"rateLimit", "bodyLimit", "timeout"} {
		if spec, _ := reg.Decorator(name, TargetOperation); !spec.RecordsErrors() {
			t.Errorf("@%s on an operation must record errors and keep the operation", name)
		}
	}

	unit := &ir.FieldDef{TypeRef: ir.TypeRef{Name: "Generic.JSON"}}
	if err := apply("conflictUnit", TargetField, Node{Field: unit}, "keyed"); err != nil || unit.ConflictUnit != ir.ConflictUnitKeyed {
		t.Errorf("conflictUnit = %q, err %v", unit.ConflictUnit, err)
	}
	if err := apply("conflictUnit", TargetField, Node{Field: unit}, "rows"); err == nil || err.Error() != `@conflictUnit "rows" is not a strategy (atomic, keyed, jsonSchema, excluded)` {
		t.Errorf("conflictUnit error = %v", err)
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
	// A limit is a whole number of at least 1: no server can apply 0, and a
	// fraction was truncated, 0.5 seconds to 0.
	for _, tc := range []struct {
		name, key string
		value     float64
		want      string
	}{
		{"rateLimit", "requestsPerMinute", 0, "@rateLimit requestsPerMinute must be a whole number of at least 1, not 0"},
		{"bodyLimit", "megabytes", -2, "@bodyLimit megabytes must be a whole number of at least 1, not -2"},
		{"timeout", "seconds", 0.5, "@timeout seconds must be a whole number of at least 1, not 0.5"},
		{"rateLimit", "requestsPerMinute", 1.5, "@rateLimit requestsPerMinute must be a whole number of at least 1, not 1.5"},
	} {
		limited := &ir.OperationSet{}
		if err := apply(tc.name, TargetOperationSet, Node{OperationSet: limited}, map[string]any{tc.key: tc.value}); err == nil || err.Error() != tc.want {
			t.Errorf("@%s %v error = %v, want %q", tc.name, tc.value, err, tc.want)
		}
	}
	if err := apply("timeout", TargetOperationSet, Node{OperationSet: set}, map[string]any{"seconds": float64(1)}); err != nil || *set.Middleware.Timeout != 1 {
		t.Errorf("timeout = %+v, err %v", set.Middleware, err)
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

// TestServiceCallersApply: @requireService and @allowService write the
// operation's or the set's clause, with each from handle as its service
// name; an operation or a set takes one of the pair, once; and a handle
// that is not an API service, or not a handle, is refused at the config.
func TestServiceCallersApply(t *testing.T) {
	reg := New(naming.Naming{})
	apply := func(name string, target DecoratorTarget, n Node, args ...any) error {
		spec, ok := reg.Decorator(name, target)
		if !ok {
			t.Fatalf("no core decorator @%s for %v", name, target)
		}
		if spec.Args != nil {
			t.Errorf("@%s: Args must stay nil, since the config is optional", name)
		}
		return spec.Apply(n, args, Site{})
	}
	handle := func(name, kind string) map[string]any { return map[string]any{"name": name, "kind": kind} }

	op := &ir.FieldDef{}
	if err := apply("requireService", TargetOperation, Node{Field: op}); err != nil {
		t.Fatal(err)
	}
	if want := (&ir.ServiceCallers{Mode: ir.ServiceCallersRequire}); !reflect.DeepEqual(op.ServiceCallers, want) {
		t.Errorf("requireService() = %+v, want %+v", op.ServiceCallers, want)
	}
	if err := apply("allowService", TargetOperation, Node{Field: op}); err == nil || err.Error() != "@allowService contradicts @requireService on the same operation: declare one of them" {
		t.Errorf("both on one operation: %v", err)
	}
	if err := apply("requireService", TargetOperation, Node{Field: op}); err == nil || err.Error() != "@requireService is declared twice on the same operation" {
		t.Errorf("twice on one operation: %v", err)
	}

	set := &ir.OperationSet{}
	from := []any{handle("orders-api", "API"), handle("billing-api", "API")}
	if err := apply("allowService", TargetOperationSet, Node{OperationSet: set}, map[string]any{"from": from}); err != nil {
		t.Fatal(err)
	}
	if want := (&ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: []string{"orders-api", "billing-api"}}); !reflect.DeepEqual(set.ServiceCallers, want) {
		t.Errorf("allowService({ from }) = %+v, want %+v", set.ServiceCallers, want)
	}
	if err := apply("requireService", TargetOperationSet, Node{OperationSet: set}); err == nil || err.Error() != "@requireService contradicts @allowService on the same operation set: declare one of them" {
		t.Errorf("both on one set: %v", err)
	}

	empty := &ir.FieldDef{}
	if err := apply("requireService", TargetOperation, Node{Field: empty}, map[string]any{"from": []any{}}); err != nil || empty.ServiceCallers.From != nil {
		t.Errorf("from: [] = %+v, err %v; want every edge", empty.ServiceCallers, err)
	}

	for _, tc := range []struct {
		arg  any
		want string
	}{
		{map[string]any{"from": []any{handle("orders-db", "DB")}}, `@requireService from lists "orders-db", a DB service: only an API service's server calls an operation`},
		{map[string]any{"from": []any{"orders-api"}}, "@requireService from entries must be service handles: import the API service's sentinel from its package"},
		{map[string]any{"from": "orders-api"}, "@requireService from must be an array of service handles"},
		{map[string]any{"services": []any{}}, `@requireService config has unknown key "services"`},
		{"orders-api", "@requireService config must be an object literal"},
	} {
		err := apply("requireService", TargetOperation, Node{Field: &ir.FieldDef{}}, tc.arg)
		var argErr *ArgError
		if !errors.As(err, &argErr) || argErr.Index != 0 || argErr.Msg != tc.want {
			t.Errorf("requireService(%v) = %v, want the config error %q", tc.arg, err, tc.want)
		}
	}
	if err := apply("requireService", TargetOperation, Node{Field: &ir.FieldDef{}}, map[string]any{}, map[string]any{}); err == nil || err.Error() != "@requireService takes at most one config object" {
		t.Errorf("two configs: %v", err)
	}
}

// TestIdentityRoutesApply: @userSessions and @userAdministration write the
// set's config, login on and register off by default; a class takes one
// of the two, once; register needs the login; and a key the config does
// not take, or a value of the wrong type, is refused at the config.
func TestIdentityRoutesApply(t *testing.T) {
	reg := New(naming.Naming{})
	apply := func(name string, set *ir.OperationSet, args ...any) error {
		spec, ok := reg.Decorator(name, TargetOperationSet)
		if !ok {
			t.Fatalf("no core decorator @%s for an operation set", name)
		}
		if spec.Args != nil || !spec.DeclaredIn("@superschematic/api") {
			t.Errorf("@%s: Args must stay nil and the package be @superschematic/api: %+v", name, spec)
		}
		return spec.Apply(Node{OperationSet: set}, args, Site{})
	}

	for _, tc := range []struct {
		arg  any
		want ir.UserSessionsConfig
	}{
		{nil, ir.UserSessionsConfig{}},
		{map[string]any{}, ir.UserSessionsConfig{}},
		{map[string]any{"path": "account", "register": true}, ir.UserSessionsConfig{Path: "account", Register: true}},
		{map[string]any{"login": true, "register": false}, ir.UserSessionsConfig{}},
		{map[string]any{"login": false}, ir.UserSessionsConfig{NoLogin: true}},
	} {
		set := &ir.OperationSet{}
		var args []any
		if tc.arg != nil {
			args = []any{tc.arg}
		}
		if err := apply("userSessions", set, args...); err != nil {
			t.Fatalf("userSessions(%v): %v", tc.arg, err)
		}
		if !reflect.DeepEqual(set.UserSessions, &tc.want) {
			t.Errorf("userSessions(%v) = %+v, want %+v", tc.arg, set.UserSessions, tc.want)
		}
	}
	admin := &ir.OperationSet{}
	if err := apply("userAdministration", admin, map[string]any{"path": "admin"}); err != nil {
		t.Fatal(err)
	}
	if want := (&ir.UserAdministrationConfig{Path: "admin"}); !reflect.DeepEqual(admin.UserAdministration, want) {
		t.Errorf("userAdministration({ path }) = %+v, want %+v", admin.UserAdministration, want)
	}

	if err := apply("userAdministration", admin); err == nil || err.Error() != "@userAdministration is declared twice on the same class" {
		t.Errorf("twice on one class: %v", err)
	}
	if err := apply("userSessions", admin); err == nil || err.Error() != "@userSessions contradicts @userAdministration on the same class: a class takes one of them" {
		t.Errorf("both on one class: %v", err)
	}
	sessions := &ir.OperationSet{}
	if err := apply("userSessions", sessions); err != nil {
		t.Fatal(err)
	}
	if err := apply("userAdministration", sessions); err == nil || err.Error() != "@userAdministration contradicts @userSessions on the same class: a class takes one of them" {
		t.Errorf("both on one class: %v", err)
	}

	for _, tc := range []struct {
		name string
		arg  any
		want string
	}{
		{"userSessions", map[string]any{"login": false, "register": true}, "@userSessions: register needs the login; drop register: true or login: false"},
		{"userSessions", map[string]any{"roles": true}, `@userSessions config has unknown key "roles"; it takes login, path and register`},
		{"userSessions", map[string]any{"login": "yes"}, "@userSessions login must be true or false"},
		{"userSessions", map[string]any{"path": 1.0}, "@userSessions path must be a string literal"},
		{"userSessions", "auth", "@userSessions config must be an object literal"},
		{"userAdministration", map[string]any{"register": true}, `@userAdministration config has unknown key "register"; it takes path`},
	} {
		set := &ir.OperationSet{}
		err := apply(tc.name, set, tc.arg)
		var argErr *ArgError
		if !errors.As(err, &argErr) || argErr.Index != 0 || argErr.Msg != tc.want {
			t.Errorf("%s(%v) = %v, want the config error %q", tc.name, tc.arg, err, tc.want)
		}
		if set.IsIdentityRoutes() {
			t.Errorf("%s(%v) failed and still marked the set: %+v", tc.name, tc.arg, set)
		}
	}
	if err := apply("userSessions", &ir.OperationSet{}, map[string]any{}, map[string]any{}); err == nil || err.Error() != "@userSessions takes at most one config object" {
		t.Errorf("two configs: %v", err)
	}
}

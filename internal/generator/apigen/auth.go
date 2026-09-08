package apigen

import (
	"embed"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// AuthProvider supplies the authentication and authorization half of a
// generated API module: which auth stores the upstream DB schema backs, the
// per-endpoint auth data, the template snippets the core templates splice in
// at their hook points, and any whole files of its own. Core registers the
// generic "session" provider; extensions register their own and
// superschematic.toml's auth_provider selects one (design:
// docs/extension-model.md section 8).
type AuthProvider interface {
	// Name is the registry key, the value auth_provider selects by:
	// "session" for the core provider.
	Name() string

	// Analyze inspects the API schema and the upstream auth DB schema and
	// reports which stores the upstream tables can back. upstream is nil
	// when the API is not public.
	Analyze(api, upstream *ir.Schema) (AuthModel, error)

	// Endpoint fills the provider-owned fields of an endpoint from its
	// operation: IsTenantEndpoint and TenantParamName (typed fields because
	// the SDK generators read them) and Auth, which is the provider's own.
	// RequiresAuth and RequiredPerms are derived by core before the call.
	Endpoint(op *ir.FieldDef, set *ir.OperationSet, ep *EndpointInfo) error

	// Templates holds the provider's templates under "templates/": the
	// snippet definitions the core templates call through authSnippet and
	// the whole files listed by Files. Every provider defines every snippet
	// in AuthSnippets, empty when it has nothing to add.
	Templates() embed.FS

	// Funcs adds template functions the provider's own templates call.
	Funcs() template.FuncMap

	// Files lists provider-owned whole files, rendered from Templates into
	// the module directory next to the core files.
	Files(output *APIOutput) []codegen.ConditionalFile

	// OpenAPIParameters lists header parameters the provider adds to every
	// operation of the OpenAPI document.
	OpenAPIParameters(output *APIOutput) []map[string]any
}

// AuthModel is what AuthProvider.Analyze returns. The two core flags gate
// the session and principal store adapters; Extra is the provider's own
// (Parable keeps its tenant, role and impersonation flags there).
type AuthModel struct {
	// HasSessionStore reports an upstream Session(id, jti, user, expiresAt)
	// table.
	HasSessionStore bool
	// HasPrincipalStore reports an upstream User(id, name) table.
	HasPrincipalStore bool
	// Extra is provider-owned data the provider's templates read.
	Extra any
}

// AuthSnippets names the hook points the core templates render through
// authSnippet. A provider's Templates must define every one of them (as
// {{ define "<name>" }} blocks in any of its templates); the snippet's data
// is the APIOutput except where noted. Each non-empty snippet starts with
// the newline that separates it from the line before, so an empty snippet
// leaves no blank line behind. Imports are sorted by gofmt afterwards, so an
// import snippet only has to land in the right group.
var AuthSnippets = []string{
	// context.tmpl
	"contextImports", // imports the auth context shims need, in the runtime import group
	"contextAuth",    // the auth context shims, after the logger and request helpers
	// middleware.tmpl
	"middlewareStdImports", // standard-library imports the store adapters need
	"middlewareImports",    // runtime and scalar imports, in the runtime import group
	"middlewareAliases",    // type aliases over the provider's runtime packages
	"middlewareAuthz",      // the RequireAuth / RequirePermissions var block
	"middlewareStores",     // ORM store adapters, cache constructors and their wiring
	// routes.tmpl
	"routesImports",             // imports the per-route permission middleware calls need
	"routesConfigStores",        // Config fields after DB (public APIs)
	"routesConfigMiddlewares",   // Config fields after AuthMiddleware (public APIs)
	"routesConfigValidate",      // Validate checks for those fields (public APIs)
	"routesConfigExample",       // doc-comment lines after AuthMiddleware in the RegisterRoutes example
	"routesSetupPre",            // RegisterRoutes setup before LoggerMiddleware (public APIs)
	"routesSetup",               // RegisterRoutes setup after DatabaseMiddleware (public APIs)
	"routesProtectedMiddleware", // middlewares after cfg.AuthMiddleware on the protected group
	"routePermissions",          // per-endpoint permission middleware; data is the EndpointInfo
	// module.tmpl
	"moduleRequires", // extra require lines
	"moduleReplaces", // extra replace lines
}

// HasTable reports whether upstream declares a DB table named name carrying
// every field in fields. Providers use it to gate their store adapters.
func HasTable(upstream *ir.Schema, name string, fields ...string) bool {
	if upstream == nil {
		return false
	}
	typeDef, ok := upstream.Types[name]
	if !ok || typeDef.Role != ir.RoleDBTable {
		return false
	}
	declared := make(map[string]bool, len(typeDef.Fields))
	for _, field := range typeDef.Fields {
		declared[field.Name] = true
	}
	for _, required := range fields {
		if !declared[required] {
			return false
		}
	}
	return true
}

// AnalyzeSessionStores is the core half of Analyze: the two tables the
// generic session model reads. Providers that extend the model call it and
// fill Extra.
func AnalyzeSessionStores(upstream *ir.Schema) AuthModel {
	return AuthModel{
		HasSessionStore:   HasTable(upstream, "Session", "id", "jti", "user", "expiresAt"),
		HasPrincipalStore: HasTable(upstream, "User", "id", "name"),
	}
}

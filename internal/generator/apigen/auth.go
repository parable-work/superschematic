package apigen

import (
	"embed"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// AuthProvider supplies the authentication and authorization half of a
// generated API module: what the upstream DB schema backs (the core's user
// model, and any store of the provider's own), the per-endpoint auth data,
// the template snippets the core templates splice in at their hook points,
// and any whole files of its own. Core registers the generic "session"
// provider; extensions register their own and superschematic.toml's
// auth_provider selects one (design: docs/extension-model.md section 8).
type AuthProvider interface {
	// Name is the registry key, the value auth_provider selects by:
	// "session" for the core provider.
	Name() string

	// Analyze inspects the API schema and the upstream auth DB schema and
	// reports what the upstream tables can back. upstream is a public
	// API's authDb, or a non-public API's when it declares the user model
	// (D50), and nil otherwise.
	Analyze(api, upstream *ir.Schema) (AuthModel, error)

	// Endpoint fills the provider-owned fields of an endpoint from its
	// operation: IsScopedEndpoint and ScopeParamName (typed fields because
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

// AuthModel is what AuthProvider.Analyze returns. User and Identity are
// the core's half, the user model (D50) the upstream schema declares and
// whether the server authenticates with it; Extra is the provider's own
// (an organization-scoped provider keeps its scope, role and
// impersonation flags there).
type AuthModel struct {
	// User is the upstream schema's user model: its table with the User
	// trait, found by the trait whatever the table is named. Nil when the
	// upstream schema has none, or the API has no upstream schema.
	User *UserModel
	// Identity reports that the generated server authenticates with the
	// identity runtime over the user model: its Config takes the identity
	// service, its AuthMiddleware defaults to the service's middleware,
	// and it serves the user model's routes. AnalyzeSessionStores sets it
	// with User. A provider that authenticates its callers its own way
	// over the same users clears it, and an API it renders then declares
	// no @userSessions or @userAdministration set.
	Identity bool
	// Extra is provider-owned data the provider's templates read.
	Extra any
}

// UserModel is the user model an upstream DB schema declares (D50): the
// table with the User trait and, beside it, the one with the UserRole
// trait. A provider's store adapter over the ORM reads the table and its
// fields from it, so it names them whatever the schema calls them.
type UserModel struct {
	// Type is the user table's type name, which the ORM names the
	// table's repository and filter after.
	Type string
	// Key is the key field's name and KeyType its type as the schema
	// names it ("Identity.UUID").
	Key     string
	KeyType string
	// Login is the login field's name.
	Login string
	// Name is the field a principal's display name comes from: the
	// trait's name, or the login when the trait names none.
	Name string
	// RoleType is the UserRole table's type name, empty without one.
	RoleType string
}

// KeyIsUUID reports whether the user table's key is an Identity.UUID,
// whose ORM filter takes the scalar UUID a store parses the principal's
// id into.
func (u *UserModel) KeyIsUUID() bool {
	return u != nil && u.KeyType == "Identity.UUID"
}

// AuthSnippets names the hook points the core templates render through
// authSnippet. The core templates wire the identity runtime themselves
// when the model's Identity is set (D50): Config.Identity, AuthMiddleware
// defaulting to its middleware, the user model's routes, the route table
// and the CORS middleware; the provider's snippets render beside that. A provider's Templates must define every one of them (as
// {{ define "<name>" }} blocks in any of its templates); the snippet's data
// is the APIOutput except where noted. Each non-empty snippet starts with
// the newline that separates it from the line before, so an empty snippet
// leaves no blank line behind. Imports are sorted by gofmt afterwards, so an
// import snippet only has to land in the right group. It must not repeat an
// import the core template writes: under --skip-format nothing removes the
// duplicate, and the file does not compile. routes.go imports the generated
// types module only when a core handler uses it (RoutesNeedTypes), so a
// routes snippet does not reference types. routes.go also imports the
// package of each raw-body check an endpoint runs (RawBodyCheckImports); a
// routesImports snippet that needs one of those packages leaves its own
// import out when ImportsRawBodyCheckPackage reports it, and one that needs
// the scalar Go module leaves it out when RoutesNeedScalars reports that
// routes.go imports it as scalars.
var AuthSnippets = []string{
	// context.tmpl
	"contextImports", // imports the auth context shims need, in the runtime import group
	"contextAuth",    // the auth context shims, after the logger and request helpers
	// middleware.tmpl
	"middlewareStdImports", // standard-library imports the store adapters need beyond context, net/http and time
	"middlewareImports",    // runtime and scalar imports, in the runtime import group
	"middlewareAliases",    // type aliases over the provider's runtime packages
	"middlewareAuthz",      // the RequireAuth / RequirePermissions var block
	"middlewareStores",     // ORM store adapters, cache constructors and their wiring
	// routes.tmpl
	"routesImports",             // imports the per-route permission middleware calls need
	"routesConfigStores",        // Config fields after DB (public APIs)
	"routesConfigMiddlewares",   // Config fields after AuthMiddleware (APIs with one: public, or over the user model, Auth.Identity)
	"routesConfigValidate",      // Validate checks for those fields (the same APIs), after the AuthMiddleware or Identity check
	"routesConfigExample",       // doc-comment lines after AuthMiddleware, or Identity, in the RegisterRoutes example
	"routesSetupPre",            // RegisterRoutes setup before LoggerMiddleware (public APIs)
	"routesSetup",               // RegisterRoutes setup after DatabaseMiddleware (public APIs)
	"routesProtectedMiddleware", // middlewares after cfg.AuthMiddleware on the protected group, and in the end-user step of a route with a service clause (D37)
	"routePermissions",          // per-endpoint permission middleware, after the rate and body limits and the service step and before the decryptor and timeout; data is the EndpointInfo
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

// AnalyzeSessionStores is the core half of Analyze: the user model the
// upstream schema declares by the User and UserRole traits (D50), which
// the identity runtime authenticates with. It finds no table by name: an
// upstream schema without a User table gives an empty model. Providers
// that extend the model call it and fill Extra.
func AnalyzeSessionStores(upstream *ir.Schema) AuthModel {
	user := userModel(upstream)
	return AuthModel{User: user, Identity: user != nil}
}

// userModel is the user model of upstream, nil without a User table.
func userModel(upstream *ir.Schema) *UserModel {
	table := upstream.UserTable()
	if table == nil || table.User == nil {
		return nil
	}
	model := &UserModel{
		Type:  table.Name,
		Login: table.User.Login,
		Name:  table.User.NameField(),
	}
	for _, field := range table.Fields {
		if field.Key {
			model.Key, model.KeyType = field.Name, field.TypeRef.Name
			break
		}
	}
	if role := upstream.UserRoleTable(); role != nil {
		model.RoleType = role.Name
	}
	return model
}

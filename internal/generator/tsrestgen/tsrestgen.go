// Package tsrestgen generates TypeScript REST API server packages from
// API-kind schemas. The generated package (Naming.NpmAPIPackage,
// <scope>/<svc>-api) carries one implementation interface per operation
// class, a Hono router built on the TypeScript HTTP runtime
// (Naming.HTTPRuntimeNpmPackage), the OpenAPI document, and Deps and the
// constructor's type in deps.ts (D51). It also writes the scaffold of the
// API's implementation (deps.go in this package).
//
// It is the TypeScript sibling of the Go and Rust API generators: endpoints
// come from the shared apigen extraction, so path, method, parameter
// placement, auth decorators and body limits agree with the Go and Rust
// routers by construction. The router is provider-neutral: the operation
// table declares each route's auth requirement, and the service supplies the
// authenticator that establishes the caller. An API whose authDb has a User
// table (D50) is the exception: its router takes the identity runtime's
// service, which authenticates every route and serves the user model's
// operations.
package tsrestgen

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/permcatalog"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

const (
	// HonoVersion pins the Hono the generated package is checked against; it
	// must match the runtime's peer range.
	HonoVersion = "4.13.8"
	// TypeScriptVersion pins the compiler the generated package is checked with.
	TypeScriptVersion = "5.9.3"
)

// ParamInfo is one path, query, or body argument as the generated
// TypeScript sees it.
type ParamInfo struct {
	Name   string // wire name
	TSName string // argument key in the implementation's args object
	TSType string // TypeScript type of the decoded value
	// Kind is the runtime ParamKind. A body argument of an object type (T,
	// T[] or T[][]) has Kind "object": the runtime parses each value with
	// the generated strict parser of T. A body argument of a JSON-valued
	// scalar (Generic.JSON) has Kind "json": any JSON value but null, and
	// null too for an optional single one, apart from absent. One of
	// a JSON object or array scalar (Generic.StringMap, Embedding.Vector) has
	// Kind "jsonObject" or "jsonArray". The runtime reads every body argument
	// from its JSON value.
	Kind     string
	Required bool
	IsArray  bool
	// IsArrayOfArrays marks a T[][] body argument (IsArray is also set). The
	// runtime decodes it from the JSON body with the list rules: an inner
	// list is never null and each element is checked at name[i][j].
	IsArrayOfArrays bool
	// IsMap marks a map body argument (Record<string, T>, or
	// Record<string, T[]> with IsArray). The runtime decodes it from a JSON
	// object whose values are checked as list elements at name[key], and a
	// list value's elements at name[key][i].
	IsMap bool
	// SpecLiteral is the ParamSpec object literal the router carries.
	SpecLiteral string
}

// EndpointInfo is one generated route.
type EndpointInfo struct {
	Name string // operation name (camelCase, the schema's)
	// Key is the endpoint's key in the router's flat tables
	// (operationSpecs, RouterOptions' bodyLimits, rateLimits, timeouts and
	// manualRoutes, and the MCP tools' operation): its name, or, when an
	// operation of another namespace has the same name, its namespace's
	// camelCase name and its own in PascalCase (orderStatusTransition), so
	// two namespaces' operations of one name keep a key each.
	Key               string
	Namespace         string // kebab-case namespace
	NamespaceProperty string // camelCase key in Implementations
	ArgsTypeName      string // <Op>Args
	Method            string
	Path              string // /api/... with {param} placeholders
	Description       string
	Title             string

	HasInput  bool
	InputType string
	// InputValidator is parse<Input>Json: the generated decoder that
	// validates the wire object (strict for @strictJSON contracts);
	// InputParser is parse<Input>FromJSON, the wire-to-runtime conversion
	// (dates). The router applies the validator, then the parser.
	InputValidator string
	InputParser    string
	InputRequired  bool
	// InputDeclaredKeys are the input type's keys when the router drops the
	// others before the validator sees them (D87): a @webhook's input whose
	// type is neither @strictJSON nor @denyUnknownFields, nor
	// @allowUnknownFields, whose own parser drops them. Nil for every other
	// input, whose undeclared key the validator refuses.
	InputDeclaredKeys []string
	// Uploads are the input's file parts (D81), in field order, nil for an
	// input without one: the operation table carries their limits, the
	// router reads the multipart body and hands the method the files in
	// args.files, and args.input omits their fields.
	Uploads []UploadInfo
	// Download is the operation's FileDownload result (D81), nil for every
	// other: the method returns the runtime's FileDownload, and the router
	// answers the file with its Content-Type and Content-Disposition.
	Download *ir.FileDownloadDef

	// OutputType is the TypeScript type expression of the result; a paged
	// operation's is Page<T>, a page of its list's elements.
	OutputType string
	// OutputIsArrayOfArrays marks a T[][] result; the runtime sends a
	// nullish inner list as [].
	OutputIsArrayOfArrays bool
	// Page is the operation's @paginated limits (D60), nil for an
	// operation that does not page. The operation table carries them, and
	// the router reads the request's cursor and limit with the runtime's
	// readPage after the query parameters and hands them to the method as
	// args.page, a PageRequest; the method answers a Page, which the
	// router sends as data. Its Sort is the operation's sort argument (D60,
	// amended), which readPage reads into args.page.sort, and which the
	// router binds the page's next to (bindPage); it is not among
	// QueryParams.
	Page *apigen.PageInfo

	PathParams  []ParamInfo
	QueryParams []ParamInfo
	BodyParams  []ParamInfo

	PublicRoute             bool
	RequiresAuth            bool
	RequiredPerms           []string
	PermsLiteral            string
	ManualRouteRegistration bool
	BodyLimitBytes          *int
	// RateLimitPerMinute and TimeoutSeconds are @rateLimit and @timeout
	// (set-level defaults with operation overrides, resolved by apigen);
	// the adapter applies them with the Go runtime's semantics.
	RateLimitPerMinute *int
	TimeoutSeconds     *int
	// WebhookProvider is the @hmacVerified provider, or empty. The router
	// mounts the route with Implementations.webhookVerifiers[provider],
	// which runs before every other step.
	WebhookProvider string
	// ServiceCallers is the effective @requireService or @allowService
	// clause, nil when the operation has none; ServiceLiteral is the
	// operation table's `service` object for it, empty without one. The
	// runtime's service step applies it before the end-user step (D37).
	ServiceCallers *ir.ServiceCallers
	ServiceLiteral string

	// DocLines is the JSDoc body of the implementation method: title,
	// description, route, and the auth requirement.
	DocLines []string

	// IdentityOperation names the user model's operation (D50) the
	// endpoint is, one of the ir.IdentityOp constants; empty for every
	// other. The router mounts it with the identity runtime's handler, so
	// it has no implementation method, and its table entry declares no
	// input: the runtime decodes the body itself.
	IdentityOperation string

	// Behavior is the behavior operation the endpoint runs (D103), nil for
	// every other: the router mounts it with a handler that runs it
	// through the table's repository in RouterOptions.orm, so it has no
	// implementation method.
	Behavior *BehaviorRoute
}

// BehaviorRoute is a behavior operation (D103) as the router mounts it:
// the operation of a use on a table of the API's database, which the
// loader added for a @behaviorOperations set, run through the table's
// repository in RouterOptions.orm (Deps.orm), as the request's caller.
type BehaviorRoute struct {
	// Set is the @behaviorOperations set that serves the operation.
	Set string
	// Database, Type, Use, Behavior and Operation are the loader's marker:
	// the API's database, the table, the use's id (its field), the
	// behavior and the operation's name in the behavior's declaration.
	Database  string
	Type      string
	Use       string
	Behavior  string
	Operation string
	// Repository is the ORM's member that holds the table's repository, as
	// tsgen names it: the table's name with its first letter lower-cased.
	Repository string
	// Key is the path parameter that holds the row's key, which the
	// repository's method takes first.
	Key ParamInfo
}

// Method is the repository's method the route calls, under RouterOptions.orm:
// order.status.transition, the operation under its use under the table's
// repository, as orm.ts declares them.
func (r *BehaviorRoute) Method() string {
	return r.Repository + "." + r.Use + "." + r.Operation
}

// UploadInfo is one file part of a multipart input (D81).
type UploadInfo struct {
	Name         string // the input type's field, the part's name
	TSName       string // its key in args.files
	Required     bool
	MaxBytes     int64
	ContentTypes []string
	Extensions   []string
}

// UploadsLiteral is the operation table's uploads, its OperationUpload list.
func (e EndpointInfo) UploadsLiteral() string {
	uploads := make([]string, 0, len(e.Uploads))
	for _, upload := range e.Uploads {
		uploads = append(uploads, fmt.Sprintf("{ name: %s, required: %t, maxBytes: %d, contentTypes: %s, extensions: %s }",
			tsString(upload.Name), upload.Required, upload.MaxBytes, tsStringList(upload.ContentTypes), tsStringList(upload.Extensions)))
	}
	return "[" + strings.Join(uploads, ", ") + "]"
}

// InputDeclaredKeysLiteral is InputDeclaredKeys as a list literal.
func (e EndpointInfo) InputDeclaredKeysLiteral() string {
	return tsStringList(e.InputDeclaredKeys)
}

// DownloadLiteral is the operation table's download, its OperationDownload.
func (e EndpointInfo) DownloadLiteral() string {
	if e.Download.Filename == "" {
		return fmt.Sprintf("{ contentType: %s }", tsString(e.Download.ContentType))
	}
	return fmt.Sprintf("{ contentType: %s, filename: %s }", tsString(e.Download.ContentType), tsString(e.Download.Filename))
}

// InputArgType is args.input's type: the input type, without its file
// fields when it has file parts (D81), which arrive in args.files.
func (e EndpointInfo) InputArgType() string {
	if len(e.Uploads) == 0 {
		return e.InputType
	}
	names := make([]string, 0, len(e.Uploads))
	for _, upload := range e.Uploads {
		names = append(names, tsString(upload.Name))
	}
	return "Omit<" + e.InputType + ", " + strings.Join(names, " | ") + ">"
}

// FilesArgType is args.files' type: each file part's File, optional when
// its field is (D81).
func (e EndpointInfo) FilesArgType() string {
	fields := make([]string, 0, len(e.Uploads))
	for _, upload := range e.Uploads {
		optional := ""
		if !upload.Required {
			optional = "?"
		}
		fields = append(fields, fmt.Sprintf("%s%s: File", upload.TSName, optional))
	}
	return "{ " + strings.Join(fields, "; ") + " }"
}

// Manual reports whether the router mounts the endpoint with a handler of
// its own rather than an implementation method: a
// @manualRouteRegistration operation, or one of the user model's. A
// behavior operation (D103) is not manual: its route decodes the request
// as an implemented one's does.
func (e EndpointInfo) Manual() bool {
	return e.ManualRouteRegistration || e.IdentityOperation != ""
}

// AuthSummary is the README's auth cell: the user clause (public, the
// permissions, authenticated, or none), and the service clause when there
// is one: `and service: ...` for @requireService with a user clause (the
// service forwards the user), `service: ...` alone without one, and `or
// service: ...` for @allowService.
func (e EndpointInfo) AuthSummary() string {
	user := "none"
	switch {
	case e.PublicRoute:
		user = "public"
	case len(e.RequiredPerms) > 0:
		user = strings.Join(e.RequiredPerms, ", ")
	case e.RequiresAuth:
		user = "authenticated"
	}
	if e.ServiceCallers == nil {
		return user
	}
	service := "service: " + serviceCallersText(e.ServiceCallers, ", ", "any caller")
	switch {
	case e.ServiceCallers.Mode == ir.ServiceCallersAllow:
		return user + " or " + service
	case e.RequiresAuth:
		return user + " and " + service
	}
	return service
}

// serviceCallersText names the APIs a clause's callers serve, joined by
// sep, or none when from is empty (every caller with an edge).
func serviceCallersText(clause *ir.ServiceCallers, sep, none string) string {
	if len(clause.From) == 0 {
		return none
	}
	return strings.Join(clause.From, sep)
}

// HasArgs reports whether the implementation method takes any argument; a
// paged operation's takes its page.
func (e EndpointInfo) HasArgs() bool {
	return e.HasInput || len(e.PathParams) > 0 || len(e.QueryParams) > 0 || len(e.BodyParams) > 0 || e.Page != nil
}

// NamespaceInfo groups the implementable (non-manual) endpoints of one
// operation class.
type NamespaceInfo struct {
	Name          string // kebab-case
	InterfaceName string // <Namespace>Implementation
	Property      string // camelCase key in Implementations
	Endpoints     []EndpointInfo
}

// IdentityInfo is the user model (D50) as the generated server wires it:
// the API's authDb has a User table, so buildRouter takes the identity
// runtime's service, which authenticates every route, and mounts the
// operations of the API's @userSessions and @userAdministration sets (the
// endpoints with IdentityOperation set) with the runtime's handlers. An
// API with no route set still authenticates with it: it verifies the
// sessions another API's login made.
type IdentityInfo struct {
	// AuthDB names the authDb schema. DescriptorModule is the module of
	// its generated TypeScript types that exports identityDescriptor, the
	// descriptor the store reads the tables from.
	AuthDB           string
	DescriptorModule string
	// HasRoles reports whether the authDb has a UserRole table.
	HasRoles bool
	// Routes counts the user model's operations the API's route sets hold.
	Routes int
	// Registration is the RegistrationOptions literal of how register
	// admits users (D81), when the API's @userSessions declares
	// approval, default roles or a first administrator; empty otherwise.
	Registration string
}

// registrationLiteral renders cfg as the identity runtime's
// RegistrationOptions, or "" for nil.
func registrationLiteral(cfg *ir.UserSessionsConfig) string {
	if cfg == nil {
		return ""
	}
	var members []string
	if cfg.Approval() {
		members = append(members, "approval: true")
	}
	if len(cfg.DefaultRoles) > 0 {
		members = append(members, "defaultRoles: "+tsStringList(cfg.DefaultRoles))
	}
	if first := cfg.FirstUser; first != nil {
		members = append(members, "firstUser: { role: "+tsString(first.Role)+", permissions: "+tsStringList(first.Permissions)+" }")
	}
	return "{ " + strings.Join(members, ", ") + " }"
}

// PackageImport lists the symbols the generated code imports from one package.
type PackageImport struct {
	Package string
	Symbols []string
}

// APIOutput is the template data for the generated package.
type APIOutput struct {
	SchemaName   string
	PackageName  string
	TypesPackage string
	// RuntimePackage is the TypeScript HTTP runtime the router is built on
	// (Naming.HTTPRuntimeNpmPackage); ScalarPackage is the scalar library
	// subpath scalar wire types come from, the one tsgen re-exports.
	RuntimePackage string
	ScalarPackage  string
	Author         string
	HonoVersion    string
	TSVersion      string
	Timestamp      string

	Namespaces      []NamespaceInfo
	Endpoints       []EndpointInfo // every endpoint, manual ones included
	ManualEndpoints []EndpointInfo
	// WebhookProviders are the @hmacVerified providers of every endpoint,
	// manual ones included, sorted: Implementations.webhookVerifiers has a
	// verifier for each, as the Go server's WebhookVerifiers map does.
	WebhookProviders []string
	// HasServiceCallers reports whether any endpoint has a service clause;
	// the docs then name RouterOptions.authenticateService.
	HasServiceCallers bool
	// HasPages reports whether any endpoint is @paginated (D60): the
	// interfaces import the runtime's Page and PageRequest, and the router
	// its readPage and PageSpec.
	HasPages bool
	// HasSortedPages reports whether a paged endpoint sorts (D60,
	// amended): the router then imports the runtime's bindPage.
	HasSortedPages bool
	// HasDownloads reports whether an implemented endpoint answers a
	// FileDownload (D81): the interfaces import the runtime's FileDownload.
	HasDownloads bool
	// Identity is the user model (D50), nil when the API's authDb has no
	// User table.
	Identity *IdentityInfo

	// FieldBehaviors reports whether the API's database, found in
	// Options.Dependencies or Options.AuthDB, has a table whose fields are
	// behaviors (D103): its repositories, orm.ts, then import the
	// behaviors' core's package (Naming.RulesNpmPackage), which a server
	// that opens Deps.orm resolves and whose wasm module its image builds.
	// Set whether or not the API serves the table's behavior operations.
	FieldBehaviors bool

	// TypeImports are the type-only imports of interfaces.ts from
	// `<package>/types` (inputs, outputs, enum parameters);
	// RouterTypeImports the subset router.ts references (inputs and enum
	// parameters, the casts around the decoded request, and a behavior
	// operation's result);
	// ValidatorImports are value imports from `<package>/validators`;
	// ScalarImports are type-only imports from ScalarPackage, and
	// RouterScalarImports the ones router.ts takes: ScalarImports and the
	// scalars of the behavior operations' keys (D103), which
	// interfaces.ts does not declare.
	TypeImports         []PackageImport
	RouterTypeImports   []PackageImport
	ValidatorImports    []PackageImport
	ScalarImports       []string
	RouterScalarImports []string
	// PeerPackages lists every generated types package the generated
	// code imports from, for package.json peerDependencies.
	PeerPackages []string

	// Deps is what deps.ts's Deps holds beside the config and the logger
	// (D51); the dispatch layer sets it with SetDeps before WriteAPI.
	Deps DepsInfo
	// Jobs are the API's jobs (D52) and Workers its workers (D53), sorted
	// by name: deps.ts declares the Jobs and Workers interfaces, and the
	// scaffold createJobs and createWorkers.
	Jobs    []JobInfo
	Workers []WorkerInfo
	// Derivers are the API's derivers (D74), sorted by name: deps.ts
	// declares the Derivers interface, and the scaffold createDerivers.
	// The dispatch layer sets them with SetDerivers.
	Derivers []DeriverInfo
	// HasEnvConfig reports whether the package's config.ts declares
	// EnvConfig and loadEnvConfig, which Deps.config holds. The dispatch
	// layer writes config.ts (envgen) and sets it.
	HasEnvConfig bool
	// ImplementationPackage is the npm name the scaffold gives the
	// implementation (Naming.NpmImplementationPackage).
	ImplementationPackage string
	// PGPeerRange, PGTypesVersion and NodeTypesVersion are the package
	// constants of the same names, for the templates.
	PGPeerRange      string
	PGTypesVersion   string
	NodeTypesVersion string

	OpenAPISpecRaw string
	// PermissionCatalogJSON is the API's permissions.json as apigen builds
	// it, written beside openapi.json and exported from the package; empty
	// when no operation names a permission.
	PermissionCatalogJSON string

	// MCP is the API's MCP endpoint with outputs.api.mcp on (D81), nil
	// otherwise: mcp.ts declares its tools and the router serves them at
	// POST /api/mcp.
	MCP *MCPInfo
}

// HasUploads reports whether an endpoint's input has file parts (D81).
func (o *APIOutput) HasUploads() bool {
	for _, endpoint := range o.Endpoints {
		if len(endpoint.Uploads) > 0 {
			return true
		}
	}
	return false
}

// DropsUndeclaredKeys reports whether the router drops an input's
// undeclared keys itself for any endpoint (InputDeclaredKeys, D87).
func (o *APIOutput) DropsUndeclaredKeys() bool {
	for _, endpoint := range o.Endpoints {
		if endpoint.InputDeclaredKeys != nil {
			return true
		}
	}
	return false
}

// HasDownloadsAny reports whether an endpoint, manual or not, answers a
// FileDownload (D81).
func (o *APIOutput) HasDownloadsAny() bool {
	for _, endpoint := range o.Endpoints {
		if endpoint.Download != nil {
			return true
		}
	}
	return false
}

// HasManualRoutes reports whether any operation is @manualRouteRegistration.
func (o *APIOutput) HasManualRoutes() bool {
	return len(o.ManualEndpoints) > 0
}

// BehaviorRoutes are the behavior operations the router mounts (D103), in
// the order of Endpoints.
func (o *APIOutput) BehaviorRoutes() []*BehaviorRoute {
	var out []*BehaviorRoute
	for _, endpoint := range o.Endpoints {
		if endpoint.Behavior != nil {
			out = append(out, endpoint.Behavior)
		}
	}
	return out
}

// HasBehaviors reports whether the API serves a table's behavior
// operations (D103): RouterOptions then takes orm, the repositories of the
// API's database, which run them.
func (o *APIOutput) HasBehaviors() bool {
	return len(o.BehaviorRoutes()) > 0
}

// RuntimeImport is router.ts's import from the runtime's main entry: the
// helpers of the behavior operations' handlers (D103), the page helpers
// (D60) and the dropper of undeclared keys (D87) the router calls, and the
// operation table's types.
func (o *APIOutput) RuntimeImport() string {
	var values []string
	if o.HasBehaviors() {
		values = append(values, "behaviorCaller", "behaviorOperation")
	}
	if o.HasPages && o.HasSortedPages {
		values = append(values, "bindPage")
	}
	if o.DropsUndeclaredKeys() {
		values = append(values, "declaredKeysOnly")
	}
	if o.HasPages {
		values = append(values, "readPage")
	}
	types := []string{"OperationSpec"}
	if o.HasPages {
		types = append(types, "PageSpec")
	}
	if len(values) == 0 {
		return "import type { " + strings.Join(types, ", ") + " } from " + tsString(o.RuntimePackage) + ";"
	}
	for _, name := range types {
		values = append(values, "type "+name)
	}
	return "import { " + strings.Join(values, ", ") + " } from " + tsString(o.RuntimePackage) + ";"
}

// HasManualMounts reports whether the router mounts any endpoint with
// mountManualOperation: a @manualRouteRegistration operation or one of the
// user model's.
func (o *APIOutput) HasManualMounts() bool {
	return o.HasManualRoutes() || (o.Identity != nil && o.Identity.Routes > 0)
}

// Options configures TypeScript REST API generation.
type Options struct {
	SchemaName string
	// Dependencies are the loaded dependency schemas, used to find the
	// package that owns an imported input or output type.
	Dependencies map[string]*ir.Schema
	// Naming supplies the npm scope, the runtime and scalar package names
	// and the package author. Empty fields fall back to naming.Default().
	Naming naming.Naming
	Clock  codegen.Clock
	// AuthDB is the schema the API's config names as its authDb, loaded,
	// or nil when it names none. When it has a User table the API has the
	// user model (D50), and the router authenticates with the identity
	// runtime.
	AuthDB *ir.Schema
	// MCP is outputs.api.mcp: the router serves the API's visible @mcp
	// tools at POST /api/mcp (D81).
	MCP bool
}

// Generate produces the TypeScript API package metadata from the endpoints
// apigen extracted (the same APIOutput the Go server and the SDKs read, so
// the OpenAPI document carries every registered hook's edits). Returns
// (nil, nil) when the schema declares no operations.
func Generate(schema *ir.Schema, apiOutput *apigen.APIOutput, opts Options) (*APIOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()
	if apiOutput == nil || len(apiOutput.Endpoints) == 0 {
		return nil, nil
	}

	b := newBuilder(schema, opts)
	output := &APIOutput{
		SchemaName:            opts.SchemaName,
		PackageName:           opts.Naming.NpmAPIPackage(opts.SchemaName),
		TypesPackage:          opts.Naming.NpmTypesPackage(opts.SchemaName),
		RuntimePackage:        opts.Naming.HTTPRuntimeNpmPackage,
		ScalarPackage:         opts.Naming.ScalarNpmPackage + "/scalars",
		Author:                opts.Naming.PackageAuthor,
		HonoVersion:           HonoVersion,
		TSVersion:             TypeScriptVersion,
		Timestamp:             opts.Clock.RFC3339(),
		OpenAPISpecRaw:        apiOutput.OpenAPISpecRaw,
		HasServiceCallers:     apiOutput.HasServiceCallers,
		PermissionCatalogJSON: apiOutput.PermissionCatalogJSON,

		ImplementationPackage: opts.Naming.NpmImplementationPackage(opts.SchemaName),
		PGPeerRange:           PGPeerRange,
		PGTypesVersion:        PGTypesVersion,
		NodeTypesVersion:      NodeTypesVersion,
		Jobs:                  jobsOf(schema),
		Workers:               workersOf(schema),
	}

	if opts.AuthDB != nil && opts.AuthDB.UserTable() != nil {
		output.Identity = &IdentityInfo{
			AuthDB:           opts.AuthDB.Name,
			DescriptorModule: opts.Naming.NpmTypesPackage(opts.AuthDB.Name) + "/identity",
			HasRoles:         opts.AuthDB.UserRoleTable() != nil,
			Registration:     registrationLiteral(apiOutput.Registration),
		}
	}

	output.FieldBehaviors = databaseHasFieldBehaviors(schema, opts)

	keyOf, err := operationKeys(apiOutput.Endpoints)
	if err != nil {
		return nil, err
	}
	byNamespace := map[string]*NamespaceInfo{}
	webhookProviders := map[string]struct{}{}
	for _, ep := range apiOutput.Endpoints {
		// The user model's operations are the identity runtime's (D50): the
		// implementation interfaces leave them out, and the router mounts
		// each with the runtime's handler.
		if ep.IdentityOperation != "" {
			if output.Identity == nil {
				return nil, fmt.Errorf("tsrestgen: operation %s.%s is the user model's %s, which the identity runtime serves from the User table of the API's authDb (%s); Options.AuthDB holds none", ep.Namespace, ep.Name, ep.IdentityOperation, authDBName(schema))
			}
			endpoint := b.identityEndpoint(ep)
			endpoint.Key = keyOf(ep)
			output.Endpoints = append(output.Endpoints, endpoint)
			output.Identity.Routes++
			continue
		}
		// A behavior operation runs through the database's repositories
		// (D103): the implementation interfaces leave it out too, and the
		// router mounts it with a handler that calls the repository.
		if op := ep.BehaviorOperation(); op != nil {
			endpoint, err := b.behaviorEndpoint(ep, op)
			if err != nil {
				return nil, err
			}
			endpoint.Key = keyOf(ep)
			output.Endpoints = append(output.Endpoints, endpoint)
			if endpoint.WebhookProvider != "" {
				webhookProviders[endpoint.WebhookProvider] = struct{}{}
			}
			continue
		}
		endpoint, err := b.endpoint(ep)
		if err != nil {
			return nil, err
		}
		endpoint.Key = keyOf(ep)
		output.Endpoints = append(output.Endpoints, endpoint)
		output.HasPages = output.HasPages || endpoint.Page != nil
		output.HasSortedPages = output.HasSortedPages || (endpoint.Page != nil && endpoint.Page.Sort != nil)
		output.HasDownloads = output.HasDownloads || (endpoint.Download != nil && !endpoint.ManualRouteRegistration)
		if endpoint.WebhookProvider != "" {
			webhookProviders[endpoint.WebhookProvider] = struct{}{}
		}
		if endpoint.ManualRouteRegistration {
			output.ManualEndpoints = append(output.ManualEndpoints, endpoint)
			continue
		}
		ns, ok := byNamespace[endpoint.Namespace]
		if !ok {
			ns = &NamespaceInfo{
				Name:          endpoint.Namespace,
				InterfaceName: codegen.ToPascalCase(endpoint.Namespace) + "Implementation",
				Property:      endpoint.NamespaceProperty,
			}
			byNamespace[endpoint.Namespace] = ns
		}
		ns.Endpoints = append(ns.Endpoints, endpoint)
	}
	for _, name := range sortedKeys(byNamespace) {
		output.Namespaces = append(output.Namespaces, *byNamespace[name])
	}
	output.WebhookProviders = sortedKeys(webhookProviders)

	output.TypeImports = b.packageImports(b.typeImports)
	output.RouterTypeImports = b.packageImports(b.routerTypeImports)
	output.ValidatorImports = b.packageImports(b.validatorImports)
	output.ScalarImports = sortedKeys(b.scalarImports)
	for symbol := range b.scalarImports {
		b.routerScalarImports[symbol] = struct{}{}
	}
	output.RouterScalarImports = sortedKeys(b.routerScalarImports)
	peers := map[string]struct{}{}
	for pkg := range b.typeImports {
		peers[pkg] = struct{}{}
	}
	for pkg := range b.validatorImports {
		peers[pkg] = struct{}{}
	}
	peers[output.TypesPackage] = struct{}{}
	output.PeerPackages = sortedKeys(peers)

	if opts.MCP {
		mcp, err := mcpInfo(output, apiOutput, opts.Clock)
		if err != nil {
			return nil, err
		}
		output.MCP = mcp
	}
	return output, nil
}

type builder struct {
	schema   *ir.Schema
	opts     Options
	depNames []string

	typeImports       map[string]map[string]struct{}
	routerTypeImports map[string]map[string]struct{}
	validatorImports  map[string]map[string]struct{}
	scalarImports     map[string]struct{}
	// routerScalarImports are the scalars router.ts imports beside
	// scalarImports: the behavior operations' keys (D103).
	routerScalarImports map[string]struct{}
}

func newBuilder(schema *ir.Schema, opts Options) *builder {
	depNames := make([]string, 0, len(opts.Dependencies))
	for name := range opts.Dependencies {
		depNames = append(depNames, name)
	}
	sort.Strings(depNames)
	return &builder{
		schema:              schema,
		opts:                opts,
		depNames:            depNames,
		typeImports:         map[string]map[string]struct{}{},
		routerTypeImports:   map[string]map[string]struct{}{},
		validatorImports:    map[string]map[string]struct{}{},
		scalarImports:       map[string]struct{}{},
		routerScalarImports: map[string]struct{}{},
	}
}

// operationKeys returns each endpoint's key in the router's flat tables
// (EndpointInfo.Key): its name, unless an endpoint of another namespace
// has the same name, when it is the namespace's camelCase name and the
// name in PascalCase (statusTransition in order and in shipment, the
// behavior operations of two tables (D103), are orderStatusTransition and
// shipmentStatusTransition). It refuses two endpoints that would take one
// key.
func operationKeys(endpoints []apigen.EndpointInfo) (func(apigen.EndpointInfo) string, error) {
	namespaces := map[string]map[string]struct{}{}
	for _, ep := range endpoints {
		name := tsutil.ToCamelCase(ep.Name)
		if namespaces[name] == nil {
			namespaces[name] = map[string]struct{}{}
		}
		namespaces[name][ep.Namespace] = struct{}{}
	}
	keyOf := func(ep apigen.EndpointInfo) string {
		name := tsutil.ToCamelCase(ep.Name)
		if len(namespaces[name]) < 2 {
			return name
		}
		return codegen.ToCamelCase(ep.Namespace) + codegen.ToPascalCase(name)
	}
	taken := map[string]apigen.EndpointInfo{}
	for _, ep := range endpoints {
		key := keyOf(ep)
		if prev, clash := taken[key]; clash {
			return nil, fmt.Errorf("tsrestgen: operations %s.%s and %s.%s both key the router's operation table as %s, since operations of several namespaces are named %s; rename one", prev.Namespace, prev.Name, ep.Namespace, ep.Name, key, tsutil.ToCamelCase(prev.Name))
		}
		taken[key] = ep
	}
	return keyOf, nil
}

// databaseHasFieldBehaviors reports whether the API's database (its authDb,
// or its one DB dependency), as opts holds it loaded, has a table whose
// fields are behaviors (D103).
func databaseHasFieldBehaviors(schema *ir.Schema, opts Options) bool {
	db, _, ok := schema.Database()
	if !ok {
		return false
	}
	dbSchema := opts.Dependencies[db]
	if opts.AuthDB != nil && opts.AuthDB.Name == db {
		dbSchema = opts.AuthDB
	}
	_, _, _, found := dbSchema.FindFieldBehavior()
	return found
}

// identifierPattern matches a TypeScript identifier a member access can
// name after a dot.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// behaviorEndpoint is a behavior operation (D103) as the router mounts it:
// its route, its key parameter, its input and its result, decoded as an
// implemented endpoint's, and the repository method it runs through. Its
// types are rendered by a builder of their own, so they add no import to
// interfaces.ts, which has no method for it: its input's validators, its
// result (the handler's cast) and its key's scalar go to router.ts alone.
func (b *builder) behaviorEndpoint(ep apigen.EndpointInfo, op *ir.BehaviorOperation) (EndpointInfo, error) {
	own := newBuilder(b.schema, b.opts)
	endpoint, err := own.endpoint(ep)
	if err != nil {
		return endpoint, err
	}
	route := &BehaviorRoute{
		Set:        behaviorSet(b.schema, ep.Operation),
		Database:   op.Database,
		Type:       op.Type,
		Use:        op.Use,
		Behavior:   op.Behavior,
		Operation:  op.Operation,
		Repository: lowerFirst(op.Type),
	}
	for _, member := range []string{route.Repository, route.Use, route.Operation} {
		if !identifierPattern.MatchString(member) {
			return endpoint, fmt.Errorf("tsrestgen: behavior operation %s.%s runs %s's %s %s, and the repository's member %q is no identifier the router can call it by", ep.Namespace, ep.Name, op.Type, op.Use, op.Operation, member)
		}
	}
	key := slices.IndexFunc(endpoint.PathParams, func(p ParamInfo) bool { return p.Name == op.Key })
	if key < 0 {
		return endpoint, fmt.Errorf("tsrestgen: behavior operation %s.%s is routed at %s %s, which has no path parameter %s, the key of %s", ep.Namespace, ep.Name, endpoint.Method, endpoint.Path, op.Key, op.Type)
	}
	route.Key = endpoint.PathParams[key]
	endpoint.Behavior = route

	// The handler hands the repository the decoded input as its
	// parameters, which take the input's wire values, with no cast to the
	// input's type: the router imports the input's validators alone.
	merge := func(into, from map[string]map[string]struct{}, skip string) {
		for pkg, symbols := range from {
			for symbol := range symbols {
				if symbol != skip {
					b.addImport(into, pkg, symbol)
				}
			}
		}
	}
	merge(b.routerTypeImports, own.routerTypeImports, ep.InputType)
	merge(b.validatorImports, own.validatorImports, "")
	for symbol := range own.scalarImports {
		b.routerScalarImports[symbol] = struct{}{}
	}
	if pkg, ok := own.ownerPackage(ep.OutputType); ok {
		b.addImport(b.routerTypeImports, pkg, ep.OutputType)
	}
	return endpoint, nil
}

// behaviorSet names the operation set that holds op: the
// @behaviorOperations set the loader added it to (D103).
func behaviorSet(schema *ir.Schema, op *ir.FieldDef) string {
	for _, set := range schema.OperationSets {
		if set != nil && slices.Contains(set.Operations, op) {
			return set.Name
		}
	}
	return ""
}

// lowerFirst lower-cases the first letter of s, as tsgen names a table's
// repository in its ORM (order for Order).
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// authDBName names the API's authDb for an error: its name, or that it
// names none.
func authDBName(schema *ir.Schema) string {
	if schema.AuthDB == "" {
		return "absent (the API's config names no authDb)"
	}
	return fmt.Sprintf("%q", schema.AuthDB)
}

// identityEndpoint is one of the user model's operations as the router
// mounts it: its route, its path parameters, its auth requirement and its
// route controls, with no input or body arguments, since the identity
// runtime decodes the body itself, and no implementation method. Its path
// parameters are rendered by a builder of their own, so they add no import
// to the files the project's operations share.
func (b *builder) identityEndpoint(ep apigen.EndpointInfo) EndpointInfo {
	endpoint := EndpointInfo{
		Name:              tsutil.ToCamelCase(ep.Name),
		Namespace:         ep.Namespace,
		NamespaceProperty: codegen.ToCamelCase(ep.Namespace),
		Method:            strings.ToUpper(ep.Method),
		Path:              ep.Path,
		Description:       ep.Description,
		Title:             ep.Title,
		PublicRoute:       ep.PublicRoute,
		RequiresAuth:      ep.RequiresAuth,
		RequiredPerms:     append([]string(nil), ep.RequiredPerms...),
		PermsLiteral:      tsStringList(ep.RequiredPerms),
		IdentityOperation: ep.IdentityOperation,
	}
	if ep.BodyLimit != nil {
		bytes := *ep.BodyLimit * 1024 * 1024
		endpoint.BodyLimitBytes = &bytes
	}
	if ep.RateLimit != nil {
		rateLimit := *ep.RateLimit
		endpoint.RateLimitPerMinute = &rateLimit
	}
	if ep.Timeout != nil {
		timeout := *ep.Timeout
		endpoint.TimeoutSeconds = &timeout
	}
	params := newBuilder(b.schema, b.opts)
	for _, p := range ep.PathParams {
		// A path parameter is a scalar or a string, which param never
		// refuses.
		info, _ := params.param(p, inPath)
		endpoint.PathParams = append(endpoint.PathParams, info)
	}
	return endpoint
}

func (b *builder) endpoint(ep apigen.EndpointInfo) (EndpointInfo, error) {
	endpoint := EndpointInfo{
		Name:                    tsutil.ToCamelCase(ep.Name),
		Namespace:               ep.Namespace,
		NamespaceProperty:       codegen.ToCamelCase(ep.Namespace),
		ArgsTypeName:            codegen.ToPascalCase(ep.Name) + "Args",
		Method:                  strings.ToUpper(ep.Method),
		Path:                    ep.Path,
		Description:             ep.Description,
		Title:                   ep.Title,
		HasInput:                ep.HasInput,
		InputType:               ep.InputType,
		InputRequired:           ep.InputRequired,
		PublicRoute:             ep.PublicRoute,
		RequiresAuth:            ep.RequiresAuth,
		RequiredPerms:           append([]string(nil), ep.RequiredPerms...),
		PermsLiteral:            tsStringList(ep.RequiredPerms),
		ManualRouteRegistration: ep.ManualRouteRegistration,
		WebhookProvider:         ep.WebhookHMACProvider,
	}
	if ep.Page != nil {
		page := *ep.Page
		endpoint.Page = &page
	}
	if ep.ServiceCallers != nil {
		clause := *ep.ServiceCallers
		clause.From = append([]string{}, clause.From...)
		endpoint.ServiceCallers = &clause
		endpoint.ServiceLiteral = fmt.Sprintf("{ mode: %s, from: %s }", tsString(string(clause.Mode)), tsStringList(clause.From))
	}
	if ep.BodyLimit != nil {
		bytes := *ep.BodyLimit * 1024 * 1024
		endpoint.BodyLimitBytes = &bytes
	}
	if ep.RateLimit != nil {
		rateLimit := *ep.RateLimit
		endpoint.RateLimitPerMinute = &rateLimit
	}
	if ep.Timeout != nil {
		timeout := *ep.Timeout
		endpoint.TimeoutSeconds = &timeout
	}
	// A file upload's input is multipart (D81): the runtime reads its
	// files, then its other fields.
	for _, field := range ep.FileUploadFields {
		endpoint.Uploads = append(endpoint.Uploads, UploadInfo{
			Name:         field.Name,
			TSName:       tsutil.ToCamelCase(field.Name),
			Required:     field.Required,
			MaxBytes:     field.MaxSize,
			ContentTypes: append([]string{}, field.AllowedTypes...),
			Extensions:   append([]string{}, field.AllowedExtensions...),
		})
	}
	endpoint.Download = ep.FileDownload
	// The Go router decrypts an encrypted operation's body with the
	// configured PayloadDecryptor before it parses it. The TypeScript router
	// has no such step and would hand the ciphertext to the body parser, so
	// the service mounts the route itself and decrypts in its handler.
	if ep.Encrypted && !ep.ManualRouteRegistration {
		return endpoint, fmt.Errorf("tsrestgen: operation %s.%s is encrypted (an Encrypted operation set, @encrypted, or an EncryptedField<T> result or argument); the TypeScript router has no decryption step, declare it @manualRouteRegistration and decrypt the payload in the service's handler", ep.Namespace, ep.Name)
	}

	if ep.HasInput {
		pkg, ok := b.ownerPackage(ep.InputType)
		if !ok {
			return endpoint, fmt.Errorf("tsrestgen: operation %s.%s input type %s is not declared by the schema or its dependencies", ep.Namespace, ep.Name, ep.InputType)
		}
		endpoint.InputValidator = "parse" + ep.InputType + "Json"
		endpoint.InputParser = "parse" + ep.InputType + "FromJSON"
		if ep.InputAllowsUnknownFields {
			if input := b.findType(ep.InputType); input != nil && !input.AllowUnknownFields {
				for _, field := range input.Fields {
					endpoint.InputDeclaredKeys = append(endpoint.InputDeclaredKeys, field.Name)
				}
			}
		}
		b.addImport(b.typeImports, pkg, ep.InputType)
		b.addImport(b.routerTypeImports, pkg, ep.InputType)
		b.addImport(b.validatorImports, pkg, endpoint.InputValidator)
		b.addImport(b.validatorImports, pkg, endpoint.InputParser)
	}

	outputType, err := b.outputType(ep)
	if err != nil {
		return endpoint, fmt.Errorf("tsrestgen: operation %s.%s: %w", ep.Namespace, ep.Name, err)
	}
	endpoint.OutputType = outputType
	endpoint.OutputIsArrayOfArrays = ep.OutputIsArrayOfArrays
	endpoint.DocLines = docLines(endpoint)

	// b.param fails only for a body argument whose type it cannot decode;
	// the error names the argument.
	param := func(p apigen.Param, place paramPlace) (ParamInfo, error) {
		info, err := b.param(p, place)
		switch {
		case err == nil:
			return info, nil
		case p.IsArrayOfArrays:
			return info, fmt.Errorf("tsrestgen does not support arrays of arrays yet (%s.%s(%s)): %w", ep.Namespace, ep.Name, p.Name, err)
		default:
			return info, fmt.Errorf("tsrestgen cannot decode body argument %s.%s(%s): %w", ep.Namespace, ep.Name, p.Name, err)
		}
	}
	for _, p := range ep.PathParams {
		info, err := param(p, inPath)
		if err != nil {
			return endpoint, err
		}
		endpoint.PathParams = append(endpoint.PathParams, info)
	}
	// The sort argument reaches the method in its page (D60, amended).
	for _, p := range ep.ArgQueryParams() {
		info, err := param(p, inQuery)
		if err != nil {
			return endpoint, err
		}
		endpoint.QueryParams = append(endpoint.QueryParams, info)
	}
	// Undecorated scalar arguments travel in the query string on GET (the Go
	// router's rule) and as fields of the JSON body object otherwise.
	scalarArgPlace := inBody
	if endpoint.Method == "GET" {
		scalarArgPlace = inQuery
	}
	for _, p := range ep.ScalarArgs {
		info, err := param(p, scalarArgPlace)
		if err != nil {
			return endpoint, err
		}
		if scalarArgPlace == inQuery {
			endpoint.QueryParams = append(endpoint.QueryParams, info)
		} else {
			endpoint.BodyParams = append(endpoint.BodyParams, info)
		}
	}
	return endpoint, nil
}

// paramPlace is where a parameter travels: the path, the query string, or
// a field of the JSON body object.
type paramPlace int

const (
	inPath paramPlace = iota
	inQuery
	inBody
)

// param resolves the runtime kind, the TypeScript type, and the ParamSpec
// literal of one parameter from apigen's parse flags. A body argument
// arrives as a JSON value, so its type may also be an object type, alone,
// as the element of T[] or T[][] (apigen admits an array of arrays only in
// the body) or as the value of a map; the runtime parses each value with
// the generated strict parser of that type. A map body argument is
// Record<string, T> (Record<string, T[]>); apigen admits a map only in the
// body. A body argument of a JSON-valued scalar
// (Generic.JSON) is taken as the JSON value it is. A body argument whose
// type is a union is an object too: parse<Union>Json checks the member the
// union picks by the member's rules, and parse<Union>FromJSON reads it, so
// a value no member reads, or whose member breaks a rule, is refused as an
// object argument that breaks one is, as the Go and Rust routers refuse
// it. param fails for a body argument of any other type that is not
// declared. A scalar-typed parameter carries the scalar's own lengths,
// pattern and range, which the runtime checks on every value.
func (b *builder) param(p apigen.Param, place paramPlace) (ParamInfo, error) {
	info := ParamInfo{
		Name:            p.Name,
		TSName:          tsutil.ToCamelCase(p.Name),
		Required:        p.Required || place == inPath,
		IsArray:         p.IsArray,
		IsArrayOfArrays: p.IsArrayOfArrays,
		IsMap:           p.IsMap,
	}
	if p.IsMap && place != inBody {
		// apigen refuses a map outside the body; the query string and the
		// path have no encoding for one.
		return info, fmt.Errorf("a map travels only in the request body")
	}
	var enumValues []string
	var valueParser string
	switch {
	case p.IsInt:
		info.Kind, info.TSType = "integer", "number"
	case p.IsFloat:
		info.Kind, info.TSType = "number", "number"
	case p.IsBool:
		info.Kind, info.TSType = "boolean", "boolean"
	case p.IsUUID:
		info.Kind, info.TSType = "uuid", b.scalarType(p.Type)
	case p.IsDateTime:
		info.Kind, info.TSType = "datetime", "Date"
	case p.IsString:
		info.Kind, info.TSType = "string", "string"
	default:
		if enumDef, pkg, ok := b.findEnum(p.Type); ok {
			info.Kind = "enum"
			info.TSType = enumDef.Name
			b.addImport(b.typeImports, pkg, enumDef.Name)
			b.addImport(b.routerTypeImports, pkg, enumDef.Name)
			for _, value := range enumDef.Values {
				serialized := value.SerializedAs
				if serialized == "" {
					serialized = value.Name
				}
				enumValues = append(enumValues, serialized)
			}
		} else if scalar, isScalar := b.findScalar(p.Type); isScalar || place != inBody {
			info.Kind, info.TSType = "string", b.scalarType(p.Type)
			if place == inBody && isJSONValued(scalar) {
				info.Kind = "json"
			}
			if place == inBody && scalar != nil {
				switch scalar.StructuredJSONType() {
				case ir.JSONSchemaObjectType:
					info.Kind = "jsonObject"
				case ir.JSONSchemaArrayType:
					info.Kind = "jsonArray"
				}
			}
		} else {
			pkg, ok := b.ownerPackage(p.Type)
			if !ok {
				what := "type"
				if p.IsArray {
					what = "element type"
				}
				return info, fmt.Errorf("%s %s is not a scalar, enum, object type or union", what, p.Type)
			}
			info.Kind, info.TSType = "object", p.Type
			b.addImport(b.typeImports, pkg, p.Type)
			b.addImport(b.routerTypeImports, pkg, p.Type)
			validator, parser := "parse"+p.Type+"Json", "parse"+p.Type+"FromJSON"
			b.addImport(b.validatorImports, pkg, validator)
			b.addImport(b.validatorImports, pkg, parser)
			valueParser = fmt.Sprintf("(value: unknown) => %s(%s(value))", parser, validator)
		}
	}
	info.TSType = tsListType(info.TSType, p.ArrayDepth())
	if p.IsMap {
		info.TSType = "Record<string, " + info.TSType + ">"
	}

	fields := []string{
		"name: " + tsString(p.Name),
		"kind: " + tsString(info.Kind),
		fmt.Sprintf("required: %t", info.Required),
	}
	if p.IsArray {
		fields = append(fields, "isArray: true")
	}
	if p.IsArrayOfArrays {
		fields = append(fields, "isArrayOfArrays: true")
	}
	if p.IsMap {
		fields = append(fields, "isMap: true")
	}
	if len(enumValues) > 0 {
		fields = append(fields, "enumValues: "+tsStringList(enumValues))
	}
	if p.DefaultValue != nil {
		fields = append(fields, "defaultValue: "+tsString(*p.DefaultValue))
	}
	if p.ValidateMin != nil {
		fields = append(fields, fmt.Sprintf("min: %v", *p.ValidateMin))
	}
	if p.ValidateMax != nil {
		fields = append(fields, fmt.Sprintf("max: %v", *p.ValidateMax))
	}
	if p.ValidateMinLength != nil {
		fields = append(fields, fmt.Sprintf("minLength: %d", *p.ValidateMinLength))
	}
	if p.ValidateMaxLength != nil {
		fields = append(fields, fmt.Sprintf("maxLength: %d", *p.ValidateMaxLength))
	}
	// List bounds bound a list, not a map or its lists, as in the generated
	// types and the Go router.
	if p.ValidateListMin != nil && !p.IsMap {
		fields = append(fields, fmt.Sprintf("listMin: %d", *p.ValidateListMin))
	}
	if p.ValidateListMax != nil && !p.IsMap {
		fields = append(fields, fmt.Sprintf("listMax: %d", *p.ValidateListMax))
	}
	if p.ValidatePattern != "" {
		fields = append(fields, "pattern: "+tsString(p.ValidatePattern))
	}
	if valueParser != "" {
		fields = append(fields, "parse: "+valueParser)
	}
	if scalar, ok := b.findScalar(p.Type); ok {
		if constraints := scalarConstraints(scalar, info.Kind); constraints != "" {
			fields = append(fields, "scalar: "+constraints)
		}
	}
	info.SpecLiteral = "{ " + strings.Join(fields, ", ") + " }"
	return info, nil
}

// isJSONValued reports whether a scalar holds any JSON value
// (Generic.JSON): its TypeScript type is superscalar's JSONValue.
func isJSONValued(scalar *ir.ScalarDef) bool {
	return scalar != nil && scalar.TypeMappings["typescript"] == "JSONValue"
}

// scalarConstraints renders the ScalarConstraints literal of a scalar's
// own lengths and pattern (kind "string") or range (kinds "integer" and
// "number"), or "" when the scalar has none for the kind. The runtime
// checks them on every value, before the argument's own constraints, and
// names a failure by its rule (D14). UUID and timestamp kinds are parsed
// by the scalar library instead.
func scalarConstraints(scalar *ir.ScalarDef, kind string) string {
	var fields []string
	switch kind {
	case "string":
		if scalar.MinLength > 0 {
			fields = append(fields, fmt.Sprintf("minLength: %d", scalar.MinLength))
		}
		if scalar.MaxLength > 0 {
			fields = append(fields, fmt.Sprintf("maxLength: %d", scalar.MaxLength))
		}
		if scalar.Pattern != "" {
			fields = append(fields, "pattern: "+tsString(scalar.Pattern))
		}
	case "integer", "number":
		if scalar.Minimum != nil {
			fields = append(fields, fmt.Sprintf("min: %d", *scalar.Minimum))
		}
		if scalar.Maximum != nil {
			fields = append(fields, fmt.Sprintf("max: %d", *scalar.Maximum))
		}
	}
	if len(fields) == 0 {
		return ""
	}
	return "{ " + strings.Join(append([]string{"name: " + tsString(scalar.Name)}, fields...), ", ") + " }"
}

// tsListType wraps a TypeScript element type in depth list levels: "T",
// "T[]" or "T[][]".
func tsListType(elem string, depth int) string {
	return codegen.WrapArray(elem, depth, func(inner string) string { return inner + "[]" })
}

// scalarType returns the scalar library wire type symbol of a named scalar,
// recording the import; unknown names fall back to string.
func (b *builder) scalarType(typeName string) string {
	if _, ok := b.findScalar(typeName); !ok {
		return "string"
	}
	symbol := strings.TrimSpace(codegen.BuildScalarTokens(typeName).Symbol)
	if symbol == "" {
		return "string"
	}
	b.scalarImports[symbol] = struct{}{}
	return symbol
}

func (b *builder) outputType(ep apigen.EndpointInfo) (string, error) {
	name := ep.OutputType
	var tsType string
	switch {
	case ep.FileDownload != nil:
		// The runtime's FileDownload (D81): the file and its name.
		return "FileDownload", nil
	case name == "":
		return "void", nil
	case codegen.IsLanguagePrimitive(name):
		tsType = name
	default:
		if scalar, ok := b.findScalar(name); ok {
			traits := codegen.BuildScalarTraits(scalar, codegen.BuildScalarTokens(name), scalar.TypeMappings["typescript"])
			if traits.IsDateTimeLike {
				tsType = "Date"
			} else {
				tsType = b.scalarType(name)
			}
		} else if pkg, ok := b.ownerPackage(name); ok {
			b.addImport(b.typeImports, pkg, name)
			tsType = name
		} else {
			return "", fmt.Errorf("output type %s is not declared by the schema or its dependencies", name)
		}
	}
	if ep.Page != nil {
		// A paged operation answers a page of its list (D60), which the
		// loader keeps to one level.
		return "Page<" + tsListType(tsType, ep.OutputArrayDepth()-1) + ">", nil
	}
	return tsListType(tsType, ep.OutputArrayDepth()), nil
}

// ownerPackage returns the generated types package that declares a type,
// an enum or a union: the schema's own package, or a dependency's.
func (b *builder) ownerPackage(name string) (string, bool) {
	if declares(b.schema, name) {
		return b.opts.Naming.NpmTypesPackage(b.opts.SchemaName), true
	}
	for _, depName := range b.depNames {
		if dep := b.opts.Dependencies[depName]; dep != nil && declares(dep, name) {
			return b.opts.Naming.NpmTypesPackage(depName), true
		}
	}
	return "", false
}

// findType returns the type named name, the schema's own or a
// dependency's, or nil.
func (b *builder) findType(name string) *ir.TypeDef {
	if typeDef := b.schema.Types[name]; typeDef != nil {
		return typeDef
	}
	for _, depName := range b.depNames {
		if dep := b.opts.Dependencies[depName]; dep != nil && dep.Types[name] != nil {
			return dep.Types[name]
		}
	}
	return nil
}

// declares reports whether schema declares name as a type, an enum or a
// union.
func declares(schema *ir.Schema, name string) bool {
	return schema.Types[name] != nil || schema.Enums[name] != nil || schema.Unions[name] != nil
}

func (b *builder) findEnum(name string) (*ir.EnumDef, string, bool) {
	if enumDef, ok := b.schema.Enums[name]; ok {
		return enumDef, b.opts.Naming.NpmTypesPackage(b.opts.SchemaName), true
	}
	for _, depName := range b.depNames {
		dep := b.opts.Dependencies[depName]
		if dep == nil {
			continue
		}
		if enumDef, ok := dep.Enums[name]; ok {
			return enumDef, b.opts.Naming.NpmTypesPackage(depName), true
		}
	}
	return nil, "", false
}

func (b *builder) findScalar(name string) (*ir.ScalarDef, bool) {
	if scalar, ok := b.schema.Scalars[name]; ok {
		return scalar, true
	}
	for _, depName := range b.depNames {
		dep := b.opts.Dependencies[depName]
		if dep == nil {
			continue
		}
		if scalar, ok := dep.Scalars[name]; ok {
			return scalar, true
		}
	}
	return nil, false
}

func (b *builder) addImport(set map[string]map[string]struct{}, pkg, symbol string) {
	if set[pkg] == nil {
		set[pkg] = map[string]struct{}{}
	}
	set[pkg][symbol] = struct{}{}
}

func (b *builder) packageImports(set map[string]map[string]struct{}) []PackageImport {
	imports := make([]PackageImport, 0, len(set))
	for _, pkg := range sortedKeys(set) {
		imports = append(imports, PackageImport{Package: pkg, Symbols: sortedKeys(set[pkg])})
	}
	return imports
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// docLines assembles the JSDoc body of an implementation method.
func docLines(ep EndpointInfo) []string {
	var lines []string
	if ep.Title != "" {
		lines = append(lines, ep.Title)
	}
	// A description written with CRLF line ends splits on its LFs alone,
	// so its CRs go first, as codegen's doc helpers drop them.
	if desc := strings.TrimSpace(strings.ReplaceAll(ep.Description, "\r\n", "\n")); desc != "" {
		lines = append(lines, strings.Split(desc, "\n")...)
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, ep.Method+" "+ep.Path)
	if ep.Page != nil {
		lines = append(lines, "Paged (@paginated): answers the Page args.page asks for, whose next is null after the last page.")
	}
	if len(ep.Uploads) > 0 {
		names := make([]string, 0, len(ep.Uploads))
		for _, upload := range ep.Uploads {
			names = append(names, upload.TSName)
		}
		lines = append(lines, "Multipart: the files ("+strings.Join(names, ", ")+") arrive in args.files, checked against their fields' limits.")
	}
	if ep.Download != nil {
		line := "Answers a file (" + ep.Download.ContentType + "): a FileDownload, its body and its filename"
		if ep.Download.Filename != "" {
			line += ", " + ep.Download.Filename + " when it names none"
		}
		lines = append(lines, line+".")
	}
	if ep.WebhookProvider != "" {
		lines = append(lines, "Webhook: the "+ep.WebhookProvider+" webhook verifier checks the request before every other step.")
	}
	if ep.RateLimitPerMinute != nil {
		lines = append(lines, fmt.Sprintf("Rate limited to %d requests per minute per client.", *ep.RateLimitPerMinute))
	}
	if ep.TimeoutSeconds != nil {
		lines = append(lines, fmt.Sprintf("Times out after %d seconds (504).", *ep.TimeoutSeconds))
	}
	switch {
	case ep.PublicRoute:
		lines = append(lines, "Public route: no authentication.")
	case len(ep.RequiredPerms) > 0:
		lines = append(lines, "Requires a principal holding one of: "+strings.Join(ep.RequiredPerms, ", ")+".")
	case ep.RequiresAuth:
		lines = append(lines, "Requires an authenticated principal.")
	}
	if clause := ep.ServiceCallers; clause != nil {
		caller := "a calling service (ctx.serviceCaller)"
		if len(clause.From) > 0 {
			caller += " that serves " + serviceCallersText(clause, " or ", "")
		}
		switch {
		case clause.Mode == ir.ServiceCallersAllow:
			lines = append(lines, "Or "+caller+", standing in for the principal (ctx.principal is null).")
		case ep.RequiresAuth:
			lines = append(lines, "Requires "+caller+", forwarding that principal.")
		default:
			lines = append(lines, "Requires "+caller+"; no end user.")
		}
	}
	return lines
}

// tsString renders a single-quoted TypeScript string literal.
func tsString(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`)
	return "'" + replacer.Replace(s) + "'"
}

// PageSpecLiteral is the operation table's page, its PageSpec: { defaultLimit:
// 2, maxLimit: 5 }, and with a sort, sort: { param: 'sort', fields: ['name',
// 'sku'], default: 'name' }.
func (e EndpointInfo) PageSpecLiteral() string {
	literal := fmt.Sprintf("{ defaultLimit: %d, maxLimit: %d", e.Page.DefaultLimit, e.Page.MaxLimit)
	if sort := e.Page.Sort; sort != nil {
		literal += fmt.Sprintf(", sort: { param: %s, fields: %s, default: %s }", tsString(sort.Param), tsStringList(sort.Fields), tsString(sort.Default))
	}
	return literal + " }"
}

func tsStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, tsString(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// WriteAPI writes the generated package into outputDir. It refuses an API
// that serves a table's behavior operations (D103) whose Deps (SetDeps)
// holds no repositories, which router.ts types RouterOptions.orm by.
func WriteAPI(output *APIOutput, outputDir string) error {
	if err := output.checkBehaviorDeps(output.Deps); err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir %s: %w", outputDir, err)
	}

	files := []struct {
		templateName string
		outputName   string
	}{
		{templateName: "package.tmpl", outputName: "package.json"},
		{templateName: "tsconfig.tmpl", outputName: "tsconfig.json"},
		{templateName: "index.tmpl", outputName: "index.ts"},
		{templateName: "interfaces.tmpl", outputName: "interfaces.ts"},
		{templateName: "router.tmpl", outputName: "router.ts"},
		{templateName: "deps.tmpl", outputName: "deps.ts"},
		{templateName: "readme.tmpl", outputName: "README.md"},
	}
	for _, file := range files {
		if err := generateFile(file.templateName, filepath.Join(outputDir, file.outputName), output); err != nil {
			return fmt.Errorf("generate %s: %w", file.outputName, err)
		}
	}
	// config.ts is envgen's, written after this when the API has a
	// configuration; one an earlier build wrote would import what no
	// longer exists.
	if !output.HasEnvConfig {
		if err := os.Remove(filepath.Join(outputDir, ConfigFile)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale %s: %w", ConfigFile, err)
		}
	}

	if output.OpenAPISpecRaw != "" {
		if !json.Valid([]byte(output.OpenAPISpecRaw)) {
			return fmt.Errorf("openapi document for %s is not valid JSON", output.SchemaName)
		}
		if err := os.WriteFile(filepath.Join(outputDir, "openapi.json"), []byte(output.OpenAPISpecRaw), 0o644); err != nil {
			return fmt.Errorf("write openapi.json: %w", err)
		}
	}
	if output.PermissionCatalogJSON != "" {
		if err := os.WriteFile(filepath.Join(outputDir, permcatalog.FileName), []byte(output.PermissionCatalogJSON), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", permcatalog.FileName, err)
		}
	}
	return writeServedDocuments(output, outputDir)
}

func generateFile(templateName, outputPath string, data any) error {
	return codegen.GenerateFile(
		codegen.NewFileConfig(templatesFS, templateName, outputPath, data, templateFuncs()),
	)
}

// ConfigFile is the API package's EnvConfig and loadEnvConfig, which
// envgen writes (WriteTypeScriptEnvConfig) when the API has a
// configuration.
const ConfigFile = "config.ts"

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"tsString":     tsString,
		"toPascalCase": codegen.ToPascalCase,
		"splitLines":   func(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") },
		"join":         strings.Join,
		"sub":          func(a, b int) int { return a - b },
		// jsdoc keeps text from ending the JSDoc comment it goes in: a
		// schedule such as */15 would.
		"jsdoc": codegen.JSDocEscape,
	}
}

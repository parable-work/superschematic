// Package apigen generates Go REST API server modules from API-kind schemas.
//
// This is the v2 port of the v1 apigen. Endpoints come from
// [ir.Schema.OperationSets]: each operation is a [ir.FieldDef] carrying an
// explicit HTTP method, REST path, auth/permission decorators, and
// middleware overrides. The v1 GraphQL-isms are gone: there is no
// Queries/Mutations split, no nested-namespace recursion, no CRUD-verb
// detection, and no legacy/resource dual paths -- the schema states the
// route and the generator emits it.
package apigen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// Param represents a single endpoint parameter: a path parameter, a query
// parameter, or a scalar argument. The Go type and parse strategy are
// precomputed here so templates branch on simple flags instead of matching
// type-name tokens.
type Param struct {
	Name   string // schema argument name (also the wire name)
	GoName string // PascalCase Go name

	Type   string // canonical schema type name (e.g. "Identity.UUID", "boolean")
	GoType string // Go type expression as used in generated code (e.g. "types.IdentityUUID", "int64")

	// Exactly one parse strategy applies. IsString covers the bare string
	// primitive; the final fallback (none of the flags set) is a
	// string-backed scalar or enum cast via GoType.
	IsInt      bool
	IsFloat    bool
	IsBool     bool
	IsUUID     bool
	IsDateTime bool
	IsString   bool

	// ParseFunc is the typed parse function for UUID-like params
	// (e.g. "types.ParseIdentityUUID"). Empty unless IsUUID.
	ParseFunc string

	Required     bool
	IsArray      bool
	IsMap        bool
	DefaultValue *string

	ValidateMin       *float64
	ValidateMax       *float64
	ValidateMinLength *int
	ValidateMaxLength *int
	ValidateListMin   *int
	ValidateListMax   *int
	ValidatePattern   string
}

// SDK and tool-binding generators use these aliases for the unified Param type.
type (
	QueryParam    = Param
	PathParam     = Param
	ScalarArg     = Param
	FieldArgument = Param
)

// FileUploadField represents a file upload field in an endpoint's input type.
type FileUploadField struct {
	Name             string
	GoName           string
	ScalarType       string
	MaxSize          int64
	AllowedTypes     []string
	Category         string
	Required         bool
	ImageConstraints *ir.ImageConstraints
}

// EndpointInfo represents one REST API endpoint extracted from an operation.
type EndpointInfo struct {
	Name          string // operation name as declared in the schema
	Title         string // short reader-facing title from @docs; "" without it
	Path          string // full route path including the /api prefix
	RoutePath     string // route path relative to the /api mount point
	Method        string // upper-case HTTP method ("GET", "POST", ...)
	HandlerName   string // handler factory name stem (e.g. "OrdersGetOrderHandler")
	ImplName      string // namespace-qualified implementation name (e.g. "OrdersGetOrder")
	ShortImplName string // implementation method name without the namespace prefix
	Namespace     string // kebab-case namespace derived from the operation set name

	InputType       string  // input type name when the operation takes an @input-style argument
	InputTypeFields []Param // fields of the input type (for scaffold docs)
	HasInput        bool
	// InputRequired reports whether the input argument is non-null.
	InputRequired bool

	OutputType    string // schema output type name
	OutputGoType  string // qualified Go type expression for the output
	OutputIsArray bool

	PathParams  []Param
	QueryParams []Param
	ScalarArgs  []Param

	Description string
	// Operation is the schema operation the endpoint is generated from.
	Operation *ir.FieldDef `json:"-"`

	// Docs is the operation's @docs record; nil without it.
	Docs *ir.OperationDocs
	// MCP is the operation's resolved @mcp record: for a visible tool, Name
	// and Description come from @docs, Icon from @icon, and Invocation is
	// set, to the policy's default when the record had none. Nil without
	// @mcp. It is a copy; the schema's record is unchanged.
	MCP *ir.OperationMCP

	RequiresAuth     bool
	RequiredPerms    []string
	RequireOwnership bool
	// PublicRoute marks an operation declared @publicRoute: intentionally
	// unauthenticated, as opposed to one that merely declares no auth.
	PublicRoute bool

	// IsScopedEndpoint and ScopeParamName are filled by the auth provider's
	// Endpoint hook; the SDK generators read them to hoist one path
	// parameter (the scope: an account, a workspace, a project) to the
	// namespace client's constructor. ScopeParamName is that path
	// parameter's name.
	IsScopedEndpoint bool
	ScopeParamName   string
	// Auth is provider-owned per-endpoint data the provider's templates read.
	Auth any

	HasFileUpload    bool
	FileUploadFields []FileUploadField

	// Middleware values resolved from the operation set defaults plus
	// field-level overrides. Nil means no directive.
	RateLimit *int
	BodyLimit *int
	Timeout   *int

	Encrypted  bool
	Filterable bool

	// ManualRouteRegistration marks operations excluded from generated route
	// registration. The handler factory and interface method are still
	// generated so services can register the route themselves.
	ManualRouteRegistration bool

	IsWebhook                    bool
	WebhookHMACProvider          string
	WebhookHMACProviderTypesExpr string

	// NeedsTypesImport reports whether the endpoint scaffold references the
	// generated types module.
	NeedsTypesImport bool
}

// APIOutput contains everything needed to render the generated API module.
type APIOutput struct {
	SchemaName  string
	ModulePath  string
	TypesModule string

	// ModuleDependencies lists sibling type modules the types module depends
	// on; the API go.mod carries replace directives for them.
	ModuleDependencies []string

	ORMModule           string // upstream ORM module path (public APIs only)
	IsPublic            bool
	UpstreamSchema      string // upstream DB schema name (public APIs only)
	UpstreamTypesModule string // upstream types module path (public APIs only)

	// Auth is the provider's analysis of the upstream schema: it gates the
	// ORM-backed auth store adapters in middleware.go on what the upstream
	// schema actually declares.
	Auth AuthModel

	// Provider rendered this output; WriteAPI renders its templates and
	// files next to the core ones.
	Provider AuthProvider `json:"-"`

	Endpoints  []EndpointInfo
	Namespaces []string

	HasAuth                  bool
	HasMiddlewareDirectives  bool
	HasEncryptedEndpoints    bool
	HasPermissionEndpoints   bool
	HasFilterableEndpoints   bool
	HasFileUpload            bool
	HasArrayQueryParams      bool
	HasArrayScalarArgsOnGET  bool
	HasWebhookHMACEndpoints  bool
	RequiredWebhookProviders []string

	// UUIDTypeExpr is the qualified Go expression for the schema's UUID
	// scalar (e.g. "types.IdentityUUID"). Empty when the schema declares no
	// UUID-like scalar; constants.go is then skipped.
	UUIDTypeExpr   string
	SystemUserUUID string

	Timestamp      string
	OpenAPISpec    string // backtick-escaped JSON for Go embedding
	OpenAPISpecRaw string // raw JSON written to openapi.json

	// Scalars carries JSON Schema metadata for tool-calling bindings.
	Scalars map[string]ScalarJSONSchemaInfo

	// TypeFields holds the fields of every object type a tool argument can
	// reach, in the schema and its dependencies, so the tool schemas expand
	// nested objects.
	TypeFields map[string][]Param
	// TypeUnions holds every union a tool argument can reach; the tool
	// schemas render them as oneOf.
	TypeUnions map[string]ToolUnionInfo
	// ToolKeys are the vendor-extension keys the SDK tool documents are
	// written with: DefaultToolKeys, as the tool hooks left them.
	ToolKeys ToolKeys
	// ToolInvocation is the invocation policy the tool documents write
	// each visible tool's policy under (Options.ToolInvocation).
	ToolInvocation ToolInvocationPolicy

	// EnvConfig holds environment-variable loader output when populated by
	// the dispatch layer before WriteAPI.
	EnvConfig *envgen.ConfigOutput `json:"-"`

	// Naming supplies the module root and runtime module paths the templates
	// import; the SDK generators derive their package names from it.
	Naming naming.Naming

	// NeedsTypesImport reports whether any endpoint interface signature
	// references the generated types module.
	NeedsTypesImport bool

	// Replace directive paths computed by SetReplacePaths. Empty values omit
	// the corresponding replace directive.
	ScalarLibReplacePath     string
	SchemaIRReplacePath      string
	HTTPRuntimeReplacePath   string
	SchemaRuntimeReplacePath string
	PtrReplacePath           string
}

// HasConstants reports whether constants.go is generated: public schemas
// with a UUID-like scalar get a SystemUserID constant.
func (o *APIOutput) HasConstants() bool {
	return o.IsPublic && o.UUIDTypeExpr != ""
}

// EndpointOutput is the template data for per-endpoint scaffold files.
type EndpointOutput struct {
	SchemaName  string
	ModulePath  string
	TypesModule string
	ORMModule   string
	IsPublic    bool
	Endpoint    EndpointInfo
	Naming      naming.Naming
}

// NamespaceOutput is the template data for per-namespace scaffold files.
type NamespaceOutput struct {
	SchemaName  string
	ModulePath  string
	TypesModule string
	ORMModule   string
	IsPublic    bool
	Namespace   string
	Naming      naming.Naming
}

// Options configures API generation.
type Options struct {
	// SchemaName is the service name (e.g. "fixture-api").
	SchemaName string

	// ModulePath is the Go module path for the generated API module.
	ModulePath string

	// TypesModule is the Go module path of the schema's generated types.
	TypesModule string

	// ModuleDependencies lists sibling type module paths the types module
	// requires (one per declared service dependency with Go types).
	ModuleDependencies []string

	// Dependencies contains loaded dependency schemas. OpenAPI generation uses
	// these to materialize components for imported enums referenced by local
	// input/output types.
	Dependencies map[string]*ir.Schema

	// IsPublic marks a public-facing API: auth middleware, the provider's
	// request-scope resolution, and the upstream ORM wiring are generated.
	IsPublic bool

	// UpstreamSchema is the DB schema backing authentication (the authDb
	// config value, or the single DB-kind dependency). Empty for non-public
	// APIs.
	UpstreamSchema string

	// UpstreamIR is the loaded IR of the upstream schema, used to gate the
	// auth store adapters on the tables it declares. Required when
	// UpstreamSchema is set.
	UpstreamIR *ir.Schema

	// ORMModule is the upstream ORM module path. Derived from
	// UpstreamSchema when empty.
	ORMModule string

	// Naming supplies the Go module root and the runtime module paths.
	// Empty fields fall back to naming.Default().
	Naming naming.Naming

	// Provider is the auth provider the output is generated with. Required;
	// the registry's SelectedAuthProvider resolves it from
	// Naming.AuthProvider.
	Provider AuthProvider

	// Clock stamps generated file headers.
	Clock codegen.Clock

	// OpenAPIHooks edit the OpenAPI document before it is written, in
	// order. The registry's OpenAPIHooks supplies them.
	OpenAPIHooks []OpenAPIHook

	// ToolHooks edit the tool vendor keys and the resolved @mcp records,
	// in order, before the SDK generators read them. The registry's
	// ToolHooks supplies them.
	ToolHooks []ToolHook

	// ToolInvocation is the invocation policy a visible tool's @mcp record
	// is resolved against: a record without one gets its default, and one
	// under another key or with another value fails the build. The zero
	// value is DefaultToolInvocationPolicy; the registry's
	// ToolInvocationPolicy supplies it.
	ToolInvocation ToolInvocationPolicy
}

// Generate extracts REST endpoints from the schema's operation sets and
// assembles the API output. Returns (nil, nil) when the schema declares no
// operations.
func Generate(schema *ir.Schema, opts Options) (*APIOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}

	if len(schema.OperationSets) == 0 {
		return nil, nil
	}
	if opts.Provider == nil {
		return nil, fmt.Errorf("apigen: Options.Provider is required")
	}
	opts.Naming = opts.Naming.OrDefault()
	opts.ToolInvocation = opts.ToolInvocation.OrDefault()
	if err := opts.ToolInvocation.Validate(); err != nil {
		return nil, fmt.Errorf("apigen: %w", err)
	}

	ormModule := opts.ORMModule
	if ormModule == "" && opts.UpstreamSchema != "" {
		ormModule = opts.Naming.GoORMModule(opts.UpstreamSchema)
	}
	upstreamTypesModule := ""
	if opts.UpstreamSchema != "" {
		upstreamTypesModule = opts.Naming.GoTypesModule(opts.UpstreamSchema)
	}

	output := &APIOutput{
		SchemaName:          opts.SchemaName,
		ModulePath:          opts.ModulePath,
		TypesModule:         opts.TypesModule,
		ModuleDependencies:  append([]string{}, opts.ModuleDependencies...),
		ORMModule:           ormModule,
		IsPublic:            opts.IsPublic,
		UpstreamSchema:      opts.UpstreamSchema,
		UpstreamTypesModule: upstreamTypesModule,
		Naming:              opts.Naming,
		Provider:            opts.Provider,
		ToolInvocation:      opts.ToolInvocation,
		Endpoints:           []EndpointInfo{},
		Timestamp:           opts.Clock.RFC3339(),
		SystemUserUUID:      "00000000-0000-4000-8000-000000000000",
		UUIDTypeExpr:        resolveUUIDTypeExpr(schema),
	}

	if opts.UpstreamSchema != "" && opts.UpstreamIR == nil {
		return nil, fmt.Errorf("apigen: upstream schema %s declared but no upstream IR provided", opts.UpstreamSchema)
	}
	auth, err := opts.Provider.Analyze(schema, opts.UpstreamIR)
	if err != nil {
		return nil, fmt.Errorf("apigen: auth provider %s: %w", opts.Provider.Name(), err)
	}
	output.Auth = auth

	types := newTypeMapper(schema, opts.Dependencies)
	output.TypeFields = types.allTypeFields()
	output.TypeUnions = types.allTypeUnions()

	for _, set := range schema.OperationSets {
		namespace := extractNamespace(set.Name)
		defaultMethod := defaultMethodForSet(set.Name)

		for _, op := range set.Operations {
			endpoint, err := operationToEndpoint(op, namespace, defaultMethod, set, types, schema, opts.Provider, opts.ToolInvocation)
			if err != nil {
				return nil, err
			}
			output.Endpoints = append(output.Endpoints, *endpoint)
		}
	}

	if len(output.Endpoints) == 0 {
		return nil, nil
	}

	for _, endpoint := range output.Endpoints {
		if endpoint.RequiresAuth {
			output.HasAuth = true
		}
		if endpoint.RateLimit != nil || endpoint.BodyLimit != nil || endpoint.Timeout != nil {
			output.HasMiddlewareDirectives = true
		}
		if endpoint.HasFileUpload {
			output.HasFileUpload = true
		}
		for _, param := range endpoint.QueryParams {
			if param.IsArray {
				output.HasArrayQueryParams = true
				break
			}
		}
		if endpoint.Method == "GET" {
			for _, arg := range endpoint.ScalarArgs {
				if arg.IsArray {
					output.HasArrayScalarArgsOnGET = true
					break
				}
			}
		}
		if endpoint.Encrypted {
			output.HasEncryptedEndpoints = true
		}
		if len(endpoint.RequiredPerms) > 0 {
			output.HasPermissionEndpoints = true
		}
		if endpoint.Filterable {
			if endpoint.Method != "GET" {
				return nil, fmt.Errorf("@filterable is only supported on GET operations, but %s.%s is %s", endpoint.Namespace, endpoint.Name, endpoint.Method)
			}
			output.HasFilterableEndpoints = true
		}
		if endpoint.WebhookHMACProvider != "" {
			output.HasWebhookHMACEndpoints = true
		}
		if endpoint.NeedsTypesImport {
			output.NeedsTypesImport = true
		}
	}
	if output.HasWebhookHMACEndpoints {
		// The WebhookVerifiers map is keyed by the directive provider string.
		output.NeedsTypesImport = true
	}

	webhookProvSet := make(map[string]struct{})
	for _, endpoint := range output.Endpoints {
		if endpoint.WebhookHMACProvider != "" {
			webhookProvSet[endpoint.WebhookHMACProvider] = struct{}{}
		}
	}
	for p := range webhookProvSet {
		output.RequiredWebhookProviders = append(output.RequiredWebhookProviders, p)
	}
	sort.Strings(output.RequiredWebhookProviders)

	sort.Slice(output.Endpoints, func(i, j int) bool {
		if output.Endpoints[i].Path != output.Endpoints[j].Path {
			return output.Endpoints[i].Path < output.Endpoints[j].Path
		}
		return output.Endpoints[i].Method < output.Endpoints[j].Method
	})

	namespaceSet := make(map[string]bool)
	for _, endpoint := range output.Endpoints {
		namespaceSet[endpoint.Namespace] = true
	}
	for ns := range namespaceSet {
		output.Namespaces = append(output.Namespaces, ns)
	}
	sort.Strings(output.Namespaces)

	if err := validateRouteCollisions(output.Endpoints); err != nil {
		return nil, err
	}
	if err := validateHandlerNameCollisions(output.Endpoints); err != nil {
		return nil, err
	}
	keys, err := applyToolHooks(schema, output.Endpoints, opts.ToolHooks)
	if err != nil {
		return nil, err
	}
	output.ToolKeys = keys
	if err := validateMCPInvocations(output.Endpoints, opts.ToolInvocation); err != nil {
		return nil, err
	}
	if err := validateMCPCollisions(output.Endpoints); err != nil {
		return nil, err
	}

	rawSpec, escapedSpec, err := generateOpenAPISpec(output, schema, opts.Dependencies, opts.OpenAPIHooks)
	if err != nil {
		return nil, fmt.Errorf("apigen: openapi spec: %w", err)
	}
	output.OpenAPISpecRaw = rawSpec
	output.OpenAPISpec = escapedSpec
	output.Scalars = extractScalarJSONSchemaInfo(schema)

	return output, nil
}

// extractNamespace derives the kebab-case namespace from an operation set
// name (e.g. "OrderQueries" -> "order", "ConnectorOperations" -> "connector").
func extractNamespace(setName string) string {
	if setName == "Query" || setName == "Mutation" {
		return "root"
	}
	name := strings.TrimSuffix(setName, "Queries")
	name = strings.TrimSuffix(name, "Mutations")
	name = strings.TrimSuffix(name, "Operations")
	name = strings.TrimSuffix(name, "Actions")
	if name == "" {
		name = setName
	}
	return codegen.ToKebabCase(name)
}

// defaultMethodForSet returns the HTTP method for operations that omit an
// explicit method. Mutation- and action-style sets default to POST.
func defaultMethodForSet(setName string) string {
	if strings.HasSuffix(setName, "Mutations") || strings.HasSuffix(setName, "Actions") {
		return "POST"
	}
	return "GET"
}

// operationToEndpoint converts an operation FieldDef into an endpoint.
func operationToEndpoint(op *ir.FieldDef, namespace, defaultMethod string, set *ir.OperationSet, types *typeMapper, schema *ir.Schema, provider AuthProvider, invocation ToolInvocationPolicy) (*EndpointInfo, error) {
	if err := ir.ValidateOperationDocs(op.Docs); err != nil {
		return nil, fmt.Errorf("apigen: operation %s.%s has invalid docs: %w", namespace, op.Name, err)
	}
	mcp, err := resolveOperationMCP(op, namespace, invocation)
	if err != nil {
		return nil, err
	}
	method := strings.ToUpper(op.HTTPMethod)
	if method == "" {
		method = defaultMethod
	}

	namespacePascal := codegen.ToPascalCase(namespace)
	shortImplName := codegen.ToPascalCase(op.Name)
	implName := namespacePascal + shortImplName

	embeddedParams := extractEmbeddedParams(op.RestPath)

	var pathParams, queryParams, scalarArgs []Param
	var inputType string
	hasInput := false
	inputRequired := false

	for _, arg := range op.Arguments {
		param, err := types.mapArgument(arg)
		if err != nil {
			return nil, fmt.Errorf("apigen: operation %s.%s argument %s: %w", namespace, op.Name, arg.Name, err)
		}

		// A @query argument whose name is also embedded in the rest path is a
		// contradiction: the path placeholder would never be substituted and the
		// handler would look for the value in the query string instead.
		if arg.IsQuery && op.RestPath != "" && embeddedParams[arg.Name] {
			return nil, fmt.Errorf(
				"apigen: operation %s.%s argument %s is declared @query but is also embedded in rest path %q; "+
					"drop the QueryParam<> wrapper so it is treated as a path parameter",
				namespace, op.Name, arg.Name, op.RestPath,
			)
		}

		switch {
		case arg.IsQuery:
			queryParams = append(queryParams, param)
		case op.RestPath != "" && embeddedParams[arg.Name]:
			pathParams = append(pathParams, param)
		case op.RestPath == "" && types.isPathParamType(arg.TypeRef.Name):
			pathParams = append(pathParams, param)
		case types.isInputType(arg.TypeRef.Name):
			if hasInput {
				return nil, fmt.Errorf("apigen: operation %s.%s declares multiple input-type arguments", namespace, op.Name)
			}
			inputType = arg.TypeRef.Name
			hasInput = true
			inputRequired = arg.Required
		default:
			scalarArgs = append(scalarArgs, param)
		}
	}

	if hasInput && len(scalarArgs) > 0 {
		names := make([]string, len(scalarArgs))
		for i, a := range scalarArgs {
			names[i] = a.Name
		}
		return nil, fmt.Errorf("apigen: operation %s.%s mixes input type %s with undecorated scalar args (%s); decorate them with @query or fold them into the input type", namespace, op.Name, inputType, strings.Join(names, ", "))
	}

	path := buildPath(op, namespace, pathParams, embeddedParams)

	var inputTypeFields []Param
	var fileUploadFields []FileUploadField
	if inputType != "" {
		inputTypeFields = types.inputTypeFields(inputType)
		fileUploadFields = types.extractFileUploadFields(inputType)
	}

	requiresAuth := op.Auth || op.RequireOwnership || len(op.Permissions) > 0

	rateLimit, bodyLimit, timeout := resolveMiddleware(set.Middleware, op.Middleware)

	outputGoType := types.qualifiedGoType(op.TypeRef.Name)

	webhookHMACTypesExpr := ""
	if op.HMACVerifiedProvider != "" {
		webhookHMACTypesExpr = fmt.Sprintf("%q", op.HMACVerifiedProvider)
	}

	title := ""
	if op.Docs != nil {
		title = op.Docs.Title
	}

	endpoint := &EndpointInfo{
		Name:                         op.Name,
		Title:                        title,
		Path:                         path,
		RoutePath:                    strings.TrimPrefix(path, "/api"),
		Method:                       method,
		HandlerName:                  implName + "Handler",
		ImplName:                     implName,
		ShortImplName:                shortImplName,
		Namespace:                    namespace,
		InputType:                    inputType,
		InputTypeFields:              inputTypeFields,
		HasInput:                     hasInput,
		InputRequired:                inputRequired,
		OutputType:                   op.TypeRef.Name,
		OutputGoType:                 outputGoType,
		OutputIsArray:                op.TypeRef.IsArray,
		PathParams:                   pathParams,
		QueryParams:                  queryParams,
		ScalarArgs:                   scalarArgs,
		Description:                  codegen.DocText(op.Description, op.Comment),
		Operation:                    op,
		Docs:                         op.Docs,
		MCP:                          mcp,
		RequiresAuth:                 requiresAuth,
		RequiredPerms:                op.Permissions,
		RequireOwnership:             op.RequireOwnership,
		PublicRoute:                  op.Public,
		HasFileUpload:                len(fileUploadFields) > 0,
		FileUploadFields:             fileUploadFields,
		RateLimit:                    rateLimit,
		BodyLimit:                    bodyLimit,
		Timeout:                      timeout,
		Encrypted:                    set.Encrypted || op.Encrypted,
		Filterable:                   op.Filterable,
		ManualRouteRegistration:      op.ManualRouteRegistration,
		IsWebhook:                    op.Webhook,
		WebhookHMACProvider:          op.HMACVerifiedProvider,
		WebhookHMACProviderTypesExpr: webhookHMACTypesExpr,
	}
	endpoint.NeedsTypesImport = endpointNeedsTypesImport(endpoint)
	if err := provider.Endpoint(op, set, endpoint); err != nil {
		return nil, fmt.Errorf("apigen: operation %s.%s: auth provider %s: %w", namespace, op.Name, provider.Name(), err)
	}

	return endpoint, nil
}

// buildPath assembles the full route path. An explicit @rest path wins;
// otherwise the path is /api/<namespace>/<kebab-name> plus path-param
// suffixes not already embedded in the segment.
func buildPath(op *ir.FieldDef, namespace string, pathParams []Param, embeddedParams map[string]bool) string {
	parts := []string{"api"}

	if op.RestPath != "" {
		parts = append(parts, strings.Trim(op.RestPath, "/"))
		return "/" + strings.Join(parts, "/")
	}

	if namespace != "" && namespace != "root" {
		parts = append(parts, namespace)
	}
	parts = append(parts, codegen.ToKebabCase(op.Name))
	for _, param := range pathParams {
		if !embeddedParams[param.Name] {
			parts = append(parts, "{"+param.Name+"}")
		}
	}
	return "/" + strings.Join(parts, "/")
}

// resolveMiddleware merges set-level middleware defaults with field-level
// overrides.
func resolveMiddleware(setMW, fieldMW *ir.MiddlewareConfig) (rateLimit, bodyLimit, timeout *int) {
	if setMW != nil {
		rateLimit = setMW.RateLimit
		bodyLimit = setMW.BodyLimit
		timeout = setMW.Timeout
	}
	if fieldMW != nil {
		if fieldMW.RateLimit != nil {
			rateLimit = fieldMW.RateLimit
		}
		if fieldMW.BodyLimit != nil {
			bodyLimit = fieldMW.BodyLimit
		}
		if fieldMW.Timeout != nil {
			timeout = fieldMW.Timeout
		}
	}
	return rateLimit, bodyLimit, timeout
}

// extractEmbeddedParams returns the set of parameter names embedded in a
// @rest path template (e.g. "orders/{id}" -> {"id"}).
func extractEmbeddedParams(restPath string) map[string]bool {
	params := make(map[string]bool)
	for i := 0; i < len(restPath); i++ {
		if restPath[i] != '{' {
			continue
		}
		end := strings.IndexByte(restPath[i:], '}')
		if end < 0 {
			break
		}
		name := restPath[i+1 : i+end]
		if name != "" {
			params[name] = true
		}
		i += end
	}
	return params
}

// endpointNeedsTypesImport reports whether the endpoint scaffold references
// the generated types module.
func endpointNeedsTypesImport(endpoint *EndpointInfo) bool {
	if endpoint.HasInput {
		return true
	}
	if strings.HasPrefix(endpoint.OutputGoType, "types.") {
		return true
	}
	for _, param := range endpoint.PathParams {
		if strings.HasPrefix(param.GoType, "types.") {
			return true
		}
	}
	for _, param := range endpoint.QueryParams {
		if strings.HasPrefix(param.GoType, "types.") {
			return true
		}
	}
	for _, arg := range endpoint.ScalarArgs {
		if strings.HasPrefix(arg.GoType, "types.") {
			return true
		}
	}
	return false
}

// resolveUUIDTypeExpr finds the schema's UUID-like scalar and returns its
// qualified Go type expression.
func resolveUUIDTypeExpr(schema *ir.Schema) string {
	var fallback string
	for name, scalarDef := range schema.Scalars {
		tokens := codegen.BuildScalarTokens(name)
		traits := codegen.BuildScalarTraits(scalarDef, tokens, "")
		if !traits.IsUUIDLike {
			continue
		}
		expr := "types." + tokens.Symbol
		if strings.EqualFold(tokens.Leaf(), "uuid") {
			return expr
		}
		if fallback == "" || expr < fallback {
			fallback = expr
		}
	}
	return fallback
}

// validateRouteCollisions checks that no two auto-registered endpoints claim
// the same (path, method) pair. Chi silently replaces handlers on collision.
func validateRouteCollisions(endpoints []EndpointInfo) error {
	type routeKey struct {
		path   string
		method string
	}

	registered := make(map[routeKey]string)
	for _, ep := range endpoints {
		if ep.ManualRouteRegistration {
			continue
		}
		key := routeKey{path: ep.Path, method: ep.Method}
		if existing, ok := registered[key]; ok && existing != ep.Name {
			return fmt.Errorf("apigen: route collision: %s %s is claimed by both %q and %q", ep.Method, ep.Path, existing, ep.Name)
		}
		registered[key] = ep.Name
	}
	return nil
}

// validateHandlerNameCollisions checks that no two endpoints produce the
// same handler factory name.
func validateHandlerNameCollisions(endpoints []EndpointInfo) error {
	seen := make(map[string]string)
	for _, ep := range endpoints {
		if existing, ok := seen[ep.HandlerName]; ok {
			return fmt.Errorf("apigen: handler name collision: %q is generated for both %s and %s", ep.HandlerName, existing, ep.Path)
		}
		seen[ep.HandlerName] = ep.Path
	}
	return nil
}

// typeMapper resolves schema type names to Go type expressions, parse
// strategies, and classification flags.
type typeMapper struct {
	schema       *ir.Schema
	dependencies map[string]*ir.Schema
	scalars      map[string]scalarEntry
}

type scalarEntry struct {
	symbol string
	traits codegen.ScalarTraits
}

func newTypeMapper(schema *ir.Schema, dependencies map[string]*ir.Schema) *typeMapper {
	scalars := make(map[string]scalarEntry, len(schema.Scalars))
	for name, scalarDef := range schema.Scalars {
		tokens := codegen.BuildScalarTokens(name)
		symbol := strings.TrimSpace(tokens.Symbol)
		if symbol == "" {
			symbol = name
		}
		scalars[name] = scalarEntry{
			symbol: symbol,
			traits: codegen.BuildScalarTraits(scalarDef, tokens, scalarDef.TypeMappings["go"]),
		}
	}
	return &typeMapper{schema: schema, dependencies: dependencies, scalars: scalars}
}

// isInputType reports whether the named type is an object type usable as an
// operation input payload. The v2 IR drops the GraphQL Object/Input split:
// any data shape passed as an operation argument is that operation's input.
func (m *typeMapper) isInputType(name string) bool {
	typeDef, ok := m.findTypeDef(name)
	if !ok {
		return false
	}
	switch typeDef.Role {
	case ir.RoleAPIInput, ir.RoleAPIView, ir.RoleEmbeddedStruct:
		return true
	default:
		return false
	}
}

func (m *typeMapper) findTypeDef(name string) (*ir.TypeDef, bool) {
	if typeDef, ok := m.schema.Types[name]; ok {
		return typeDef, true
	}
	for _, dep := range m.dependencies {
		if dep == nil {
			continue
		}
		if typeDef, ok := dep.Types[name]; ok {
			return typeDef, true
		}
	}
	return nil, false
}

// isPathParamType reports whether the named type can appear as a URL path
// parameter when no explicit @rest path positions the arguments.
func (m *typeMapper) isPathParamType(name string) bool {
	if entry, ok := m.scalars[name]; ok {
		return entry.traits.IsPathLike || entry.traits.IsUUIDLike || entry.traits.IsIDLike || entry.traits.IsIntegerLike
	}
	return codegen.LanguagePrimitiveTraits(name).IsPathLike
}

// mapArgument converts an IR argument into a Param with its Go type and
// parse strategy resolved.
func (m *typeMapper) mapArgument(arg *ir.ArgumentDef) (Param, error) {
	param := Param{
		Name:              arg.Name,
		GoName:            codegen.ToPascalCase(arg.Name),
		Type:              arg.TypeRef.Name,
		Required:          arg.Required,
		IsArray:           arg.TypeRef.IsArray,
		IsMap:             arg.TypeRef.IsMap,
		DefaultValue:      arg.Default,
		ValidateMin:       arg.ValidateMin,
		ValidateMax:       arg.ValidateMax,
		ValidateMinLength: arg.ValidateMinLength,
		ValidateMaxLength: arg.ValidateMaxLength,
		ValidateListMin:   arg.ValidateListMin,
		ValidateListMax:   arg.ValidateListMax,
		ValidatePattern:   arg.ValidatePattern,
	}
	m.applyGoMapping(&param, arg.TypeRef.Name)
	return param, nil
}

// applyGoMapping resolves the Go type expression and parse flags for a
// schema type name.
func (m *typeMapper) applyGoMapping(param *Param, typeName string) {
	switch typeName {
	case codegen.PrimitiveString:
		param.GoType = "string"
		param.IsString = true
		return
	case codegen.PrimitiveNumber:
		param.GoType = "float64"
		param.IsFloat = true
		return
	case codegen.PrimitiveBoolean:
		param.GoType = "bool"
		param.IsBool = true
		return
	}

	if entry, ok := m.scalars[typeName]; ok {
		switch {
		case entry.traits.IsUUIDLike:
			param.GoType = "types." + entry.symbol
			param.IsUUID = true
			param.ParseFunc = "types.Parse" + entry.symbol
		case entry.traits.IsDateTimeLike || entry.symbol == "TemporalDateTime":
			param.GoType = "types." + entry.symbol
			param.IsDateTime = true
			param.ParseFunc = "types.Parse" + entry.symbol
		case entry.traits.IsIntegerLike:
			param.GoType = "int64"
			param.IsInt = true
		case entry.traits.IsFloatLike && !entry.traits.IsStringLike:
			param.GoType = "float64"
			param.IsFloat = true
		case entry.traits.IsBooleanLike && !entry.traits.IsStringLike:
			param.GoType = "bool"
			param.IsBool = true
		default:
			// String-backed scalar: cast from the raw string and validate
			// via the scalar's Validate/ValidateRequired methods.
			param.GoType = "types." + entry.symbol
		}
		return
	}

	// Enums and any other named definition: string-backed cast.
	param.GoType = "types." + codegen.ToPascalCase(typeName)
}

// qualifiedGoType returns the Go type expression for an output type name.
func (m *typeMapper) qualifiedGoType(typeName string) string {
	switch typeName {
	case codegen.PrimitiveString:
		return "string"
	case codegen.PrimitiveNumber:
		return "float64"
	case codegen.PrimitiveBoolean:
		return "bool"
	}
	if entry, ok := m.scalars[typeName]; ok {
		return "types." + entry.symbol
	}
	return "types." + codegen.ToPascalCase(typeName)
}

// inputTypeFields extracts the fields of an input type for scaffold docs.
func (m *typeMapper) inputTypeFields(inputTypeName string) []Param {
	typeDef, ok := m.findTypeDef(inputTypeName)
	if !ok {
		return nil
	}
	return m.fieldsFromTypeDef(typeDef)
}

// fieldsFromTypeDef maps a type's fields to Params, with their shape and
// Validate<> bounds.
func (m *typeMapper) fieldsFromTypeDef(typeDef *ir.TypeDef) []Param {
	var fields []Param
	for _, field := range typeDef.Fields {
		param := Param{
			Name:              field.Name,
			GoName:            codegen.ToPascalCase(field.Name),
			Type:              field.TypeRef.Name,
			Required:          field.Required,
			IsArray:           field.TypeRef.IsArray,
			IsMap:             field.TypeRef.IsMap,
			ValidateMin:       field.ValidateMin,
			ValidateMax:       field.ValidateMax,
			ValidateMinLength: field.ValidateMinLength,
			ValidateMaxLength: field.ValidateMaxLength,
			ValidateListMin:   field.ValidateListMin,
			ValidateListMax:   field.ValidateListMax,
			ValidatePattern:   field.ValidatePattern,
		}
		m.applyGoMapping(&param, field.TypeRef.Name)
		fields = append(fields, param)
	}
	return fields
}

// extractFileUploadFields extracts file upload fields from an input type.
func (m *typeMapper) extractFileUploadFields(inputTypeName string) []FileUploadField {
	typeDef, ok := m.findTypeDef(inputTypeName)
	if !ok {
		return nil
	}

	var fields []FileUploadField
	for _, field := range typeDef.Fields {
		scalarDef, ok := m.findScalarDef(field.TypeRef.Name)
		if !ok {
			continue
		}

		var maxSize int64
		var allowedTypes []string
		var category string
		imageConstraints := scalarDef.ImageConstraints
		if scalarDef.FileUpload != nil {
			fu := scalarDef.FileUpload
			maxSize = int64(fu.MaxSize)
			allowedTypes = fu.AllowedTypes
			category = fu.Category
		}
		if scalarDef.FileUpload == nil {
			switch field.TypeRef.Name {
			case "Artifact.File", "Asset.File":
				category = "file"
			case "Asset.Image":
				category = "image"
			case "Asset.LogoImage":
				category = "image"
				if imageConstraints == nil {
					imageConstraints = &ir.ImageConstraints{RequireTransparency: true}
				}
			default:
				continue
			}
		}
		if field.ValidateUploadMaxBytes != nil {
			maxSize = *field.ValidateUploadMaxBytes
		}
		if maxSize <= 0 {
			maxSize = 100 * 1024 * 1024
		}
		if allowedTypes == nil {
			allowedTypes = []string{}
		}

		fields = append(fields, FileUploadField{
			Name:             field.Name,
			GoName:           codegen.ToPascalCase(field.Name),
			ScalarType:       field.TypeRef.Name,
			MaxSize:          maxSize,
			AllowedTypes:     allowedTypes,
			Category:         category,
			Required:         field.Required,
			ImageConstraints: imageConstraints,
		})
	}
	return fields
}

func (m *typeMapper) findScalarDef(name string) (*ir.ScalarDef, bool) {
	if scalarDef, ok := m.schema.Scalars[name]; ok {
		return scalarDef, true
	}
	for _, dep := range m.dependencies {
		if dep == nil {
			continue
		}
		if scalarDef, ok := dep.Scalars[name]; ok {
			return scalarDef, true
		}
	}
	return nil, false
}

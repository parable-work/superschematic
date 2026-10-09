// Package rustrestgen generates Rust Axum REST API server crates from
// API-kind schemas.
package rustrestgen

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/permcatalog"
	"github.com/parable-work/superschematic/internal/generator/rustapigen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// EndpointInfo describes a generated Rust REST endpoint.
type EndpointInfo struct {
	Name         string
	Namespace    string
	FunctionName string
	HandlerName  string
	Path         string
	Method       string
	Description  string
	// ArgsName is the operation's Args struct: its decoded path, query and
	// body arguments and its input, one field each.
	ArgsName  string
	PathArgs  []ParamInfo
	QueryArgs []ParamInfo
	// BodyArgs are the undecorated scalar arguments of an operation that is
	// not GET, read from the JSON body object; BodyRequired reports whether
	// one is required, so a request without a body is refused.
	BodyArgs     []ParamInfo
	BodyRequired bool
	// Input is the operation's input type, read from the body; nil without
	// one.
	Input *InputInfo
	// OutputRustType is the Rust type of the operation's result.
	OutputRustType string
	// WebhookProvider is the @hmacVerified provider, or empty. build_router
	// wraps the route in webhook_verified with that provider's verifier.
	WebhookProvider string
	// RequiresAuth marks a route that needs a caller (@auth,
	// @requirePermission, @requireOwnership, an Authenticated set), which
	// Implementations.authenticator establishes. RequiredPerms is its
	// @requirePermission list, of which the caller must hold one.
	// RequireOwnership leaves the ownership check to the implementation.
	RequiresAuth     bool
	RequiredPerms    []string
	RequireOwnership bool
	// RateLimit, BodyLimit and Timeout are the route's @rateLimit requests
	// per minute, @bodyLimit megabytes and @timeout seconds; 0 without the
	// directive. A value below 1 is no directive, as in the TypeScript
	// server.
	RateLimit int
	BodyLimit int
	Timeout   int
	// ServiceCallers is the operation's effective @requireService or
	// @allowService clause; nil without one. ServiceStep marks a route of a
	// crate whose schema has a service clause anywhere: Implementations then
	// has a service_authenticator, and every route verifies a service
	// credential when the request carries one (D37).
	ServiceCallers *ir.ServiceCallers
	ServiceStep    bool
	// Manual marks an operation declared @manualRouteRegistration, which the
	// service mounts itself.
	Manual bool
}

// AllowsService reports whether the route is @allowService: a listed
// service caller stands in for the end user, so the handler may get no
// principal.
func (e EndpointInfo) AllowsService() bool {
	return e.ServiceCallers != nil && e.ServiceCallers.Mode == ir.ServiceCallersAllow
}

// ConstName is the operation's OperationInfo constant in the crate's
// operations module: ORDERS_GET_ORDER.
func (e EndpointInfo) ConstName() string {
	return constName(e.HandlerName)
}

// ImplementationsField is the field of Implementations that holds the
// operation's namespace implementation.
func (e EndpointInfo) ImplementationsField() string {
	return rustutil.ToSnakeCase(e.Namespace)
}

// SnakeName is the operation's namespace and method in snake_case,
// orders_get_order, as the router names its handler and decoder.
func (e EndpointInfo) SnakeName() string {
	return e.ImplementationsField() + "_" + e.FunctionName
}

// HasArgs reports whether the implementation method takes an Args struct.
func (e EndpointInfo) HasArgs() bool {
	return e.Input != nil || len(e.PathArgs) > 0 || len(e.QueryArgs) > 0 || len(e.BodyArgs) > 0
}

// ReadsBody reports whether the handler reads the request body: for the
// input, which the Go router reads on any method, or for scalar arguments.
func (e EndpointInfo) ReadsBody() bool {
	return e.Input != nil || len(e.BodyArgs) > 0
}

// DecodeParams are the parameters of the handler's decode function, one
// per place it reads an argument from.
func (e EndpointInfo) DecodeParams() []string {
	var params []string
	if len(e.PathArgs) > 0 {
		params = append(params, "captures: &HashMap<String, String>")
	}
	if len(e.QueryArgs) > 0 {
		params = append(params, "query: &QueryValues")
	}
	if e.ReadsBody() {
		params = append(params, "body: Option<Value>")
	}
	return params
}

// DecodeCall is the argument list the handler passes its decode function.
func (e EndpointInfo) DecodeCall() string {
	var args []string
	if len(e.PathArgs) > 0 {
		args = append(args, "&path_params")
	}
	if len(e.QueryArgs) > 0 {
		args = append(args, "&query")
	}
	if e.ReadsBody() {
		args = append(args, "body")
	}
	return strings.Join(args, ", ")
}

// UsesTypes reports whether the method's signature names the types crate.
func (e EndpointInfo) UsesTypes() bool {
	return strings.Contains(e.OutputRustType, "types::")
}

// HasControls reports whether the route has a traffic control, needs a
// caller or runs the service step: build_router applies RouteControls to
// it.
func (e EndpointInfo) HasControls() bool {
	return e.RequiresAuth || e.ServiceStep || e.RateLimit > 0 || e.BodyLimit > 0 || e.Timeout > 0
}

// ControlCalls are the RouteControls builder calls of the route, in the
// order a request meets them: the rate limit, the body limit, the service
// step, the permission check, then the timeout around the handler.
// authenticator and serviceAuthenticator are the Rust expressions of the
// Arc<dyn Authenticator> and the Arc<dyn ServiceAuthenticator> the checks
// use.
func (e EndpointInfo) ControlCalls(authenticator, serviceAuthenticator string) []string {
	var calls []string
	if e.RateLimit > 0 {
		calls = append(calls, fmt.Sprintf(".rate_limit(%d)", e.RateLimit))
	}
	if e.BodyLimit > 0 {
		calls = append(calls, fmt.Sprintf(".body_limit_megabytes(%d)", e.BodyLimit))
	}
	if e.ServiceStep {
		calls = append(calls, e.serviceCall(serviceAuthenticator))
	}
	if e.RequiresAuth {
		perms := make([]string, len(e.RequiredPerms))
		for i, perm := range e.RequiredPerms {
			perms[i] = rustString(perm)
		}
		calls = append(calls, fmt.Sprintf(".authorize(%s, &[%s])", authenticator, strings.Join(perms, ", ")))
	}
	if e.Timeout > 0 {
		calls = append(calls, fmt.Sprintf(".timeout_seconds(%d)", e.Timeout))
	}
	return calls
}

// serviceCall is the route's service step: its @requireService or
// @allowService rule with the APIs of its from, or, on a route without a
// rule, identify_service, which verifies a credential that is present and
// puts its caller on the request.
func (e EndpointInfo) serviceCall(serviceAuthenticator string) string {
	if e.ServiceCallers == nil {
		return fmt.Sprintf(".identify_service(%s)", serviceAuthenticator)
	}
	from := make([]string, len(e.ServiceCallers.From))
	for i, api := range e.ServiceCallers.From {
		from[i] = rustString(api)
	}
	method := "require_service"
	if e.AllowsService() {
		method = "allow_service"
	}
	return fmt.Sprintf(".%s(%s, &[%s])", method, serviceAuthenticator, strings.Join(from, ", "))
}

// APIOutput contains generated Rust REST API metadata.
type APIOutput struct {
	rustapigen.APIOutputBase
	// Endpoints are the operations the router mounts.
	Endpoints []EndpointInfo
	// ManualEndpoints are the @manualRouteRegistration operations, which the
	// service mounts itself.
	ManualEndpoints []EndpointInfo
	// WebhookProviders are the @hmacVerified providers of every endpoint,
	// manual ones included, sorted: Implementations.webhook_verifiers needs a
	// verifier for each, as the Go server's WebhookVerifiers map does.
	WebhookProviders []string
	// HasAuth reports whether an endpoint, manual ones included, needs a
	// caller: Implementations then has an authenticator.
	HasAuth bool
	// HasControls reports whether an endpoint, manual ones included, has
	// RouteControls.
	HasControls bool
	// HasServiceCallers reports whether an endpoint, manual ones included,
	// has a service clause: Implementations then has a
	// service_authenticator, and every route runs the service step (D37).
	HasServiceCallers bool
	// OpenAPIJSON is the service's OpenAPI document as apigen builds it
	// for every server: written to openapi.json, embedded in src/openapi.rs
	// and served at GET /api/openapi.json.
	OpenAPIJSON string
	// PermissionCatalogJSON is the API's permissions.json as apigen builds
	// it, written beside openapi.json; empty when no operation names a
	// permission.
	PermissionCatalogJSON string
	// HasEnvConfig is true when the build writes src/config.rs, the env
	// loader of the service's @envVars type; lib.rs then declares it.
	HasEnvConfig bool
	// TypesCrateIdent is TypesCrate as a Rust path segment; lib.rs
	// re-exports the crate as `types`.
	TypesCrateIdent string
	// Patterns are the compiled patterns the parameter specs share.
	Patterns []PatternStatic
	// DependencyCrates are the dependency types crates whose validators
	// the router calls, for an input or argument of a type a dependency
	// declares.
	DependencyCrates []DependencyCrate
}

// AllEndpoints are the mounted and the manual operations, in the order
// sortEndpoints gives them: the operations module declares each.
func (o *APIOutput) AllEndpoints() []EndpointInfo {
	all := append(append([]EndpointInfo{}, o.Endpoints...), o.ManualEndpoints...)
	sortEndpoints(all)
	return all
}

// Specs are the ParamSpec statics of every mounted operation's arguments.
func (o *APIOutput) Specs() []ParamInfo {
	var specs []ParamInfo
	for _, endpoint := range o.Endpoints {
		specs = append(specs, endpoint.PathArgs...)
		specs = append(specs, endpoint.QueryArgs...)
		specs = append(specs, endpoint.BodyArgs...)
	}
	return specs
}

// NamespaceOutput contains data for generating namespace scaffold files.
type NamespaceOutput struct {
	SchemaName        string
	Namespace         string
	CrateName         string
	CrateIdent        string
	RuntimeCrateIdent string
	// Endpoints are the namespace's mounted operations, each a method of
	// its trait and a file of its own.
	Endpoints []EndpointInfo
}

// UsesTypes reports whether a method signature of the namespace names the
// types crate.
func (n NamespaceOutput) UsesTypes() bool {
	for _, endpoint := range n.Endpoints {
		if endpoint.UsesTypes() {
			return true
		}
	}
	return false
}

// ArgsNames are the Args structs the namespace's methods take.
func (n NamespaceOutput) ArgsNames() []string {
	var names []string
	for _, endpoint := range n.Endpoints {
		if endpoint.HasArgs() {
			names = append(names, endpoint.ArgsName)
		}
	}
	return names
}

// EndpointOutput contains data for generating endpoint scaffold files.
type EndpointOutput struct {
	SchemaName        string
	Namespace         string
	CrateName         string
	CrateIdent        string
	RuntimeCrateIdent string
	Endpoint          EndpointInfo
}

// Options configures Rust REST API generation.
type Options struct {
	SchemaName string
	// Dependencies are the loaded dependency schemas, which declare the
	// enums, scalars and types an argument, input or result may name.
	Dependencies map[string]*ir.Schema
	TypesCrate   string
	TypesDir     string
	OutputDir    string
	Naming       naming.Naming
	Clock        codegen.Clock
}

// Generate produces Rust REST API metadata for a schema. Each mounted
// operation's method takes an Args struct of its decoded arguments, typed
// as the schema declares them, and returns its result type (D39). The
// router decodes path, query and body arguments as the TypeScript router
// does (the runtime crate's ParamSpec), and parses an input with its type's
// generated parse_<type>, refusing a top-level key the type does not
// declare; a refusal is a 400 problem that names the parameter, or the
// input's field errors. Each Args struct's check makes the same checks on
// arguments a caller builds, and the operations module declares each
// operation's route and auth rules (OperationInfo), so an in-process caller
// runs an operation by its route's rules.
//
// An operation declared @manualRouteRegistration is left to the service, as
// the Go server leaves it out of RegisterRoutes and the TypeScript server
// out of its implementation interfaces: the router does not mount it, its
// namespace trait has no method for it, and it gets no scaffold. The service
// adds its route to the router build_router returns. The router has no step
// that decrypts a request body and none that reads a multipart one, so an
// encrypted operation or a file upload that is not
// @manualRouteRegistration is refused.
//
// An @hmacVerified operation's route runs its provider's WebhookVerifier
// before the handler, as the Go router runs the provider's WebhookVerifier
// before its other middleware, and build_router panics when
// Implementations.webhook_verifiers lacks a provider's verifier.
//
// A route's other controls follow the verifier, in the Go router's order:
// @rateLimit, @bodyLimit, the permission check of a route that needs a
// caller, and @timeout around the handler. They are the runtime crate's
// RouteControls. The caller comes from Implementations.authenticator,
// which the crate has when an operation needs one (D29).
//
// When an operation, a manual one included, has a service clause
// (@requireService or @allowService), Implementations has a
// service_authenticator and every route runs the service step between the
// body limit and the permission check (D37): a route with a clause applies
// it, and the others verify a service credential when one is present. A
// listed caller on an @allowService route skips the permission check, so
// its handler takes the principal as an Option. A schema without a service
// clause generates the crate it did before.
//
// The endpoints and the OpenAPI document come from api, the apigen output
// generator.Run builds once for the Go and TypeScript servers and every
// SDK, with the service's dependencies, naming, OpenAPI and tool hooks and
// its authDb's auth model (D38). A nil api, or one without endpoints, is
// no server.
func Generate(schema *ir.Schema, api *apigen.APIOutput, opts Options) (*APIOutput, error) {
	if api == nil || len(api.Endpoints) == 0 {
		return nil, nil
	}
	b := newBuilder(schema, opts)

	output := &APIOutput{
		APIOutputBase: rustapigen.NewBase(rustapigen.BaseOptions{
			SchemaName: opts.SchemaName,
			TypesCrate: opts.TypesCrate,
			TypesDir:   opts.TypesDir,
			OutputDir:  opts.OutputDir,
			Naming:     opts.Naming,
			Clock:      opts.Clock,
		}),
		Endpoints:             make([]EndpointInfo, 0, len(api.Endpoints)),
		OpenAPIJSON:           api.OpenAPISpecRaw,
		PermissionCatalogJSON: api.PermissionCatalogJSON,
	}
	output.TypesCrateIdent = strings.ReplaceAll(output.TypesCrate, "-", "_")

	// The user model's operations are the identity runtime's (D50): the
	// Implementations traits and the router leave them out, and the
	// runtime serves their routes.
	var implemented []apigen.EndpointInfo
	for _, endpoint := range api.Endpoints {
		if endpoint.IdentityOperation == "" {
			implemented = append(implemented, endpoint)
		}
	}
	for _, endpoint := range implemented {
		output.HasServiceCallers = output.HasServiceCallers || endpoint.ServiceCallers != nil
	}

	namespaceSet := make(map[string]struct{})
	webhookProviders := make(map[string]struct{})
	for _, endpoint := range implemented {
		// The Go router decrypts an encrypted operation's body with the
		// configured PayloadDecryptor before it parses it. The Rust router
		// has no such step and would hand the envelope to the implementation
		// as the body. An operation declared @manualRouteRegistration is not
		// mounted: the service's own route receives the envelope and
		// decrypts it.
		if endpoint.Encrypted && !endpoint.ManualRouteRegistration {
			return nil, fmt.Errorf("rustrestgen: operation %s.%s is encrypted (an Encrypted operation set, @encrypted, or an EncryptedField<T> result or argument); the Rust router has no decryption step, declare it @manualRouteRegistration and add its route in the service, which decrypts the payload", endpoint.Namespace, endpoint.Name)
		}
		// The Go router reads a file upload's multipart body. The Rust
		// router's handlers take JSON, so axum would answer every upload
		// 415; the service mounts the route and reads the multipart body.
		if endpoint.HasFileUpload && !endpoint.ManualRouteRegistration {
			return nil, fmt.Errorf("rustrestgen: operation %s.%s uploads files; the Rust router has no multipart step, declare it @manualRouteRegistration and add its route in the service, which reads the multipart body", endpoint.Namespace, endpoint.Name)
		}
		ns := endpoint.Namespace
		if ns == "" {
			ns = "root"
		}

		fnName := rustutil.ToSnakeCase(endpoint.ShortImplName)
		if fnName == "" {
			fnName = rustutil.ToSnakeCase(endpoint.Name)
		}
		if fnName == "" {
			fnName = "call"
		}

		handlerName := rustutil.ToPascalCase(ns) + rustutil.ToPascalCase(fnName)
		info := EndpointInfo{
			Name:         endpoint.Name,
			Namespace:    ns,
			FunctionName: fnName,
			HandlerName:  handlerName,
			ArgsName:     handlerName + "Args",
			// apigen {param} placeholders pass through: axum 0.8 uses
			// {param} path captures natively (the 0.7-era :param panics).
			Path:        endpoint.Path,
			Method:      strings.ToLower(endpoint.Method),
			Description: endpoint.Description,

			WebhookProvider: endpoint.WebhookHMACProvider,

			RequiresAuth:     endpoint.RequiresAuth,
			RequiredPerms:    endpoint.RequiredPerms,
			RequireOwnership: endpoint.RequireOwnership,
			RateLimit:        positive(endpoint.RateLimit),
			BodyLimit:        positive(endpoint.BodyLimit),
			Timeout:          positive(endpoint.Timeout),
			ServiceCallers:   endpoint.ServiceCallers,
			ServiceStep:      output.HasServiceCallers,
			Manual:           endpoint.ManualRouteRegistration,
		}
		if info.WebhookProvider != "" {
			webhookProviders[info.WebhookProvider] = struct{}{}
		}
		output.HasAuth = output.HasAuth || info.RequiresAuth
		output.HasControls = output.HasControls || info.HasControls()
		if endpoint.ManualRouteRegistration {
			output.ManualEndpoints = append(output.ManualEndpoints, info)
			continue
		}
		// A manual operation's route is the service's, which decodes its
		// own arguments.
		if err := b.params(endpoint, &info); err != nil {
			return nil, err
		}
		namespaceSet[ns] = struct{}{}
		output.Endpoints = append(output.Endpoints, info)
	}

	output.Namespaces = rustapigen.SortedNamespaces(namespaceSet)
	for provider := range webhookProviders {
		output.WebhookProviders = append(output.WebhookProviders, provider)
	}
	sort.Strings(output.WebhookProviders)
	sortEndpoints(output.Endpoints)
	sortEndpoints(output.ManualEndpoints)
	output.Patterns = b.patterns
	output.DependencyCrates = b.dependencyCrates()

	return output, nil
}

// positive is the value of a directive, or 0 without one or for a value
// below 1.
func positive(value *int) int {
	if value == nil || *value < 1 {
		return 0
	}
	return *value
}

// sortEndpoints orders endpoints by namespace, then path, then method.
func sortEndpoints(endpoints []EndpointInfo) {
	sort.Slice(endpoints, func(i, j int) bool {
		if endpoints[i].Namespace == endpoints[j].Namespace {
			if endpoints[i].Path == endpoints[j].Path {
				return endpoints[i].Method < endpoints[j].Method
			}
			return endpoints[i].Path < endpoints[j].Path
		}
		return endpoints[i].Namespace < endpoints[j].Namespace
	})
}

// WriteAPI writes generated Rust REST API module files.
func WriteAPI(output *APIOutput, outputDir string) error {
	if err := os.MkdirAll(filepath.Join(outputDir, "src"), 0o755); err != nil {
		return fmt.Errorf("create output dir %s: %w", outputDir, err)
	}

	files := []struct {
		templateName string
		outputName   string
	}{
		{templateName: "cargo.tmpl", outputName: "Cargo.toml"},
		{templateName: "lib.tmpl", outputName: filepath.Join("src", "lib.rs")},
		{templateName: "interfaces.tmpl", outputName: filepath.Join("src", "interfaces.rs")},
		{templateName: "router.tmpl", outputName: filepath.Join("src", "router.rs")},
		{templateName: "openapi.tmpl", outputName: filepath.Join("src", "openapi.rs")},
		{templateName: "operations.tmpl", outputName: filepath.Join("src", "operations.rs")},
	}

	for _, file := range files {
		if err := generateFile(file.templateName, filepath.Join(outputDir, file.outputName), output); err != nil {
			return fmt.Errorf("generate %s: %w", file.outputName, err)
		}
	}

	// The document every server's build writes; src/openapi.rs embeds it.
	document := output.OpenAPIJSON
	if document == "" {
		document = "{}"
	}
	if !json.Valid([]byte(document)) {
		return fmt.Errorf("openapi.json for %s is not valid JSON", output.SchemaName)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "openapi.json"), []byte(document), 0o644); err != nil {
		return fmt.Errorf("write openapi.json: %w", err)
	}
	if output.PermissionCatalogJSON != "" {
		if err := os.WriteFile(filepath.Join(outputDir, permcatalog.FileName), []byte(output.PermissionCatalogJSON), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", permcatalog.FileName, err)
		}
	}

	return nil
}

// WriteScaffolds writes implementation scaffold files and skips existing
// files: a mod.rs that declares one module per namespace, and in each
// namespace's directory (its snake_case name, a Rust module name) a mod.rs,
// implementation.rs with the struct and its one impl of the namespace
// trait, and a file per operation with the function that impl calls. The
// files name the generated crate, not crate::, since they belong to the
// service's own crate.
func WriteScaffolds(output *APIOutput, scaffoldsDir string) (*codegen.ScaffoldResult, error) {
	moduleOf := func(namespace string) string { return rustutil.ToSnakeCase(namespace) }
	namespaceByModule := make(map[string]string, len(output.Namespaces))
	modules := make([]string, 0, len(output.Namespaces))
	for _, namespace := range output.Namespaces {
		namespaceByModule[moduleOf(namespace)] = namespace
		modules = append(modules, moduleOf(namespace))
	}
	namespaceData := func(module string) NamespaceOutput {
		namespace := namespaceByModule[module]
		data := NamespaceOutput{
			SchemaName:        output.SchemaName,
			Namespace:         namespace,
			CrateName:         output.CrateName,
			CrateIdent:        strings.ReplaceAll(output.CrateName, "-", "_"),
			RuntimeCrateIdent: output.RuntimeCrateIdent,
		}
		for _, endpoint := range output.Endpoints {
			if endpoint.Namespace == namespace {
				data.Endpoints = append(data.Endpoints, endpoint)
			}
		}
		return data
	}
	result, err := codegen.WriteScaffolds(codegen.ScaffoldConfig[EndpointInfo]{
		ScaffoldsDir: scaffoldsDir,
		ReadmeData:   output,
		Namespaces:   modules,
		Endpoints:    output.Endpoints,
		EndpointNamespace: func(endpoint EndpointInfo) string {
			return moduleOf(endpoint.Namespace)
		},
		NamespaceData: func(module string) any {
			return namespaceData(module)
		},
		EndpointData: func(namespace string, endpoint EndpointInfo) any {
			return EndpointOutput{
				SchemaName:        output.SchemaName,
				Namespace:         namespace,
				CrateName:         output.CrateName,
				CrateIdent:        strings.ReplaceAll(output.CrateName, "-", "_"),
				RuntimeCrateIdent: output.RuntimeCrateIdent,
				Endpoint:          endpoint,
			}
		},
		EndpointFileName: func(endpoint EndpointInfo) string {
			return endpoint.FunctionName + ".rs"
		},
		ImplFileName: "implementation.rs",
		GenerateFile: generateFile,
	})
	if err != nil {
		return nil, err
	}
	record := func(generated bool, path string) {
		if generated {
			result.Generated = append(result.Generated, path)
		} else {
			result.Skipped = append(result.Skipped, path)
		}
	}
	rootPath := filepath.Join(scaffoldsDir, "mod.rs")
	generated, err := codegen.GenerateFileIfNotExists(generateFile, "scaffold-root.tmpl", rootPath, output)
	if err != nil {
		return nil, fmt.Errorf("generate scaffold %s: %w", rootPath, err)
	}
	record(generated, rootPath)
	for _, module := range modules {
		modPath := filepath.Join(scaffoldsDir, module, "mod.rs")
		generated, err := codegen.GenerateFileIfNotExists(generateFile, "scaffold-mod.tmpl", modPath, namespaceData(module))
		if err != nil {
			return nil, fmt.Errorf("generate scaffold %s: %w", modPath, err)
		}
		record(generated, modPath)
	}
	return result, nil
}

func generateFile(templateName, outputPath string, data any) error {
	return codegen.GenerateFile(
		codegen.NewFileConfig(templatesFS, templateName, outputPath, data, templateFuncs()),
	)
}

func templateFuncs() template.FuncMap {
	return rustapigen.TemplateFuncs(template.FuncMap{
		"isGetMethod": func(m string) bool { return strings.EqualFold(m, "get") },
		"join":        strings.Join,
		"toUpper":     strings.ToUpper,
		"rustString":  rustString,
	})
}

// rustString renders a Rust string literal.
func rustString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s) + `"`
}

// SetReplacePaths computes dependency path fields for generated Cargo.toml.
// Unlike the Go targets, the http-runtime path dependency is mandatory: there
// is no published crate for Cargo to fall back on, so an unset [paths]
// http_runtime_rust is an error rather than a no-op.
func SetReplacePaths(output *APIOutput, paths naming.LocalPaths, outputDir string) error {
	if paths.HTTPRuntimeRust == "" || outputDir == "" {
		return fmt.Errorf("rustrestgen: [paths] http_runtime_rust and an output dir are required to locate the http-runtime crate")
	}
	rel, err := naming.RelPath(outputDir, paths.HTTPRuntimeRust)
	if err != nil {
		return fmt.Errorf("http runtime crate path: %w", err)
	}
	output.RuntimeDepPath = rel
	return nil
}

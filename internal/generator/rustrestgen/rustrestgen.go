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
	"github.com/parable-work/superschematic/internal/generator/rustapigen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
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
	HasInput     bool
	InputType    string
	OutputType   string
	PathParams   []string
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
}

// HasControls reports whether the route has a traffic control or needs a
// caller: build_router applies RouteControls to it.
func (e EndpointInfo) HasControls() bool {
	return e.RequiresAuth || e.RateLimit > 0 || e.BodyLimit > 0 || e.Timeout > 0
}

// ControlCalls are the RouteControls builder calls of the route, in the
// order a request meets them: the rate limit, the body limit, the
// permission check, then the timeout around the handler. authenticator is
// the Rust expression of the Arc<dyn Authenticator> the check uses.
func (e EndpointInfo) ControlCalls(authenticator string) []string {
	var calls []string
	if e.RateLimit > 0 {
		calls = append(calls, fmt.Sprintf(".rate_limit(%d)", e.RateLimit))
	}
	if e.BodyLimit > 0 {
		calls = append(calls, fmt.Sprintf(".body_limit_megabytes(%d)", e.BodyLimit))
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
	// OpenAPIJSON is the service's OpenAPI document as apigen builds it
	// for every server: written to openapi.json, embedded in src/openapi.rs
	// and served at GET /api/openapi.json.
	OpenAPIJSON string
	// HasEnvConfig is true when the build writes src/config.rs, the env
	// loader of the service's @envVars type; lib.rs then declares it.
	HasEnvConfig bool
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

// EndpointOutput contains data for generating endpoint scaffold files.
type EndpointOutput struct {
	SchemaName        string
	Namespace         string
	CrateName         string
	RuntimeCrateIdent string
	Endpoint          EndpointInfo
}

// Options configures Rust REST API generation.
type Options struct {
	SchemaName string
	TypesCrate string
	TypesDir   string
	OutputDir  string
	Naming     naming.Naming
	Clock      codegen.Clock
}

// Generate produces Rust REST API metadata for a schema. Handlers take
// the request body and return the response as serde_json::Value, so a body
// argument or response that is an array of arrays (T[][]) passes through as
// nested JSON arrays; the implementation decodes it.
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
// The endpoints and the OpenAPI document come from api, the apigen output
// generator.Run builds once for the Go and TypeScript servers and every
// SDK, with the service's dependencies, naming, OpenAPI and tool hooks and
// its authDb's auth model (D38). A nil api, or one without endpoints, is
// no server.
func Generate(api *apigen.APIOutput, opts Options) (*APIOutput, error) {
	if api == nil || len(api.Endpoints) == 0 {
		return nil, nil
	}

	output := &APIOutput{
		APIOutputBase: rustapigen.NewBase(rustapigen.BaseOptions{
			SchemaName: opts.SchemaName,
			TypesCrate: opts.TypesCrate,
			TypesDir:   opts.TypesDir,
			OutputDir:  opts.OutputDir,
			Naming:     opts.Naming,
			Clock:      opts.Clock,
		}),
		Endpoints:   make([]EndpointInfo, 0, len(api.Endpoints)),
		OpenAPIJSON: api.OpenAPISpecRaw,
	}

	namespaceSet := make(map[string]struct{})
	webhookProviders := make(map[string]struct{})
	for _, endpoint := range api.Endpoints {
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

		pathParams := make([]string, 0, len(endpoint.PathParams))
		for _, param := range endpoint.PathParams {
			pathParams = append(pathParams, param.Name)
		}

		info := EndpointInfo{
			Name:         endpoint.Name,
			Namespace:    ns,
			FunctionName: fnName,
			HandlerName:  rustutil.ToPascalCase(ns) + rustutil.ToPascalCase(fnName),
			// apigen {param} placeholders pass through: axum 0.8 uses
			// {param} path captures natively (the 0.7-era :param panics).
			Path:        endpoint.Path,
			Method:      strings.ToLower(endpoint.Method),
			Description: endpoint.Description,
			HasInput:    endpoint.HasInput || len(endpoint.ScalarArgs) > 0,
			InputType:   endpoint.InputType,
			OutputType:  endpoint.OutputType,
			PathParams:  pathParams,

			WebhookProvider: endpoint.WebhookHMACProvider,

			RequiresAuth:     endpoint.RequiresAuth,
			RequiredPerms:    endpoint.RequiredPerms,
			RequireOwnership: endpoint.RequireOwnership,
			RateLimit:        positive(endpoint.RateLimit),
			BodyLimit:        positive(endpoint.BodyLimit),
			Timeout:          positive(endpoint.Timeout),
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

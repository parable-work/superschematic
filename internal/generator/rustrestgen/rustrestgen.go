// Package rustrestgen generates Rust Axum REST API server crates from
// API-kind schemas.
package rustrestgen

import (
	"embed"
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
	HasInput     bool
	InputType    string
	OutputType   string
	PathParams   []string
	// WebhookProvider is the @hmacVerified provider, or empty. build_router
	// wraps the route in webhook_verified with that provider's verifier.
	WebhookProvider string
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
}

// NamespaceOutput contains data for generating namespace scaffold files.
type NamespaceOutput struct {
	SchemaName        string
	Namespace         string
	CrateName         string
	RuntimeCrateIdent string
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
	SchemaName     string
	IsPublic       bool
	UpstreamSchema string
	UpstreamIR     *ir.Schema
	TypesCrate     string
	TypesDir       string
	OutputDir      string
	Naming         naming.Naming
	// AuthProvider is the auth provider apigen derives the endpoint auth
	// data with. Required.
	AuthProvider apigen.AuthProvider
	Clock        codegen.Clock
}

// Generate produces Rust REST API metadata from an IR schema. Handlers take
// the request body and return the response as serde_json::Value, so a body
// argument or response that is an array of arrays (T[][]) passes through as
// nested JSON arrays; the implementation decodes it.
//
// An operation declared @manualRouteRegistration is left to the service, as
// the Go server leaves it out of RegisterRoutes and the TypeScript server
// out of its implementation interfaces: the router does not mount it, its
// namespace trait has no method for it, and it gets no scaffold. The service
// adds its route to the router build_router returns. The router has no step
// that decrypts a request body, so an encrypted operation that is not
// @manualRouteRegistration is refused.
//
// An @hmacVerified operation's route runs its provider's WebhookVerifier
// before the handler, as the Go router runs the provider's WebhookVerifier
// before its other middleware, and build_router panics when
// Implementations.webhook_verifiers lacks a provider's verifier.
func Generate(schema *ir.Schema, opts Options) (*APIOutput, error) {
	generated, err := rustapigen.Generate(schema, rustapigen.Options{
		SchemaName:     opts.SchemaName,
		IsPublic:       opts.IsPublic,
		UpstreamSchema: opts.UpstreamSchema,
		UpstreamIR:     opts.UpstreamIR,
		TypesCrate:     opts.TypesCrate,
		TypesDir:       opts.TypesDir,
		OutputDir:      opts.OutputDir,
		Naming:         opts.Naming,
		AuthProvider:   opts.AuthProvider,
		Clock:          opts.Clock,
	})
	if err != nil {
		return nil, err
	}
	if generated == nil {
		return nil, nil
	}

	output := &APIOutput{
		APIOutputBase: generated.Base,
		Endpoints:     make([]EndpointInfo, 0, len(generated.Endpoints)),
	}

	namespaceSet := make(map[string]struct{})
	webhookProviders := make(map[string]struct{})
	for _, endpoint := range generated.Endpoints {
		// The Go router decrypts an encrypted operation's body with the
		// configured PayloadDecryptor before it parses it. The Rust router
		// has no such step and would hand the envelope to the implementation
		// as the body. An operation declared @manualRouteRegistration is not
		// mounted: the service's own route receives the envelope and
		// decrypts it.
		if endpoint.Encrypted && !endpoint.ManualRouteRegistration {
			return nil, fmt.Errorf("rustrestgen: operation %s.%s is encrypted (an Encrypted operation set, @encrypted, or an EncryptedField<T> result or argument); the Rust router has no decryption step, declare it @manualRouteRegistration and add its route in the service, which decrypts the payload", endpoint.Namespace, endpoint.Name)
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
		}
		if info.WebhookProvider != "" {
			webhookProviders[info.WebhookProvider] = struct{}{}
		}
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
	}

	for _, file := range files {
		if err := generateFile(file.templateName, filepath.Join(outputDir, file.outputName), output); err != nil {
			return fmt.Errorf("generate %s: %w", file.outputName, err)
		}
	}

	return nil
}

// WriteScaffolds writes implementation scaffold files and skips existing files.
func WriteScaffolds(output *APIOutput, scaffoldsDir string) (*codegen.ScaffoldResult, error) {
	return codegen.WriteScaffolds(codegen.ScaffoldConfig[EndpointInfo]{
		ScaffoldsDir: scaffoldsDir,
		ReadmeData:   output,
		Namespaces:   output.Namespaces,
		Endpoints:    output.Endpoints,
		EndpointNamespace: func(endpoint EndpointInfo) string {
			return endpoint.Namespace
		},
		NamespaceData: func(namespace string) any {
			return NamespaceOutput{
				SchemaName:        output.SchemaName,
				Namespace:         namespace,
				CrateName:         output.CrateName,
				RuntimeCrateIdent: output.RuntimeCrateIdent,
			}
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

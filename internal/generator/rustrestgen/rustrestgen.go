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
}

// APIOutput contains generated Rust REST API metadata.
type APIOutput struct {
	rustapigen.APIOutputBase
	Endpoints []EndpointInfo
}

// NamespaceOutput contains data for generating namespace scaffold files.
type NamespaceOutput struct {
	SchemaName string
	Namespace  string
	CrateName  string
}

// EndpointOutput contains data for generating endpoint scaffold files.
type EndpointOutput struct {
	SchemaName string
	Namespace  string
	CrateName  string
	Endpoint   EndpointInfo
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

// Generate produces Rust REST API metadata from an IR schema.
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
	for _, endpoint := range generated.Endpoints {
		ns := endpoint.Namespace
		if ns == "" {
			ns = "root"
		}
		namespaceSet[ns] = struct{}{}

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

		output.Endpoints = append(output.Endpoints, EndpointInfo{
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
		})
	}

	output.Namespaces = rustapigen.SortedNamespaces(namespaceSet)
	sort.Slice(output.Endpoints, func(i, j int) bool {
		if output.Endpoints[i].Namespace == output.Endpoints[j].Namespace {
			if output.Endpoints[i].Path == output.Endpoints[j].Path {
				return output.Endpoints[i].Method < output.Endpoints[j].Method
			}
			return output.Endpoints[i].Path < output.Endpoints[j].Path
		}
		return output.Endpoints[i].Namespace < output.Endpoints[j].Namespace
	})

	return output, nil
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
				SchemaName: output.SchemaName,
				Namespace:  namespace,
				CrateName:  output.CrateName,
			}
		},
		EndpointData: func(namespace string, endpoint EndpointInfo) any {
			return EndpointOutput{
				SchemaName: output.SchemaName,
				Namespace:  namespace,
				CrateName:  output.CrateName,
				Endpoint:   endpoint,
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
	})
}

// SetReplacePaths computes dependency path fields for generated Cargo.toml.
// Unlike the Go targets, the http-runtime path dependency is mandatory: there
// is no published crate for Cargo to fall back on, so empty inputs are an
// error rather than a no-op.
func SetReplacePaths(output *APIOutput, scalarLibPath, outputDir string) error {
	if scalarLibPath == "" || outputDir == "" {
		return fmt.Errorf("rustrestgen: scalar-lib path and output dir are required to locate the http-runtime crate")
	}

	absScalarLib, err := filepath.Abs(scalarLibPath)
	if err != nil {
		return fmt.Errorf("resolve scalar-lib absolute path: %w", err)
	}
	absOutputDir, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve output dir absolute path: %w", err)
	}

	rel, err := filepath.Rel(absOutputDir, filepath.Join(absScalarLib, "..", "psgen", "http-runtime", "rust"))
	if err != nil {
		return fmt.Errorf("compute relative http-runtime path: %w", err)
	}
	output.RuntimeDepPath = filepath.ToSlash(rel)
	return nil
}

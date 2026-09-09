// Package sessionauth is the core auth provider for the superschematic api generator:
// bearer sessions over an upstream Session table, an optional principal
// (User) table, plain-string permissions, and no tenancy. Its generated code
// depends only on the generic http-runtime session package
// (docs/extension-model.md section 8.2).
package sessionauth

import (
	"embed"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// Name is the registry key of the core provider.
const Name = "session"

// Provider implements apigen.AuthProvider for the generic session model.
type Provider struct{}

var _ apigen.AuthProvider = Provider{}

// Name implements apigen.AuthProvider.
func (Provider) Name() string { return Name }

// Analyze implements apigen.AuthProvider: the core session and principal
// table probes, nothing else.
func (Provider) Analyze(_, upstream *ir.Schema) (apigen.AuthModel, error) {
	return apigen.AnalyzeSessionStores(upstream), nil
}

// Endpoint implements apigen.AuthProvider. The session model hoists no
// scope parameter, so every endpoint keeps IsScopedEndpoint false.
func (Provider) Endpoint(*ir.FieldDef, *ir.OperationSet, *apigen.EndpointInfo) error {
	return nil
}

// Templates implements apigen.AuthProvider.
func (Provider) Templates() embed.FS { return templatesFS }

// Funcs implements apigen.AuthProvider.
func (Provider) Funcs() template.FuncMap { return nil }

// Files implements apigen.AuthProvider: the session provider adds no files.
func (Provider) Files(*apigen.APIOutput) []codegen.ConditionalFile { return nil }

// OpenAPIParameters implements apigen.AuthProvider: no extra headers.
func (Provider) OpenAPIParameters(*apigen.APIOutput) []map[string]any { return nil }

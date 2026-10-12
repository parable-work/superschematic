// Package sessionauth is the core auth provider for the superschematic api
// generator: the core user model (D50). An upstream DB schema with a User
// table, found by its trait, makes the generated server authenticate with
// the identity runtime (runtime/http/go/identity), which signs users in,
// resolves their sessions and roles, and serves the user model's routes;
// the core templates wire it in (apigen.AuthModel.Identity). Permissions are
// plain strings and there is no tenancy. Its own snippets depend only on
// the generic http-runtime session package (docs/extension-model.md
// section 8.2).
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

// Analyze implements apigen.AuthProvider: the user model the upstream
// schema declares by its traits, nothing else.
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

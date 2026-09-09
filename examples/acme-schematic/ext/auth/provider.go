// Package auth is the acme auth provider for the superschematic api
// generator: callers identify themselves with an X-API-Key header instead of
// a bearer session. It is built on the generic session runtime
// (runtime/http/go/session): the generated middleware resolves the key to a
// principal id and puts it on the context with the runtime's own helpers, so
// RequireAuth and RequirePermissions from that package work unchanged.
//
// It implements registry.AuthProvider against the public registry package
// only, which is what any out-of-tree provider looks like. superschematic.toml
// selects it with auth_provider = "apikey"; the core-only binary refuses that
// file because it registers only "session".
package auth

import (
	"embed"
	"text/template"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// Name is the registry key superschematic.toml's auth_provider selects.
const Name = "apikey"

// Header is the request header the generated middleware reads.
const Header = "X-API-Key"

// Provider implements registry.AuthProvider.
type Provider struct{}

var _ registry.AuthProvider = Provider{}

// Stores is the provider's half of the auth model, AuthModel.Extra. The
// generated key store adapter is only emitted when the upstream DB schema
// declares a table of the shape it queries, so the generated middleware
// always compiles against the upstream ORM.
type Stores struct {
	// HasKeyStore reports an upstream ApiKey(id, secret, user) table.
	HasKeyStore bool
}

// Name implements registry.AuthProvider.
func (Provider) Name() string { return Name }

// Analyze implements registry.AuthProvider: the core principal probe plus
// the ApiKey table.
func (Provider) Analyze(_, upstream *ir.Schema) (registry.AuthModel, error) {
	model := registry.AnalyzeSessionStores(upstream)
	model.Extra = Stores{HasKeyStore: registry.HasTable(upstream, "ApiKey", "id", "secret", "user")}
	return model, nil
}

// Endpoint implements registry.AuthProvider. API keys scope nothing, so
// every endpoint keeps IsScopedEndpoint false.
func (Provider) Endpoint(*ir.FieldDef, *ir.OperationSet, *registry.EndpointInfo) error {
	return nil
}

// Templates implements registry.AuthProvider.
func (Provider) Templates() embed.FS { return templatesFS }

// Funcs implements registry.AuthProvider: the snippets call no functions of
// their own.
func (Provider) Funcs() template.FuncMap { return nil }

// Files implements registry.AuthProvider: no whole files, every addition is
// a snippet at a core hook point.
func (Provider) Files(*registry.APIOutput) []registry.ConditionalFile { return nil }

// OpenAPIParameters implements registry.AuthProvider: the X-API-Key header
// on every operation of a public API.
func (Provider) OpenAPIParameters(output *registry.APIOutput) []map[string]any {
	if !output.IsPublic {
		return nil
	}
	return []map[string]any{
		{
			"name":        Header,
			"in":          "header",
			"required":    false,
			"description": "API key of the caller. Protected routes reject a request without a valid key.",
			"schema":      map[string]any{"type": "string"},
		},
	}
}

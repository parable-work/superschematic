// Package registry is the public face of the extension seam. A downstream
// module implements Extension against these names;
// the engine keeps its implementation in internal/registry, which Go's
// internal rule hides from other modules. Every identifier here is an alias
// or a forwarding function, so the two packages cannot drift.
//
// Design: docs/extension-model.md sections 3 and 10.
package registry

import (
	ir "github.com/parable-work/superschematic/ir"

	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/goutil"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

type (
	Extension       = registry.Extension
	Registry        = registry.Registry
	KindSpec        = registry.KindSpec
	DecoratorSpec   = registry.DecoratorSpec
	DecoratorTarget = registry.DecoratorTarget
	DocumentSpec    = registry.DocumentSpec
	GeneratorSpec   = registry.GeneratorSpec
	Node            = registry.Node
	Site            = registry.Site
	ArgError        = registry.ArgError
	LoadContext     = registry.LoadContext
	GenerateContext = registry.GenerateContext
	BuildAllHook    = registry.BuildAllHook
	BuildAllContext = registry.BuildAllContext
	BuildAllService = registry.BuildAllService
	// CheckSpec is a verification rule over schemas of any kind, reporting
	// through VerifyReporter.
	CheckSpec = registry.CheckSpec
	// OpenAPIHook edits the OpenAPI document the api generator builds.
	OpenAPIHook   = registry.OpenAPIHook
	ScalarCatalog = registry.ScalarCatalog
	// SchemaCatalogEntry is one discovered service's identity facts, the
	// value type of LoadContext.Catalog.
	SchemaCatalogEntry = registry.SchemaCatalogEntry

	// EnvConfig is a schema's resolved @envVars contract: the fields an
	// environment-driven configuration exposes, with their types, defaults
	// and @secret marks. GenerateContext.EnvConfig returns one; EnvConfigOf
	// builds one outside a run.
	EnvConfig      = envgen.ConfigOutput
	EnvConfigField = envgen.ConfigField

	// The auth provider seam (docs/extension-model.md section 8). An
	// extension registers one with Registry.RegisterAuthProvider; the api
	// generator renders with the one Naming.AuthProvider names.
	AuthProvider = registry.AuthProvider
	AuthModel    = registry.AuthModel

	// What an AuthProvider's methods receive and return: the per-endpoint
	// record Endpoint fills, the module-level output Files and
	// OpenAPIParameters read, the parameter record inside EndpointInfo, and
	// the whole-file entry Files returns.
	EndpointInfo    = apigen.EndpointInfo
	APIOutput       = apigen.APIOutput
	EndpointParam   = apigen.Param
	ConditionalFile = codegen.ConditionalFile

	Naming             = registry.Naming
	Options            = registry.Options
	Result             = registry.Result
	Outputs            = registry.Outputs
	TargetOutputConfig = registry.TargetOutputConfig
	APIOutputConfig    = registry.APIOutputConfig
)

const (
	TargetType         = registry.TargetType
	TargetField        = registry.TargetField
	TargetOperationSet = registry.TargetOperationSet
	TargetOperation    = registry.TargetOperation

	LangGo         = registry.LangGo
	LangTypeScript = registry.LangTypeScript
	LangPython     = registry.LangPython
	LangRust       = registry.LangRust

	APILanguageGo   = registry.APILanguageGo
	APILanguageRust = registry.APILanguageRust
	APIProtocolREST = registry.APIProtocolREST
)

// New returns a registry with the core kinds and decorators registered; see
// internal/registry.New. Core generators are not included: call RegisterCore
// before Use and Finalize (the assembly sequence in docs/extension-model.md
// section 3.2).
func New(n Naming) *Registry { return registry.New(n) }

// RegisterCore adds the core generators and build-all hooks whose closures
// live in internal/generator; see internal/generator.RegisterCore.
func RegisterCore(reg *Registry) error { return generator.RegisterCore(reg) }

// Assemble runs the whole sequence: New, RegisterCore, Use(exts...), Finalize.
// It is what a superschematic binary calls once per process.
func Assemble(n Naming, exts ...Extension) (*Registry, error) {
	reg := registry.New(n)
	if err := generator.RegisterCore(reg); err != nil {
		return nil, err
	}
	if err := reg.Use(exts...); err != nil {
		return nil, err
	}
	if err := reg.Finalize(); err != nil {
		return nil, err
	}
	return reg, nil
}

// DefaultNaming is the core's naming, the value used when no
// superschematic.toml is found; see internal/generator/naming.Default.
func DefaultNaming() Naming { return naming.Default() }

// LoadNaming reads <schemasRoot>/superschematic.toml, falling back to
// DefaultNaming when the file is absent; see internal/generator/naming.Load.
func LoadNaming(schemasRoot string) (Naming, error) { return naming.Load(schemasRoot) }

// ParseNaming decodes superschematic.toml bytes; name labels errors.
func ParseNaming(data []byte, name string) (Naming, error) { return naming.Parse(data, name) }

// DecodeArgs decodes a decorator's single argument into v; see
// internal/registry.DecodeArgs.
func DecodeArgs(args []any, v any) error { return registry.DecodeArgs(args, v) }

// ArgErrorf reports a bad decorator argument by index; see
// internal/registry.ArgErrorf.
func ArgErrorf(index int, format string, a ...any) error {
	return registry.ArgErrorf(index, format, a...)
}

// ParseOutputs parses a schema config's outputs block against reg; see
// internal/registry.ParseOutputs.
func ParseOutputs(raw map[string]any, reg *Registry) (*Outputs, error) {
	return registry.ParseOutputs(raw, reg)
}

// DecodeOutput decodes one outputs section into v; see
// internal/registry.DecodeOutput.
func DecodeOutput(o *Outputs, key string, v any) error { return registry.DecodeOutput(o, key, v) }

// CoreScalars is the catalog of the scalar package the engine links, the one
// Registry.Scalars returns when no extension called RegisterScalars; see
// internal/registry.CoreScalars.
func CoreScalars() ScalarCatalog { return registry.CoreScalars() }

// ScalarCatalogOf wraps a scalar metadata map as a ScalarCatalog; see
// internal/registry.ScalarCatalogOf.
func ScalarCatalogOf(rows map[string]*scalars.ScalarMetadata) ScalarCatalog {
	return registry.ScalarCatalogOf(rows)
}

// EnvConfigOf resolves schema's @envVars contract under the default naming
// with no dependency schemas, for extensions and tests that hold an IR but
// no run; see internal/generator/envgen.Generate. (nil, nil) when the schema
// declares no @envVars type.
func EnvConfigOf(schema *ir.Schema, schemaName string) (*EnvConfig, error) {
	return envgen.Generate(schema, schemaName)
}

// GoPublicIdentifier converts an arbitrary handle into an exported Go
// identifier, the way the core generators name generated constants; see
// internal/generator/goutil.GoPublicIdentifier.
func GoPublicIdentifier(value string) string { return goutil.GoPublicIdentifier(value) }

// AuthSnippets names the hook points an AuthProvider's templates must
// define; see internal/generator/apigen.AuthSnippets.
var AuthSnippets = apigen.AuthSnippets

// HasTable reports whether upstream declares a DB table named name carrying
// every field in fields; see internal/generator/apigen.HasTable.
func HasTable(upstream *ir.Schema, name string, fields ...string) bool {
	return apigen.HasTable(upstream, name, fields...)
}

// AnalyzeSessionStores is the core half of AuthProvider.Analyze; see
// internal/generator/apigen.AnalyzeSessionStores.
func AnalyzeSessionStores(upstream *ir.Schema) AuthModel {
	return apigen.AnalyzeSessionStores(upstream)
}

// AuthSnippetFunc parses provider's templates and returns the renderer the
// core templates call through authSnippet, or an error naming the first
// missing snippet. Providers test themselves with it; see
// internal/generator/apigen.AuthSnippetFunc.
func AuthSnippetFunc(provider AuthProvider) (func(name string, data any) (string, error), error) {
	return apigen.AuthSnippetFunc(provider)
}

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
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
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
	// ClassRef is a class named as a value in a decorator argument:
	// {"class": "Accessory"} in every form; see ir.ClassRef.
	ClassRef        = registry.ClassRef
	LoadContext     = registry.LoadContext
	GenerateContext = registry.GenerateContext
	BuildAllHook    = registry.BuildAllHook
	BuildAllContext = registry.BuildAllContext
	BuildAllService = registry.BuildAllService
	// CheckSpec is a verification rule over schemas of any kind, reporting
	// through VerifyReporter.
	CheckSpec = registry.CheckSpec
	// OpenAPIHook edits the OpenAPI document the api generator builds.
	OpenAPIHook = registry.OpenAPIHook
	// ToolHook edits the vendor keys of an API's SDK tool documents and the
	// resolved @mcp records of its operations. It edits a ToolSet: the
	// ToolKeys (with ToolKeyValue entries) and one Tool per operation.
	ToolHook     = registry.ToolHook
	ToolSet      = apigen.ToolSet
	Tool         = apigen.Tool
	ToolKeys     = apigen.ToolKeys
	ToolKeyValue = apigen.ToolKeyValue
	// ToolInvocationPolicy is the key, values and default of a visible
	// MCP tool's invocation policy; RegisterToolInvocationPolicy replaces
	// the core's.
	ToolInvocationPolicy = registry.ToolInvocationPolicy
	ScalarCatalog        = registry.ScalarCatalog
	// UploadCatalog is a ScalarCatalog that declares its file-upload
	// scalars; ScalarUpload is the metadata it declares for one.
	UploadCatalog = registry.UploadCatalog
	ScalarUpload  = registry.ScalarUpload
	// RawBodyCheckCatalog is a ScalarCatalog that declares raw-body checks
	// for some of its scalars; ScalarRawBodyCheck is the check it declares
	// for one: the Go function a generated route calls on the raw JSON of
	// its request body before decoding it.
	RawBodyCheckCatalog = registry.RawBodyCheckCatalog
	ScalarRawBodyCheck  = registry.ScalarRawBodyCheck
	// NpmPackageCatalog is a ScalarCatalog that names the npm package a
	// TypeScript schema imports each of some of its namespaces from.
	NpmPackageCatalog = registry.NpmPackageCatalog
	// SchemaCatalogEntry is one discovered service's identity facts, the
	// value type of LoadContext.Catalog.
	SchemaCatalogEntry = registry.SchemaCatalogEntry

	// BehaviorSpec registers a behavior from its JSON declaration, whose
	// shape is BehaviorDeclaration with its BehaviorField,
	// BehaviorOperation and BehaviorVeto entries. Behavior is a
	// registered one, as Registry.Behavior returns it.
	BehaviorSpec        = registry.BehaviorSpec
	BehaviorDeclaration = registry.BehaviorDeclaration
	BehaviorField       = registry.BehaviorField
	BehaviorOperation   = registry.BehaviorOperation
	BehaviorVeto        = registry.BehaviorVeto
	Behavior            = registry.Behavior

	// EnvConfig is a schema's resolved @envVars contract: the fields an
	// environment-driven configuration exposes, with their types, defaults
	// and @secret marks. GenerateContext.EnvConfig returns one; EnvConfigOf
	// builds one outside a run.
	EnvConfig      = envgen.ConfigOutput
	EnvConfigField = envgen.ConfigField

	// RustAPI is the Rust REST API crate the api generator writes for a
	// service whose API language is Rust (D39): its crate names, and each
	// operation, mounted (Endpoints), manual (ManualEndpoints) or the user
	// model's, which the identity runtime serves (IdentityEndpoints, D50),
	// as a RustEndpoint with its Args struct, its arguments (RustParam),
	// its input (RustInput), its result type and its auth rules. Identity
	// is set when the crate authenticates with the identity runtime.
	// RustAPIOf returns it.
	RustAPI      = rustrestgen.APIOutput
	RustEndpoint = rustrestgen.EndpointInfo
	RustParam    = rustrestgen.ParamInfo
	RustInput    = rustrestgen.InputInfo

	// The auth provider seam (docs/extension-model.md section 8). An
	// extension registers one with Registry.RegisterAuthProvider; the api
	// generator renders with the one Naming.AuthProvider names.
	AuthProvider = registry.AuthProvider
	AuthModel    = registry.AuthModel
	// UserModel is AuthModel.User: the user model (D50) the upstream
	// schema declares by its User trait, found whatever its table is
	// named.
	UserModel = registry.UserModel

	// What an AuthProvider's methods receive and return: the per-endpoint
	// record Endpoint fills, the module-level output Files and
	// OpenAPIParameters read, the parameter record inside EndpointInfo, and
	// the whole-file entry Files returns.
	EndpointInfo    = apigen.EndpointInfo
	APIOutput       = apigen.APIOutput
	EndpointParam   = apigen.Param
	ConditionalFile = codegen.ConditionalFile

	// The stack model's registrations (docs/stack-model.md, section 6): a
	// platform places a deployable kind on a runtime and lowers it to
	// resources, a connector realizes an edge between two platforms, a
	// target bundles a platform per deployable kind with its values
	// schema, resource type schemas and policy rules, a DNS platform holds
	// a domain's records, and a provisioner applies the resource graph.
	PlatformSpec     = registry.PlatformSpec
	PlatformContext  = registry.PlatformContext
	Lowered          = registry.Lowered
	ConnectorSpec    = registry.ConnectorSpec
	ConnectorContext = registry.ConnectorContext
	Connected        = registry.Connected
	TargetSpec       = registry.TargetSpec
	PolicyRule       = registry.PolicyRule
	DNSPlatformSpec  = registry.DNSPlatformSpec
	DNSContext       = registry.DNSContext
	ProvisionerSpec  = registry.ProvisionerSpec
	Provisioner      = registry.Provisioner
	ProvisionRequest = registry.ProvisionRequest
	StateBackend     = registry.StateBackend
	PlannedChange    = registry.PlannedChange
	StackEnvironment = registry.StackEnvironment

	// The seams the cloud half of the `stack` commands drives
	// (docs/stack-model.md, sections 7.3 and 11): a target's StateStore
	// keeps its provisioner's state backend and each Run's deploy
	// manifest, its SecretStore the values of secrets and platform
	// Credentials, its Bootstrapper prepares a cloud project
	// (BootstrapRequest) and returns the BootstrapValues the core records
	// in the schema (BootstrapResult), its MigrationRunner runs
	// MigrationPlans (MigrationRequest) between the deploy's steps, its
	// ImageBuilder builds a server's or a job's image (BuildRequest)
	// before them, and its JobRunner runs a deployed job once on demand
	// (JobRunRequest, D52).
	Run              = registry.Run
	StateStore       = registry.StateStore
	SecretStore      = registry.SecretStore
	Bootstrapper     = registry.Bootstrapper
	BootstrapRequest = registry.BootstrapRequest
	BootstrapResult  = registry.BootstrapResult
	BootstrapValue   = registry.BootstrapValue
	MigrationRunner  = registry.MigrationRunner
	MigrationRequest = registry.MigrationRequest
	MigrationPlan    = registry.MigrationPlan
	ImageBuilder     = registry.ImageBuilder
	BuildRequest     = registry.BuildRequest
	JobRunner        = registry.JobRunner
	JobRunRequest    = registry.JobRunRequest
	Credential       = registry.Credential

	// A target's SitePublisher puts a site's files and its config for a
	// run where the site's platform serves them (SitePublishRequest, D55).
	SitePublisher      = registry.SitePublisher
	SitePublishRequest = registry.SitePublishRequest

	// The generated CI (docs/stack-model.md, section 11.3, D47): a CI
	// renderer (CIRendererSpec) renders a CIRequest, a stack (CIStack) and
	// its CIEnvironments with the renderer's CIOptions, to CIFiles. A
	// target's CI seam (CIIdentities) gives each environment a CIIdentity
	// per CIRole, a provisioner's spec lists the CLITools a CI job
	// installs, and the request names the release's CIArchive for each
	// platform, which a job that compiles a Go server links.
	CIRendererSpec = registry.CIRendererSpec
	CIRequest      = registry.CIRequest
	CIArchive      = registry.CIArchive
	CIStack        = registry.CIStack
	CIEnvironment  = registry.CIEnvironment
	CIOptions      = registry.CIOptions
	CIFile         = registry.CIFile
	CIIdentities   = registry.CIIdentities
	CIIdentity     = registry.CIIdentity
	CIRole         = registry.CIRole
	CLITool        = registry.CLITool

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

	APILanguageGo         = registry.APILanguageGo
	APILanguageRust       = registry.APILanguageRust
	APILanguageTypeScript = registry.APILanguageTypeScript
	APIProtocolREST       = registry.APIProtocolREST

	// SQLDialectPostgres and SQLDialectSQLite are the SQL dialects a
	// database platform declares.
	SQLDialectPostgres = registry.SQLDialectPostgres
	SQLDialectSQLite   = registry.SQLDialectSQLite

	// CIPlanner and CIDeployer are the roles a CI job signs in as;
	// PackageManagerBun and PackageManagerNPM the package managers a
	// CIStack names; DefaultCIBranch the branch CIOptions default to.
	CIPlanner         = registry.CIPlanner
	CIDeployer        = registry.CIDeployer
	PackageManagerBun = registry.PackageManagerBun
	PackageManagerNPM = registry.PackageManagerNPM
	DefaultCIBranch   = registry.DefaultCIBranch

	// OpenAPIDocsKey is the vendor-extension key an operation's @docs record
	// is written under in the OpenAPI document; an OpenAPIHook renames it.
	OpenAPIDocsKey = apigen.OpenAPIDocsKey

	// DefaultToolScalarKey and DefaultToolGuidanceKey are the vendor keys
	// the SDK tool documents carry unless a ToolHook renames them.
	DefaultToolScalarKey   = apigen.DefaultToolScalarKey
	DefaultToolGuidanceKey = apigen.DefaultToolGuidanceKey

	// DefaultToolInvocationKey, ToolInvocationAuto and ToolInvocationAsk
	// are the core's invocation policy: the key, its values, and (auto)
	// the default.
	DefaultToolInvocationKey = apigen.DefaultToolInvocationKey
	ToolInvocationAuto       = apigen.ToolInvocationAuto
	ToolInvocationAsk        = apigen.ToolInvocationAsk
)

// ErrNoManifest is what a StateStore's ReadManifest wraps for a run that
// was never deployed; ErrSecretNotCreated is what a SecretStore's Set
// wraps when the secret's storage does not exist yet.
var (
	ErrNoManifest       = registry.ErrNoManifest
	ErrSecretNotCreated = registry.ErrSecretNotCreated
)

// DefaultToolInvocationPolicy returns the core's invocation policy:
// invocationPolicy, auto or ask, auto by default.
func DefaultToolInvocationPolicy() ToolInvocationPolicy { return apigen.DefaultToolInvocationPolicy() }

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

// DecodeClassRef decodes a class reference, a whole decorator argument or a
// value inside one, into the class's declared name; see
// internal/registry.DecodeClassRef.
func DecodeClassRef(v any) (string, error) { return registry.DecodeClassRef(v) }

// ClassRefSchema is the JSON Schema of a class reference, for a
// DecoratorSpec's Args to use wherever its argument takes a class; see
// internal/registry.ClassRefSchema.
var ClassRefSchema = registry.ClassRefSchema

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

// ScalarCatalogWithUploads declares file-upload metadata on scalars of
// catalog; see internal/registry.ScalarCatalogWithUploads.
func ScalarCatalogWithUploads(catalog ScalarCatalog, uploads map[string]ScalarUpload) (UploadCatalog, error) {
	return registry.ScalarCatalogWithUploads(catalog, uploads)
}

// ScalarCatalogWithRawBodyChecks declares raw-body checks on scalars of
// catalog; see internal/registry.ScalarCatalogWithRawBodyChecks.
func ScalarCatalogWithRawBodyChecks(catalog ScalarCatalog, checks map[string]ScalarRawBodyCheck) (RawBodyCheckCatalog, error) {
	return registry.ScalarCatalogWithRawBodyChecks(catalog, checks)
}

// ScalarCatalogWithNpmPackages names the npm package that exports each
// namespace of packages, keyed by namespace ({"Acme": "@acme/schema"}); see
// internal/registry.ScalarCatalogWithNpmPackages.
func ScalarCatalogWithNpmPackages(catalog ScalarCatalog, packages map[string]string) (NpmPackageCatalog, error) {
	return registry.ScalarCatalogWithNpmPackages(catalog, packages)
}

// EnvConfigOf resolves schema's @envVars contract under the default naming
// with no dependency schemas, for extensions and tests that hold an IR but
// no run; see internal/generator/envgen.Generate. (nil, nil) when the schema
// declares no @envVars type.
func EnvConfigOf(schema *ir.Schema, schemaName string) (*EnvConfig, error) {
	return envgen.Generate(schema, schemaName)
}

// RustAPIOf is the Rust REST API crate the api generator writes for the
// service c generates, as the generator builds it; (nil, nil) for a schema
// without operations. It does not read the outputs block: an extension
// that writes Rust beside the crate checks that the service's API language
// is Rust. See internal/generator.RustAPIOf.
func RustAPIOf(c GenerateContext) (*RustAPI, error) { return generator.RustAPIOf(c) }

// APIDir is the directory the api generator writes service's server to
// under outputRoot, the Rust crate's directory for a Rust API; see
// internal/generator.APIDir.
func APIDir(outputRoot, service string) string { return generator.APIDir(outputRoot, service) }

// RustIdentifier is name as a snake_case Rust identifier, a keyword
// escaped, or fallback when nothing is left: how the Rust server and SDK
// name an argument's field; see internal/generator/rustutil.Identifier.
func RustIdentifier(name, fallback string) string { return rustutil.Identifier(name, fallback) }

// GoPublicIdentifier converts an arbitrary handle into an exported Go
// identifier, the way the core generators name generated constants; see
// internal/generator/goutil.GoPublicIdentifier.
func GoPublicIdentifier(value string) string { return goutil.GoPublicIdentifier(value) }

// AuthSnippets names the hook points an AuthProvider's templates must
// define; see internal/generator/apigen.AuthSnippets.
var AuthSnippets = apigen.AuthSnippets

// HasTable reports whether upstream declares a DB table named name carrying
// every field in fields, for a store of the provider's own; see
// internal/generator/apigen.HasTable. The user model's tables are found by
// their traits instead (AnalyzeSessionStores).
func HasTable(upstream *ir.Schema, name string, fields ...string) bool {
	return apigen.HasTable(upstream, name, fields...)
}

// AnalyzeSessionStores is the core half of AuthProvider.Analyze: the user
// model the upstream schema declares by its User and UserRole traits (D50),
// which the identity runtime authenticates with; see
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

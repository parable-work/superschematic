package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	validator "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// KindSpec describes one schema kind: what the loader lets a schema of that
// kind author, and which generators Run executes for it.
type KindSpec struct {
	// Name is the string that appears as `kind` in schema.config and as
	// Schema.Kind.
	Name string

	// Extension is the registering extension's Name(); "" for core.
	Extension string

	// StructRole is the ir.Role a plain decorated class gets in this kind:
	// DB tables for DB, embedded structs everywhere else.
	StructRole ir.Role

	// SourceProjectionRole is the role a @source(...) class gets. Zero means
	// @source is not allowed in this kind.
	SourceProjectionRole ir.Role

	// AllowsOperationSets gates classes with methods; only API sets it.
	AllowsOperationSets bool

	// ForbiddenPackages lists authoring packages ("@superschematic/db") a schema of
	// this kind may not import.
	ForbiddenPackages map[string]bool

	// AllowedReferences and DeniedReferences are the cross-kind type
	// reference rules: a positive allowlist, a denylist, or neither
	// (unconstrained). Keys are kind names.
	AllowedReferences map[string]bool
	DeniedReferences  map[string]bool

	// Pipeline is the ordered list of GeneratorSpec.Name values Run executes
	// for this kind. Finalize checks every name is registered. Empty is
	// valid: a grouping kind with no generated outputs leaves it nil.
	Pipeline []string

	// NoSentinel marks a kind whose services are groupings of other
	// services rather than importable members: superschematic writes no
	// src/service.generated.ts for them and the sibling sweep skips them.
	NoSentinel bool

	// ImportsSiblingSentinels marks a kind whose schema files import other
	// services' sentinels as values. `superschematic build` writes every sibling's
	// sentinel before it constructs the program for a service of this kind,
	// so the imports resolve on a fresh checkout.
	ImportsSiblingSentinels bool

	// Verify runs after the core verification checks on an assembled schema
	// of this kind, once per load, in every frontend. It reports through r;
	// an error fails the load. nil means the kind adds no checks of its own.
	Verify func(schema *ir.Schema, r VerifyReporter)
}

// VerifyReporter receives the findings of a KindSpec.Verify or
// CheckSpec.Verify run. file is the schema source path the finding points
// at, or "" for a schema-level finding.
type VerifyReporter interface {
	Errorf(file string, format string, args ...any)
	Warnf(file string, format string, args ...any)
}

// CheckSpec is a verification rule over schemas of any kind, including the
// core kinds an extension cannot attach a KindSpec.Verify to. It is how an
// extension enforces its policy on what core decorators write: a closed set
// of @docs audiences, an icon set for @icon. Checks run after the core
// checks and the kind's own Verify, in registration order, once per load, in
// every frontend.
type CheckSpec struct {
	// Name identifies the check; it must be unique.
	Name string
	// Extension is the registering extension's Name().
	Extension string
	// Kinds restricts the check to schema kinds. nil = every kind.
	Kinds []string
	// Verify inspects the assembled schema and reports through r; an error
	// fails the load.
	Verify func(schema *ir.Schema, r VerifyReporter)
}

// AllowsKind reports whether the check runs on a schema of kind.
func (s CheckSpec) AllowsKind(kind string) bool {
	return len(s.Kinds) == 0 || slices.Contains(s.Kinds, kind)
}

// DecoratorTarget is the AST node a decorator may sit on.
type DecoratorTarget uint8

// Decorator targets, one per walker switch.
const (
	TargetType         DecoratorTarget = iota + 1 // class declaration
	TargetField                                   // property
	TargetOperationSet                            // class with methods
	TargetOperation                               // method
)

// String names the target the way the frontend's "not valid on" diagnostic
// does.
func (t DecoratorTarget) String() string {
	switch t {
	case TargetType:
		return "a type declaration"
	case TargetField:
		return "a field"
	case TargetOperationSet:
		return "an operation set"
	case TargetOperation:
		return "an operation"
	}
	return "an unknown target"
}

// DecoratorSpec describes one decorator for one target. The TS frontend
// resolves a decorator identifier to (declaring package, name) and looks the
// pair up here; the data forms look up the keys under a node's
// `extensions.<ext>` object the same way.
type DecoratorSpec struct {
	// Name without the "@": "secret", "requirePermission".
	Name string
	// Extension is the registering extension's Name(); "" for core. Extension
	// decorators read and write node.Extensions[Extension]; core decorators
	// write the typed IR field.
	Extension string
	// Packages lists the authoring packages the TS frontend accepts the
	// identifier from: {"@superschematic/api"}, {"@acme/schematic"}. Several entries
	// are for names the core packages declare more than once (@superschematic/api and
	// @superschematic/schema both declare source, uiHidden and virtual). Every entry
	// joins the set IsAuthoringPackage accepts.
	Packages []string
	// Target is which AST node the decorator may sit on. The same Name may
	// be registered for several targets.
	Target DecoratorTarget
	// Kinds restricts the decorator to schema kinds. nil = any kind.
	Kinds []string
	// Args is the JSON Schema for the decorator's single argument, as the
	// data forms write it under extensions.<ext>.<name>. nil means the
	// decorator takes no arguments and is encoded as `true`. When set, both
	// frontends validate the argument against it before calling Apply.
	Args json.RawMessage
	// Apply writes the decorator into the IR node. args holds the
	// statically evaluated decorator arguments (string, float64, bool, nil,
	// []any, map[string]any), in order. A nil Apply marks a decorator the
	// frontend interprets itself (role selection, @source linkage, @envVars,
	// @versioned): it still passes the origin, target and kind checks and
	// appears in the JSON Schema, but its arguments are not evaluated.
	// Extensions must set Apply.
	Apply func(node Node, args []any, at Site) error

	// recordsErrors keeps the node when Apply fails instead of dropping it:
	// the walker records the error and goes on to the node's other decorators
	// and checks. Core sets it for the middleware trio on operations, which the
	// walker has always treated that way; extensions cannot set it.
	recordsErrors bool

	// typeArgs is the number of type arguments the decorator takes
	// (@projection<Source>). The TypeScript frontend resolves each to the
	// name of the schema class it references and passes the names to Apply
	// ahead of the value arguments. Only core decorators set it: the data
	// forms write core decorators as typed IR fields and have no place for
	// a type argument in an extension slot.
	typeArgs int

	compiledArgs *validator.Schema
}

// RecordsErrors reports whether the frontend keeps the node when Apply
// fails. False means the node is dropped after the first Apply error, as for
// every other field and operation decorator.
func (s DecoratorSpec) RecordsErrors() bool {
	return s.recordsErrors
}

// TypeArgs returns the number of class type arguments the frontend resolves
// and passes to Apply ahead of the value arguments; zero for every
// decorator but the core ones that name a table (@projection, @join).
func (s DecoratorSpec) TypeArgs() int {
	return s.typeArgs
}

// DeclaredIn reports whether pkg is one of the spec's authoring packages.
func (s DecoratorSpec) DeclaredIn(pkg string) bool {
	return slices.Contains(s.Packages, pkg)
}

// AllowsKind reports whether the decorator may appear in a schema of kind.
func (s DecoratorSpec) AllowsKind(kind string) bool {
	return len(s.Kinds) == 0 || slices.Contains(s.Kinds, kind)
}

// KindError formats the Kinds violation for a schema of kind:
// "@source is only allowed in API or General schemas (this service is kind DB)".
func (s DecoratorSpec) KindError(kind string) string {
	return fmt.Sprintf("@%s is only allowed in %s schemas (this service is kind %s)", s.Name, strings.Join(s.Kinds, " or "), kind)
}

// ValidateArgs checks the decorator's argument against Args. It is a no-op
// for decorators registered without Args. With Args set the decorator takes
// exactly one argument and it must satisfy the schema; the error names the
// decorator so the TS and data forms report the same text.
func (s DecoratorSpec) ValidateArgs(args []any) error {
	if s.compiledArgs == nil {
		return nil
	}
	if len(args) != 1 {
		return fmt.Errorf("@%s takes exactly one argument", s.Name)
	}
	raw, err := json.Marshal(args[0])
	if err != nil {
		return fmt.Errorf("@%s argument: %w", s.Name, err)
	}
	instance, err := validator.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("@%s argument: %w", s.Name, err)
	}
	if err := s.compiledArgs.Validate(instance); err != nil {
		return fmt.Errorf("@%s argument: %w", s.Name, err)
	}
	return nil
}

// DecodeArgs decodes a decorator's single argument into v through a JSON
// round trip: the same conversion the walker performs for object-literal
// arguments and the shape the data forms already hold. Validation against
// DecoratorSpec.Args has run by the time Apply is called.
func DecodeArgs(args []any, v any) error {
	if len(args) != 1 {
		return fmt.Errorf("expected exactly one argument, got %d", len(args))
	}
	raw, err := json.Marshal(args[0])
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// ArgError is an Apply error located at one of the decorator's arguments
// rather than at the decorator itself. The TS frontend points the
// diagnostic at that argument's node; the data forms report Msg as is.
type ArgError struct {
	// Index of the argument, counting from zero.
	Index int
	Msg   string
}

func (e *ArgError) Error() string { return e.Msg }

// ArgErrorf builds an ArgError with a formatted message.
func ArgErrorf(index int, format string, a ...any) error {
	return &ArgError{Index: index, Msg: fmt.Sprintf(format, a...)}
}

// Node is the union of IR holders a decorator can target. Exactly one of
// Type, Field, OperationSet is set, matching the Target.
type Node struct {
	Schema       *ir.Schema
	Type         *ir.TypeDef      // TargetType
	Field        *ir.FieldDef     // TargetField, TargetOperation
	OperationSet *ir.OperationSet // TargetOperationSet
}

// Site locates the decorator in its source file for diagnostics. The data
// forms set File to the schema file and leave Line and Col zero.
type Site struct {
	File string
	Line int
	Col  int
}

// DocumentSpec describes a sidecar document a service directory may carry
// next to its schema (deploy.values.ts, catalog.config.ts).
//
// The loader discovers the sidecar by File: when the file exists next to the
// schema config it calls Loader and stores the result in
// Schema.Documents[Name]; when it does not, nothing is stored. A spec with
// no File but a Loader is called once per service regardless. A spec with no
// Loader is data-form only (the `documents.<Name>` section of a JSON/YAML
// schema file).
type DocumentSpec struct {
	// Name keys Schema.Documents and the `documents.<name>` section of the
	// data form.
	Name      string
	Extension string
	// File is the sidecar path relative to the service directory.
	File string
	// Kinds restricts which schema kinds may carry the document. nil = any.
	// A sidecar present in a service of another kind is a load error.
	Kinds []string
	// Loader turns the sidecar into JSON plus the authoring imports the
	// build cache tracks. A nil doc with a nil error means the document is
	// absent for this service.
	Loader func(ctx context.Context, lc LoadContext) (doc json.RawMessage, imports []string, err error)
	// Schema is the JSON Schema of the loaded document.
	Schema json.RawMessage
	// Dirs returns the directories Generate writes under Options.OutputRoot
	// when the document is present: the build cache's restore/store surface
	// for the document. nil when Generate writes nothing there.
	Dirs func(ctx GenerateContext) []string
	// Generate emits the document's outputs. nil for documents that only
	// feed BuildAllHooks.
	Generate func(ctx GenerateContext, doc json.RawMessage) error
}

// SchemaCatalogEntry is one discovered schema service's identity facts,
// supplied by build-all from its discovery pass. Documents that reference
// other services by name resolve them here without adding build-order
// edges. Single-service builds carry no catalog.
type SchemaCatalogEntry struct {
	// Kind is the schema kind string ("DB", "API", "General", ...).
	Kind string
	// AuthDB names the DB-kind schema the service authenticates against
	// (empty when none).
	AuthDB string
}

// LoadContext is what a DocumentSpec.Loader receives.
type LoadContext struct {
	ServicePath string
	Schema      *ir.Schema
	Config      *schemaconfig.SchemaConfig
	// Registry is the registry the loader runs with, for kind lookups.
	Registry *Registry
	// Catalog is the discovered schema set keyed by service name, or nil
	// when the build sees one service.
	Catalog map[string]SchemaCatalogEntry
	// RunModule executes a TypeScript module with the bun document harness
	// and returns the default export as JSON plus the transitive imports.
	// file is relative to ServicePath.
	RunModule func(file string) (json.RawMessage, []string, error)
	// DecodeData reads a JSON or YAML file relative to ServicePath and
	// validates it against schema (a JSON Schema; nil skips validation).
	DecodeData func(file string, schema json.RawMessage) (json.RawMessage, error)
	Log        io.Writer
}

// GeneratorSpec describes one generator a kind's pipeline can name.
type GeneratorSpec struct {
	// Name is the pipeline identifier. Core: "types", "sql", "orm", "api",
	// "sdks", "envConfig".
	Name      string
	Extension string
	// Kinds lists kinds whose pipeline may include this generator, and
	// whose pipelines it is appended to when they do not name it. nil =
	// any kind, appended to none.
	Kinds []string
	// OutputKey is the schema.config `outputs` key this generator reads,
	// or "" if it has none. The union of OutputKeys is the set ParseOutputs
	// accepts.
	OutputKey string
	// OutputSchema is the JSON Schema of outputs.<OutputKey>. It is
	// compiled at registration, and ParseOutputs validates the section
	// against it before any generator reads it. nil leaves the section to
	// the generator.
	OutputSchema json.RawMessage
	// Dirs returns the output directories this generator writes under
	// OutputRoot for the run. Run rejects a pipeline in which two enabled
	// generators claim the same directory before executing any of them.
	Dirs func(ctx GenerateContext) []string
	// Enabled decides whether Run executes Generate. A non-empty reason is
	// appended to Result.Skipped when the generator is disabled.
	Enabled func(ctx GenerateContext) (bool, string)
	// Generate emits the outputs and records them on ctx.Result.
	Generate func(ctx GenerateContext) error

	compiledOutput *validator.Schema
}

// AuthProvider and AuthModel are declared in apigen (which this package
// imports for APIOutput) and aliased here so extensions register providers
// through the registry vocabulary. See docs/extension-model.md section 8.
// OpenAPIHook and ToolHook are declared there for the same reason: apigen
// runs them. ToolInvocationPolicy too: apigen resolves every visible tool
// against it.
type (
	AuthProvider         = apigen.AuthProvider
	AuthModel            = apigen.AuthModel
	OpenAPIHook          = apigen.OpenAPIHook
	ToolHook             = apigen.ToolHook
	ToolInvocationPolicy = apigen.ToolInvocationPolicy
)

// GenerateContext is the per-run state every generator in a pipeline shares.
type GenerateContext struct {
	Schema   *ir.Schema
	Config   *schemaconfig.SchemaConfig
	Outputs  *Outputs
	Options  Options
	Registry *Registry
	// LoadDependency memoises Options.LoadDependency across the generators
	// of one run; a failed load is not cached.
	LoadDependency func(name string) (*ir.Schema, error)
	// APIOutput memoises apigen.Generate so "sdks" reuses "api"'s result.
	// (nil, nil) means the schema declares no operations.
	APIOutput func() (*apigen.APIOutput, error)
	// EnvConfig memoises the schema's @envVars contract (with dependency
	// schemas resolved) for generators that emit configuration derived
	// from it. (nil, nil) means the schema declares no @envVars type.
	EnvConfig func() (*envgen.ConfigOutput, error)
	Result    *Result
}

// Logf writes to the run's log when one is configured.
func (ctx GenerateContext) Logf(format string, args ...any) {
	if ctx.Options.Log != nil {
		_, _ = fmt.Fprintf(ctx.Options.Log, format, args...)
	}
}

// Done records an output key written to dir.
func (ctx GenerateContext) Done(key, dir string) {
	ctx.Result.Outputs[key] = dir
	ctx.Logf("  + %s written to %s\n", key, dir)
}

// Skip records a requested output that produced nothing for this schema.
func (ctx GenerateContext) Skip(key string) {
	ctx.Result.Skipped = append(ctx.Result.Skipped, key)
	ctx.Logf("  - %s: skipped (not applicable to this schema)\n", key)
}

// InstallTargetDir resolves a repo-root-relative install directory for
// generators that write outside the output root. The repo root is the
// nearest ancestor of Options.ServicePath containing .git (a directory in a
// normal checkout, a file in a linked worktree). The target directory must
// already exist: install never scaffolds it, and a typo'd path failing
// loudly beats a junk tree under the repo root. kind names the directory's
// role ("chart", "manifest") in errors.
//
// The empty return with a nil error means there is no repo above the
// service path: a source-only build context (Docker codegen stages copy the
// schema tree without .git or the consuming infrastructure tree). Callers
// skip the install step on it; the outputs under the output root are
// already written.
func (ctx GenerateContext) InstallTargetDir(relDir, kind string) (string, error) {
	// Loaders reject empty install directories; this guard covers IR built
	// without them, where "" would resolve to the repo root itself and an
	// install prune would eat unrelated files.
	if relDir == "" {
		return "", fmt.Errorf("install target %s directory must not be empty", kind)
	}
	if ctx.Options.ServicePath == "" {
		return "", fmt.Errorf("install target %q needs a service path to resolve the repo root", relDir)
	}
	dir, err := filepath.Abs(ctx.Options.ServicePath)
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
	targetDir := filepath.Join(dir, filepath.FromSlash(relDir))
	if info, statErr := os.Stat(targetDir); statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("install target %q: %s directory %s does not exist", relDir, kind, targetDir)
	}
	return targetDir, nil
}

// BuildAllHook runs in `superschematic build-all` once every service's
// output is in place: built by this run, restored from the build cache, or
// already up to date. It runs on every build-all, including one that built
// nothing.
type BuildAllHook struct {
	Name      string
	Extension string
	Run       func(ctx context.Context, bc BuildAllContext) error
}

// BuildAllContext is what a BuildAllHook receives.
type BuildAllContext struct {
	// ServiceNames in build order.
	ServiceNames []string
	// Services are the discovered services in build order, each with the
	// directories its output lives in.
	Services []BuildAllService
	// SchemaFor returns the IR of a service this process loaded: every
	// service it built and every dependency it loaded for one. A service
	// restored from the cache or already up to date is not loaded, so a
	// hook that must see every service reads Services[i].OutputDirs.
	SchemaFor  func(name string) (*ir.Schema, bool)
	RepoRoot   string
	OutputRoot string
	Naming     Naming
	Log        io.Writer
}

// BuildAllService is one discovered service as build-all hands it to a
// hook.
type BuildAllService struct {
	Name string
	Kind string
	// Dir is the service directory, the one that holds schema.config.*.
	Dir string
	// OutputDirs are the absolute directories the service's present
	// documents and enabled generators write, usually under OutputRoot.
	// They are the set the build cache stores and restores, so they hold
	// the service's output whether this run built it, restored it or found
	// it up to date.
	OutputDirs []string
}

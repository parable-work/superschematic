// Package registry is the extension seam of psgen: the kinds, decorators,
// documents, generators and build-all hooks a psgen binary knows about are
// registered here rather than enumerated in switches. Core registers today's
// behaviour; a downstream Extension adds its own without editing engine code.
//
// The package sits below both internal/loader and internal/generator so each
// can consult it: it imports schema-ir, schemaconfig, codegen, apigen, naming
// and profile, none of which import the loader root, the generator root,
// tsreader, schemafile or buildplan. It must never import any of those five;
// they import it (design: docs/extension-model.md section 3.1).
package registry

import (
	"bytes"
	"fmt"
	"sort"

	validator "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// Extension is the one interface a downstream project implements. Name is
// the registry key for everything the extension owns; Register adds its
// specs to r.
type Extension interface {
	Name() string
	Register(r *Registry) error
}

// Registry is built once per CLI process (or per test) and threaded through
// Options.Registry. It is not a package-level global. Generators keep
// registration order; kinds and documents are reported sorted.
type Registry struct {
	naming naming.Naming

	kinds          map[string]KindSpec
	decorators     map[decoratorKey]DecoratorSpec
	documents      map[string]DocumentSpec
	generators     map[string]GeneratorSpec
	generatorOrder []string
	hooks          []BuildAllHook
	authProviders  map[string]AuthProvider

	// authoring is Naming.AuthoringPackages plus every registered
	// decorator's Packages: the set IsAuthoringPackage answers from.
	authoring map[string]bool
	// extensions is every extension name seen in Use or on a spec, for the
	// closed `extensions` object of the data-form JSON Schema.
	extensions map[string]bool

	// scalars is the catalog RegisterScalars installed; nil means Scalars
	// falls back to CoreScalars (scalars.go).
	scalars      ScalarCatalog
	scalarsOwner string

	failed    error
	finalized bool
}

type decoratorKey struct {
	name   string
	target DecoratorTarget
}

// New returns a registry with the core kinds and core decorators
// registered: everything the loader consults. It never errors. Core
// generators and the build-all hook are added by generator.RegisterCore,
// whose closures live in that package; generator.CoreRegistry runs the whole
// sequence for callers that want the core in one call.
func New(n naming.Naming) *Registry {
	r := &Registry{
		naming:        n.OrDefault(),
		kinds:         map[string]KindSpec{},
		decorators:    map[decoratorKey]DecoratorSpec{},
		documents:     map[string]DocumentSpec{},
		generators:    map[string]GeneratorSpec{},
		authProviders: map[string]AuthProvider{},
		authoring:     map[string]bool{},
		extensions:    map[string]bool{},
	}
	for _, pkg := range r.naming.AuthoringPackages {
		r.authoring[pkg] = true
	}
	for specifier := range r.naming.PackageAliases {
		r.authoring[specifier] = true
	}
	for _, spec := range coreKinds() {
		if err := r.RegisterKind(spec); err != nil {
			panic("registry: core kinds: " + err.Error())
		}
	}
	for _, spec := range coreDecorators() {
		if err := r.RegisterDecorator(spec); err != nil {
			panic("registry: core decorators: " + err.Error())
		}
	}
	// The core session provider is the only one the core registers; every
	// other provider arrives through an extension (the Parable one from
	// utils/parable-schematic/ext/auth). Finalize rejects a Naming whose
	// AuthProvider names one that never arrived. See
	// docs/extension-model.md section 8.
	if err := r.RegisterAuthProvider(sessionauth.Provider{}); err != nil {
		panic("registry: core auth provider: " + err.Error())
	}
	return r
}

// Use calls ext.Register for each extension in order and returns the first
// error. Registration is fail-closed: after a failed Use, Finalize returns
// that error and the registry must not be used.
func (r *Registry) Use(exts ...Extension) error {
	for _, ext := range exts {
		if ext.Name() == "" {
			r.failed = fmt.Errorf("registry: extension has no name")
			return r.failed
		}
		r.extensions[ext.Name()] = true
		if err := ext.Register(r); err != nil {
			r.failed = fmt.Errorf("registry: extension %s: %w", ext.Name(), err)
			return r.failed
		}
	}
	return nil
}

func (r *Registry) noteExtension(name string) {
	if name != "" {
		r.extensions[name] = true
	}
}

// Finalize checks cross-references once everything is registered: every
// KindSpec.Pipeline entry names a registered generator that accepts the kind,
// every OutputKey is unique, and Naming.AuthProvider names a registered
// auth provider. Registration after Finalize is an error.
func (r *Registry) Finalize() error {
	if r.failed != nil {
		return r.failed
	}
	if _, ok := r.authProviders[r.naming.AuthProvider]; !ok {
		return fmt.Errorf("registry: auth_provider %q names no registered auth provider (registered: %v)", r.naming.AuthProvider, r.AuthProviders())
	}
	for _, kindName := range r.Kinds() {
		for _, genName := range r.kinds[kindName].Pipeline {
			gen, ok := r.generators[genName]
			if !ok {
				return fmt.Errorf("registry: kind %s pipeline names unregistered generator %q", kindName, genName)
			}
			if len(gen.Kinds) > 0 && !containsString(gen.Kinds, kindName) {
				return fmt.Errorf("registry: kind %s pipeline includes generator %q, which is restricted to kinds %v", kindName, genName, gen.Kinds)
			}
		}
	}
	owners := map[string]string{}
	for _, name := range r.generatorOrder {
		key := r.generators[name].OutputKey
		if key == "" {
			continue
		}
		if prev, dup := owners[key]; dup {
			return fmt.Errorf("registry: generators %s and %s both claim output key %q", prev, name, key)
		}
		owners[key] = name
	}
	r.finalized = true
	return nil
}

func (r *Registry) registrable(what string) error {
	if r.finalized {
		return fmt.Errorf("registry: cannot register %s after Finalize", what)
	}
	return nil
}

// RegisterKind adds a schema kind. Duplicate names are an error.
func (r *Registry) RegisterKind(spec KindSpec) error {
	if err := r.registrable("kind " + spec.Name); err != nil {
		return err
	}
	if spec.Name == "" {
		return fmt.Errorf("registry: kind spec has no name")
	}
	if _, dup := r.kinds[spec.Name]; dup {
		return fmt.Errorf("registry: kind %q is already registered", spec.Name)
	}
	r.kinds[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// RegisterDecorator adds a decorator keyed by (Name, Target). Its Packages
// join the authoring set and its Args schema, when set, is compiled here so
// a malformed schema fails at registration rather than on first use.
func (r *Registry) RegisterDecorator(spec DecoratorSpec) error {
	if err := r.registrable("decorator " + spec.Name); err != nil {
		return err
	}
	if spec.Name == "" || spec.Target == 0 {
		return fmt.Errorf("registry: decorator spec needs a name and a target")
	}
	if len(spec.Packages) == 0 {
		return fmt.Errorf("registry: decorator @%s names no authoring package", spec.Name)
	}
	if spec.Apply == nil && spec.Extension != "" {
		return fmt.Errorf("registry: decorator @%s has no Apply function", spec.Name)
	}
	key := decoratorKey{name: spec.Name, target: spec.Target}
	if _, dup := r.decorators[key]; dup {
		return fmt.Errorf("registry: decorator @%s is already registered for that target", spec.Name)
	}
	if len(spec.Args) > 0 {
		compiled, err := compileArgs(spec)
		if err != nil {
			return err
		}
		spec.compiledArgs = compiled
	}
	r.decorators[key] = spec
	for _, pkg := range spec.Packages {
		r.authoring[pkg] = true
	}
	r.noteExtension(spec.Extension)
	return nil
}

func compileArgs(spec DecoratorSpec) (*validator.Schema, error) {
	resource, err := validator.UnmarshalJSON(bytes.NewReader(spec.Args))
	if err != nil {
		return nil, fmt.Errorf("registry: decorator @%s Args: %w", spec.Name, err)
	}
	compiler := validator.NewCompiler()
	url := fmt.Sprintf("psgen://decorators/%s/%d.json", spec.Name, spec.Target)
	if err := compiler.AddResource(url, resource); err != nil {
		return nil, fmt.Errorf("registry: decorator @%s Args: %w", spec.Name, err)
	}
	compiled, err := compiler.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("registry: decorator @%s Args: %w", spec.Name, err)
	}
	return compiled, nil
}

// RegisterDocument adds a sidecar document. Duplicate names are an error.
func (r *Registry) RegisterDocument(spec DocumentSpec) error {
	if err := r.registrable("document " + spec.Name); err != nil {
		return err
	}
	if spec.Name == "" {
		return fmt.Errorf("registry: document spec has no name")
	}
	if _, dup := r.documents[spec.Name]; dup {
		return fmt.Errorf("registry: document %q is already registered", spec.Name)
	}
	r.documents[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// RegisterGenerator adds a generator. Duplicate names are an error; output
// key collisions are reported by Finalize, once every generator is known.
func (r *Registry) RegisterGenerator(spec GeneratorSpec) error {
	if err := r.registrable("generator " + spec.Name); err != nil {
		return err
	}
	if spec.Name == "" {
		return fmt.Errorf("registry: generator spec has no name")
	}
	if spec.Generate == nil {
		return fmt.Errorf("registry: generator %q has no Generate function", spec.Name)
	}
	if _, dup := r.generators[spec.Name]; dup {
		return fmt.Errorf("registry: generator %q is already registered", spec.Name)
	}
	r.generators[spec.Name] = spec
	r.generatorOrder = append(r.generatorOrder, spec.Name)
	r.noteExtension(spec.Extension)
	return nil
}

// RegisterAuthProvider adds an auth provider for the api generator to render
// with. Duplicate names are an error.
func (r *Registry) RegisterAuthProvider(p AuthProvider) error {
	if p == nil {
		return fmt.Errorf("registry: auth provider is nil")
	}
	if err := r.registrable("auth provider " + p.Name()); err != nil {
		return err
	}
	if p.Name() == "" {
		return fmt.Errorf("registry: auth provider has no name")
	}
	if _, dup := r.authProviders[p.Name()]; dup {
		return fmt.Errorf("registry: auth provider %q is already registered", p.Name())
	}
	r.authProviders[p.Name()] = p
	return nil
}

// AuthProvider returns the registered provider named name.
func (r *Registry) AuthProvider(name string) (AuthProvider, bool) {
	p, ok := r.authProviders[name]
	return p, ok
}

// AuthProviders returns the registered provider names, sorted.
func (r *Registry) AuthProviders() []string {
	names := make([]string, 0, len(r.authProviders))
	for name := range r.authProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SelectedAuthProvider returns the provider Naming.AuthProvider names: the
// one the api generator renders with. Finalize has checked it exists.
func (r *Registry) SelectedAuthProvider() (AuthProvider, error) {
	p, ok := r.authProviders[r.naming.AuthProvider]
	if !ok {
		return nil, fmt.Errorf("registry: auth_provider %q names no registered auth provider (registered: %v)", r.naming.AuthProvider, r.AuthProviders())
	}
	return p, nil
}

// RegisterBuildAllHook adds a hook `psgen build-all` runs after every
// service has built. Hooks run in registration order. Like every other
// Register*, it refuses registrations after Finalize: a hook added later
// would run in some builds and not others depending on when the registering
// code ran, and Finalize is the point after which the registry's contents
// are fixed. Hooks need a Name so build-all can attribute their errors and
// a Run so the registration does something.
func (r *Registry) RegisterBuildAllHook(h BuildAllHook) error {
	if err := r.registrable("build-all hook " + h.Name); err != nil {
		return err
	}
	if h.Name == "" {
		return fmt.Errorf("registry: build-all hook has no name")
	}
	if h.Run == nil {
		return fmt.Errorf("registry: build-all hook %q has no Run function", h.Name)
	}
	for _, existing := range r.hooks {
		if existing.Name == h.Name {
			return fmt.Errorf("registry: build-all hook %q is already registered", h.Name)
		}
	}
	r.hooks = append(r.hooks, h)
	r.noteExtension(h.Extension)
	return nil
}

// Kind returns the spec registered under name.
func (r *Registry) Kind(name string) (KindSpec, bool) {
	spec, ok := r.kinds[name]
	return spec, ok
}

// KnowsKind reports whether kind is registered. The schemaconfig readers
// take it as their kind predicate: the registry's kind set is the only set
// of schema kinds, core or extension.
func (r *Registry) KnowsKind(kind ir.SchemaKind) bool {
	_, ok := r.kinds[string(kind)]
	return ok
}

// Kinds returns the registered kind names, sorted.
func (r *Registry) Kinds() []string {
	names := make([]string, 0, len(r.kinds))
	for name := range r.kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Decorator returns the spec registered for (name, target).
func (r *Registry) Decorator(name string, target DecoratorTarget) (DecoratorSpec, bool) {
	spec, ok := r.decorators[decoratorKey{name: name, target: target}]
	return spec, ok
}

// Decorators returns every registered decorator sorted by extension, name
// and target, the order the JSON Schema composition emits them in.
func (r *Registry) Decorators() []DecoratorSpec {
	specs := make([]DecoratorSpec, 0, len(r.decorators))
	for _, spec := range r.decorators {
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool {
		a, b := specs[i], specs[j]
		if a.Extension != b.Extension {
			return a.Extension < b.Extension
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Target < b.Target
	})
	return specs
}

// IsAuthoringPackage reports whether pkg is a schema authoring package: one
// of Naming.AuthoringPackages or the declaring package of a registered
// decorator. The TS frontend accepts decorators and type wrappers only from
// these, and verify applies the per-kind import rules to them.
// IsAuthoringPackage reports whether pkg is an authoring package: one of
// Naming.AuthoringPackages, a specifier the [package_aliases] table maps onto
// one, or a package some registered decorator declares.
func (r *Registry) IsAuthoringPackage(pkg string) bool {
	return r.authoring[pkg]
}

// PackageAllowsKind reports whether a schema of kind may import authoring
// package pkg as far as the registered decorators go: true when no decorator
// is declared in pkg (type wrappers, enums, config helpers), or when at least
// one decorator declared in pkg allows kind. A package whose every decorator
// is restricted to other kinds offers a schema of this kind nothing it may
// use, so verify rejects the import the way it rejects
// KindSpec.ForbiddenPackages. This is how a grouping kind's authoring
// package (@superschematic/platform) stays out of DB, API and General schemas without
// those kinds naming it.
func (r *Registry) PackageAllowsKind(pkg string, kind string) bool {
	declared := false
	for _, spec := range r.decorators {
		if !spec.DeclaredIn(pkg) {
			continue
		}
		declared = true
		if spec.AllowsKind(kind) {
			return true
		}
	}
	return !declared
}

// Extensions returns the names of every extension that registered
// something or was passed to Use, sorted.
func (r *Registry) Extensions() []string {
	names := make([]string, 0, len(r.extensions))
	for name := range r.extensions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Document returns the document spec registered under name.
func (r *Registry) Document(name string) (DocumentSpec, bool) {
	spec, ok := r.documents[name]
	return spec, ok
}

// Documents returns every registered document, sorted by Name.
func (r *Registry) Documents() []DocumentSpec {
	specs := make([]DocumentSpec, 0, len(r.documents))
	for _, spec := range r.documents {
		specs = append(specs, spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// Generator returns the generator spec registered under name.
func (r *Registry) Generator(name string) (GeneratorSpec, bool) {
	spec, ok := r.generators[name]
	return spec, ok
}

// Pipeline returns the generators Run executes for kind: the kind's own
// Pipeline in order, then every other generator whose Kinds names this kind,
// in registration order. Unknown kinds yield nil.
func (r *Registry) Pipeline(kind string) []GeneratorSpec {
	spec, ok := r.kinds[kind]
	if !ok {
		return nil
	}
	seen := make(map[string]bool, len(spec.Pipeline))
	pipeline := make([]GeneratorSpec, 0, len(spec.Pipeline))
	for _, name := range spec.Pipeline {
		gen, ok := r.generators[name]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		pipeline = append(pipeline, gen)
	}
	for _, name := range r.generatorOrder {
		gen := r.generators[name]
		if seen[name] || !containsString(gen.Kinds, kind) {
			continue
		}
		seen[name] = true
		pipeline = append(pipeline, gen)
	}
	return pipeline
}

// OutputKeys returns the schema.config outputs keys some generator claims.
// Core keys come first in registration order, extension keys after them
// sorted: the ParseOutputs error message lists them and its text must not
// change for core-only registries.
func (r *Registry) OutputKeys() []string {
	var core, ext []string
	for _, name := range r.generatorOrder {
		gen := r.generators[name]
		if gen.OutputKey == "" {
			continue
		}
		if gen.Extension == "" {
			core = append(core, gen.OutputKey)
		} else {
			ext = append(ext, gen.OutputKey)
		}
	}
	sort.Strings(ext)
	return append(core, ext...)
}

// BuildAllHooks returns the registered hooks in registration order.
func (r *Registry) BuildAllHooks() []BuildAllHook {
	return append([]BuildAllHook(nil), r.hooks...)
}

// Naming returns the naming configuration the registry was built with.
func (r *Registry) Naming() Naming {
	return r.naming
}

// ExtensionConfig returns the [extension.<ext>] table from superschematic.toml,
// nil when the file declares none.
func (r *Registry) ExtensionConfig(ext string) map[string]any {
	cfg, _ := r.naming.ExtensionConfig(ext)
	return cfg
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

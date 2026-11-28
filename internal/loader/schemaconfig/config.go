// Package schemaconfig owns the service configuration contract shared by the
// three schema frontends: the SchemaConfig struct, and validation of the
// JSON/YAML config forms (schema.config.json, schema.config.yaml) against
// the JSON Schema generated from @superschematic/schema-config.
//
// The TypeScript form (schema.config.ts) is read statically by the tsreader
// off the defineConfig({...}) object literal, which it converts to the JSON
// the data forms write and decodes through DecodeJSON too. So every form is
// held to one contract: @superschematic/schema-config declares it,
// gen-json-schema projects it to the embedded JSON Schema, and this package
// enforces it.
//
// A service has its config in one form: Locate finds it and refuses a
// directory that holds more than one.
package schemaconfig

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	ir "github.com/parable-work/superschematic/ir"
)

// definitionBytes is the JSON Schema generated from the SchemaConfigDocument
// type in @superschematic/schema-config. Regenerate with:
//
//	cd packages/schema-config && bun run gen-json-schema
//
//go:embed schema-config.schema.json
var definitionBytes []byte

// ServiceDependency names another schema service this service depends on.
type ServiceDependency struct {
	// Name is the dependency's service name.
	Name string `json:"name" yaml:"name"`

	// Kind is the dependency's schema kind.
	Kind ir.SchemaKind `json:"kind" yaml:"kind"`
}

// SchemaConfig is the service configuration extracted from
// schema.config.{ts,json,yaml}. The TypeScript form is read statically off
// the defineConfig({...}) object literal -- never executed. Every form
// decodes into this struct through DecodeJSON.
type SchemaConfig struct {
	// Name is the service name (e.g., "web-db").
	Name string `json:"name" yaml:"name"`

	// Kind classifies the schema and drives role assignment and the
	// kind/import rules during the walk.
	Kind ir.SchemaKind `json:"kind" yaml:"kind"`

	// Public marks a service whose API is exposed publicly.
	Public bool `json:"public,omitempty" yaml:"public,omitempty"`

	// AuthDB names the DB service used for authentication, when set.
	AuthDB string `json:"authDb,omitempty" yaml:"authDb,omitempty"`

	// AuthDBKind is the kind the authDb handle gives, when the config
	// spells it as a handle: the TypeScript form does, and the data forms
	// write the name alone. The build plan checks it against the service
	// AuthDB names.
	AuthDBKind ir.SchemaKind `json:"-" yaml:"-"`

	// Dependencies lists the services this service imports types from. The
	// JSON/YAML forms carry this as an explicit array: there is no import
	// system in the data forms to derive it from.
	Dependencies []ServiceDependency `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`

	// Calls lists the API services an API's implementation calls, or a
	// site's code calls from the browser. It is valid on an API or a Site
	// config only, and each entry names an API service. The build plan
	// builds each callee before its caller (docs/stack-model.md, sections
	// 3.3 and 8.10).
	Calls []ServiceDependency `json:"calls,omitempty" yaml:"calls,omitempty"`

	// Site is a Site service's build: its package.json script, the
	// directory it writes and its single-page fallback (D55). It is valid
	// on a Site config only.
	Site *ir.SiteConfig `json:"site,omitempty" yaml:"site,omitempty"`

	// Buckets lists the Bucket services an API's implementation uses
	// (docs/stack-model.md, section 8.9, D54). It is valid on an API config
	// only, and each entry names a Bucket service.
	Buckets []ServiceDependency `json:"buckets,omitempty" yaml:"buckets,omitempty"`

	// MemoryStores lists the MemoryStore services an API's implementation
	// uses (D75). It is valid on an API config only, and each entry names a
	// MemoryStore service.
	MemoryStores []ServiceDependency `json:"memoryStores,omitempty" yaml:"memoryStores,omitempty"`

	// RateLimitStore names the MemoryStore service an API's @rateLimit
	// routes count in, which its replicas share (D75). It is valid on an
	// API config only, and the API uses the store whether or not
	// MemoryStores lists it.
	RateLimitStore *ServiceDependency `json:"rateLimitStore,omitempty" yaml:"rateLimitStore,omitempty"`

	// ClientVersions has an API turn away a client too old to speak to it
	// (D143): its EnvConfig gains the optional setting MIN_CLIENT_VERSIONS,
	// and its server answers a request whose X-Client-Version names a
	// listed client at a lower version 426. It is valid on an API config
	// only.
	ClientVersions bool `json:"clientVersions,omitempty" yaml:"clientVersions,omitempty"`

	// ContentAddressed has a Bucket service name its objects by their
	// content (D142): an API that lists it holds a content bucket in its
	// Deps, which puts a body under the SHA-256 of its bytes if no object
	// has that name. It is valid on a Bucket config only.
	ContentAddressed bool `json:"contentAddressed,omitempty" yaml:"contentAddressed,omitempty"`

	// MaxBytes is the largest object a content-addressed bucket puts, in
	// bytes; 0 is ir.DefaultContentMaxBytes. It is valid only beside
	// ContentAddressed.
	MaxBytes int64 `json:"maxBytes,omitempty" yaml:"maxBytes,omitempty"`

	// Providers lists the runtime provider packages an API may use (D159):
	// each a directory relative to the service's directory, in slash form,
	// whose package.json declares the provider. The loader reads each into
	// ir.Schema.Providers. It is valid on an API config only.
	Providers []string `json:"providers,omitempty" yaml:"providers,omitempty"`

	// Outputs is the raw outputs configuration. The v2 generators
	// own its typed interpretation.
	Outputs map[string]any `json:"outputs,omitempty" yaml:"outputs,omitempty"`
}

// Definition returns the JSON Schema the data config forms validate against.
// This is what `superschematic json-schema --config` emits.
func Definition() []byte {
	return definitionBytes
}

var (
	compileOnce sync.Once
	compiled    *validator.Schema
	compileErr  error
)

func compiledSchema() (*validator.Schema, error) {
	compileOnce.Do(func() {
		resource, err := validator.UnmarshalJSON(bytes.NewReader(definitionBytes))
		if err != nil {
			compileErr = fmt.Errorf("decoding embedded schema-config JSON Schema: %w", err)
			return
		}
		compiler := validator.NewCompiler()
		if err := compiler.AddResource("superschematic://schema-config.schema.json", resource); err != nil {
			compileErr = fmt.Errorf("registering schema-config JSON Schema: %w", err)
			return
		}
		compiled, compileErr = compiler.Compile("superschematic://schema-config.schema.json")
	})
	return compiled, compileErr
}

// KindSet is the set of schema kinds a config may name: the caller's
// registry (*registry.Registry satisfies it). This package cannot import
// registry, and there is no closed set of kinds here, so the readers take
// the set as an interface. Kinds is what the unknown-kind diagnostic lists.
type KindSet interface {
	KnowsKind(kind ir.SchemaKind) bool
	Kinds() []string
}

// ReadFile loads and validates the data-form service config, the one
// Locate finds. It returns os.ErrNotExist (wrapped) when the service has
// no config or has schema.config.ts, letting callers fall through to the
// TypeScript form, and Locate's error when it has more than one.
func ReadFile(servicePath string, known KindSet) (*SchemaConfig, error) {
	path, form, ok, err := Locate(servicePath)
	if err != nil {
		return nil, err
	}
	if !ok || form == FormTS {
		return nil, fmt.Errorf("service %s has no schema.config.{json,yaml}: %w", servicePath, os.ErrNotExist)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	name := form.FileName()
	if form == FormYAML {
		var raw any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if data, err = json.Marshal(raw); err != nil {
			return nil, fmt.Errorf("%s: converting to JSON: %w", name, err)
		}
	}
	return DecodeJSON(data, name, known)
}

// DecodeJSON validates a config payload against the generated JSON Schema,
// strict-decodes it and checks its shape with ValidateShapeWith. source
// prefixes each error. The schema checks the core output sections and
// admits any other outputs key; registry.ParseOutputs checks those against
// the registered generators and their OutputSchema. The data forms and the
// TypeScript form, converted to JSON, all decode here.
func DecodeJSON(data []byte, source string, known KindSet) (*SchemaConfig, error) {
	sch, err := compiledSchema()
	if err != nil {
		return nil, err
	}
	instance, err := validator.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := sch.Validate(instance); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}

	cfg := &SchemaConfig{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	// Only a Site service, which generates nothing under the output root,
	// leaves outputs out (D55); the schema cannot say so by kind.
	if cfg.Outputs == nil && cfg.Kind != ir.SchemaKindSite {
		return nil, fmt.Errorf("%s: schema config for %s has no outputs; only a %s service may leave them out", source, cfg.Name, ir.SchemaKindSite)
	}
	return ValidateShapeWith(cfg, known)
}

// ValidateShapeWith checks the required config fields, accepting the kinds
// in known. Both forms use it with the registry's kinds, so an
// extension-registered kind loads without touching this package. The JSON
// Schema the data forms are checked against first leaves kind open (any
// string), and the TS form's kind type admits any string too, so this is
// the one place a typo in kind is caught; the error lists the kinds the
// binary knows so the author can tell a typo from a missing extension.
func ValidateShapeWith(cfg *SchemaConfig, known KindSet) (*SchemaConfig, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("schema config is missing a name")
	}
	if !known.KnowsKind(cfg.Kind) {
		return nil, fmt.Errorf("schema config for %s has unknown kind %q (registered kinds: %s)", cfg.Name, cfg.Kind, strings.Join(known.Kinds(), ", "))
	}
	for _, dep := range cfg.Dependencies {
		if dep.Name == "" || !known.KnowsKind(dep.Kind) {
			return nil, fmt.Errorf("schema config for %s has a malformed dependency (name %q, kind %q; registered kinds: %s)", cfg.Name, dep.Name, dep.Kind, strings.Join(known.Kinds(), ", "))
		}
	}
	if cfg.AuthDBKind != "" && !known.KnowsKind(cfg.AuthDBKind) {
		return nil, fmt.Errorf("schema config for %s has an authDb of unknown kind %q (registered kinds: %s)", cfg.Name, cfg.AuthDBKind, strings.Join(known.Kinds(), ", "))
	}
	if err := validateCalls(cfg); err != nil {
		return nil, err
	}
	if cfg.Site != nil && cfg.Kind != ir.SchemaKindSite {
		return nil, fmt.Errorf("schema config for %s sets site, which only a %s service may set (this service is kind %s)", cfg.Name, ir.SchemaKindSite, cfg.Kind)
	}
	if cfg.Site != nil {
		if err := cfg.Site.Check(); err != nil {
			return nil, fmt.Errorf("schema config for %s: %w", cfg.Name, err)
		}
	}
	if err := validateBuckets(cfg); err != nil {
		return nil, err
	}
	if err := validateMemoryStores(cfg); err != nil {
		return nil, err
	}
	if cfg.ClientVersions && cfg.Kind != ir.SchemaKindAPI {
		return nil, fmt.Errorf("schema config for %s sets clientVersions, which only an API service may set (this service is kind %s)", cfg.Name, cfg.Kind)
	}
	if err := validateProviders(cfg); err != nil {
		return nil, err
	}
	if _, ok := cfg.Outputs["ci"]; ok && cfg.Kind != ir.SchemaKindStack {
		return nil, fmt.Errorf("schema config for %s sets outputs.ci, which only a %s service may set: the generated CI deploys a stack's environments (this service is kind %s)", cfg.Name, ir.SchemaKindStack, cfg.Kind)
	}
	return cfg, nil
}

// validateCalls checks calls on its own: an API or a Site config's list of
// other API services, each named once.
func validateCalls(cfg *SchemaConfig) error {
	if len(cfg.Calls) > 0 && cfg.Kind != ir.SchemaKindAPI && cfg.Kind != ir.SchemaKindSite {
		return fmt.Errorf("schema config for %s sets calls, which only an API or a Site service may set (this service is kind %s)", cfg.Name, cfg.Kind)
	}
	seen := make(map[string]bool, len(cfg.Calls))
	for _, call := range cfg.Calls {
		switch {
		case call.Name == "":
			return fmt.Errorf("schema config for %s has a calls entry with no name", cfg.Name)
		case call.Kind != ir.SchemaKindAPI:
			return fmt.Errorf("schema config for %s calls %s, a handle of kind %q; calls names API services only", cfg.Name, call.Name, call.Kind)
		case call.Name == cfg.Name:
			return fmt.Errorf("schema config for %s calls itself", cfg.Name)
		case seen[call.Name]:
			return fmt.Errorf("schema config for %s calls %s more than once", cfg.Name, call.Name)
		}
		seen[call.Name] = true
	}
	return nil
}

// validateBuckets checks buckets, an API config's list of Bucket services,
// each named once (D54), and a Bucket service's own config: its name and
// kind, and whether it names its objects by their content, with the
// largest it puts (D142), are all it has, since it names no other service
// and generates nothing.
func validateBuckets(cfg *SchemaConfig) error {
	if cfg.Kind == ir.SchemaKindBucket {
		switch {
		case cfg.AuthDB != "" || len(cfg.Dependencies) > 0:
			return fmt.Errorf("schema config for %s sets authDb or dependencies; a %s service names no other service, and the APIs that use it list it in their buckets", cfg.Name, ir.SchemaKindBucket)
		case cfg.Public:
			return fmt.Errorf("schema config for %s sets public; a %s service is private, and a browser reaches an object through a signed URL", cfg.Name, ir.SchemaKindBucket)
		case len(cfg.Outputs) > 0:
			return fmt.Errorf("schema config for %s sets outputs; a %s service generates nothing, so its outputs are {}", cfg.Name, ir.SchemaKindBucket)
		}
	}
	switch {
	case cfg.ContentAddressed && cfg.Kind != ir.SchemaKindBucket:
		return fmt.Errorf("schema config for %s sets contentAddressed, which only a %s service may set (this service is kind %s)", cfg.Name, ir.SchemaKindBucket, cfg.Kind)
	case cfg.MaxBytes != 0 && !cfg.ContentAddressed:
		return fmt.Errorf("schema config for %s sets maxBytes without contentAddressed; maxBytes is the largest object a content-addressed %s puts", cfg.Name, ir.SchemaKindBucket)
	case cfg.MaxBytes < 0:
		return fmt.Errorf("schema config for %s sets maxBytes to %d; it is a whole number of bytes above 0", cfg.Name, cfg.MaxBytes)
	}
	if len(cfg.Buckets) > 0 && cfg.Kind != ir.SchemaKindAPI {
		return fmt.Errorf("schema config for %s sets buckets, which only an API service may set (this service is kind %s)", cfg.Name, cfg.Kind)
	}
	seen := make(map[string]bool, len(cfg.Buckets))
	for _, bucket := range cfg.Buckets {
		switch {
		case bucket.Name == "":
			return fmt.Errorf("schema config for %s has a buckets entry with no name", cfg.Name)
		case bucket.Kind != ir.SchemaKindBucket:
			return fmt.Errorf("schema config for %s lists %s in buckets, a handle of kind %q; buckets names %s services only", cfg.Name, bucket.Name, bucket.Kind, ir.SchemaKindBucket)
		case seen[bucket.Name]:
			return fmt.Errorf("schema config for %s lists bucket %s more than once", cfg.Name, bucket.Name)
		}
		seen[bucket.Name] = true
	}
	return nil
}

// validateMemoryStores checks memoryStores and rateLimitStore, an API
// config's MemoryStore services, each named once (D75), and a MemoryStore
// service's own config: it names no authDb and is not public, and its
// outputs are types alone, in Go, TypeScript or Rust. Its dependencies are
// the services whose object types its channels carry.
func validateMemoryStores(cfg *SchemaConfig) error {
	if cfg.Kind == ir.SchemaKindMemoryStore {
		switch {
		case cfg.AuthDB != "":
			return fmt.Errorf("schema config for %s sets authDb; a %s service authenticates no one, and the APIs that use it list it in their memoryStores", cfg.Name, ir.SchemaKindMemoryStore)
		case cfg.Public:
			return fmt.Errorf("schema config for %s sets public; a %s service is reached only by the workloads of the APIs that list it", cfg.Name, ir.SchemaKindMemoryStore)
		}
		for key, value := range cfg.Outputs {
			if key != "types" {
				return fmt.Errorf("schema config for %s sets outputs.%s; a %s service's outputs are its definitions, outputs.types, in go, typescript or rust", cfg.Name, key, ir.SchemaKindMemoryStore)
			}
			langs, _ := value.(map[string]any)
			for lang := range langs {
				switch lang {
				case "go", "typescript", "rust":
				default:
					return fmt.Errorf("schema config for %s sets outputs.types.%s; a %s service's definitions are written in go, typescript or rust", cfg.Name, lang, ir.SchemaKindMemoryStore)
				}
			}
		}
	}
	if (len(cfg.MemoryStores) > 0 || cfg.RateLimitStore != nil) && cfg.Kind != ir.SchemaKindAPI {
		return fmt.Errorf("schema config for %s sets memoryStores or rateLimitStore, which only an API service may set (this service is kind %s)", cfg.Name, cfg.Kind)
	}
	seen := make(map[string]bool, len(cfg.MemoryStores))
	for _, store := range cfg.MemoryStores {
		switch {
		case store.Name == "":
			return fmt.Errorf("schema config for %s has a memoryStores entry with no name", cfg.Name)
		case store.Kind != ir.SchemaKindMemoryStore:
			return fmt.Errorf("schema config for %s lists %s in memoryStores, a handle of kind %q; memoryStores names %s services only", cfg.Name, store.Name, store.Kind, ir.SchemaKindMemoryStore)
		case seen[store.Name]:
			return fmt.Errorf("schema config for %s lists memory store %s more than once", cfg.Name, store.Name)
		}
		seen[store.Name] = true
	}
	if store := cfg.RateLimitStore; store != nil {
		switch {
		case store.Name == "":
			return fmt.Errorf("schema config for %s has a rateLimitStore with no name", cfg.Name)
		case store.Kind != ir.SchemaKindMemoryStore:
			return fmt.Errorf("schema config for %s names %s its rateLimitStore, a handle of kind %q; rateLimitStore names a %s service", cfg.Name, store.Name, store.Kind, ir.SchemaKindMemoryStore)
		}
	}
	return nil
}

// ContentMaxBytes is the largest object a content-addressed bucket puts:
// its maxBytes, or ir.DefaultContentMaxBytes when it names none.
func (c *SchemaConfig) ContentMaxBytes() int64 {
	if c.MaxBytes > 0 {
		return c.MaxBytes
	}
	return ir.DefaultContentMaxBytes
}

// validateProviders checks providers (D159): an API config's list of
// runtime provider package directories, each a clean relative path in
// slash form, listed once.
func validateProviders(cfg *SchemaConfig) error {
	if len(cfg.Providers) > 0 && cfg.Kind != ir.SchemaKindAPI {
		return fmt.Errorf("schema config for %s sets providers, which only an API service may set (this service is kind %s)", cfg.Name, cfg.Kind)
	}
	seen := make(map[string]bool, len(cfg.Providers))
	for _, dir := range cfg.Providers {
		switch {
		case dir == "" || path.IsAbs(dir) || strings.Contains(dir, `\`) || path.Clean(dir) != dir:
			return fmt.Errorf("schema config for %s lists provider %q; a provider is its package's directory, a clean relative path in slash form from the service's directory", cfg.Name, dir)
		case seen[dir]:
			return fmt.Errorf("schema config for %s lists provider %s more than once", cfg.Name, dir)
		}
		seen[dir] = true
	}
	return nil
}

// UsedMemoryStores are the MemoryStore services the config's API uses:
// its memoryStores in order, then its rateLimitStore when the list leaves
// it out (D75).
func (c *SchemaConfig) UsedMemoryStores() []ServiceDependency {
	stores := append([]ServiceDependency(nil), c.MemoryStores...)
	if c.RateLimitStore == nil {
		return stores
	}
	for _, store := range stores {
		if store.Name == c.RateLimitStore.Name {
			return stores
		}
	}
	return append(stores, *c.RateLimitStore)
}

// NewSchema returns an empty schema for the configured service, holding the
// references the config makes: its authDb, dependencies, calls, buckets,
// memory stores and rate limit store. Every frontend starts its schema here, so the IR records them
// whatever form the config takes.
func (c *SchemaConfig) NewSchema() *ir.Schema {
	schema := ir.NewSchema(c.Name, c.Kind)
	schema.AuthDB = c.AuthDB
	schema.Dependencies = serviceRefs(c.Dependencies)
	schema.Calls = serviceRefs(c.Calls)
	if c.Site != nil {
		site := *c.Site
		schema.Site = &site
	}
	schema.Buckets = serviceRefs(c.Buckets)
	schema.MemoryStores = serviceRefs(c.UsedMemoryStores())
	if c.RateLimitStore != nil {
		schema.RateLimitStore = &ir.ServiceRef{Name: c.RateLimitStore.Name, Kind: c.RateLimitStore.Kind}
	}
	schema.ClientVersions = c.ClientVersions
	return schema
}

// serviceRefs converts config references to IR references; none is nil, so
// the IR leaves the key out.
func serviceRefs(deps []ServiceDependency) []ir.ServiceRef {
	if len(deps) == 0 {
		return nil
	}
	refs := make([]ir.ServiceRef, len(deps))
	for i, dep := range deps {
		refs[i] = ir.ServiceRef{Name: dep.Name, Kind: dep.Kind}
	}
	return refs
}

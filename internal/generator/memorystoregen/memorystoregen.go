// Package memorystoregen is the MemoryStore kind's generator (D75). A
// MemoryStore service's schema declares each channel and key the store
// carries (ir.MemoryStoreDeclaration). Its build writes, once, the store's
// inventory (inventory.go), and for each language its config's
// outputs.types enables, one module of typed definitions: a value per
// declaration whose For takes the parts in order, so a caller never
// formats a name.
//
// Each language's entry point sits in a file of its own, with one stable
// signature, so each language's definitions are built apart:
//
//	GenerateGo(Input) error          golang.go
//	GenerateTypeScript(Input) error  typescript.go
//	GenerateRust(Input) error        rust.go
//
// Generate is the dispatcher the core's generator calls: it resolves the
// payloads, writes the inventory, then calls the entry point of each
// language outputs.types names, each into the directory Dirs gives it.
package memorystoregen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Name is the generator's name in the MemoryStore kind's pipeline.
const Name = "memorystore"

// ErrNotBuilt is what a language's entry point returns while its
// definitions are not built.
var ErrNotBuilt = errors.New("not built yet")

// Languages are the languages a MemoryStore service's outputs.types may
// enable, in the order the build writes them.
var Languages = []string{registry.LangGo, registry.LangTypeScript, registry.LangRust}

// Dirs are the directories one build of a MemoryStore service writes:
// Dir, its inventory's, and the definitions directory of each language
// outputs.types enables, which typesDir names (the types generator's,
// types/<lang>/<service>).
func Dirs(c registry.GenerateContext, typesDir func(root, lang, service string) string) []string {
	dirs := []string{Dir(c.Options.OutputRoot, c.Config.Name)}
	for _, lang := range c.Outputs.EnabledTypeLanguages() {
		if slices.Contains(Languages, lang) {
			dirs = append(dirs, typesDir(c.Options.OutputRoot, lang, c.Config.Name))
		}
	}
	return dirs
}

// Dir is the directory of a MemoryStore service's inventory:
// <output-root>/memorystore/<service>.
func Dir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "memorystore", service)
}

// Input is what a language's entry point writes its definitions from.
type Input struct {
	// Context is the build's context: its options, naming, paths and
	// dependency loader. An entry point reads what it needs of it and
	// records nothing; Generate records each language's output.
	Context registry.GenerateContext

	// Store is the MemoryStore service's name (`shop-cache`): the first
	// segment of every name, and what each language names its module or
	// package after (`shopcache`, `shop_cache`).
	Store string

	// Schema is the store's verified schema.
	Schema *ir.Schema

	// Declarations are the declarations each language writes a definition
	// of, sorted by name: the schema's, without the core's reserved ones,
	// which the runtimes name themselves.
	Declarations []*ir.MemoryStoreDeclaration

	// Payloads are the payload types the channels name, by type name.
	Payloads map[string]Payload

	// Dependencies are the schemas of the store's dependencies that a
	// payload, or an enum a part names, comes from, by service name.
	Dependencies map[string]*ir.Schema

	// Dir is the directory the language writes its definitions into,
	// which the build emptied: types/<lang>/<service> under the output
	// root.
	Dir string
}

// Payload is the type a channel's messages hold: an object type of a
// dependency of the store's schema, which that dependency's types package
// already generates in each language.
type Payload struct {
	// Type is the type's name (`StockLevel`).
	Type string

	// Service is the dependency service that declares it.
	Service string

	// Package is the package the store's schema imports it from
	// (`@acme/shop-common`).
	Package string

	// Def is its definition.
	Def *ir.TypeDef
}

// Generate is the MemoryStore kind's build: it resolves the payloads,
// writes inventory.json into Dir and calls the entry point of each
// language outputs.types enables, recording each output. typesDir names a
// language's directory, as Dirs does. A language the kind does not
// generate, Python, is refused.
func Generate(c registry.GenerateContext, typesDir func(root, lang, service string) string) error {
	in, err := NewInput(c)
	if err != nil {
		return err
	}
	dir := Dir(c.Options.OutputRoot, c.Config.Name)
	if err := WriteInventory(dir, in); err != nil {
		return err
	}
	c.Done("memorystore-inventory", dir)
	entries := map[string]func(Input) error{
		registry.LangGo:         GenerateGo,
		registry.LangTypeScript: GenerateTypeScript,
		registry.LangRust:       GenerateRust,
	}
	for _, lang := range c.Outputs.EnabledTypeLanguages() {
		entry, ok := entries[lang]
		if !ok {
			return fmt.Errorf("memory store %s: outputs.types.%s: a %s service's definitions are written in %s", c.Config.Name, lang, ir.SchemaKindMemoryStore, languageList())
		}
		langIn := in
		langIn.Dir = typesDir(c.Options.OutputRoot, lang, c.Config.Name)
		if err := os.RemoveAll(langIn.Dir); err != nil {
			return fmt.Errorf("memory store %s: %w", c.Config.Name, err)
		}
		if err := os.MkdirAll(langIn.Dir, 0o755); err != nil {
			return fmt.Errorf("memory store %s: %w", c.Config.Name, err)
		}
		if err := entry(langIn); err != nil {
			return fmt.Errorf("memory store %s: %s definitions: %w", c.Config.Name, registry.LanguageName(lang), err)
		}
		c.Done("memorystore-"+lang, langIn.Dir)
	}
	return nil
}

func languageList() string {
	names := make([]string, len(Languages))
	for i, lang := range Languages {
		names[i] = registry.LanguageName(lang)
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// NewInput reads what every language's definitions are written from: the
// store's declarations without the reserved ones, and each payload's type
// from the dependency the schema imports it from. Dir is left empty.
func NewInput(c registry.GenerateContext) (Input, error) {
	in := Input{
		Context:      c,
		Store:        c.Config.Name,
		Schema:       c.Schema,
		Payloads:     map[string]Payload{},
		Dependencies: map[string]*ir.Schema{},
	}
	for _, decl := range c.Schema.MemoryStoreDeclarations {
		if decl != nil && !decl.Reserved {
			in.Declarations = append(in.Declarations, decl)
		}
	}
	ir.SortMemoryStoreDeclarations(in.Declarations)
	load := func(service string) (*ir.Schema, error) {
		if dep, ok := in.Dependencies[service]; ok {
			return dep, nil
		}
		if c.LoadDependency == nil {
			return nil, fmt.Errorf("no dependency loader is configured")
		}
		dep, err := c.LoadDependency(service)
		if err != nil {
			return nil, err
		}
		in.Dependencies[service] = dep
		return dep, nil
	}
	for _, decl := range in.Declarations {
		if decl.Payload == nil {
			continue
		}
		name := decl.Payload.Name
		if _, done := in.Payloads[name]; done {
			continue
		}
		pkg := importOf(c.Schema, name)
		if pkg == "" {
			return Input{}, fmt.Errorf("memory store %s: channel %s carries %s, which the schema imports from no dependency", in.Store, decl.Name, name)
		}
		service := typegen.DependencyServiceName(pkg)
		dep, err := load(service)
		if err != nil {
			return Input{}, fmt.Errorf("memory store %s: channel %s carries %s of %s: %w", in.Store, decl.Name, name, service, err)
		}
		def := dep.Types[name]
		if def == nil {
			return Input{}, fmt.Errorf("memory store %s: channel %s carries %s, which %s does not declare as an object type", in.Store, decl.Name, name, service)
		}
		in.Payloads[name] = Payload{Type: name, Service: service, Package: pkg, Def: def}
	}
	for _, decl := range in.Declarations {
		for _, part := range decl.Parts {
			if part.Format != ir.MemoryStorePartEnum {
				continue
			}
			if pkg := importOf(c.Schema, part.Type.Name); pkg != "" {
				if _, err := load(typegen.DependencyServiceName(pkg)); err != nil {
					return Input{}, fmt.Errorf("memory store %s: part %s of %s is enum %s: %w", in.Store, part.Name, decl.Name, part.Type.Name, err)
				}
			}
		}
	}
	return in, nil
}

// importOf is the package the schema imports the symbol from, or "".
func importOf(schema *ir.Schema, symbol string) string {
	for _, imp := range schema.Imports {
		if slices.Contains(imp.Types, symbol) {
			return imp.Package
		}
	}
	return ""
}

// PayloadOf is the payload a channel carries, and false for a signal or a
// key.
func (in Input) PayloadOf(decl *ir.MemoryStoreDeclaration) (Payload, bool) {
	if decl.Payload == nil {
		return Payload{}, false
	}
	p, ok := in.Payloads[decl.Payload.Name]
	return p, ok
}

// Package tswriter is the TypeScript back end of the schema writer:
// Document -> *.schema.ts source text. It is the inverse of tsreader.
//
// The writer emits exactly the authoring surface the tsreader walks: import
// statements grouped by source package, decorator syntax with arguments,
// generic wrapper forms (Nullable, Default, Validate, AutoGenerate, ...),
// heritage clauses reconstructed from RawHeritage, and IR Comment metadata
// as '//' comments above each node.
//
// TypeScript is the only format that cannot carry the full IR: schema-level
// comments and descriptions, plot pipelines and versions, combinator and
// primitive implementation refs, unions, and map-typed fields have no
// TypeScript authoring form. The writer returns a descriptive error rather
// than silently dropping such content.
package tswriter

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

// Context supplies service-level information a single document cannot carry.
// All fields are optional; a nil Context restricts the writer to what the
// document itself declares.
type Context struct {
	// Enums maps service-wide enum names to definitions, for rendering
	// Default<Enum, Enum.Member> values when the enum lives in another file.
	Enums map[string]*ir.EnumDef

	// DefLocations maps same-service definition names to the extension-free
	// schema file base that declares them (e.g. "src/tenant.schema"), for
	// relative imports of definitions declared in sibling files.
	DefLocations map[string]string

	// Base is this document's own extension-free schema file base, used to
	// compute relative import paths.
	Base string
}

// Write renders a document as TypeScript schema source.
func Write(doc *schemafile.Document) ([]byte, error) {
	return WriteContext(doc, nil)
}

// WriteContext renders a document as TypeScript schema source with
// service-level context for cross-file references.
func WriteContext(doc *schemafile.Document, ctx *Context) ([]byte, error) {
	if ctx == nil {
		ctx = &Context{}
	}
	e := &emitter{
		doc:        doc,
		ctx:        ctx,
		pkgImports: make(map[string]map[string]bool),
		relImports: make(map[string]map[string]bool),
	}
	e.emitDocument()
	if len(e.errs) > 0 {
		return nil, errors.Join(e.errs...)
	}

	var out strings.Builder
	e.renderImports(&out)
	body := strings.TrimLeft(e.body.String(), "\n")
	if out.Len() > 0 && body != "" {
		out.WriteString("\n")
	}
	out.WriteString(body)
	return []byte(out.String()), nil
}

// emitter accumulates the file body and the imports the body requires.
type emitter struct {
	doc  *schemafile.Document
	ctx  *Context
	body strings.Builder

	// pkgImports maps package names to imported symbols.
	pkgImports map[string]map[string]bool

	// relImports maps relative module specifiers to imported symbols.
	relImports map[string]map[string]bool

	errs []error
}

func (e *emitter) failf(format string, args ...any) {
	e.errs = append(e.errs, fmt.Errorf(format, args...))
}

// symbolPackages maps every toolchain symbol the writer emits to its
// declaring package; use folds each onto the specifier the active naming's
// [package_aliases] table gives it.
var symbolPackages = map[string]string{
	// @superschematic/schema
	"Default": "@superschematic/schema", "Nullable": "@superschematic/schema",
	"Validate": "@superschematic/schema", "Secret": "@superschematic/schema", "trait": "@superschematic/schema",
	"source": "@superschematic/schema", "temporalFormat": "@superschematic/schema", "virtual": "@superschematic/schema",
	// jsonField is exported by both @superschematic/schema and @superschematic/db; emit the
	// @superschematic/schema import so General schemas (which do not stage @superschematic/db)
	// round-trip.
	"jsonField": "@superschematic/schema",
	// @superschematic/db
	"index": "@superschematic/db", "key": "@superschematic/db",
	"searchField": "@superschematic/db", "sourceMustProject": "@superschematic/db", "unique": "@superschematic/db",
	"versioned":    "@superschematic/db",
	"AutoGenerate": "@superschematic/db", "HasMany": "@superschematic/db", "JsonField": "@superschematic/db",
	"ManyToMany": "@superschematic/db", "Relation": "@superschematic/db",
	// @superschematic/api
	"Authenticated": "@superschematic/api", "Encrypted": "@superschematic/api",
	"bodyLimit": "@superschematic/api", "manualRouteRegistration": "@superschematic/api",
	"rateLimit": "@superschematic/api", "requireOwnership": "@superschematic/api",
	"requirePermission": "@superschematic/api", "rest": "@superschematic/api",
	"timeout": "@superschematic/api", "uiHidden": "@superschematic/api",
	"HttpMethod": "@superschematic/api", "EncryptedField": "@superschematic/api", "QueryParam": "@superschematic/api",
	// @superschematic/schema-config
	"envVars": "@superschematic/schema-config",
}

// use records a toolchain symbol import and returns the symbol for inline use.
func (e *emitter) use(symbol string) string {
	pkg, ok := symbolPackages[symbol]
	if !ok {
		e.failf("internal: symbol %q has no package mapping", symbol)
		return symbol
	}
	e.importSymbol(naming.Active().Specifier(pkg), symbol)
	return symbol
}

func (e *emitter) importSymbol(pkg, symbol string) {
	if e.pkgImports[pkg] == nil {
		e.pkgImports[pkg] = make(map[string]bool)
	}
	e.pkgImports[pkg][symbol] = true
}

func (e *emitter) importRelative(module, symbol string) {
	if e.relImports[module] == nil {
		e.relImports[module] = make(map[string]bool)
	}
	e.relImports[module][symbol] = true
}

// renderImports writes the import block: packages sorted by name, then
// relative modules, each with sorted named imports.
func (e *emitter) renderImports(out *strings.Builder) {
	for _, pkg := range sortedKeys(e.pkgImports) {
		fmt.Fprintf(out, "import { %s } from %q;\n", strings.Join(sortedKeys(e.pkgImports[pkg]), ", "), pkg)
	}
	for _, module := range sortedKeys(e.relImports) {
		fmt.Fprintf(out, "import { %s } from %q;\n", strings.Join(sortedKeys(e.relImports[module]), ", "), module)
	}
}

// emitDocument renders every definition in the document in a deterministic,
// declaration-order-safe sequence: enums, types (topologically sorted so
// base classes precede subclasses), and operation sets.
func (e *emitter) emitDocument() {
	if e.doc.Comment != "" || e.doc.Description != "" {
		e.failf("schema-level comments and descriptions have no TypeScript form; they are document metadata in JSON and YAML only")
	}
	for _, name := range sortedKeys(e.doc.Unions) {
		e.failf("union %s: unions have no TypeScript authoring form", name)
	}
	e.checkScalars()

	for _, name := range sortedKeys(e.doc.Enums) {
		e.emitEnum(e.doc.Enums[name])
	}
	for _, name := range e.sortedTypes() {
		e.emitType(e.doc.Types[name])
	}
	for _, set := range e.doc.OperationSets {
		e.emitOperationSet(set)
	}
}

// checkScalars verifies every declared scalar is expressible: TypeScript
// schemas reference scalars through the scalar library's branded namespaces, so
// a scalar must use a namespaced name and carry no metadata beyond its
// language primitive (the scalar registry owns the rich metadata).
func (e *emitter) checkScalars() {
	for _, name := range sortedKeys(e.doc.Scalars) {
		def := e.doc.Scalars[name]
		if !strings.Contains(name, ".") {
			e.failf("scalar %s: TypeScript schemas can only reference namespaced %s scalars", name, naming.Active().ScalarNpmPackage)
			continue
		}
		bare := *def
		bare.Name = ""
		bare.LanguagePrimitive = ""
		stripScalarLibHydratedMetadata(name, &bare)
		if !reflect.DeepEqual(bare, ir.ScalarDef{}) {
			e.failf("scalar %s: declares metadata beyond its language primitive, which TypeScript schemas cannot express (scalar metadata lives in scalar-lib)", name)
		}
	}
}

func stripScalarLibHydratedMetadata(name string, def *ir.ScalarDef) {
	if def == nil {
		return
	}
	metadata, ok := scalars.ScalarMetadataByCanonical[name]
	if !ok {
		return
	}

	if def.Description == metadata.Description {
		def.Description = ""
	}
	if def.Primitive == metadata.Primitive {
		def.Primitive = ""
	}
	if def.LanguagePrimitive == scalarLibLanguagePrimitive(metadata.Primitive) {
		def.LanguagePrimitive = ""
	}
	if def.MaxLength == metadata.MaxLength {
		def.MaxLength = 0
	}
	if def.MinLength == metadata.MinLength {
		def.MinLength = 0
	}
	if def.Pattern == metadata.Pattern {
		def.Pattern = ""
	}
	if def.Format == metadata.Format {
		def.Format = ""
	}
	def.HasCustomNormalize = false
	def.HasCustomParse = false
	def.HasCustomValidate = false

	if def.TypeMappings == nil {
		return
	}
	cleaned := make(map[string]string, len(def.TypeMappings))
	for key, value := range def.TypeMappings {
		switch {
		case key == "go" && value == metadata.Symbol:
			continue
		case key == "rust" && value == metadata.Symbol:
			continue
		case key == "sql" && value == metadata.SQLType:
			continue
		case key == "json_schema" && value == metadata.JSONSchemaType:
			continue
		case key == "typescript" && value == metadata.GoType:
			continue
		default:
			cleaned[key] = value
		}
	}
	if len(cleaned) == 0 {
		def.TypeMappings = nil
		return
	}
	def.TypeMappings = cleaned
}

func scalarLibLanguagePrimitive(primitive string) ir.LanguagePrimitive {
	switch strings.ToLower(strings.TrimSpace(primitive)) {
	case "string", "str":
		return ir.LanguageString
	case "number", "float", "float64", "int", "int32", "int64", "integer":
		return ir.LanguageNumber
	case "bool", "boolean":
		return ir.LanguageBoolean
	case "type", "object", "json", "jsonb":
		return ir.LanguageObject
	default:
		return ir.LanguageObject
	}
}

// sortedTypes returns type names topologically sorted so heritage and
// @source targets are declared before the classes that reference them
// (decorator arguments and extends clauses are value positions in
// TypeScript: forward references do not compile).
func (e *emitter) sortedTypes() []string {
	names := sortedKeys(e.doc.Types)
	deps := make(map[string][]string, len(names))
	for _, name := range names {
		def := e.doc.Types[name]
		var d []string
		if def.Extends != "" {
			if _, ok := e.doc.Types[def.Extends]; ok {
				d = append(d, def.Extends)
			}
		}
		for _, tr := range def.Implements {
			if _, ok := e.doc.Types[tr.Name]; ok {
				d = append(d, tr.Name)
			}
		}
		if def.Source != nil {
			if target, ok := e.localSourceTarget(def.Source.Target); ok {
				if _, declared := e.doc.Types[target]; declared {
					d = append(d, target)
				}
			}
		}
		deps[name] = d
	}

	var order []string
	state := make(map[string]int, len(names)) // 0 unvisited, 1 visiting, 2 done
	var visit func(string)
	visit = func(name string) {
		switch state[name] {
		case 1:
			e.failf("type %s: heritage cycle detected", name)
			return
		case 2:
			return
		}
		state[name] = 1
		for _, dep := range deps[name] {
			visit(dep)
		}
		state[name] = 2
		order = append(order, name)
	}
	for _, name := range names {
		visit(name)
	}
	return order
}

// localSourceTarget resolves an @source target ("svc.Type") to the local
// type name when the target lives in this service.
func (e *emitter) localSourceTarget(target string) (string, bool) {
	i := strings.LastIndex(target, ".")
	if i < 0 {
		return target, true
	}
	if target[:i] == e.doc.Name {
		return target[i+1:], true
	}
	return "", false
}

// comment writes an IR comment as '//' lines at the given indent.
func (e *emitter) comment(indent, text string) {
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			e.body.WriteString(indent)
			e.body.WriteString("//\n")
			continue
		}
		e.body.WriteString(indent)
		e.body.WriteString("// ")
		e.body.WriteString(line)
		e.body.WriteString("\n")
	}
}

var identifierRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// ident validates a name renders as a bare TypeScript identifier.
func (e *emitter) ident(name, what string) string {
	if !identifierRe.MatchString(name) {
		e.failf("%s %q is not a valid TypeScript identifier", what, name)
	}
	return name
}

// quote renders a TypeScript double-quoted string literal.
func quote(s string) string {
	out := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s)
	return `"` + out + `"`
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

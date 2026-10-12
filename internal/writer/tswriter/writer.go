// Package tswriter is the TypeScript back end of the schema writer:
// Document -> *.schema.ts source text. It is the inverse of tsreader.
//
// The writer emits exactly the authoring surface the tsreader walks: import
// statements grouped by source package, decorator syntax with arguments,
// generic wrapper forms (Nullable, Default, Validate, AutoGenerate, ...),
// heritage clauses reconstructed from RawHeritage and the user model's
// traits, and IR Comment metadata as '//' comments above each node.
//
// TypeScript is the only format that cannot carry the full IR: schema-level
// comments, extension data and documents, descriptions on the schema and on
// type, enum and operation-set definitions, unions, map-typed fields, and
// scalar metadata beyond the language primitive have no TypeScript authoring
// form. The writer returns a descriptive error rather than silently dropping
// such content.
package tswriter

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/registry"
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
	// schema file base that declares them (e.g. "src/orders.schema"), for
	// relative imports of definitions declared in sibling files.
	DefLocations map[string]string

	// Base is this document's own extension-free schema file base, used to
	// compute relative import paths.
	Base string

	// Scalars is the scalar catalog the document was read with. A scalar
	// the document declares may carry what its row says, such as the
	// json_schema mapping the TypeScript form records of an extension's
	// JSON scalar, and the TypeScript form leaves that to the catalog. The
	// catalog also names the npm package an extension's namespace is
	// imported from (registry.NpmPackageCatalog). Nil is the core catalog.
	Scalars registry.ScalarCatalog
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
	"denyUnknownFields": "@superschematic/schema", "strictJSON": "@superschematic/schema",
	"purpose": "@superschematic/schema", "behavior": "@superschematic/schema", "display": "@superschematic/schema",
	// jsonField is exported by both @superschematic/schema and @superschematic/db; emit the
	// @superschematic/schema import so General schemas (which do not stage @superschematic/db)
	// round-trip.
	"jsonField": "@superschematic/schema",
	// @superschematic/db
	"index": "@superschematic/db", "key": "@superschematic/db",
	"searchField": "@superschematic/db", "sourceMustProject": "@superschematic/db", "unique": "@superschematic/db",
	"versioned": "@superschematic/db", "optimistic": "@superschematic/db", "queue": "@superschematic/db",
	"projection": "@superschematic/db", "join": "@superschematic/db", "column": "@superschematic/db",
	"versionGraph": "@superschematic/db", "graphMember": "@superschematic/db", "conflictUnit": "@superschematic/db",
	"AutoGenerate": "@superschematic/db", "HasMany": "@superschematic/db", "JsonField": "@superschematic/db",
	"ManyToMany": "@superschematic/db", "Relation": "@superschematic/db",
	"User": "@superschematic/db", "UserRole": "@superschematic/db",
	// @superschematic/api
	"Authenticated": "@superschematic/api", "Encrypted": "@superschematic/api",
	"bodyLimit": "@superschematic/api", "manualRouteRegistration": "@superschematic/api",
	"rateLimit": "@superschematic/api", "requireOwnership": "@superschematic/api",
	"requirePermission": "@superschematic/api", "rest": "@superschematic/api",
	"requireService": "@superschematic/api", "allowService": "@superschematic/api",
	"timeout": "@superschematic/api", "uiHidden": "@superschematic/api",
	"HttpMethod": "@superschematic/api", "EncryptedField": "@superschematic/api", "QueryParam": "@superschematic/api",
	"mcp": "@superschematic/api", "userSessions": "@superschematic/api", "userAdministration": "@superschematic/api",
	"job": "@superschematic/api", "worker": "@superschematic/api",
	// @superschematic/schema-config
	"envVars": "@superschematic/schema-config",
	// @superschematic/stack
	"stack": "@superschematic/stack", "server": "@superschematic/stack",
	"database": "@superschematic/stack", "environment": "@superschematic/stack",
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

// useAs records an aliased import of symbol from the declaring package pkg
// and returns the alias. @superschematic/api (operations) and
// @superschematic/schema (fields) both export docs and icon; the aliases
// keep a file that uses both unambiguous.
func (e *emitter) useAs(pkg, symbol, alias string) string {
	e.importSymbol(naming.Active().Specifier(pkg), symbol+" as "+alias)
	return alias
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
	if len(e.doc.Extensions) > 0 || len(e.doc.Documents) > 0 {
		e.failf("schema-level extension data and documents have no TypeScript form; they are document metadata in JSON and YAML only")
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
	for _, job := range e.doc.Jobs {
		e.emitJob(job)
	}
	for _, worker := range e.doc.Workers {
		e.emitWorker(worker)
	}
}

// emitWorker renders a worker as the class the reader reads it from (D53):
// `@worker({ queue: <class>, ... })` on an abstract class with no members.
// The queue is a class the schema imports, which the document's imports
// name.
func (e *emitter) emitWorker(worker *ir.Worker) {
	if worker == nil {
		return
	}
	imported := false
	for _, imp := range e.doc.Imports {
		if slices.Contains(imp.Types, worker.Queue) {
			e.importSymbol(imp.Package, worker.Queue)
			imported = true
			break
		}
	}
	if !imported {
		e.failf("worker %s: queue %s is not covered by the document's imports", worker.Name, worker.Queue)
	}
	parts := []string{"queue: " + e.ident(worker.Queue, "queue")}
	if worker.Concurrency != 0 {
		parts = append(parts, fmt.Sprintf("concurrency: %d", worker.Concurrency))
	}
	if worker.Grace != "" {
		parts = append(parts, "grace: "+quote(worker.Grace))
	}
	e.body.WriteString("\n")
	e.comment("", worker.Comment)
	fmt.Fprintf(&e.body, "@%s(%s)\nexport abstract class %s {}\n", e.use("worker"), objectLiteral(parts), e.ident(worker.Name, "worker"))
}

// queueArgs renders @queue's argument (D53): the options it sets, or none.
func queueArgs(def *ir.QueueDef) string {
	var parts []string
	if def.Retries != nil {
		parts = append(parts, fmt.Sprintf("retries: %d", *def.Retries))
	}
	if def.Backoff != "" {
		parts = append(parts, "backoff: "+quote(def.Backoff))
	}
	if def.Lease != "" {
		parts = append(parts, "lease: "+quote(def.Lease))
	}
	if len(parts) == 0 {
		return ""
	}
	return objectLiteral(parts)
}

// emitJob renders a job as the class the reader reads it from (D52):
// `@job({ ... })` on an abstract class with no members.
func (e *emitter) emitJob(job *ir.Job) {
	if job == nil {
		return
	}
	var parts []string
	if job.Schedule != "" {
		parts = append(parts, "schedule: "+quote(job.Schedule))
	}
	if job.TimeZone != "" {
		parts = append(parts, "timeZone: "+quote(job.TimeZone))
	}
	if job.Timeout != "" {
		parts = append(parts, "timeout: "+quote(job.Timeout))
	}
	if job.Retries != 0 {
		parts = append(parts, fmt.Sprintf("retries: %d", job.Retries))
	}
	arg := ""
	if len(parts) > 0 {
		arg = objectLiteral(parts)
	}
	e.body.WriteString("\n")
	e.comment("", job.Comment)
	fmt.Fprintf(&e.body, "@%s(%s)\nexport abstract class %s {}\n", e.use("job"), arg, e.ident(job.Name, "job"))
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
		stripScalarLibHydratedMetadata(name, &bare, e.ctx.Scalars)
		if !reflect.DeepEqual(bare, ir.ScalarDef{}) {
			e.failf("scalar %s: declares metadata beyond its language primitive, which TypeScript schemas cannot express (scalar metadata lives in superscalar)", name)
		}
	}
}

// stripScalarLibHydratedMetadata clears what def holds of its catalog row,
// which the TypeScript form does not write: the row comes from catalog, or
// from the core table when catalog is nil.
func stripScalarLibHydratedMetadata(name string, def *ir.ScalarDef, catalog registry.ScalarCatalog) {
	if def == nil {
		return
	}
	if catalog == nil {
		catalog = registry.CoreScalars()
	}
	metadata, ok := catalog.Scalar(name)
	if !ok {
		return
	}

	if def.Description == metadata.Description {
		def.Description = ""
	}
	if def.Primitive == metadata.Primitive {
		def.Primitive = ""
	}
	if lp, _ := ir.CatalogLanguagePrimitive(metadata.Primitive); def.LanguagePrimitive == lp {
		def.LanguagePrimitive = ""
	}
	if def.MaxLength == metadata.MaxLength {
		def.MaxLength = 0
	}
	if def.MinLength == metadata.MinLength {
		def.MinLength = 0
	}
	if equalOptionalInt64(def.Maximum, metadata.Maximum) {
		def.Maximum = nil
	}
	if equalOptionalInt64(def.Minimum, metadata.Minimum) {
		def.Minimum = nil
	}
	if def.Pattern == metadata.Pattern {
		def.Pattern = ""
	}
	if def.Format == metadata.Format {
		def.Format = ""
	}
	if def.CaseInsensitive == metadata.CaseInsensitive {
		def.CaseInsensitive = false
	}
	if slices.Equal(def.ReservedWords, metadata.ReservedWords) || len(def.ReservedWords) == 0 && len(metadata.ReservedWords) == 0 {
		def.ReservedWords = nil
	}
	if def.ReservedWordsCaseInsensitive == metadata.ReservedWordsCaseInsensitive {
		def.ReservedWordsCaseInsensitive = false
	}
	if def.ReservedWordsMatchPartial == metadata.ReservedWordsMatchPartial {
		def.ReservedWordsMatchPartial = false
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
		case key == "typescript" && (value == metadata.TypeScriptType || value == metadata.GoType):
			continue
		case key == "python" && value == metadata.PythonType:
			continue
		case key == "rust" && (value == metadata.RustType || value == metadata.Symbol):
			continue
		case key == "sql" && value == metadata.SQLType:
			continue
		case key == "json_schema" && value == metadata.JSONSchemaType:
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

func equalOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// sortedTypes returns type names topologically sorted so heritage, @source
// and @graphMember targets, and the classes a Stack declaration names, are
// declared before the classes that reference them (decorator arguments and
// extends clauses are value positions in TypeScript: forward references do
// not compile). The @environment classes come last, in declaration order
// (ir.EnvironmentNames), since the reader numbers them by their place.
func (e *emitter) sortedTypes() []string {
	var names []string
	for _, name := range sortedKeys(e.doc.Types) {
		if def := e.doc.Types[name]; def == nil || def.Environment == nil {
			names = append(names, name)
		}
	}
	environments := ir.EnvironmentNames(e.doc.Types)
	names = append(names, environments...)
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
		// @graphMember names its root and its parent's type as values,
		// which a class may not use before its declaration; a member may
		// name itself.
		if member := def.GraphMember; member != nil {
			for _, target := range []string{member.Graph, parentOf(member)} {
				if _, ok := e.doc.Types[target]; ok && target != name {
					d = append(d, target)
				}
			}
		}
		// A Stack declaration names a declared deployable by its class.
		for _, ref := range stackClassRefs(def) {
			if _, ok := e.doc.Types[ref]; ok && ref != name {
				d = append(d, ref)
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

	// The environments with an order come first, and the reader numbers
	// the classes in the order they are written, so those must be written
	// first and in their order. An environment whose order comes before the
	// class it extends cannot be: the parent is written first. Those
	// without an order come by name, which no order declared, so their
	// parents may move them.
	var written []string
	ordered := 0
	for _, name := range order {
		if def := e.doc.Types[name]; def != nil && def.Environment != nil {
			written = append(written, name)
			if def.Environment.Order > 0 {
				ordered++
			}
		}
	}
	if !slices.Equal(written[:ordered], environments[:ordered]) {
		e.failf("the @environment classes in their order, %s, have no TypeScript form: a class is declared after the class it extends, so the reader would number them %s",
			strings.Join(environments[:ordered], ", "), strings.Join(written, ", "))
	}
	return order
}

// stackClassRefs returns the declared deployables' classes def's Stack
// declarations name: @stack's exposed classes and each settings element's
// of.
func stackClassRefs(def *ir.TypeDef) []string {
	var refs []string
	if def.Stack != nil {
		for _, ref := range def.Stack.Expose {
			if ref.Deployable != "" {
				refs = append(refs, ref.Deployable)
			}
		}
	}
	if def.Environment != nil {
		for _, settings := range def.Environment.Settings {
			if settings != nil && settings.Of.Deployable != "" {
				refs = append(refs, settings.Of.Deployable)
			}
		}
	}
	return refs
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

// parentOf returns the parent type a graph member names, or "".
func parentOf(member *ir.GraphMemberConfig) string {
	if member.Parent == nil {
		return ""
	}
	return member.Parent.Of
}

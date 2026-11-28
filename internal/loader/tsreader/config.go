package tsreader

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// SchemaConfig is the service configuration contract, owned by the
// schemaconfig package and shared by all three frontends.
type SchemaConfig = schemaconfig.SchemaConfig

// ServiceDependency names another schema service this service depends on.
type ServiceDependency = schemaconfig.ServiceDependency

// readConfig reads the service config in the one form it has
// (schemaconfig.Locate refuses a service with two). The walker argument
// supplies the checker used for the static defineConfig read; it is only
// required for the TypeScript form. The JSON and YAML forms are validated
// against the JSON Schema generated from @superschematic/schema-config and
// decoded by the schemaconfig package.
func readConfig(w *walker, servicePath string, configFile *astSourceFile) (*SchemaConfig, error) {
	if configFile != nil {
		if _, _, _, err := schemaconfig.Locate(servicePath); err != nil {
			return nil, err
		}
		return readConfigTS(w, configFile)
	}
	return schemaconfig.ReadFile(servicePath, w.reg)
}

// configPackage is the authoring package that declares defineConfig and
// service.
const configPackage = "@superschematic/schema-config"

// sentinelFile is the base name of a generated service sentinel
// (sentinel.GeneratedFile).
const sentinelFile = "service.generated.ts"

// readConfigTS statically extracts the SchemaConfig from a schema.config.ts
// source file: the default export must be a defineConfig({...}) call from
// @superschematic/schema-config whose argument is an object literal, and the
// file's imports must pass checkConfigImports.
func readConfigTS(w *walker, file *astSourceFile) (*SchemaConfig, error) {
	if serr := checkConfigImports(w, file); serr != nil {
		return nil, serr
	}
	obj, stmt, serr := configObject(w, file)
	if serr != nil {
		return nil, serr
	}
	raw, serr := w.evaluateExpression(obj)
	if serr != nil {
		return nil, serr
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, errorAtNode(obj, "defineConfig argument must be an object literal")
	}
	return decodeConfig(fields, stmt, w.reg)
}

// readConfigIdentityTS reads only name and kind from a schema.config.ts. It
// evaluates no other key and does not check the imports, so a config whose
// handles name a sibling with no sentinel yet still yields the facts that
// sibling's sweep needs (D34).
func readConfigIdentityTS(w *walker, file *astSourceFile) (*SchemaConfig, error) {
	obj, _, serr := configObject(w, file)
	if serr != nil {
		return nil, serr
	}
	cfg := &SchemaConfig{}
	for _, p := range obj.AsObjectLiteralExpression().Properties.Nodes {
		if p.Kind != kindPropertyAssignment {
			continue
		}
		key, serr := w.evaluatePropertyName(p.Name(), 0)
		if serr != nil || (key != "name" && key != "kind") {
			continue
		}
		v, serr := w.evaluateExpression(p.AsPropertyAssignment().Initializer)
		if serr != nil {
			return nil, serr
		}
		text, ok := v.(string)
		if !ok {
			return nil, errorAtNode(p, "defineConfig %s must be a string", key)
		}
		if key == "name" {
			cfg.Name = text
		} else {
			cfg.Kind = ir.SchemaKind(text)
		}
	}
	return schemaconfig.ValidateShapeWith(cfg, w.reg)
}

// configObject finds the object literal a schema.config.ts default-exports
// through defineConfig, and the export statement. The argument may reach the
// literal through a const or an `as` expression, as the evaluator reads it.
func configObject(w *walker, file *astSourceFile) (*astNode, *astNode, *SchemaError) {
	for _, stmt := range file.Statements.Nodes {
		if stmt.Kind != kindExportAssignment {
			continue
		}
		expr := stmt.AsExportAssignment().Expression
		if expr == nil || expr.Kind != kindCallExpression {
			return nil, nil, errorAtNode(stmt, "schema.config.ts default export must be a defineConfig({...}) call")
		}
		call := expr.AsCallExpression()
		id, ok := w.identityOf(call.Expression)
		if !ok || !id.is(configPackage, "defineConfig") {
			return nil, nil, errorAtNode(expr, "schema.config.ts default export must call defineConfig from @superschematic/schema-config")
		}
		if call.Arguments == nil || len(call.Arguments.Nodes) != 1 {
			return nil, nil, errorAtNode(expr, "defineConfig takes exactly one object literal argument")
		}
		arg := call.Arguments.Nodes[0]
		for depth := 0; arg != nil && arg.Kind != kindObjectLiteralExpression && depth < maxEvalDepth; depth++ {
			switch arg.Kind {
			case kindAsExpression:
				arg = arg.AsAsExpression().Expression
			case kindIdentifier:
				arg = w.constInitializer(arg)
			default:
				arg = nil
			}
		}
		if arg == nil || arg.Kind != kindObjectLiteralExpression {
			return nil, nil, errorAtNode(expr, "defineConfig argument must be an object literal")
		}
		return arg, stmt, nil
	}
	return nil, nil, &SchemaError{Msg: fmt.Sprintf("%s has no default export", file.FileName())}
}

// constInitializer returns the initializer of the const an identifier names,
// or nil.
func (w *walker) constInitializer(node *astNode) *astNode {
	sym := w.checker.GetSymbolAtLocation(node)
	if sym == nil {
		return nil
	}
	decl := w.checker.SkipAlias(sym).ValueDeclaration
	if decl == nil || decl.Kind != kindVariableDeclaration {
		return nil
	}
	return decl.AsVariableDeclaration().Initializer
}

// checkConfigImports enforces D34's import rule on a schema.config.ts. Every
// binding a config imports is either declared in the config package, under
// whatever specifier resolves to it, or a service sentinel: a const whose
// initializer is a service(...) call, in another service's
// service.generated.ts, imported by name as a value. A default import, a
// type-only import, a schema class, a type, a namespace of any other module
// and a side-effect import are refused at the import, with a message that
// says which, so the commands that read configs (build, build-all,
// build --with-deps) accept the same ones.
func checkConfigImports(w *walker, file *astSourceFile) *SchemaError {
	for _, stmt := range file.Statements.Nodes {
		if stmt.Kind != kindImportDeclaration {
			continue
		}
		decl := stmt.AsImportDeclaration()
		spec := ""
		if decl.ModuleSpecifier != nil {
			spec = decl.ModuleSpecifier.Text()
		}
		if decl.ImportClause == nil {
			return errorAtNode(stmt, "schema.config.ts may not import %q for its side effects; %s", spec, configImportRule)
		}
		if name := decl.ImportClause.Name(); name != nil {
			return errorAtNode(name, "schema.config.ts imports %s from %q as a default import; a sentinel is a named export, and %s", name.Text(), spec, configImportRule)
		}
		typeOnly := decl.ImportClause.IsTypeOnly()
		named := decl.ImportClause.AsImportClause().NamedBindings
		if named == nil {
			continue
		}
		switch named.Kind {
		case kindNamespaceImport:
			if serr := w.checkConfigBinding(named.Name(), spec, typeOnly); serr != nil {
				return serr
			}
		case kindNamedImports:
			for _, element := range named.AsNamedImports().Elements.Nodes {
				if serr := w.checkConfigBinding(element.Name(), spec, typeOnly || element.IsTypeOnly()); serr != nil {
					return serr
				}
			}
		}
	}
	return nil
}

// configImportRule ends every refusal of a config import.
const configImportRule = "a config imports only " + configPackage + " and other services' sentinels (D34)"

// checkConfigBinding checks one imported name of a schema.config.ts. A
// binding of the config package passes under any form; anything else must be
// a sentinel imported as a value.
func (w *walker) checkConfigBinding(binding *astNode, spec string, typeOnly bool) *SchemaError {
	sym := w.checker.GetSymbolAtLocation(binding)
	if sym != nil {
		sym = w.checker.SkipAlias(sym)
	}
	if sym == nil || len(sym.Declarations) == 0 {
		return errorAtNode(binding, "schema.config.ts imports %s from %q, which does not resolve", binding.Text(), spec)
	}
	// A named binding resolves to its declaration in the config package
	// through any re-export; a namespace resolves to the module imported,
	// which may be a [package_aliases] alias of it.
	if id, ok := w.identityOfSymbol(sym); ok && w.reg.Naming().DeclaringPackage(id.pkg) == configPackage {
		return nil
	}
	if typeOnly {
		return errorAtNode(binding, "schema.config.ts imports %s from %q as a type only; a config names a sentinel as a value, and %s", binding.Text(), spec, configImportRule)
	}
	if w.isSentinel(sym) {
		return nil
	}
	return errorAtNode(binding, "schema.config.ts imports %s from %q, which is %s, not a service sentinel; %s", binding.Text(), spec, importedThing(sym), configImportRule)
}

// importedThing names what a refused import resolves to.
func importedThing(sym *astSymbol) string {
	switch {
	case sym.Flags&symbolFlagsModule != 0:
		return "a namespace of another module"
	case sym.Flags&symbolFlagsClass != 0:
		return "a class"
	case sym.Flags&symbolFlagsEnum != 0:
		return "an enum"
	case sym.Flags&(symbolFlagsInterface|symbolFlagsTypeAlias) != 0:
		return "a type"
	case sym.Flags&symbolFlagsFunction != 0:
		return "a function"
	}
	return "a value"
}

// isSentinel reports whether sym is a service sentinel: a const in a
// service.generated.ts whose initializer calls service from the config
// package.
func (w *walker) isSentinel(sym *astSymbol) bool {
	decl := sym.ValueDeclaration
	if decl == nil || decl.Kind != kindVariableDeclaration {
		return false
	}
	file := getSourceFileOfNode(decl)
	if file == nil || path.Base(file.FileName()) != sentinelFile {
		return false
	}
	init := decl.AsVariableDeclaration().Initializer
	if init == nil || init.Kind != kindCallExpression {
		return false
	}
	id, ok := w.identityOf(init.AsCallExpression().Expression)
	return ok && id.is(configPackage, "service")
}

// handleKeys are the defineConfig keys that hold service handles, and
// whether each holds a list of them. The TypeScript types take a handle
// there, which the JSON below cannot tell from a plain {name, kind} object,
// so the handles are checked before the conversion.
var handleKeys = []struct {
	key  string
	list bool
}{
	{"authDb", false},
	{"dependencies", true},
	{"calls", true},
	{"buckets", true},
	{"memoryStores", true},
	{"rateLimitStore", false},
}

// decodeConfig decodes the evaluated defineConfig object through the decoder
// the data forms use, so every form is held to the same JSON Schema and
// checks. Each handle becomes the {name, kind} object the data forms write,
// and authDb the name alone, as they write it. The authDb handle's kind,
// which that JSON cannot carry, is set on the decoded config and checked
// after.
func decodeConfig(obj map[string]any, at *astNode, reg *registry.Registry) (*SchemaConfig, error) {
	for _, h := range handleKeys {
		value, set := obj[h.key]
		if !set {
			continue
		}
		if !h.list {
			if _, ok := value.(serviceHandle); !ok {
				return nil, errorAtNode(at, "defineConfig %s must be a service({...}) sentinel, not %s", h.key, handleMiss(value))
			}
			continue
		}
		items, ok := value.([]any)
		if !ok {
			return nil, errorAtNode(at, "defineConfig %s must be a list of service({...}) sentinels, not %s", h.key, handleMiss(value))
		}
		for _, item := range items {
			if _, ok := item.(serviceHandle); !ok {
				return nil, errorAtNode(at, "defineConfig %s entries must be service({...}) sentinels, not %s", h.key, handleMiss(item))
			}
		}
	}
	doc := maps.Clone(obj)
	authDB, hasAuthDB := obj["authDb"].(serviceHandle)
	if hasAuthDB {
		doc["authDb"] = authDB.name
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, errorAtNode(at, "defineConfig: %v", err)
	}
	cfg, err := schemaconfig.DecodeJSON(data, "defineConfig", reg)
	if err != nil {
		return nil, errorAtNode(at, "%v", err)
	}
	if !hasAuthDB {
		return cfg, nil
	}
	cfg.AuthDBKind = ir.SchemaKind(authDB.kind)
	if _, err := schemaconfig.ValidateShapeWith(cfg, reg); err != nil {
		return nil, errorAtNode(at, "%v", err)
	}
	return cfg, nil
}

// handleMiss names what a handle key holds in place of a handle.
func handleMiss(v any) string {
	if _, ok := v.(string); ok {
		return "a string"
	}
	return describeValue(v)
}

package tsreader

import (
	"fmt"
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

// readConfig reads the service config, preferring the TypeScript form. The
// walker argument supplies the checker used for the static defineConfig read;
// it is only required for the TypeScript form. The JSON and YAML forms are
// validated against the JSON Schema generated from @superschematic/schema-config and
// decoded by the schemaconfig package.
func readConfig(w *walker, servicePath string, configFile *astSourceFile) (*SchemaConfig, error) {
	if configFile != nil {
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
	return configFromMap(fields, stmt, w.reg)
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
// service.generated.ts. A schema class, a type, a namespace of any other
// module and a side-effect import are refused at the import, so the
// commands that read configs (build, build-all, build --with-deps) accept
// the same ones.
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
			return errorAtNode(stmt, "schema.config.ts may not import %q for its side effects; a config imports only %s and other services' sentinels", spec, configPackage)
		}
		var bindings []*astNode
		if name := decl.ImportClause.Name(); name != nil {
			bindings = append(bindings, name)
		}
		if named := decl.ImportClause.AsImportClause().NamedBindings; named != nil {
			switch named.Kind {
			case kindNamespaceImport:
				bindings = append(bindings, named.Name())
			case kindNamedImports:
				for _, element := range named.AsNamedImports().Elements.Nodes {
					bindings = append(bindings, element.Name())
				}
			}
		}
		for _, binding := range bindings {
			if serr := w.checkConfigBinding(binding, spec); serr != nil {
				return serr
			}
		}
	}
	return nil
}

// checkConfigBinding checks one imported name of a schema.config.ts.
func (w *walker) checkConfigBinding(binding *astNode, spec string) *SchemaError {
	sym := w.checker.GetSymbolAtLocation(binding)
	if sym != nil {
		sym = w.checker.SkipAlias(sym)
	}
	if sym == nil || len(sym.Declarations) == 0 {
		return errorAtNode(binding, "schema.config.ts imports %s from %q, which does not resolve", binding.Text(), spec)
	}
	if id, ok := w.identityOfSymbol(sym); ok && id.pkg == configPackage {
		return nil
	}
	if w.isSentinel(sym) {
		return nil
	}
	return errorAtNode(binding, "schema.config.ts imports %s from %q, which is neither from %s nor a service sentinel; a config imports only the config package and other services' handles (D34)", binding.Text(), spec, configPackage)
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

// configFromMap converts the evaluated defineConfig object into a
// SchemaConfig; kinds are checked against the registry.
func configFromMap(obj map[string]any, at *astNode, reg *registry.Registry) (*SchemaConfig, error) {
	cfg := &SchemaConfig{}

	name, _ := obj["name"].(string)
	cfg.Name = name

	kind, _ := obj["kind"].(string)
	cfg.Kind = ir.SchemaKind(kind)

	if public, ok := obj["public"].(bool); ok {
		cfg.Public = public
	}
	if authDB, ok := obj["authDb"].(serviceHandle); ok {
		cfg.AuthDB = authDB.name
		cfg.AuthDBKind = ir.SchemaKind(authDB.kind)
	}
	var err *SchemaError
	if cfg.Dependencies, err = handleList(obj, "dependencies", at); err != nil {
		return nil, err
	}
	if cfg.Calls, err = handleList(obj, "calls", at); err != nil {
		return nil, err
	}
	if outputs, ok := obj["outputs"].(map[string]any); ok {
		cfg.Outputs = outputs
	}

	return schemaconfig.ValidateShapeWith(cfg, reg)
}

// handleList reads a config key that holds a list of service handles.
func handleList(obj map[string]any, key string, at *astNode) ([]ServiceDependency, *SchemaError) {
	items, ok := obj[key].([]any)
	if !ok {
		return nil, nil
	}
	var deps []ServiceDependency
	for _, item := range items {
		handle, ok := item.(serviceHandle)
		if !ok {
			return nil, errorAtNode(at, "%s entries must be service({...}) sentinels", key)
		}
		deps = append(deps, ServiceDependency{Name: handle.name, Kind: ir.SchemaKind(handle.kind)})
	}
	return deps, nil
}

package tsreader

import (
	"fmt"

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

// readConfigTS statically extracts the SchemaConfig from a schema.config.ts
// source file: the default export must be a defineConfig({...}) call from
// @superschematic/schema-config whose argument is an object literal.
func readConfigTS(w *walker, file *astSourceFile) (*SchemaConfig, error) {
	for _, stmt := range file.Statements.Nodes {
		if stmt.Kind != kindExportAssignment {
			continue
		}
		expr := stmt.AsExportAssignment().Expression
		if expr == nil || expr.Kind != kindCallExpression {
			return nil, errorAtNode(stmt, "schema.config.ts default export must be a defineConfig({...}) call")
		}
		call := expr.AsCallExpression()
		id, ok := w.identityOf(call.Expression)
		if !ok || !id.is("@superschematic/schema-config", "defineConfig") {
			return nil, errorAtNode(expr, "schema.config.ts default export must call defineConfig from @superschematic/schema-config")
		}
		if call.Arguments == nil || len(call.Arguments.Nodes) != 1 {
			return nil, errorAtNode(expr, "defineConfig takes exactly one object literal argument")
		}
		raw, serr := w.evaluateExpression(call.Arguments.Nodes[0])
		if serr != nil {
			return nil, serr
		}
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, errorAtNode(expr, "defineConfig argument must be an object literal")
		}
		return configFromMap(obj, stmt, w.reg)
	}
	return nil, fmt.Errorf("%s has no default export", file.FileName())
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

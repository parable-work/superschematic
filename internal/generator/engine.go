package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/enginedoc"
	"github.com/parable-work/superschematic/internal/generator/projectengine"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// enginedocGenerator is the name of the core generator of a DB schema's
// engine document (D58).
const enginedocGenerator = "enginedoc"

// projectengineGenerator is the name of the core generator of a schema's
// project engine crate (D58).
const projectengineGenerator = "projectengine"

// engineOutputSchema is the JSON Schema of outputs.engine. The namespace's
// own rule is enginedoc.Namespace's, which names the setting when it
// refuses one. mount is a path of one or more segments, which a server of
// an API on the database mounts the engine's routes under; policy is the
// engine's access policy, a shorthand or its rules, which the engine
// checks when a server opens it; retention is the event log's, the
// engine's RetentionOptions with its ranges, an age, a count or both,
// checked here so that no entrypoint opens an engine that refuses it
// (D58); tools.invocation is the MCP invocation policy of a built-in tool
// or a behavior operation by name, which enginedoc checks against the
// binary's policy and the behaviors the engine types compose, and requires
// for each commerce operation that moves money or stock; tools.hidden
// lists behavior operations the tools leave out (D70).
var engineOutputSchema = []byte(`{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"namespace": {"type": "string", "minLength": 1},
		"mount": {"type": "string", "pattern": "^(/[A-Za-z0-9._~-]+)+$"},
		"policy": {"oneOf": [
			{"enum": ["permission", "allowAll"]},
			{
				"type": "object",
				"required": ["rules"],
				"additionalProperties": false,
				"properties": {"rules": {"type": "array", "items": {"type": "object"}}}
			}
		]},
		"tools": {
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"invocation": {
					"type": "object",
					"propertyNames": {"pattern": "^(?:[A-Za-z][A-Za-z0-9_]*|(?:[A-Za-z][A-Za-z0-9_-]*\\.)?[A-Z][A-Za-z0-9]*\\.[A-Za-z][A-Za-z0-9_]*)$"},
					"additionalProperties": {"type": "string", "minLength": 1}
				},
				"hidden": {
					"type": "array",
					"items": {"type": "string", "pattern": "^(?:[A-Za-z][A-Za-z0-9_-]*\\.)?[A-Z][A-Za-z0-9]*\\.[A-Za-z][A-Za-z0-9_]*$"},
					"uniqueItems": true
				}
			}
		},
		"retention": {
			"type": "object",
			"additionalProperties": false,
			"anyOf": [{"required": ["maxAgeMs"]}, {"required": ["maxEvents"]}],
			"properties": {
				"maxAgeMs": {"type": "integer", "minimum": 1, "maximum": 9007199254740991},
				"maxEvents": {"type": "integer", "minimum": 1, "maximum": 9007199254740991},
				"maxHoldMs": {"type": "integer", "minimum": 1, "maximum": 9007199254740991},
				"everyMs": {"type": "integer", "minimum": 1000, "maximum": 9007199254740991},
				"batchSize": {"type": "integer", "minimum": 1, "maximum": 10000}
			}
		}
	}
}`)

// buildEngineDocument builds the run's engine document once
// (GenerateContext.EngineDocument): nil when the schema's kind has no
// tables or no type of it composes a behavior.
func (m *runMemo) buildEngineDocument() ([]byte, error) {
	if m.engineDocReady {
		return m.engineDocument, nil
	}
	r := m.r
	if !r.hasEngineTypes() {
		m.engineDocReady = true
		return nil, nil
	}
	deps, err := r.loadDependencySchemas()
	if err != nil {
		return nil, err
	}
	doc, err := enginedoc.Build(r.Schema, enginedoc.Options{
		SchemaName:   r.Config.Name,
		Namespace:    r.Outputs.EngineNamespace(),
		Registry:     r.Registry,
		Dependencies: deps,
		Invocation:   r.Outputs.EngineInvocation(),
		Hidden:       r.Outputs.EngineHidden(),
	})
	if err != nil {
		return nil, fmt.Errorf("generator: engine document for %s: %w", r.Config.Name, err)
	}
	data, err := doc.JSON()
	if err != nil {
		return nil, fmt.Errorf("generator: engine document for %s: %w", r.Config.Name, err)
	}
	m.engineDocument, m.engineDocReady = data, true
	return data, nil
}

// engineDocument is the run's engine document, or nil when the context
// builds none.
func (r run) engineDocument() ([]byte, error) {
	if r.EngineDocument == nil {
		return nil, nil
	}
	return r.EngineDocument()
}

// EngineDocumentPath is where the enginedoc generator writes a DB
// service's engine document: engine/<service>.json in its Go types
// module, beside identity/ and versiongraph/.
func EngineDocumentPath(root, schemaName string) string {
	return filepath.Join(TypesDir(root, LangGo, schemaName), enginedoc.Dir, schemaName+".json")
}

// generateEngineDocument writes the engine document of a DB schema with
// engine types into its Go types module, and removes the one an earlier
// build wrote when the schema has none now. The types generator holds the
// same document as a constant in each language.
func (r run) generateEngineDocument() error {
	data, err := r.engineDocument()
	if err != nil {
		return err
	}
	path := EngineDocumentPath(r.Options.OutputRoot, r.Config.Name)
	if data == nil {
		return removeStaleEngineDocument(path)
	}
	if !r.Outputs.TypesEnabled(LangGo) {
		r.Logf("  - engine: outputs.types.go is off, so no Go types module holds engine/%s.json; the types of the other languages hold the document\n", r.Config.Name)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("generator: engine document for %s: %w", r.Config.Name, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("generator: engine document for %s: %w", r.Config.Name, err)
	}
	r.Done("engine", path)
	return nil
}

// removeStaleEngineDocument removes an engine document a schema no longer
// writes, and its directory when nothing else is left in it.
func removeStaleEngineDocument(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("generator: remove the stale %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("generator: remove the empty %s: %w", dir, err)
		}
	}
	return nil
}

// engineDatabase is the run's API's engine database (D58): the DB service
// the API connects to, its authDb or its one DB-kind dependency, when a
// type of it composes a behavior, with what outputs.engine of its config
// sets. deps are the run's loaded dependencies, which hold the database
// when the API names it among them; one its config names as its authDb
// alone is loaded. Nil when the API connects to no database, its database
// holds no engine type, or the build cannot load it, which the generators
// that need it report.
func (r run) engineDatabase(deps map[string]*ir.Schema) (*apigen.EngineInfo, error) {
	db, _, ok := r.Schema.Database()
	if !ok {
		return nil, nil
	}
	schema := deps[db]
	if schema == nil {
		if r.Options.LoadDependency == nil || r.Config.AuthDB != db {
			return nil, nil
		}
		loaded, err := r.LoadDependency(db)
		if err != nil {
			return nil, nil
		}
		schema = loaded
	}
	if kind, ok := r.Registry.Kind(string(schema.Kind)); !ok || kind.StructRole != ir.RoleDBTable {
		return nil, nil
	}
	types := codegen.EngineTypes(schema)
	if len(types) == 0 {
		return nil, nil
	}
	var outputs *registry.Outputs
	if r.Options.DependencyConfig != nil {
		if cfg, ok := r.Options.DependencyConfig(db); ok {
			parsed, err := registry.ParseOutputs(cfg.Outputs, r.Registry)
			if err != nil {
				return nil, fmt.Errorf("generator: schema config for %s: %w", db, err)
			}
			outputs = parsed
		}
	}
	namespace, err := enginedoc.Namespace(db, outputs.EngineNamespace())
	if err != nil {
		return nil, fmt.Errorf("generator: %s, the database of %s: %w", db, r.Config.Name, err)
	}
	info := &apigen.EngineInfo{
		Database:  db,
		Namespace: namespace,
		Mount:     outputs.EngineMount(db),
		Policy:    outputs.EnginePolicy(),
		Retention: outputs.EngineRetention(),
	}
	for _, td := range types {
		info.Types = append(info.Types, td.Name)
	}
	for _, b := range enginedoc.Behaviors(types, r.Registry) {
		info.Behaviors = append(info.Behaviors, apigen.EngineBehavior{Name: b.Name, Extension: b.Extension, Package: b.Package})
	}
	return info, nil
}

// generateProjectEngine writes the project's engine crate of a schema
// whose types compose a behavior of the work queue or of an extension into
// engine/rust/<schema> (internal/generator/projectengine), and removes the
// one an earlier build wrote when the schema needs none now. An extension
// behavior that names no Rust crate, or one the project does not hold,
// leaves the schema with no engine that can run it, and the log says so:
// an engine refuses it.
func (r run) generateProjectEngine() error {
	dir := EngineDir(r.Options.OutputRoot, LangRust, r.Config.Name)
	crate, why, err := projectengine.Build(r.Schema, r.projectEngineOptions(r.Config.Name, dir))
	if err != nil {
		return fmt.Errorf("generator: engine crate for %s: %w", r.Config.Name, err)
	}
	if crate == nil {
		if why != "" {
			r.Logf("  - engine-rust: %s\n", why)
		}
		return projectengine.RemoveStale(dir, r.Options.OutputRoot)
	}
	if err := crate.Write(dir); err != nil {
		return fmt.Errorf("generator: engine crate for %s: %w", r.Config.Name, err)
	}
	r.Done("engine-rust", dir)
	return nil
}

// projectEngineOptions are the options the project's engine crate of the
// schema named schema, in dir, is built with: the build's registry,
// naming, project and [paths].
func (r run) projectEngineOptions(schema, dir string) projectengine.Options {
	return projectengine.Options{
		SchemaName:     schema,
		Registry:       r.Registry,
		Naming:         r.Options.Naming,
		RepositoryRoot: r.Options.RepositoryRoot,
		Paths:          r.Options.Paths,
		Dir:            dir,
		RustVersion:    servergen.RustVersion,
	}
}

// projectEngine is the project's engine crate of the engine database db
// (D58), which db's own build writes (generateProjectEngine), for an
// entrypoint whose engine opens on it: nil when the core's engine archive
// runs every behavior db composes. what names the entrypoint and the API
// it serves in the error of a database whose behaviors no Rust engine
// runs, an extension's that names no Rust crate or one the project does
// not hold.
func (r run) projectEngine(what, db string) (*servergen.EngineProject, error) {
	schema, err := r.LoadDependency(db)
	if err != nil {
		return nil, fmt.Errorf("%s, whose engine database is %s: %w", what, db, err)
	}
	dir := EngineDir(r.Options.OutputRoot, LangRust, db)
	crate, why, err := projectengine.Build(schema, r.projectEngineOptions(db, dir))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if crate == nil {
		if why != "" {
			return nil, fmt.Errorf("%s, whose engine database is %s, and %s", what, db, why)
		}
		return nil, nil
	}
	project := &servergen.EngineProject{
		Database: db,
		Dir:      dir,
		Lockfile: servergen.EngineLockfile(r.Options.Paths),
		Crate:    crate.Name,
		Ident:    crate.Ident,
		Archive:  "lib" + strings.ReplaceAll(crate.FFIName(), "-", "_") + ".a",
	}
	for _, ext := range crate.Extensions {
		project.Crates = append(project.Crates, filepath.Clean(filepath.Join(dir, filepath.FromSlash(ext.Path))))
	}
	return project, nil
}

// linkEngineProject records project, the project's engine crate of an
// engine the entrypoint opens, as the one its binary links, or refuses a
// second: a binary links one engine archive, and each project's holds the
// behaviors of its own database alone.
func linkEngineProject(what string, linked **servergen.EngineProject, project *servergen.EngineProject) error {
	if project == nil {
		return nil
	}
	if *linked != nil && (*linked).Dir != project.Dir {
		return fmt.Errorf("%s opens the engines of %s and %s, which each compose behaviors only their own project's engine archive runs, and a binary links one engine archive; serve the APIs on them from servers of their own", what, (*linked).Database, project.Database)
	}
	*linked = project
	return nil
}

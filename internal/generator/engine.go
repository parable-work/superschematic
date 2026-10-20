package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/parable-work/superschematic/internal/generator/enginedoc"
	"github.com/parable-work/superschematic/internal/generator/projectengine"
	"github.com/parable-work/superschematic/internal/generator/servergen"
)

// enginedocGenerator is the name of the core generator of a DB schema's
// engine document (D58).
const enginedocGenerator = "enginedoc"

// projectengineGenerator is the name of the core generator of a schema's
// project engine crate (D58).
const projectengineGenerator = "projectengine"

// engineOutputSchema is the JSON Schema of outputs.engine. The namespace's
// own rule is enginedoc.Namespace's, which names the setting when it
// refuses one.
var engineOutputSchema = []byte(`{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"namespace": {"type": "string", "minLength": 1}
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

// generateProjectEngine writes the project's engine crate of a schema
// whose types compose an extension's behavior into engine/rust/<schema>
// (internal/generator/projectengine), and removes the one an earlier build
// wrote when the schema needs none now. An extension behavior that names
// no Rust crate, or one the project does not hold, leaves the schema with
// no engine that can run it, and the log says so: an engine refuses it.
func (r run) generateProjectEngine() error {
	dir := EngineDir(r.Options.OutputRoot, LangRust, r.Config.Name)
	crate, why, err := projectengine.Build(r.Schema, projectengine.Options{
		SchemaName:     r.Config.Name,
		Registry:       r.Registry,
		Naming:         r.Options.Naming,
		RepositoryRoot: r.Options.RepositoryRoot,
		Paths:          r.Options.Paths,
		Dir:            dir,
		RustVersion:    servergen.RustVersion,
	})
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

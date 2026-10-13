package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/parable-work/superschematic/internal/generator/enginedoc"
)

// enginedocGenerator is the name of the core generator of a DB schema's
// engine document (D58).
const enginedocGenerator = "enginedoc"

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

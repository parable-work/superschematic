package migrateplan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/writer"
	ir "github.com/parable-work/superschematic/ir"
)

// Recorded is what a runtime database records of its last publish (D117)
// that a move to compiled mode takes: the DB service, the schema
// documents published, and the model the publish's plan took the
// database to, the model a build's first plan starts from.
type Recorded struct {
	Service   string
	Documents []json.RawMessage
	Model     *sqlmigrate.Model
}

// ModelFile is the file Export writes the recorded model to, which `migrate
// plan --from` takes.
const ModelFile = "model.json"

// Export writes a runtime database's schemas as a compiled project's DB
// service (D117): schema.config.ts, which names the service, its kind and
// its dialect; one src/<name>.schema.ts per published document, the tables
// format --to=ts writes, a General document of development mode's as its
// table; and model.json, the model the database recorded. Runtime to
// compiled moves no rows: the build's first plan, from model.json, is
// empty unless the compiler changed, and the generated servers take the
// tables over. The files are returned by their paths in the service
// directory, which the caller writes.
//
// A document is named after its instance type, or its first type, in
// kebab case (Note is note.schema.ts).
func Export(recorded *Recorded, env Env) (map[string][]byte, error) {
	if recorded.Model == nil {
		return nil, fmt.Errorf("the database records no model to export")
	}
	if err := CheckModel(recorded.Model, recorded.Service, recorded.Model.Dialect); err != nil {
		return nil, err
	}
	sources, err := schemaFiles(recorded.Documents)
	if err != nil {
		return nil, err
	}
	env.Service = recorded.Service
	schema, model, err := Documents(sources, recorded.Model.Dialect, env)
	if err != nil {
		return nil, fmt.Errorf("the recorded documents: %w", err)
	}
	if got, want := hashOf(model), hashOf(recorded.Model); got != want {
		return nil, fmt.Errorf("the recorded documents resolve to model %s, and the database recorded model %s: this compiler plans them otherwise than the one that published them", got, want)
	}

	dir, err := os.MkdirTemp("", "superschematic-export-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	written, err := writer.WriteService(schema, writer.FormatTS, dir)
	if err != nil {
		return nil, fmt.Errorf("writing the schemas in TypeScript: %w", err)
	}
	files := map[string][]byte{}
	for _, rel := range written {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		files[rel] = data
	}
	files["schema.config.ts"] = compiledConfig(schema.Name, recorded.Model.Dialect, env)
	canonical, err := recorded.Model.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	files[ModelFile] = append(canonical, '\n')
	return files, nil
}

// hashOf is a model's hash, or the error that left it with none.
func hashOf(model *sqlmigrate.Model) string {
	hash, err := model.Hash()
	if err != nil {
		return err.Error()
	}
	return hash
}

// schemaFiles names each published document after the file Export writes
// it to, src/<name>.schema.json, which the types it declares are owned by.
func schemaFiles(documents []json.RawMessage) ([]Source, error) {
	taken := map[string]int{}
	sources := make([]Source, len(documents))
	for i, doc := range documents {
		var head struct {
			Name  string                     `json:"name"`
			Types map[string]json.RawMessage `json:"types"`
		}
		if err := json.Unmarshal(doc, &head); err != nil {
			return nil, fmt.Errorf("documents[%d]: not a schema document: %w", i, err)
		}
		base := kebab(fileType(head.Name, head.Types))
		if base == "" {
			base = "schema"
		}
		taken[base]++
		if n := taken[base]; n > 1 {
			base = fmt.Sprintf("%s-%d", base, n)
		}
		sources[i] = Source{Name: "src/" + base + ".schema.json", Data: doc}
	}
	return sources, nil
}

// fileType is the type a document's file is named after: the type named
// like the document, else its first type in name order, else the
// document's name.
func fileType(name string, types map[string]json.RawMessage) string {
	if _, ok := types[name]; ok {
		return name
	}
	names := make([]string, 0, len(types))
	for typeName := range types {
		names = append(names, typeName)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return name
}

// kebab writes a type's name in kebab case: OrderLine is order-line.
func kebab(name string) string {
	var b strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case unicode.IsUpper(r):
			if i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1])) && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// compiledConfig is the exported service's schema.config.ts: a DB with Go
// types, as every DB has, built for the database's dialect.
func compiledConfig(service string, dialect sqlmigrate.Dialect, env Env) []byte {
	config := env.Naming.OrDefault().Specifier("@superschematic/schema-config")
	var b strings.Builder
	fmt.Fprintf(&b, "import { defineConfig, SchemaKind, TargetLanguage } from %q;\n\n", config)
	b.WriteString("// A runtime database's schemas, exported as a compiled project's DB service\n")
	b.WriteString("// (D117). Its first plan from model.json, the model the database recorded, is\n")
	b.WriteString("// empty, so the generated servers take its tables over as they are.\n")
	b.WriteString("export default defineConfig({\n")
	fmt.Fprintf(&b, "  name: %q,\n  kind: SchemaKind.%s,\n", service, ir.SchemaKindDB)
	b.WriteString("  outputs: {\n    types: { [TargetLanguage.Go]: { enabled: true } }")
	if dialect == sqlmigrate.SQLite {
		// A DB lists postgres, which its Go ORM runs on, beside sqlite.
		b.WriteString(",\n    sql: { dialects: [\"postgres\", \"sqlite\"] }")
	}
	b.WriteString("\n  }\n});\n")
	return []byte(b.String())
}

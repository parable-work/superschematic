package sqlgen

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// WriteDDL writes create.sql and drop.sql into outputDir.
func WriteDDL(output *DDLOutput, outputDir string) error {
	files := []codegen.ConditionalFile{
		{Condition: true, Template: "create.tmpl", Filename: "create.sql"},
		{Condition: true, Template: "drop.tmpl", Filename: "drop.sql"},
	}
	return codegen.WriteConditionalFiles(files, outputDir, func(templateName, outputPath string) error {
		return generateFile(templateName, outputPath, output)
	})
}

// ProjectionsSubdir is the directory under the SQL output where the
// projection Arrow schemas and docs files are written, and the default
// parent of their migrations.
const ProjectionsSubdir = "projections"

// WriteProjections writes every projection's migration pair into
// migrationsDir and its Arrow schema and docs file under
// outputDir/projections. A schema without projections gets neither
// directory.
func WriteProjections(schema *ir.Schema, output *DDLOutput, outputDir, migrationsDir string) error {
	if output == nil || len(output.Projections) == 0 {
		return nil
	}
	artifactsDir := filepath.Join(outputDir, ProjectionsSubdir)
	for _, dir := range []string{artifactsDir, migrationsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	for _, view := range output.Projections {
		files, err := ProjectionMigrationFiles(view)
		if err != nil {
			return err
		}
		for _, file := range files {
			if err := os.WriteFile(filepath.Join(migrationsDir, file.Name), file.Content, 0o644); err != nil {
				return err
			}
		}
		arrow, err := ArrowSchemaJSON(schema, output.SchemaName, view, output.MetadataKeyPrefix)
		if err != nil {
			return fmt.Errorf("projection %s: arrow schema: %w", view.Relation, err)
		}
		if err := os.WriteFile(filepath.Join(artifactsDir, view.Relation+".arrow.json"), arrow, 0o644); err != nil {
			return err
		}
		docs, err := DocsJSON(output.SchemaName, view)
		if err != nil {
			return fmt.Errorf("projection %s: docs: %w", view.Relation, err)
		}
		if err := os.WriteFile(filepath.Join(artifactsDir, view.Relation+".docs.json"), docs, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// MigrationFile is one file of a projection's migration pair: its name in
// the migrations directory and the bytes WriteProjections writes there.
type MigrationFile struct {
	Name    string
	Content []byte
}

// ProjectionMigrationFiles renders view's migration pair, the up file then
// the down file, as WriteProjections writes them. A build records their
// bytes under the stamp, which names them for good (D102).
func ProjectionMigrationFiles(view ProjectionView) ([]MigrationFile, error) {
	files := make([]MigrationFile, 0, 2)
	for _, file := range []struct{ tmpl, name string }{
		{"projection_up.tmpl", view.UpFileName},
		{"projection_down.tmpl", view.DownFileName},
	} {
		content, err := renderTemplate(file.tmpl, &view)
		if err != nil {
			return nil, fmt.Errorf("projection %s: %w", view.Relation, err)
		}
		files = append(files, MigrationFile{Name: file.name, Content: content})
	}
	return files, nil
}

// renderTemplate executes templateName with the funcs generateFile gives
// it and returns the bytes generateFile would write.
func renderTemplate(templateName string, data any) ([]byte, error) {
	text, err := templatesFS.ReadFile("templates/" + templateName)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(templateName).Funcs(codegen.BaseTemplateFuncs()).Funcs(customTemplateFuncs()).Parse(string(text))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", templateName, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template %s: %w", templateName, err)
	}
	return buf.Bytes(), nil
}

func generateFile(templateName, outputPath string, data any) error {
	cfg := codegen.NewFileConfig(templatesFS, templateName, outputPath, data, customTemplateFuncs())
	return codegen.GenerateFile(cfg)
}

// projectionViewDDL renders the CREATE VIEW statement and its comments for
// one projection, as create.sql and the migration write them: the view, a
// blank line, then each comment on its own line.
func projectionViewDDL(view ProjectionView) string {
	statements := ProjectionViewStatements(view)
	return statements[0] + ";\n\n" + strings.Join(statements[1:], ";\n") + ";"
}

// oneLine folds a schema comment onto one line for a SQL COMMENT.
func oneLine(doc string) string {
	return strings.Join(strings.Fields(doc), " ")
}

// rawSQLSection is the raw SQL create.sql runs at one place.
type rawSQLSection struct {
	Where  string
	Pieces []sqlutil.RawSQL
}

// rawSQLAt is the section of the pieces that run at position: after the
// tables, or after every object the schema declares.
func rawSQLAt(pieces []sqlutil.RawSQL, position string) rawSQLSection {
	section := rawSQLSection{Where: "after the tables"}
	if position == ir.RawSQLAfterIndexes {
		section.Where = "after every object above"
	}
	for _, piece := range pieces {
		if piece.Position == position {
			section.Pieces = append(section.Pieces, piece)
		}
	}
	return section
}

// rawSQLDowns lists the pieces with a down, the last one to run first.
func rawSQLDowns(pieces []sqlutil.RawSQL) []sqlutil.RawSQL {
	var out []sqlutil.RawSQL
	for i := len(pieces) - 1; i >= 0; i-- {
		if len(pieces[i].Down) > 0 {
			out = append(out, pieces[i])
		}
	}
	return out
}

func customTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"searchTextExpr": SearchTextExpr,
		// quote spells a derived name, an index's, a constraint's, a
		// function's or a trigger's, as a statement must: quoted when a
		// mixed-case @table or @column puts capitals in it (D85).
		"quote":             sqlutil.QuoteIdentifier,
		"searchIndexName":   SearchIndexName,
		"projectionViewDDL": projectionViewDDL,
		"rawSQLAt":          rawSQLAt,
		"rawSQLDowns":       rawSQLDowns,
		"reverse": func(tables []Table) []Table {
			reversed := make([]Table, len(tables))
			for i, t := range tables {
				reversed[len(tables)-1-i] = t
			}
			return reversed
		},
		"reverseHistoryTables": func(tables []HistoryTable) []HistoryTable {
			reversed := make([]HistoryTable, len(tables))
			for i, t := range tables {
				reversed[len(tables)-1-i] = t
			}
			return reversed
		},
		"reverseOptimisticTables": func(tables []OptimisticTable) []OptimisticTable {
			reversed := make([]OptimisticTable, len(tables))
			for i, t := range tables {
				reversed[len(tables)-1-i] = t
			}
			return reversed
		},
	}
}

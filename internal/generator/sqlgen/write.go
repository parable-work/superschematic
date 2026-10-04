package sqlgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
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
		for _, file := range []struct{ tmpl, name string }{
			{"projection_up.tmpl", view.UpFileName},
			{"projection_down.tmpl", view.DownFileName},
		} {
			if err := generateFile(file.tmpl, filepath.Join(migrationsDir, file.name), &view); err != nil {
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

func customTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"searchTextExpr":    SearchTextExpr,
		"projectionViewDDL": projectionViewDDL,
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

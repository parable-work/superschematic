package sqlgen

import (
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
// one projection. create.sql and the migration share it, so the two never
// disagree. security_barrier keeps a function in a reader's query from
// seeing rows the predicates exclude.
func projectionViewDDL(view ProjectionView) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"CREATE VIEW %s WITH (security_barrier = true) AS\n-- Projection column order is the published Arrow contract.\nSELECT",
		view.QualifiedView)
	if len(view.DistinctOn) > 0 {
		fmt.Fprintf(&b, " DISTINCT ON (%s)", strings.Join(view.DistinctOn, ", "))
	}
	b.WriteString(" -- noqa: ST06\n")
	for i, col := range view.Columns {
		sep := ","
		if i == len(view.Columns)-1 {
			sep = ""
		}
		// A column that keeps its source name is selected bare: sqlfluff
		// (AL09) refuses a self-alias, and generated migrations are linted
		// like hand-written ones.
		if col.SelfNamed {
			fmt.Fprintf(&b, "  %s%s\n", col.Expr, sep)
		} else {
			fmt.Fprintf(&b, "  %s AS %s%s\n", col.Expr, col.QuotedName, sep)
		}
	}
	fmt.Fprintf(&b, "FROM %s AS %s\n", view.QuotedBaseTable, view.QuotedBaseAlias)
	for _, j := range view.Joins {
		fmt.Fprintf(&b, "  %s JOIN %s AS %s ON %s\n", j.Kind, j.QuotedTable, j.QuotedAlias, j.On)
	}
	for i, pred := range view.Predicates {
		if i == 0 {
			fmt.Fprintf(&b, "WHERE %s", pred)
		} else {
			fmt.Fprintf(&b, "\n  AND %s", pred)
		}
	}
	if len(view.OrderBy) > 0 {
		fmt.Fprintf(&b, "\nORDER BY %s", strings.Join(view.OrderBy, ", "))
	}
	b.WriteString(";\n")
	doc := oneLine(view.Doc)
	if doc == "" {
		doc = "Generated from projection type: " + view.TypeName
	}
	fmt.Fprintf(&b, "\nCOMMENT ON VIEW %s IS %s;\n", view.QualifiedView, sqlLiteral(doc))
	for _, col := range view.Columns {
		if col.Doc == "" {
			continue
		}
		fmt.Fprintf(&b, "COMMENT ON COLUMN %s.%s IS %s;\n", view.QualifiedView, col.QuotedName, sqlLiteral(oneLine(col.Doc)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// oneLine folds a schema comment onto one line for a SQL COMMENT.
func oneLine(doc string) string {
	return strings.Join(strings.Fields(doc), " ")
}

// searchTextExpr generates the COALESCE expression for the search_text
// generated column: each field wrapped in COALESCE with an empty-string
// fallback, joined by a single space.
func searchTextExpr(fields []string) string {
	if len(fields) == 0 {
		return "''"
	}
	parts := make([]string, len(fields))
	for i, field := range fields {
		parts[i] = fmt.Sprintf("COALESCE(%s, '')", sqlutil.QuoteIdentifier(field))
	}
	return strings.Join(parts, " || ' ' || ")
}

func customTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"searchTextExpr":    searchTextExpr,
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
	}
}

package sqlgen

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
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

func generateFile(templateName, outputPath string, output *DDLOutput) error {
	cfg := codegen.NewFileConfig(templatesFS, templateName, outputPath, output, customTemplateFuncs())
	return codegen.GenerateFile(cfg)
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
		"searchTextExpr": searchTextExpr,
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

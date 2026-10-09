package sqlgen

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

// The derived objects create.sql writes, the functions, triggers and views
// the decorators add, render here, one statement each without its trailing
// semicolon. create.tmpl renders them through these functions, and the
// migration planner's model (internal/sqlmigrate) takes its definitions from
// them, so the two never disagree.

// objectTemplates holds the named templates of templates/objects.tmpl.
var objectTemplates = template.Must(
	template.New("objects.tmpl").Funcs(codegen.BaseTemplateFuncs()).ParseFS(templatesFS, "templates/objects.tmpl"),
)

func renderObject(name string, data any) (string, error) {
	var b strings.Builder
	if err := objectTemplates.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("sqlgen: render %s: %w", name, err)
	}
	return b.String(), nil
}

// Trigger is a row trigger create.sql writes.
type Trigger struct {
	Name string
	// Timing is when it fires: "BEFORE UPDATE", "AFTER INSERT OR UPDATE" or
	// "AFTER DELETE".
	Timing      string
	Table       string
	QuotedTable string
	// Function is the trigger function it executes, which takes no
	// arguments.
	Function string
}

// SQL renders the CREATE TRIGGER statement.
func (t Trigger) SQL() (string, error) {
	return renderObject("trigger", t)
}

// PruneFunctionArguments is the argument type list of a prune function, as
// DROP FUNCTION names it.
const PruneFunctionArguments = "INTEGER, INTEGER"

// CaptureFunctionSQL renders the history capture function: the CREATE OR
// REPLACE FUNCTION statement every history trigger of the table executes.
func (h HistoryTable) CaptureFunctionSQL() (string, error) {
	return renderObject("captureFunction", h)
}

// PruneFunctionSQL renders the prune function a retention adds.
func (h HistoryTable) PruneFunctionSQL() (string, error) {
	return renderObject("pruneFunction", h)
}

// Triggers returns the history triggers on the source table, in the order
// create.sql creates them.
func (h HistoryTable) Triggers() []Trigger {
	trigger := func(name, timing string) Trigger {
		return Trigger{
			Name:        name,
			Timing:      timing,
			Table:       h.SourceTableName,
			QuotedTable: h.QuotedSourceTableName,
			Function:    h.FunctionName,
		}
	}
	return []Trigger{
		trigger(h.VersionTriggerName, "BEFORE UPDATE"),
		trigger(h.WriteTriggerName, "AFTER INSERT OR UPDATE"),
		trigger(h.DeleteTriggerName, "AFTER DELETE"),
	}
}

// Partitioned reports whether the history table is partitioned.
func (h HistoryTable) Partitioned() bool {
	return h.PartitionBy == "month"
}

// PartitionClause is what create.sql writes after PARTITION BY, or "" for a
// table that is not partitioned.
func (h HistoryTable) PartitionClause() string {
	if !h.Partitioned() {
		return ""
	}
	return "RANGE (recorded_at)"
}

// Columns returns the history table's columns in create.sql order. A
// partitioned table has no primary key, since one would have to include
// recorded_at.
func (h HistoryTable) Columns() []Column {
	return []Column{
		{
			Name: "history_id", QuotedName: "history_id", Type: "UUID",
			DefaultValue: "gen_random_uuid()", IsPrimaryKey: !h.Partitioned(),
		},
		{Name: h.KeyColumn, QuotedName: h.QuotedKeyColumn, Type: h.KeyColumnType},
		{Name: "_version", QuotedName: "_version", Type: "BIGINT"},
		{Name: "operation", QuotedName: "operation", Type: "TEXT"},
		{Name: "data", QuotedName: "data", Type: "JSONB"},
		{Name: "recorded_at", QuotedName: "recorded_at", Type: "TIMESTAMPTZ", DefaultValue: "clock_timestamp()"},
	}
}

// BumpFunctionSQL renders an @optimistic table's version bump function.
func (o OptimisticTable) BumpFunctionSQL() (string, error) {
	return renderObject("bumpFunction", o)
}

// Trigger returns the trigger that runs the bump function.
func (o OptimisticTable) Trigger() Trigger {
	return Trigger{
		Name:        o.TriggerName,
		Timing:      "BEFORE UPDATE",
		Table:       o.TableName,
		QuotedTable: o.QuotedTableName,
		Function:    o.FunctionName,
	}
}

// SearchTextColumn is the generated column @searchField adds.
const SearchTextColumn = "search_text"

// SearchTextExpr is the expression of a table's search_text column: each
// search field wrapped in COALESCE with an empty-string fallback, joined by
// a single space.
func SearchTextExpr(fields []string) string {
	return SearchTextExprQuoted(fields, sqlutil.QuoteIdentifier)
}

// SearchTextExprQuoted is SearchTextExpr with each field quoted by quote,
// for a dialect whose keywords are not Postgres's.
func SearchTextExprQuoted(fields []string, quote func(string) string) string {
	if len(fields) == 0 {
		return "''"
	}
	parts := make([]string, len(fields))
	for i, field := range fields {
		parts[i] = fmt.Sprintf("COALESCE(%s, '')", quote(field))
	}
	return strings.Join(parts, " || ' ' || ")
}

// SearchIndexName is the name of the trigram index over a table's
// search_text column.
func SearchIndexName(table string) string {
	return "idx_" + table + "_search_trgm"
}

// ProjectionViewStatements renders the CREATE VIEW statement of one
// projection and its COMMENT statements, in order, each without its
// trailing semicolon. security_barrier keeps a function in a reader's query
// from seeing rows the predicates exclude.
func ProjectionViewStatements(view ProjectionView) []string {
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
	statements := []string{b.String()}

	doc := oneLine(view.Doc)
	if doc == "" {
		doc = "Generated from projection type: " + view.TypeName
	}
	statements = append(statements, fmt.Sprintf("COMMENT ON VIEW %s IS %s", view.QualifiedView, sqlLiteral(doc)))
	for _, col := range view.Columns {
		if col.Doc == "" {
			continue
		}
		statements = append(statements, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", view.QualifiedView, col.QuotedName, sqlLiteral(oneLine(col.Doc))))
	}
	return statements
}

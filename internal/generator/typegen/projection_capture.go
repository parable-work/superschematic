package typegen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
)

// ProjectionCaptureInfo is what projection_capture.go writes (D168): the
// two functions the naming file's [go.projection_capture] table names,
// over the views it selects.
type ProjectionCaptureInfo struct {
	// QueriesFunc and UUIDColumnsFunc are the functions' names; Setting
	// and Pool are the table's, which their comments name.
	QueriesFunc     string
	UUIDColumnsFunc string
	Setting         string
	Pool            string
	// Views are the selected views, by relation.
	Views []ProjectionCaptureView
}

// ProjectionCaptureView is one view the capture functions list, as Go
// literals.
type ProjectionCaptureView struct {
	// RelationLiteral is the relation a reader queries, pool.name.
	RelationLiteral string
	// SQLLiteral is the SELECT the view wraps (sqlgen.ProjectionSelectSQL).
	SQLLiteral string
	// UUIDColumnsLiteral is a []string of the columns whose Postgres type
	// is uuid, in column order, or nil when there is none.
	UUIDColumnsLiteral string
}

// buildProjectionCapture selects, from views, each view that reads exactly
// the table's setting and no optional setting, in the table's pool when it
// names one. It returns nil when the table is off or selects no view, so
// the module writes no projection_capture.go.
func buildProjectionCapture(cfg naming.GoProjectionCapture, views []sqlgen.ProjectionView) *ProjectionCaptureInfo {
	if !cfg.Enabled() {
		return nil
	}
	info := &ProjectionCaptureInfo{QueriesFunc: cfg.Queries, UUIDColumnsFunc: cfg.UUIDColumns, Setting: cfg.Setting, Pool: cfg.Pool}
	selected := make([]sqlgen.ProjectionView, 0, len(views))
	for _, view := range views {
		if len(view.Settings) != 1 || view.Settings[0] != cfg.Setting || len(view.OptionalSettings) != 0 {
			continue
		}
		if cfg.Pool != "" && view.Pool != cfg.Pool {
			continue
		}
		selected = append(selected, view)
	}
	if len(selected) == 0 {
		return nil
	}
	slices.SortFunc(selected, func(a, b sqlgen.ProjectionView) int { return strings.Compare(a.Relation, b.Relation) })
	for _, view := range selected {
		info.Views = append(info.Views, ProjectionCaptureView{
			RelationLiteral:    strconv.Quote(view.Relation),
			SQLLiteral:         strconv.Quote(sqlgen.ProjectionSelectSQL(view)),
			UUIDColumnsLiteral: uuidColumnsLiteral(view),
		})
	}
	return info
}

// uuidColumnsLiteral is the Go literal of the columns of view whose
// Postgres type is uuid: a field of a uuid scalar, or of a scalar that
// aliases one, which sqlutil.ColumnType types UUID. A list of them is
// UUID[], not uuid.
func uuidColumnsLiteral(view sqlgen.ProjectionView) string {
	var quoted []string
	for _, column := range view.Columns {
		if strings.EqualFold(column.Type, "uuid") {
			quoted = append(quoted, strconv.Quote(column.Name))
		}
	}
	if len(quoted) == 0 {
		return "nil"
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

// checkProjectionCaptureNames refuses capture functions whose names a
// type, a union or an enum of the module already declares, which Go would
// refuse as a duplicate declaration.
func checkProjectionCaptureNames(output *ModuleOutput) error {
	if output.ProjectionCapture == nil {
		return nil
	}
	taken := declaredTypeNames(output)
	for _, name := range []string{output.ProjectionCapture.QueriesFunc, output.ProjectionCapture.UUIDColumnsFunc} {
		if previous, ok := taken[name]; ok {
			return fmt.Errorf("typegen: go.projection_capture names the function %s, which %s already declares; rename one of them", name, previous)
		}
	}
	return nil
}

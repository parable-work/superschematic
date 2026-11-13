package sqlutil

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// A table's fences as its statements advance them (D118). A @fenced field
// is a counter every covered write sets to the stored value plus one, and
// a trigger refuses a write that does not, so the repositories advance it
// themselves: a statement whose columns are fixed, a soft delete's and a
// reseal's, carries the assignment (TableStatements), and a write whose
// columns its caller names, an update's, an UpdateMany's and an upsert's
// update, adds it where it builds its assignments, by one rule each
// language's repositories write once from the table's Fences. The rule is
// FenceBumps', over the assignments in place of the columns:
//
//   - A fence is advanced when the write sets a column it covers, or any
//     column when it lists no covers. Whether the value differs from the
//     stored one is not asked: the statement cannot know, and the trigger
//     allows the bump either way.
//   - A write that sets the fence's own column is written as it stands,
//     with no bump of that fence. Its caller is driving the counter, and
//     the trigger judges the value.
//
// A repository reads which columns a write sets off its assignments, each
// of which begins with its column and AssignmentInfix (Assigns).

// AssignmentInfix follows the column in every assignment sqlutil writes:
// Clauses.Set, SetNull and SetExcluded, TableStatements.UpdatedAt and
// UpdatedBy, UniqueKey.Keep and FenceModel.Bump.
const AssignmentInfix = " = "

// Assigns reports whether assignment, one sqlutil writes, sets column, as
// a statement names it.
func Assigns(assignment, column string) bool {
	return strings.HasPrefix(assignment, column+AssignmentInfix)
}

// FenceModel is a fence of a table: a @fenced field (D118).
type FenceModel struct {
	// Field is the fence's field.
	Field string
	// Column is the fence's column, and Covers the columns a write of which
	// advances it, in the order the declaration lists their fields, a to-one
	// relation as its key's column. Each is as a statement names it
	// (ColumnModel.Quoted). A fence with no Covers counts every update of
	// the row.
	Column string
	Covers []string
	// Bump is the assignment that advances the fence by one. It reads the
	// stored value by the table's name: an upsert's update also sees the row
	// it would have inserted, so the column alone names two values there.
	Bump string
}

// CoversRow reports whether the fence counts every update of the row.
func (f FenceModel) CoversRow() bool {
	return len(f.Covers) == 0
}

// fencesOf models td's fences over table, whose columns are read. A fence
// or a covered field that stores no column of the table is one
// verification refuses (verify's checkFences), and fails here.
func fencesOf(td *ir.TypeDef, table *TableModel) ([]FenceModel, error) {
	var fences []FenceModel
	for _, fence := range td.Fences {
		if fence == nil {
			continue
		}
		column := table.columnOfField(fence.Field)
		if column == nil {
			return nil, fmt.Errorf("@fenced on %s.%s: the field stores no column of table %s", td.Name, fence.Field, table.Name)
		}
		model := FenceModel{
			Field:  fence.Field,
			Column: column.Quoted,
			Bump:   column.Quoted + AssignmentInfix + column.Qualified + " + 1",
		}
		for _, name := range fence.Covers {
			covered := table.columnOfField(name)
			if covered == nil {
				return nil, fmt.Errorf("@fenced on %s.%s: covers lists %s, which stores no column of table %s", td.Name, fence.Field, name, table.Name)
			}
			model.Covers = append(model.Covers, covered.Quoted)
		}
		fences = append(fences, model)
	}
	return fences, nil
}

// columnOfField is the column that stores the field named field, a to-one
// relation's being its key's, or nil.
func (t *TableModel) columnOfField(field string) *ColumnModel {
	for i := range t.Columns {
		if t.Columns[i].Field == field {
			return &t.Columns[i]
		}
	}
	return nil
}

// FenceBumps are the assignments that advance the table's fences in a
// write that sets columns, each as a statement names it, in the fences'
// order (D118): the Bump of each fence that covers one of them, or of a
// fence with no covers when the write sets any column, but of no fence
// whose own column the write sets.
func (t *TableModel) FenceBumps(columns ...string) []string {
	var bumps []string
	for _, fence := range t.Fences {
		if slices.Contains(columns, fence.Column) {
			continue
		}
		if fence.CoversRow() && len(columns) > 0 || slices.ContainsFunc(fence.Covers, func(covered string) bool { return slices.Contains(columns, covered) }) {
			bumps = append(bumps, fence.Bump)
		}
	}
	return bumps
}

// fenced is the bumps of the fences a statement that sets columns
// advances, each after ", ", to follow the statement's own assignments;
// "" when it advances none.
func (t *TableModel) fenced(columns ...string) string {
	var out strings.Builder
	for _, bump := range t.FenceBumps(columns...) {
		out.WriteString(", " + bump)
	}
	return out.String()
}

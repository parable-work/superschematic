package sqlmigrate

import (
	"slices"
	"strings"
	"testing"
)

// TestARenamedVectorIsNoDimensionChange: a @table and an @column that
// rename a table and its vector column plan renames, each a compat hazard
// that names the override, and keep the column's type and its vectors: no
// step drops, adds or casts the vector, as the dimension change of a
// vector(3) of another name would (D84, D85).
func TestARenamedVectorIsNoDimensionChange(t *testing.T) {
	i := slices.IndexFunc(planCases, func(pc planCase) bool { return pc.name == "rename-vector-column-by-override" })
	if i < 0 {
		t.Fatal("no plan case rename-vector-column-by-override")
	}
	plan := planCases[i].plan(t)
	var renames []string
	for _, step := range plan.Steps {
		for _, h := range step.Hazards {
			if h.Class == HazardDestructive || h.Class == HazardDataDependent {
				t.Errorf("step %d (%s %s) is %s: %s", step.Index, step.Op, step.Subject, h.Class, h.Reason)
			}
		}
		for _, stmt := range step.Statements {
			if strings.Contains(stmt, "vector(") || strings.Contains(stmt, "DROP COLUMN") || strings.Contains(stmt, "ADD COLUMN") {
				t.Errorf("step %d (%s %s) rewrites the vector: %s", step.Index, step.Op, step.Subject, stmt)
			}
		}
		if strings.HasPrefix(step.Op, "rename") {
			renames = append(renames, step.Subject)
		}
	}
	for _, want := range []string{"table/catalogItem", "table/catalogItem/column/embeddingVector"} {
		if !slices.Contains(renames, want) {
			t.Errorf("renames = %v, want %s", renames, want)
		}
	}
}

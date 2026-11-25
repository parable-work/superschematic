package sqlutil

import (
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The merge of two rows of a Mergeable table (D147): mergeInto folds the
// row `from` into the row `into`. Its statements are written here, once,
// from the relations and the unique keys the build knows, and both ORMs run
// them as they are, in one transaction, on Postgres. They are no rule of
// the behaviors' core: a merge moves foreign keys, which the build
// enumerates and the core does not see.
//
// Every statement takes the key of `from` as $1 and the key of `into` as
// $2, and one whose Actor names an audit field the acting user as $3, null
// for none.

// MergePlan is what mergeInto of a Mergeable table runs.
type MergePlan struct {
	// Type is the table type whose rows merge.
	Type string

	// Lock reads the two rows, each locked for the transaction, live rows
	// alone on a soft-deletable table, in key order: a row per one it
	// finds, true for `from` and false for `into`. The merge refuses a row
	// it does not return.
	Lock string

	// Steps are the relations of the schema's tables that reference the
	// table, one per to-one relation, by source type and then field. A
	// behavior's sub-table is left out: its rows are the behavior's state
	// of `from`, and go with it.
	Steps []MergeStep

	// HistoryActor reports that a step deletes rows of a @versioned table
	// whose tombstone records its actor, which the repository sets for the
	// merge's transaction before the steps run and clears after.
	HistoryActor bool
}

// MergeStep is one relation that references the merged table.
type MergeStep struct {
	// Source is the referencing table's type, and Field its relation.
	Source string
	Field  string

	// SelfReference reads whether the row `into` references `from` through
	// this relation, on a relation of the table to itself: repointing it
	// would make `into` reference itself, so the merge refuses. Empty for
	// a relation of another table.
	SelfReference string

	// Collisions each delete the rows that reference `from` and that one
	// unique key of the source would refuse once they referenced `into`: a
	// row that references `into` already holds the key's other values. One
	// per unique key that holds the relation's column, in the key's order.
	Collisions []MergeStatement

	// Repoint moves every row that references `from` to `into`, deleted
	// rows among them, stamping updatedAt and updatedBy where the source
	// keeps them.
	Repoint MergeStatement
}

// MergeStatement is one statement of a merge.
type MergeStatement struct {
	SQL string
	// Actor is the audit field of the statement's table whose column
	// takes the acting user as $3, updatedBy or deletedBy; "" for a
	// statement that takes none.
	Actor string
}

// MergePlanOf writes the merge of the Mergeable table of type name among
// tables, the tables of schema (TablesOf).
func MergePlanOf(schema *ir.Schema, tables []TableModel, name string) (*MergePlan, error) {
	var target *TableModel
	for i := range tables {
		if tables[i].Type == name {
			target = &tables[i]
		}
	}
	if target == nil {
		return nil, fmt.Errorf("sqlutil: the Mergeable type %s is no table of %s", name, schema.Name)
	}
	key := QuoteIdentifier(target.Key)
	plan := &MergePlan{Type: name}
	plan.Lock = "SELECT (" + key + " = $1) FROM " + target.Quoted + " WHERE " + key + " IN ($1, $2)"
	if target.SoftDelete {
		plan.Lock += " AND " + target.AuditQuoted(AuditDeletedAt) + " IS NULL"
	}
	plan.Lock += " ORDER BY " + key + " FOR UPDATE"

	for i := range tables {
		source := &tables[i]
		td := schema.Types[source.Type]
		if td == nil || td.BehaviorTable != nil {
			continue
		}
		for c := range source.Columns {
			column := &source.Columns[c]
			if column.Relation != name {
				continue
			}
			step, err := mergeStep(td, source, column, name)
			if err != nil {
				return nil, err
			}
			for _, collision := range step.Collisions {
				// A delete of a @versioned table's row writes a tombstone
				// that records the actor the transaction names.
				if source.HistoryActor != "" && strings.Contains(collision.SQL, " DELETE FROM ") {
					plan.HistoryActor = true
				}
			}
			plan.Steps = append(plan.Steps, step)
		}
	}
	sort.SliceStable(plan.Steps, func(a, b int) bool {
		if plan.Steps[a].Source != plan.Steps[b].Source {
			return plan.Steps[a].Source < plan.Steps[b].Source
		}
		return plan.Steps[a].Field < plan.Steps[b].Field
	})
	return plan, nil
}

// mergeStep writes the step of the relation column of source, whose type is
// td, that references the merged table of type name.
func mergeStep(td *ir.TypeDef, source *TableModel, column *ColumnModel, name string) (MergeStep, error) {
	t := source.Quoted
	pk := Qualify(source.Name, source.Key)
	ref := column.Qualified
	step := MergeStep{Source: source.Type, Field: column.Field}
	if source.Type == name {
		step.SelfReference = "SELECT 1 FROM " + t + " WHERE " + pk + " = $2 AND " + ref + " = $1"
	}

	// The keys that hold the relation's column: the primary key, a @unique
	// field and each unique @index, as uniqueKeys lists them.
	seen := map[string]bool{}
	add := func(fields []string, idx *ir.IndexDef) error {
		keyName := strings.Join(fields, ",")
		if seen[keyName] {
			return nil
		}
		seen[keyName] = true
		var columns []*ColumnModel
		holds := false
		deletedAt := false
		for _, field := range fields {
			keyColumn := source.columnOfKey(field)
			if keyColumn == nil {
				return fmt.Errorf("sqlutil: unique key %q names no column of table %s", keyName, source.Name)
			}
			columns = append(columns, keyColumn)
			holds = holds || keyColumn.Name == column.Name
			deletedAt = deletedAt || keyColumn.Audit == AuditDeletedAt
		}
		if !holds {
			return nil
		}
		predicate := ""
		live := false
		if idx != nil {
			liveCondition := ""
			if source.SoftDelete && !deletedAt {
				liveCondition = source.AuditQuoted(AuditDeletedAt) + " IS NULL"
				live = true
			}
			predicate = IndexPredicate(idx.Where, liveCondition)
		}
		where := func(param string) string {
			out := " WHERE " + ref + " = " + param
			if predicate != "" {
				out += " AND (" + predicate + ")"
			}
			return out
		}
		equal := " = "
		if idx != nil && idx.NullsNotDistinct {
			equal = " IS NOT DISTINCT FROM "
		}
		var moved, held, match []string
		if idx != nil && idx.Expression != "" {
			// The expression reads the key's columns: over a row of the
			// moved row's values with the relation's column as `into`, it
			// is the key the row would hold once repointed.
			values := make([]string, len(columns))
			names := make([]string, len(columns))
			for i, keyColumn := range columns {
				names[i] = keyColumn.Quoted
				values[i] = keyColumn.Qualified
				if keyColumn.Name == column.Name {
					values[i] = "CAST($2 AS " + keyColumn.SQLType() + ")"
				}
			}
			moved = append(moved, "(SELECT ("+idx.Expression+") FROM (VALUES ("+strings.Join(values, ", ")+")) AS "+t+" ("+strings.Join(names, ", ")+")) AS k1")
			held = append(held, "("+idx.Expression+") AS k1")
			match = append(match, "moved.k1"+equal+"held.k1")
		} else {
			n := 0
			for _, keyColumn := range columns {
				if keyColumn.Name == column.Name {
					continue
				}
				n++
				alias := fmt.Sprintf("k%d", n)
				moved = append(moved, keyColumn.Qualified+" AS "+alias)
				held = append(held, keyColumn.Qualified+" AS "+alias)
				match = append(match, "moved."+alias+equal+"held."+alias)
			}
		}
		heldList := "1 AS k0"
		if len(held) > 0 {
			heldList = strings.Join(held, ", ")
		}
		on := ""
		if len(match) > 0 {
			on = " WHERE " + strings.Join(match, " AND ")
		}
		collide := "WITH moved AS (SELECT " + strings.Join(append([]string{pk + " AS merge_key"}, moved...), ", ") + " FROM " + t + where("$1") + "), " +
			"held AS (SELECT " + heldList + " FROM " + t + where("$2") + ")"
		rows := " WHERE " + pk + " IN (SELECT moved.merge_key FROM moved WHERE EXISTS (SELECT 1 FROM held" + on + "))"
		statement := MergeStatement{}
		if live {
			// The key holds live rows alone, so the stamp lifts the
			// collision, as the source's own delete would.
			deletedAtColumn, deletedByColumn := source.AuditQuoted(AuditDeletedAt), ""
			stamps := deletedAtColumn + " = now()"
			written := []string{deletedAtColumn}
			if source.DeletedBy {
				deletedByColumn = source.AuditQuoted(AuditDeletedBy)
				stamps += ", " + deletedByColumn + " = $3"
				written = append(written, deletedByColumn)
				statement.Actor = AuditDeletedBy
			}
			statement.SQL = collide + " UPDATE " + t + " SET " + stamps + source.fenced(written...) + rows
		} else {
			statement.SQL = collide + " DELETE FROM " + t + rows
		}
		step.Collisions = append(step.Collisions, statement)
		return nil
	}
	if err := add([]string{source.KeyField}, nil); err != nil {
		return MergeStep{}, err
	}
	for _, f := range td.Fields {
		if f.Unique && !f.Key {
			if err := add([]string{f.Name}, nil); err != nil {
				return MergeStep{}, err
			}
		}
	}
	for i := range td.Indexes {
		if idx := &td.Indexes[i]; idx.Unique && len(idx.Keys) > 0 {
			if err := add(append([]string(nil), idx.Keys...), idx); err != nil {
				return MergeStep{}, err
			}
		}
	}

	sets := column.Quoted + " = $2"
	written := []string{column.Quoted}
	if updatedAt := source.AuditQuoted(AuditUpdatedAt); source.AuditColumn(AuditUpdatedAt) != nil {
		sets += ", " + updatedAt + " = now()"
		written = append(written, updatedAt)
	}
	if updatedBy := source.AuditQuoted(AuditUpdatedBy); source.AuditColumn(AuditUpdatedBy) != nil {
		// With no acting user, the row keeps who updated it last, as an
		// update of the repository's does.
		sets += ", " + updatedBy + " = COALESCE($3, " + updatedBy + ")"
		written = append(written, updatedBy)
		step.Repoint.Actor = AuditUpdatedBy
	}
	step.Repoint.SQL = "UPDATE " + t + " SET " + sets + source.fenced(written...) + " WHERE " + ref + " = $1"
	return step, nil
}

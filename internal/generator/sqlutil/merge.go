package sqlutil

import (
	"fmt"
	"regexp"
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

	// Steps are the foreign keys of the schema that reference the table,
	// by source and then field: one per to-one relation of a table; one
	// per @hasMany of the table whose target declares no relation back,
	// whose column the build adds to the target; and one per @manyToMany
	// that names the table or that the table declares, whose rows are a
	// join table's. A behavior's sub-table is left out: its rows are the
	// behavior's state of `from`, and go with it, but for an Aliased use's
	// (D147): its aliases move to `into`, `from`'s name among them, and its
	// rows of the names table name `into` as their holder, so no name of
	// either row is freed or taken by the merge.
	Steps []MergeStep

	// RequiresUser reports that the merge stamps a row deleted, the row
	// merged away on a soft-deletable table or a refused row of one, which
	// needs an acting user as the repository's own soft delete does: the
	// merge refuses to start without one.
	RequiresUser bool

	// HistoryActor reports that a step deletes rows of a @versioned table
	// whose tombstone records its actor, which the repository sets for the
	// merge's transaction before the steps run and clears after.
	HistoryActor bool
}

// MergeStep is one relation that references the merged table.
type MergeStep struct {
	// Source is what the counts name the step's rows by, and Field what
	// holds the reference: the referencing table's type and its relation;
	// for a @hasMany's back-reference the target's type and the column
	// the build adds; and for a join table the declaring type with its
	// @manyToMany field, dotted (Board.labels), and the join table's
	// column.
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

	// Before are statements the step runs before its collisions and its
	// repoint, which the counts leave out: an Aliased use's insert of
	// `from`'s name as an alias of `into` (D147), which reads `from`'s rows
	// of the names table and its aliases before the steps move them.
	Before []MergeStatement

	// Repoint moves every row that references `from` to `into`, deleted
	// rows among them, stamping updatedAt and updatedBy where the source
	// keeps them. A row a collision stamped deleted is repointed by that
	// statement, so no row is counted by both.
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
	return mergePlanOf(schema, tables, name, false)
}

// mergePlanOf writes the merge, its statements Postgres's, but for the row
// an expression's collision reads, which on SQLite is a SELECT of named
// values, since SQLite names no VALUES list's columns.
func mergePlanOf(schema *ir.Schema, tables []TableModel, name string, sqlite bool) (*MergePlan, error) {
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
	plan.RequiresUser = target.SoftDelete

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
			if td.GraphMember != nil || td.VersionGraph != nil {
				// A commit holds the rows of a graph's tables as they
				// were: one made before the merge would bring back a
				// reference to the row merged away.
				return nil, fmt.Errorf("sqlutil: %s.%s references %s, which implements Mergeable, and %s is a version graph's table, whose rows a commit versions: a merge writes rows in place, so a commit made before it would still reference the row merged away; keep the reference in a table outside the graph, or leave Mergeable off %s (D147)", source.Type, column.Field, name, source.Type, name)
			}
			step, err := mergeStep(td, source, column, name, sqlite)
			if err != nil {
				return nil, err
			}
			for _, collision := range step.Collisions {
				// A delete of a @versioned table's row writes a tombstone
				// that records the actor the transaction names.
				if source.HistoryActor != "" && strings.Contains(collision.SQL, " DELETE FROM ") {
					plan.HistoryActor = true
				}
				// A stamp of a soft-deletable table's row needs a user, as
				// that table's own delete does.
				if strings.Contains(collision.SQL, " UPDATE ") {
					plan.RequiresUser = true
				}
			}
			plan.Steps = append(plan.Steps, step)
		}
	}
	plan.Steps = append(plan.Steps, mergeHiddenSteps(schema, tables, target)...)
	aliased, err := mergeAliasedSteps(schema, tables, target)
	if err != nil {
		return nil, err
	}
	plan.Steps = append(plan.Steps, aliased...)
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
func mergeStep(td *ir.TypeDef, source *TableModel, column *ColumnModel, name string, sqlite bool) (MergeStep, error) {
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
		// Two unique indexes over the same fields, apart in their where
		// or their expression, each hold rows of their own.
		seenAs := keyName
		if idx != nil {
			seenAs = fmt.Sprintf("%s|%s|%s|%t", keyName, idx.Where, idx.Expression, idx.NullsNotDistinct)
		}
		if seen[seenAs] {
			return nil
		}
		seen[seenAs] = true
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
			row := "(VALUES (" + strings.Join(values, ", ") + ")) AS " + t + " (" + strings.Join(names, ", ") + ")"
			if sqlite {
				named := make([]string, len(values))
				for i := range values {
					named[i] = values[i] + " AS " + names[i]
				}
				row = "(SELECT " + strings.Join(named, ", ") + ") AS " + t
			}
			moved = append(moved, "(SELECT ("+idx.Expression+") FROM "+row+") AS k1")
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
			// The stamped row is repointed here too: it is no live row, so
			// the index no longer holds it, and Repoint, which no longer
			// finds it, counts only the rows that stay.
			stamps += ", " + column.Quoted + " = $2"
			written = append(written, column.Quoted)
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
		idx := &td.Indexes[i]
		if !idx.Unique {
			continue
		}
		if len(idx.Keys) == 0 {
			// An index that lists no key gives the plan no way to tell
			// whether it holds the relation's column: a merge that met it
			// would fail on the database's unique violation.
			return MergeStep{}, fmt.Errorf("sqlutil: %s has a unique @index that lists no keys%s, and %s.%s references %s, which implements Mergeable: a merge cannot tell whether the index holds that relation's column; list the fields the index reads in its keys (D147)", source.Type, indexLabel(idx), source.Type, column.Field, name)
		}
		if err := add(append([]string(nil), idx.Keys...), idx); err != nil {
			return MergeStep{}, err
		}
	}
	step.Repoint = repointOf(source, column.Quoted, ref)
	return step, nil
}

// indexLabel names an index in a refusal: its name, or its expression.
func indexLabel(idx *ir.IndexDef) string {
	switch {
	case idx.Name != "":
		return " (" + idx.Name + ")"
	case idx.Expression != "":
		return " (over " + idx.Expression + ")"
	}
	return ""
}

// repointOf writes the statement that moves every row of source whose
// column references `from` to `into`, stamping updatedAt and updatedBy
// where source keeps them and advancing each fence the write covers.
func repointOf(source *TableModel, quoted, qualified string) MergeStatement {
	var out MergeStatement
	sets := quoted + " = $2"
	written := []string{quoted}
	if updatedAt := source.AuditQuoted(AuditUpdatedAt); source.AuditColumn(AuditUpdatedAt) != nil {
		sets += ", " + updatedAt + " = now()"
		written = append(written, updatedAt)
	}
	if updatedBy := source.AuditQuoted(AuditUpdatedBy); source.AuditColumn(AuditUpdatedBy) != nil {
		// With no acting user, the row keeps who updated it last, as an
		// update of the repository's does.
		sets += ", " + updatedBy + " = COALESCE($3, " + updatedBy + ")"
		written = append(written, updatedBy)
		out.Actor = AuditUpdatedBy
	}
	out.SQL = "UPDATE " + source.Quoted + " SET " + sets + source.fenced(written...) + " WHERE " + qualified + " = $1"
	return out
}

// SQLiteMergePlanOf writes the merge of the Mergeable table of type name in
// SQLite's dialect (D147's merges on SQLite): MergePlanOf's statements,
// each placeholder ?N where Postgres writes $N; the lock a read that takes
// none, since SQLite's one writer holds the database from a write
// transaction's first statement; the clock strftime's, as the queues' times
// hold it; a value compared with nulls alike by IS; and a key read into an
// expression's row as it is bound, since SQLite's columns are typed by
// affinity and a cast to Postgres's type would change it. A merge whose
// steps delete a @versioned table's rows, whose tombstones record their
// actor on Postgres alone, is refused.
func SQLiteMergePlanOf(schema *ir.Schema, tables []TableModel, name string) (*MergePlan, error) {
	plan, err := mergePlanOf(schema, tables, name, true)
	if err != nil {
		return nil, err
	}
	if plan.HistoryActor {
		return nil, fmt.Errorf("sqlutil: a merge of %s deletes rows of a @versioned table, whose tombstones record their actor on Postgres alone; it has no merge on SQLite (D147)", name)
	}
	out := *plan
	out.Lock = sqliteMergeSQL(strings.TrimSuffix(plan.Lock, " FOR UPDATE"))
	out.Steps = make([]MergeStep, len(plan.Steps))
	for i, step := range plan.Steps {
		s := step
		s.SelfReference = sqliteMergeSQL(step.SelfReference)
		s.Before = sqliteMergeStatements(step.Before)
		s.Collisions = sqliteMergeStatements(step.Collisions)
		s.Repoint.SQL = sqliteMergeSQL(step.Repoint.SQL)
		out.Steps[i] = s
	}
	return &out, nil
}

// sqliteMergeStatements are statements in SQLite's dialect.
func sqliteMergeStatements(in []MergeStatement) []MergeStatement {
	if in == nil {
		return nil
	}
	out := make([]MergeStatement, len(in))
	for i, statement := range in {
		out[i] = MergeStatement{SQL: sqliteMergeSQL(statement.SQL), Actor: statement.Actor}
	}
	return out
}

var (
	mergePlaceholder = regexp.MustCompile(`\$([0-9]+)`)
	mergeCast        = regexp.MustCompile(`CAST\(\?([0-9]+) AS [^)]+\)`)
)

// sqliteMergeSQL is one of a merge's statements in SQLite's dialect.
func sqliteMergeSQL(text string) string {
	if text == "" {
		return ""
	}
	text = mergePlaceholder.ReplaceAllString(text, "?$1")
	text = mergeCast.ReplaceAllString(text, "?$1")
	text = strings.ReplaceAll(text, " IS NOT DISTINCT FROM ", " IS ")
	return strings.ReplaceAll(text, "now()", sqliteNow)
}

// The behavior and the tables of an Aliased use whose rows a merge moves
// (D147), as its declaration names them.
const (
	aliasedBehavior = "Aliased"
	aliasedAliases  = "aliases"
	aliasedNames    = "names"
)

// mergeAliasedSteps are the steps of each Aliased use of target (D147): its
// aliases sub-table's, whose Before inserts `from`'s name as an alias of
// `into`, read from `from`'s rows of the names table that are no alias of
// `from`'s, and whose repoint moves `from`'s aliases to `into`; and its
// names table's, whose repoint names `into` the holder of `from`'s names,
// moving each row's version on, as the core's writes of it do. A name is
// one row's, so `from`'s names and `into`'s are apart, and no collision is
// possible. The aliases' step sorts before the names' (<Row><Member>Aliases
// before <Row><Member>Names), so the insert reads both as they were.
func mergeAliasedSteps(schema *ir.Schema, tables []TableModel, target *TableModel) ([]MergeStep, error) {
	byType := map[string]*TableModel{}
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	td := schema.Types[target.Type]
	if td == nil {
		return nil, nil
	}
	var steps []MergeStep
	for _, use := range td.FieldBehaviors() {
		if use.Name != aliasedBehavior {
			continue
		}
		var aliases, names *TableModel
		var aliasesType *ir.TypeDef
		for _, name := range sortedStringKeys(schema.Types) {
			sub := schema.Types[name]
			if sub == nil || sub.BehaviorTable == nil || sub.BehaviorTable.Row != target.Type || sub.BehaviorTable.Use != use.UseID() || sub.BehaviorTable.Behavior != aliasedBehavior {
				continue
			}
			switch sub.BehaviorTable.Table {
			case aliasedAliases:
				aliases, aliasesType = byType[name], sub
			case aliasedNames:
				names = byType[name]
			}
		}
		var value *ir.FieldDef
		for _, fd := range td.Fields {
			if fd != nil && fd.Name == use.Field {
				value = fd
			}
		}
		if aliases == nil || names == nil || aliasesType == nil || value == nil {
			return nil, fmt.Errorf("sqlutil: %s.%s is an Aliased use, and the schema holds no table of its aliases or its names, or no column of the name (D147)", target.Type, use.Field)
		}
		row := aliases.columnOfField(aliasesType.BehaviorTable.RowField)
		normalized := aliases.columnOfField(aliasesType.BehaviorTable.Key)
		alias := aliases.columnOfField("alias")
		key := names.columnOfField("normalized")
		holder := names.columnOfField("holder")
		name := target.columnOfField(value.Name)
		if row == nil || normalized == nil || alias == nil || key == nil || holder == nil || name == nil {
			return nil, fmt.Errorf("sqlutil: %s.%s is an Aliased use whose tables lack a column its merge moves (D147)", target.Type, use.Field)
		}
		insert := "INSERT INTO " + aliases.Quoted + " (" + row.Quoted + ", " + normalized.Quoted + ", " + alias.Quoted + ")" +
			" SELECT $2, held." + key.Quoted + ", merged." + name.Quoted +
			" FROM " + names.Quoted + " AS held, " + target.Quoted + " AS merged" +
			" WHERE merged." + QuoteIdentifier(target.Key) + " = $1 AND held." + holder.Quoted + " = $1" +
			" AND NOT EXISTS (SELECT 1 FROM " + aliases.Quoted + " AS kept WHERE kept." + row.Quoted + " = $1 AND kept." + normalized.Quoted + " = held." + key.Quoted + ")"
		steps = append(steps,
			MergeStep{
				Source:  aliases.Type,
				Field:   row.Field,
				Before:  []MergeStatement{{SQL: insert}},
				Repoint: MergeStatement{SQL: "UPDATE " + aliases.Quoted + " SET " + row.Quoted + " = $2 WHERE " + row.Quoted + " = $1"},
			},
			MergeStep{
				Source:  names.Type,
				Field:   holder.Field,
				Repoint: MergeStatement{SQL: "UPDATE " + names.Quoted + " SET " + holder.Quoted + " = $2, " + QuoteIdentifier(VersionColumn) + " = " + QuoteIdentifier(VersionColumn) + " + 1 WHERE " + holder.Quoted + " = $1"},
			},
		)
	}
	return steps, nil
}

// sortedStringKeys are a map's keys in order.
func sortedStringKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// mergeHiddenSteps are the steps of the foreign keys that reference target
// and that no table's columns model (D147), as sqlgen writes them: the
// column a @hasMany of target adds to a table that declares no relation
// back, <target's table>_id, and the two columns of a @manyToMany's join
// table, <declaring table>_<named table>, each <table>_id. A row of a join
// table is its pair, so a pair that would repeat is deleted and the rest
// repointed.
func mergeHiddenSteps(schema *ir.Schema, tables []TableModel, target *TableModel) []MergeStep {
	byType := map[string]*TableModel{}
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	var steps []MergeStep
	for i := range tables {
		owner := &tables[i]
		td := schema.Types[owner.Type]
		if td == nil || td.BehaviorTable != nil {
			continue
		}
		for _, f := range td.Fields {
			other := byType[f.TypeRef.Name]
			if other == nil || f.JsonField || f.TypeRef.IsMap || !f.TypeRef.IsArray {
				continue
			}
			switch {
			case f.ManyToMany:
				join := QuoteIdentifier(owner.Name + "_" + other.Name)
				left, right := QuoteIdentifier(owner.Name+"_id"), QuoteIdentifier(other.Name+"_id")
				if left == right {
					// A table's @manyToMany to itself names both columns
					// alike, which no DDL can hold.
					continue
				}
				side := func(moved, kept string) MergeStep {
					return MergeStep{
						Source:     owner.Type + "." + f.Name,
						Field:      strings.Trim(moved, `"`),
						Collisions: []MergeStatement{{SQL: "DELETE FROM " + join + " AS moved WHERE moved." + moved + " = $1 AND EXISTS (SELECT 1 FROM " + join + " AS held WHERE held." + moved + " = $2 AND held." + kept + " = moved." + kept + ")"}},
						Repoint:    MergeStatement{SQL: "UPDATE " + join + " SET " + moved + " = $2 WHERE " + moved + " = $1"},
					}
				}
				if other.Type == target.Type {
					steps = append(steps, side(right, left))
				}
				if owner.Type == target.Type {
					steps = append(steps, side(left, right))
				}
			case f.HasMany && owner.Type == target.Type:
				column := owner.Name + "_id"
				if other.Column(column) != nil {
					// The target declares the relation: its column is a
					// step of its own.
					continue
				}
				quoted := QuoteIdentifier(column)
				steps = append(steps, MergeStep{Source: other.Type, Field: column, Repoint: repointOf(other, quoted, Qualify(other.Name, column))})
			}
		}
	}
	return steps
}

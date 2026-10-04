package sqlmigrate

import (
	"fmt"
	"sort"
	"strings"
)

// renames maps the previous model's names to the new model's for the
// tables and columns the caller renames (--rename), and for what is named
// after them: a renamed table's history table and its partition, the join
// tables it is a side of and their <table>_id columns, and the <table>_id
// columns @hasMany adds to other tables (D27).
type renames struct {
	tableTo   map[string]string            // previous table -> new table
	tableFrom map[string]string            // new table -> previous table
	columnTo  map[string]map[string]string // previous table -> previous column -> new column
	colFrom   map[string]map[string]string // new table -> new column -> previous column
	// because names, for a rename the caller did not pass, the rename it
	// follows from ("purchase=order").
	because map[string]string
}

func newRenames() *renames {
	return &renames{
		tableTo:   map[string]string{},
		tableFrom: map[string]string{},
		columnTo:  map[string]map[string]string{},
		colFrom:   map[string]map[string]string{},
		because:   map[string]string{},
	}
}

// table returns the new name of the previous model's table prev.
func (r *renames) table(prev string) string {
	if name, ok := r.tableTo[prev]; ok {
		return name
	}
	return prev
}

// prevTable returns the previous name of the new model's table name.
func (r *renames) prevTable(name string) string {
	if prev, ok := r.tableFrom[name]; ok {
		return prev
	}
	return name
}

// column returns the new name of column prev of the previous model's table
// prevTable.
func (r *renames) column(prevTable, prev string) string {
	if name, ok := r.columnTo[prevTable][prev]; ok {
		return name
	}
	return prev
}

// prevColumn returns the previous name of column name of the new model's
// table table.
func (r *renames) prevColumn(table, name string) string {
	if prev, ok := r.colFrom[table][name]; ok {
		return prev
	}
	return name
}

func (r *renames) addTable(prev, name, because string) {
	r.tableTo[prev] = name
	r.tableFrom[name] = prev
	if because != "" {
		r.because["table:"+name] = because
	}
}

func (r *renames) addColumn(prevTable, prev, table, name, because string) {
	if r.columnTo[prevTable] == nil {
		r.columnTo[prevTable] = map[string]string{}
	}
	if r.colFrom[table] == nil {
		r.colFrom[table] = map[string]string{}
	}
	r.columnTo[prevTable][prev] = name
	r.colFrom[table][name] = prev
	if because != "" {
		r.because["column:"+table+"."+name] = because
	}
}

// resolveRenames checks each rename the caller passed and derives the
// renames that follow from them. A table rename names a table of the
// previous version that the new one does not have, and a table of the new
// version the previous one does not have; a column rename does the same
// for a column, of a table that is the same in both versions or renamed by
// a table rename.
func resolveRenames(from, to *Model, given []Rename) (*renames, error) {
	r := newRenames()
	fromTables, toTables := tablesByName(from), tablesByName(to)
	flag := func(rn Rename) string { return "--rename " + rn.From + "=" + rn.To }

	var tableRenames []Rename
	for _, rn := range given {
		_, _, fromColumn := strings.Cut(rn.From, ".")
		_, _, toColumn := strings.Cut(rn.To, ".")
		if fromColumn != toColumn {
			return nil, fmt.Errorf("sqlmigrate: %s: a rename is of a table (old=new) or of a column (table.old=table.new), not one to the other", flag(rn))
		}
		if fromColumn {
			continue
		}
		switch {
		case fromTables[rn.From] == nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the previous version has no table %s", flag(rn), rn.From)
		case toTables[rn.From] != nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the new version still has a table %s", flag(rn), rn.From)
		case toTables[rn.To] == nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the new version has no table %s", flag(rn), rn.To)
		case fromTables[rn.To] != nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the previous version already has a table %s", flag(rn), rn.To)
		case r.tableTo[rn.From] != "":
			return nil, fmt.Errorf("sqlmigrate: %s: table %s is renamed twice", flag(rn), rn.From)
		case r.tableFrom[rn.To] != "":
			return nil, fmt.Errorf("sqlmigrate: %s: two tables are renamed to %s", flag(rn), rn.To)
		}
		r.addTable(rn.From, rn.To, "")
		tableRenames = append(tableRenames, rn)
	}

	for _, rn := range given {
		prevTable, prevColumn, isColumn := strings.Cut(rn.From, ".")
		if !isColumn {
			continue
		}
		newTable, newColumn, _ := strings.Cut(rn.To, ".")
		ft, tt := fromTables[prevTable], toTables[newTable]
		switch {
		case ft == nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the previous version has no table %s", flag(rn), prevTable)
		case columnNamed(ft, prevColumn) == nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the previous version has no column %s", flag(rn), rn.From)
		case tt == nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the new version has no table %s", flag(rn), newTable)
		case r.table(prevTable) != newTable:
			return nil, fmt.Errorf("sqlmigrate: %s: table %s is %s in the new version, so the column's new name is %s.%s",
				flag(rn), prevTable, r.table(prevTable), r.table(prevTable), newColumn)
		case columnNamed(tt, newColumn) == nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the new version has no column %s", flag(rn), rn.To)
		case columnNamed(tt, prevColumn) != nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the new version still has a column %s.%s", flag(rn), newTable, prevColumn)
		case columnNamed(ft, newColumn) != nil:
			return nil, fmt.Errorf("sqlmigrate: %s: the previous version already has a column %s.%s", flag(rn), prevTable, newColumn)
		case r.columnTo[prevTable][prevColumn] != "":
			return nil, fmt.Errorf("sqlmigrate: %s: column %s is renamed twice", flag(rn), rn.From)
		case r.colFrom[newTable][newColumn] != "":
			return nil, fmt.Errorf("sqlmigrate: %s: two columns are renamed to %s", flag(rn), rn.To)
		}
		r.addColumn(prevTable, prevColumn, newTable, newColumn, "")
	}

	r.deriveRenames(from, to, tableRenames)
	return r, nil
}

// deriveRenames adds the renames that follow from the caller's table
// renames. Each one applies only when both versions agree: the old name is
// in the previous version and not the new one, and the new name the other
// way round.
func (r *renames) deriveRenames(from, to *Model, tableRenames []Rename) {
	fromTables, toTables := tablesByName(from), tablesByName(to)
	freeTable := func(prev, name string) bool {
		return prev != name && fromTables[prev] != nil && toTables[name] != nil &&
			toTables[prev] == nil && fromTables[name] == nil &&
			r.tableTo[prev] == "" && r.tableFrom[name] == ""
	}
	freeColumn := func(prevTable, prev, table, name string) bool {
		ft, tt := fromTables[prevTable], toTables[table]
		return prev != name && ft != nil && tt != nil &&
			columnNamed(ft, prev) != nil && columnNamed(tt, name) != nil &&
			columnNamed(tt, prev) == nil && columnNamed(ft, name) == nil &&
			r.columnTo[prevTable][prev] == "" && r.colFrom[table][name] == ""
	}
	sort.Slice(tableRenames, func(i, j int) bool { return tableRenames[i].From < tableRenames[j].From })

	for _, rn := range tableRenames {
		because := rn.From + "=" + rn.To
		ft, tt := fromTables[rn.From], toTables[rn.To]
		// The history table and its partitions.
		if ft.History != "" && tt.History != "" && freeTable(ft.History, tt.History) {
			r.addTable(ft.History, tt.History, because)
		}
		// The <table>_id columns @hasMany adds to other tables, and any
		// other <table>_id column whose foreign key references the table.
		for _, other := range from.Tables {
			if other.Kind != TableEntity {
				continue
			}
			for _, fk := range other.ForeignKeys {
				if fk.RefTable != rn.From || len(fk.Columns) != 1 || fk.Columns[0] != rn.From+"_id" {
					continue
				}
				table := r.table(other.Name)
				if freeColumn(other.Name, fk.Columns[0], table, rn.To+"_id") {
					r.addColumn(other.Name, fk.Columns[0], table, rn.To+"_id", because)
				}
			}
		}
	}

	// Join tables are named <left>_<right> after the tables they join, and
	// their columns <left>_id and <right>_id.
	for _, jt := range from.Tables {
		if jt.Kind != TableJoin || len(jt.Columns) != 2 {
			continue
		}
		left, right := joinSide(jt, jt.Columns[0].Name), joinSide(jt, jt.Columns[1].Name)
		if left == "" || right == "" || jt.Name != left+"_"+right {
			continue
		}
		newLeft, newRight := r.table(left), r.table(right)
		name := newLeft + "_" + newRight
		if name == jt.Name || !freeTable(jt.Name, name) {
			continue
		}
		because := r.renameOf(left)
		if newLeft == left {
			because = r.renameOf(right)
		}
		r.addTable(jt.Name, name, because)
		for _, side := range []struct{ prev, name string }{{left, newLeft}, {right, newRight}} {
			if side.prev != side.name && freeColumn(jt.Name, side.prev+"_id", name, side.name+"_id") {
				r.addColumn(jt.Name, side.prev+"_id", name, side.name+"_id", because)
			}
		}
	}
}

// renameOf returns the "old=new" of the rename table prev takes, or "".
func (r *renames) renameOf(prev string) string {
	if name, ok := r.tableTo[prev]; ok {
		if because := r.because["table:"+name]; because != "" {
			return because
		}
		return prev + "=" + name
	}
	return ""
}

// joinSide returns the table a join table's column references.
func joinSide(jt *Table, column string) string {
	for _, fk := range jt.ForeignKeys {
		if len(fk.Columns) == 1 && fk.Columns[0] == column {
			return fk.RefTable
		}
	}
	return ""
}

func tablesByName(m *Model) map[string]*Table {
	out := map[string]*Table{}
	for _, t := range m.Tables {
		out[t.Name] = t
	}
	return out
}

func columnNamed(t *Table, name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

package sqlmigrate

import (
	"fmt"
	"sort"
)

// steps orders the changes, renders them through the dialect and numbers
// the steps: every expand step, then every contract step, each phase in
// D27's order.
func (d *differ) steps() ([]*Step, error) {
	phaseOrder := map[Phase]int{Expand: 0, Contract: 1}
	sort.SliceStable(d.changes, func(i, j int) bool {
		a, b := d.changes[i], d.changes[j]
		if a.phase != b.phase {
			return phaseOrder[a.phase] < phaseOrder[b.phase]
		}
		if rank[a.op] != rank[b.op] {
			return rank[a.op] < rank[b.op]
		}
		if a.order != b.order {
			return a.order < b.order
		}
		if a.subject != b.subject {
			return a.subject < b.subject
		}
		return a.op < b.op
	})

	steps := []*Step{}
	for _, phase := range []Phase{Expand, Contract} {
		var changes []*change
		for _, c := range d.changes {
			if c.phase == phase {
				changes = append(changes, c)
			}
		}
		rebuilds := d.rebuilds(phase, changes)
		for _, c := range changes {
			var r rendered
			var hazards []*Hazard
			var err error
			rb := d.rebuildOf(c, rebuilds)
			switch {
			case rb != nil && rb.at != c:
				continue
			case rb != nil:
				r, err = d.dialect.rebuild(rb)
				for _, bc := range rb.changes {
					hazards = append(hazards, bc.hazards...)
				}
			default:
				r, err = d.dialect.render(c)
				hazards = c.hazards
			}
			if err != nil {
				return nil, err
			}
			if len(r.steps) == 0 {
				continue
			}
			if r.main < 0 || r.main >= len(r.steps) {
				return nil, fmt.Errorf("sqlmigrate: %s %s: the dialect named step %d of %d", c.op, c.subject, r.main, len(r.steps))
			}
			main := r.steps[r.main]
			main.Hazards = append(main.Hazards, hazards...)
			for _, step := range r.steps {
				step.Phase = phase
				sortHazards(step.Hazards)
				steps = append(steps, step)
			}
		}
	}
	for i, step := range steps {
		step.Index = i + 1
	}
	return steps, nil
}

// rebuilds groups the tables a phase rebuilds by copying them, by table
// name. A table the previous model has is rebuilt when the dialect cannot
// make one of the phase's changes to it in place. So is every table that
// references a rebuilt one, directly or through another table: dropping the
// old table would run the ON DELETE actions of the tables that reference
// it, and the copies of those tables reference the new one instead. The
// rebuilt tables that reference one another, before or after the phase,
// share one step, at the place of the first change among them the dialect
// cannot make, so a table is copied at most once per phase. The step makes
// every change the phase makes to its tables but the renames of the tables
// and their columns, including the drop of a table that contract drops.
func (d *differ) rebuilds(phase Phase, changes []*change) map[string]*tableRebuild {
	first := map[string]*change{}
	for _, c := range changes {
		if d.existing(c) && first[c.table] == nil && !d.dialect.canAlter(c) {
			first[c.table] = c
		}
	}
	if len(first) == 0 {
		return nil
	}
	tables := d.phaseTableSet(phase)
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)

	rebuilt := map[string]bool{}
	for name := range first {
		rebuilt[name] = true
	}
	for grew := true; grew; {
		grew = false
		for _, name := range names {
			if rebuilt[name] {
				continue
			}
			for _, fk := range tables[name].before.ForeignKeys {
				if rebuilt[fk.RefTable] {
					rebuilt[name] = true
					grew = true
					break
				}
			}
		}
	}

	// Rebuilt tables that reference one another share a step.
	group := map[string]string{}
	var root func(string) string
	root = func(name string) string {
		if group[name] == "" || group[name] == name {
			return name
		}
		r := root(group[name])
		group[name] = r
		return r
	}
	for _, name := range names {
		if !rebuilt[name] {
			continue
		}
		for _, t := range []*Table{tables[name].before, tables[name].after} {
			if t == nil {
				continue
			}
			for _, fk := range t.ForeignKeys {
				if a, b := root(name), root(fk.RefTable); rebuilt[fk.RefTable] && a != b {
					group[max(a, b)] = min(a, b)
				}
			}
		}
	}
	out := map[string]*tableRebuild{}
	byRoot := map[string]*tableRebuild{}
	for _, name := range names {
		if !rebuilt[name] {
			continue
		}
		rb := byRoot[root(name)]
		if rb == nil {
			rb = &tableRebuild{}
			byRoot[root(name)] = rb
		}
		rb.tables = append(rb.tables, tables[name])
		out[name] = rb
	}
	for _, c := range changes {
		rb := d.rebuildOf(c, out)
		if rb == nil {
			continue
		}
		if rb.at == nil && first[c.table] == c {
			rb.at = c
		}
		rb.changes = append(rb.changes, c)
	}
	return out
}

// rebuildOf returns the rebuild that makes c, or nil: c changes a table a
// rebuild copies, or drops one, and is not a rename of a table or of a
// column.
func (d *differ) rebuildOf(c *change, rebuilds map[string]*tableRebuild) *tableRebuild {
	rb := rebuilds[c.table]
	if rb == nil || c.op == opRenameTable || c.op == opRenameColumn {
		return nil
	}
	if c.op == opDropTable || d.existing(c) {
		return rb
	}
	return nil
}

// phaseTableSet returns the tables a phase may rebuild, by name, as the
// phase finds them and leaves them: the previous model's tables as their
// renames leave them in expand, the ones contract drops among them, and
// every table expand leaves in contract, with no after for the ones
// contract drops. The tables expand creates are left out of expand: no
// server writes to them before the rollout, so dropping a table they
// reference runs no ON DELETE action, and their keys name the new table
// once it takes the name.
func (d *differ) phaseTableSet(phase Phase) map[string]*rebuiltTable {
	out := map[string]*rebuiltTable{}
	for _, tt := range d.to.Tables {
		switch {
		case d.fromTables[d.renames.prevTable(tt.Name)] != nil:
			before, after := d.phaseTables(tt.Name, phase)
			out[tt.Name] = &rebuiltTable{name: tt.Name, before: before, after: after}
		case phase == Contract:
			out[tt.Name] = &rebuiltTable{name: tt.Name, before: tt, after: tt}
		}
	}
	for _, ft := range d.from.Tables {
		if !d.dropped[ft.Name] {
			continue
		}
		t := d.renamedTable(ft)
		rt := &rebuiltTable{name: t.Name, before: t}
		if phase == Expand {
			rt.after = t
		}
		out[t.Name] = rt
	}
	return out
}

// existing reports whether c changes a table the previous model has, as
// opposed to one the plan creates or an object outside any table.
func (d *differ) existing(c *change) bool {
	return c.table != "" && !c.created && d.fromTables[d.renames.prevTable(c.table)] != nil && !d.dropped[c.table]
}

// phaseTables returns a table as the given phase finds it and as it leaves
// it, for a dialect that rebuilds the table. Expand finds the previous
// model's table as its renames leave it, since they run first, and leaves
// the new table with what contract still removes or tightens: the columns
// it drops, the constraints and indexes it drops, the nullability and
// defaults it tightens. Contract finds that and leaves the new model's
// table.
func (d *differ) phaseTables(table string, phase Phase) (before, after *Table) {
	tt := d.toTables[table]
	ft := d.fromTables[d.renames.prevTable(table)]
	middle := d.betweenPhases(ft, tt)
	if phase == Expand {
		return d.renamedTable(ft), middle
	}
	return middle, tt
}

// renamedTable is a table of the previous model with the renames applied:
// its name, its columns, the columns its key, constraints and indexes
// cover, and the tables and columns its foreign keys reference.
func (d *differ) renamedTable(ft *Table) *Table {
	t := *ft
	t.Name = d.renames.table(ft.Name)
	t.Columns = nil
	for _, col := range ft.Columns {
		c := *col
		c.Name = d.renames.column(ft.Name, col.Name)
		t.Columns = append(t.Columns, &c)
	}
	if ft.PrimaryKey != nil {
		t.PrimaryKey = d.renamedConstraint(ft.Name, ft.PrimaryKey)
	}
	t.Uniques = nil
	for _, u := range ft.Uniques {
		t.Uniques = append(t.Uniques, d.renamedConstraint(ft.Name, u))
	}
	t.ForeignKeys = nil
	for _, fk := range ft.ForeignKeys {
		t.ForeignKeys = append(t.ForeignKeys, d.renamedFK(ft.Name, fk))
	}
	t.Indexes = nil
	for _, idx := range ft.Indexes {
		t.Indexes = append(t.Indexes, d.renamedIndex(ft.Name, idx))
	}
	return &t
}

// renamedColumns maps columns of the previous model's table prevTable to
// their new names.
func (d *differ) renamedColumns(prevTable string, columns []string) []string {
	out := make([]string, len(columns))
	for i, col := range columns {
		out[i] = d.renames.column(prevTable, col)
	}
	return out
}

func (d *differ) renamedConstraint(prevTable string, c *Constraint) *Constraint {
	out := *c
	out.Columns = d.renamedColumns(prevTable, c.Columns)
	return &out
}

func (d *differ) renamedIndex(prevTable string, idx *Index) *Index {
	out := *idx
	out.Columns = d.renamedColumns(prevTable, idx.Columns)
	return &out
}

func (d *differ) renamedFK(prevTable string, fk *ForeignKey) *ForeignKey {
	out := *fk
	out.Columns = d.renamedColumns(prevTable, fk.Columns)
	out.RefTable = d.renames.table(fk.RefTable)
	out.RefColumns = d.renamedColumns(fk.RefTable, fk.RefColumns)
	return &out
}

// betweenPhases is the table between expand and contract: the new table
// with what contract still drops or tightens put back. A column contract
// drops is nullable when expand drops its NOT NULL (dropColumn), and keeps
// it when it has a default or is generated. A foreign key contract replaces
// is the previous one under the name expand gave it.
func (d *differ) betweenPhases(ft, tt *Table) *Table {
	t := *tt
	t.Columns = nil
	for _, col := range tt.Columns {
		c := *col
		for _, ch := range d.changes {
			if ch.phase != Contract || ch.table != tt.Name || ch.column == nil || ch.column.Name != col.Name {
				continue
			}
			switch ch.op {
			case opSetNotNull:
				c.Nullable = true
			case opDropDefault:
				if fc := columnNamed(ft, d.renames.prevColumn(tt.Name, col.Name)); fc != nil {
					c.Default = fc.Default
				}
			}
		}
		t.Columns = append(t.Columns, &c)
	}
	t.Uniques = append([]*Constraint(nil), tt.Uniques...)
	t.Indexes = append([]*Index(nil), tt.Indexes...)
	t.ForeignKeys = nil
	contractFKs := map[string]*ForeignKey{}
	for _, ch := range d.changes {
		if ch.phase != Contract || ch.table != tt.Name {
			continue
		}
		switch ch.op {
		case opDropColumn:
			c := *ch.column
			if c.Default == "" && c.Generated == "" {
				c.Nullable = true
			}
			t.Columns = append(t.Columns, &c)
		case opDropUnique:
			t.Uniques = append(t.Uniques, d.renamedConstraint(ft.Name, ch.constraint))
		case opDropIndex:
			t.Indexes = append(t.Indexes, d.renamedIndex(ft.Name, ch.index))
		case opAddForeignKey:
			contractFKs[ch.foreignKey.Name] = nil
		case opReplaceFK:
			old := d.renamedFK(ft.Name, ch.oldFK)
			old.Name = ch.foreignKey.Name
			contractFKs[ch.foreignKey.Name] = old
		case opDropForeignKey:
			contractFKs[ch.foreignKey.Name] = d.renamedFK(ft.Name, ch.foreignKey)
		}
	}
	for _, fk := range tt.ForeignKeys {
		if old, ok := contractFKs[fk.Name]; ok {
			if old != nil {
				t.ForeignKeys = append(t.ForeignKeys, old)
			}
			delete(contractFKs, fk.Name)
			continue
		}
		t.ForeignKeys = append(t.ForeignKeys, fk)
	}
	for _, old := range contractFKs {
		if old != nil {
			t.ForeignKeys = append(t.ForeignKeys, old)
		}
	}
	sort.Slice(t.ForeignKeys, func(i, j int) bool { return t.ForeignKeys[i].Name < t.ForeignKeys[j].Name })
	return &t
}

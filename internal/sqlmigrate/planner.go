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
		// The changes a dialect cannot make in place on a table that
		// exists are rebuilt together, at the first one's place.
		rebuilds := map[string][]*change{}
		for _, c := range changes {
			if d.existing(c) && !d.dialect.canAlter(c) {
				rebuilds[c.table] = append(rebuilds[c.table], c)
			}
		}
		done := map[string]bool{}
		for _, c := range changes {
			var r rendered
			var hazards []*Hazard
			var err error
			if batch := rebuilds[c.table]; d.existing(c) && len(batch) > 0 && !d.dialect.canAlter(c) {
				if done[c.table] {
					continue
				}
				done[c.table] = true
				before, after := d.phaseTables(c.table, phase)
				r, err = d.dialect.rebuild(before, after, batch)
				for _, bc := range batch {
					hazards = append(hazards, bc.hazards...)
				}
			} else {
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

// existing reports whether c changes a table the previous model has, as
// opposed to one the plan creates or an object outside any table.
func (d *differ) existing(c *change) bool {
	return c.table != "" && !c.created && d.fromTables[d.renames.prevTable(c.table)] != nil && !d.dropped[c.table]
}

// phaseTables returns a table as the given phase finds it and as it leaves
// it, for a dialect that rebuilds the table. Expand finds the previous
// model's table under its new name and leaves the new table with what
// contract still removes or tightens: the columns it drops (nullable), the
// constraints and indexes it drops, the nullability and defaults it
// tightens. Contract finds that and leaves the new model's table.
func (d *differ) phaseTables(table string, phase Phase) (before, after *Table) {
	tt := d.toTables[table]
	ft := d.fromTables[d.renames.prevTable(table)]
	middle := d.betweenPhases(ft, tt)
	if phase == Expand {
		renamed := *ft
		renamed.Name = table
		return &renamed, middle
	}
	return middle, tt
}

// betweenPhases is the table between expand and contract: the new table
// with what contract still drops or tightens put back.
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
			c.Nullable = true
			t.Columns = append(t.Columns, &c)
		case opDropUnique:
			t.Uniques = append(t.Uniques, ch.constraint)
		case opDropIndex:
			t.Indexes = append(t.Indexes, ch.index)
		case opAddForeignKey:
			contractFKs[ch.foreignKey.Name] = nil
		case opReplaceFK:
			contractFKs[ch.foreignKey.Name] = ch.oldFK
		case opDropForeignKey:
			contractFKs[ch.foreignKey.Name] = ch.foreignKey
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

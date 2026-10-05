package sqlmigrate

import (
	"fmt"
	"sort"
	"strings"
)

// The shared hazards: the diff computes destructive, compat,
// data-dependent, api-breaking and history the same way for every dialect.
// A reason names the schema field a column stores where it has one.

// fieldOf names a column by the schema field it stores ("Order.total"), or
// by table and column when the generator added it.
func fieldOf(t *Table, c *Column) string {
	if c.Origin != "" {
		return c.Origin
	}
	return t.Name + "." + c.Name
}

// originNote is " (Order.total)" for a column that stores a field, else "".
func originNote(c *Column) string {
	if c == nil || c.Origin == "" {
		return ""
	}
	return " (" + c.Origin + ")"
}

// valuesOf says whose values a dropped column holds, ending the sentence.
func valuesOf(t *Table, c *Column) string {
	if c.Origin != "" {
		return "the values of " + c.Origin + "."
	}
	return "its values."
}

// uniqueWhat names what a unique constraint or index makes unique.
func uniqueWhat(t *Table, columns []string) string {
	if len(columns) == 1 {
		if c := columnNamed(t, columns[0]); c != nil {
			return fieldOf(t, c)
		}
	}
	return t.Name + " (" + strings.Join(columns, ", ") + ")"
}

func fkReason(t *Table, fk *ForeignKey) string {
	return fmt.Sprintf("Validating %s fails when a row of %s has a %s that %s(%s) does not hold.",
		fk.Name, t.Name, strings.Join(fk.Columns, ", "), fk.RefTable, strings.Join(fk.RefColumns, ", "))
}

// breaksReaders adds an api-breaking hazard to c for every read of
// table.column (any column of table when column is empty) by a reader live
// at c's phase: the readers before the rollout for an expand step, those
// after it for a contract step. table and column are the previous model's
// names; a contract step is checked against the new names too, which a
// reader of the new version reads by.
func (d *differ) breaksReaders(c *change, table, column, verb string) {
	readers := d.opts.ReadersBefore
	if c.phase == Contract {
		readers = d.opts.ReadersAfter
	}
	names := [][2]string{{table, column}}
	if c.phase == Contract && column != "" {
		names = append(names, [2]string{d.renames.table(table), d.renames.column(table, column)})
	} else if c.phase == Contract {
		names = append(names, [2]string{d.renames.table(table), ""})
	}
	matches := func(read Read) bool {
		for _, name := range names {
			if read.Table == name[0] && (name[1] == "" || read.Column == name[1]) {
				return true
			}
		}
		return false
	}
	reads := append([]Read(nil), readers...)
	sort.SliceStable(reads, func(i, j int) bool {
		if reads[i].Reader != reads[j].Reader {
			return reads[i].Reader < reads[j].Reader
		}
		return reads[i].Via < reads[j].Via
	})
	for _, read := range reads {
		if !matches(read) {
			continue
		}
		c.addHazard(HazardAPIBreaking, read.Reader+"/"+read.Via, fmt.Sprintf(
			"%s reads %s.%s through %s, and this step %s.", read.Reader, read.Table, read.Column, read.Via, verb))
	}
}

// graphContent adds a history hazard to c when it changes a content column
// of a version graph member: before is the column in the previous model
// (nil for an add), after in the new one (nil for a drop).
func (d *differ) graphContent(c *change, ft *Table, before *Column, tt *Table, after *Column) {
	fromGraph, fromContent := graphOf(d.from, ft)
	toGraph, toContent := graphOf(d.to, tt)
	var field string
	switch {
	case before != nil && fromContent[before.Name]:
		field = fieldOf(ft, before)
	case after != nil && toContent[after.Name]:
		field = fieldOf(tt, after)
	default:
		return
	}
	graph := toGraph
	if graph == nil {
		graph = fromGraph
	}
	c.addHazard(HazardHistory, "", fmt.Sprintf(
		"%s is content of version graph %s: commits made before this change hash and merge rows of the old shape. %s",
		field, graph.Name, epochNote(fromGraph, toGraph)))
}

// epochNote says whether a version graph's schemaEpoch rose between the
// two models. One of the graphs is nil when the table is a member in one
// model only.
func epochNote(from, to *Graph) string {
	switch {
	case from == nil:
		return fmt.Sprintf("The graph's schemaEpoch stays %d.", to.SchemaEpoch)
	case to == nil || to.SchemaEpoch == from.SchemaEpoch:
		return fmt.Sprintf("The graph's schemaEpoch stays %d.", from.SchemaEpoch)
	case to.SchemaEpoch > from.SchemaEpoch:
		return fmt.Sprintf("The graph's schemaEpoch rose from %d to %d.", from.SchemaEpoch, to.SchemaEpoch)
	}
	return fmt.Sprintf("The graph's schemaEpoch fell from %d to %d.", from.SchemaEpoch, to.SchemaEpoch)
}

// graphOf returns the version graph t is a member of in m, with t's
// content columns, or nil.
func graphOf(m *Model, t *Table) (*Graph, map[string]bool) {
	if t == nil {
		return nil, nil
	}
	for _, g := range m.Graphs {
		for _, member := range g.Members {
			if member.Table == t.Name {
				content := map[string]bool{}
				for _, col := range member.Content {
					content[col] = true
				}
				return g, content
			}
		}
	}
	return nil, nil
}

// retypeHazards are the shared hazards of changing a column's type.
func (d *differ) retypeHazards(c *change, prevTable string, r *retype) {
	ft, tt := d.fromTables[prevTable], d.toTables[r.table]
	field := fieldOf(tt, r.after)
	before, after := typeNames(r.before, r.after)
	change := fmt.Sprintf("%s changes from %s to %s", field, before, after)
	c.addHazard(HazardCompat, "", fmt.Sprintf(
		"%s: servers built from the previous version still read and write %s.", change, before))
	if r.conv.lossy {
		loss := r.conv.loss
		if loss == "" {
			loss = fmt.Sprintf("the cast truncates or rounds values %s cannot hold exactly.", after)
		}
		c.addHazard(HazardDestructive, "", change+": "+loss)
	}
	if r.conv.kind == convertMayFail {
		c.addHazard(HazardDataDependent, "", fmt.Sprintf(
			"%s: the cast fails on a value %s cannot hold.", change, after))
	}
	if ft.History != "" && tt.History != "" {
		c.addHazard(HazardHistory, "", fmt.Sprintf(
			"%s is versioned: images recorded before this change keep %s as %s, which the history readers read as %s.",
			tt.Name, field, before, after))
	}
	d.graphContent(c, ft, r.before, tt, r.after)
	d.breaksReaders(c, prevTable, r.before.Name, "changes its type to "+after)
}

// typeNames names the types of a column before and after a change, in a
// reason or an error. Where what the column holds changes (Column.Holds),
// each name says whether the column is a scalar, a list or a JSON value,
// since a SQLite type alone does not. Where a list's element changes
// (Column.Element), each name gives the element.
func typeNames(before, after *Column) (string, string) {
	if before.Holds == after.Holds && before.Element != after.Element {
		return "a list of " + before.Element, "a list of " + after.Element
	}
	if before.Holds == after.Holds {
		return before.Type, after.Type
	}
	name := func(c *Column) string {
		switch c.Holds {
		case holdsList:
			return "a list (" + c.Type + " holding a JSON array)"
		case holdsJSON:
			return "a JSON value (" + c.Type + ")"
		}
		return "a scalar (" + c.Type + ")"
	}
	return name(before), name(after)
}

// viewHazard adds an api-breaking hazard for a projection view whose
// published columns change (after nil: the view is dropped). The reader is
// the view: its Arrow schema is its readers' contract.
func viewHazard(c *change, before, after *View) {
	reader := before.Schema + "." + before.Name
	h := &Hazard{
		ID:      HazardID(HazardAPIBreaking, viewSubject(before), reader),
		Class:   HazardAPIBreaking,
		Subject: viewSubject(before),
		Reader:  reader,
	}
	switch {
	case after == nil:
		h.Reason = fmt.Sprintf("The projection view %s is dropped: its readers lose the relation and its Arrow schema.", reader)
	case viewColumns(before) != viewColumns(after):
		h.Reason = fmt.Sprintf("The projection view %s publishes (%s) and now publishes (%s): its readers' Arrow schema changes.",
			reader, viewColumns(before), viewColumns(after))
	default:
		return
	}
	for _, existing := range c.hazards {
		if existing.ID == h.ID {
			return
		}
	}
	c.hazards = append(c.hazards, h)
}

// viewColumns lists a view's published columns by name and type, in order.
func viewColumns(v *View) string {
	parts := make([]string, len(v.Columns))
	for i, col := range v.Columns {
		parts[i] = col.Name + " " + col.Type
	}
	return strings.Join(parts, ", ")
}

// sortHazards orders a step's hazards by class, in D27's order, then by
// subject and reader.
func sortHazards(hazards []*Hazard) {
	order := map[HazardClass]int{}
	for i, class := range HazardClasses {
		order[class] = i
	}
	sort.SliceStable(hazards, func(i, j int) bool {
		a, b := hazards[i], hazards[j]
		if order[a.Class] != order[b.Class] {
			return order[a.Class] < order[b.Class]
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Reader < b.Reader
	})
}

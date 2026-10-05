package sqlmigrate

import (
	"sort"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

// expandedModel is the model the database holds between the plan's phases:
// the previous model with every expand step applied (D27, amended). It has
// the new model's objects, with what contract removes or tightens still
// there, under the names expand gave them:
//
//   - every table of the new model, as betweenPhases puts back what
//     contract drops or tightens, and the previous model's tables contract
//     drops, as their renames leave them;
//   - every extension and pool schema of either model, since neither is
//     ever dropped;
//   - the new model's functions, triggers and views, and those of the
//     previous model contract drops or replaces;
//   - the version graphs of both models (expandedGraphs).
//
// Diff sets it on a plan with contract steps only.
func (d *differ) expandedModel() (*Model, error) {
	m := &Model{
		Version:    d.to.Version,
		Dialect:    d.to.Dialect,
		Service:    d.to.Service,
		Extensions: union(d.from.Extensions, d.to.Extensions),
		Schemas:    union(d.from.Schemas, d.to.Schemas),
		Graphs:     d.expandedGraphs(),
	}

	for _, tt := range d.to.Tables {
		ft := d.fromTables[d.renames.prevTable(tt.Name)]
		if ft == nil {
			m.Tables = append(m.Tables, tt)
			continue
		}
		t := d.betweenPhases(ft, tt)
		t.History, t.HistoryExclude = tt.History, tt.HistoryExclude
		if tt.History == "" && ft.History != "" {
			// Contract drops the history table and its triggers, so the
			// table records history until then.
			t.History = ft.History
			t.HistoryExclude = d.renamedColumns(ft.Name, ft.HistoryExclude)
		}
		t.Optimistic = (ft.Optimistic || tt.Optimistic) && t.History == ""
		m.Tables = append(m.Tables, t)
	}
	for _, ft := range d.from.Tables {
		if d.dropped[ft.Name] {
			m.Tables = append(m.Tables, d.renamedTable(ft))
		}
	}

	m.Functions = append(m.Functions, d.to.Functions...)
	for _, ff := range d.from.Functions {
		if d.droppedFunctions[ff.Name] {
			m.Functions = append(m.Functions, ff)
		}
	}

	// A trigger contract replaces or drops is the previous one, on its
	// table as renamed and under the name expand gave it.
	contractTriggers := map[string]*Trigger{}
	var dropped []*Trigger
	for _, c := range d.changes {
		if c.phase != Contract {
			continue
		}
		switch c.op {
		case opReplaceTrigger:
			old, err := d.renamedTrigger(c.oldTrigger, c.table, c.trigger.Name)
			if err != nil {
				return nil, err
			}
			contractTriggers[c.trigger.Table+"/"+c.trigger.Name] = old
		case opDropTrigger:
			old, err := d.renamedTrigger(c.oldTrigger, c.table, c.oldTrigger.Name)
			if err != nil {
				return nil, err
			}
			dropped = append(dropped, old)
		}
	}
	for _, t := range d.to.Triggers {
		if old := contractTriggers[t.Table+"/"+t.Name]; old != nil {
			t = old
		}
		m.Triggers = append(m.Triggers, t)
	}
	m.Triggers = append(m.Triggers, dropped...)

	m.Views = append(m.Views, d.to.Views...)
	for _, c := range d.changes {
		if c.phase == Contract && c.op == opDropView {
			m.Views = append(m.Views, d.renamedView(c.oldView))
		}
	}

	m.sort()
	return m, nil
}

// union returns the names in a or b, sorted, once each.
func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, name := range list {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// renamedTrigger is a trigger of the previous model on table, as the
// expand phase leaves it: renamed to name, on the table's new name, and
// executing its function's new name. Postgres follows each rename in the
// trigger, so the definition is written again only when one applies.
func (d *differ) renamedTrigger(t *Trigger, table, name string) (*Trigger, error) {
	out := *t
	out.Table, out.Name = table, name
	timing, function, ok := parseTrigger(t)
	if !ok {
		return &out, nil
	}
	renamedFunction := function
	if fn, ok := d.functionTo[function]; ok {
		renamedFunction = fn
	}
	if table == t.Table && name == t.Name && renamedFunction == function {
		return &out, nil
	}
	definition, err := sqlgen.Trigger{
		Name: name, Timing: timing, Table: table, QuotedTable: sqlutil.QuoteIdentifier(table), Function: renamedFunction,
	}.SQL()
	if err != nil {
		return nil, err
	}
	out.Definition = definition
	return &out, nil
}

// renamedView is a view of the previous model as the expand phase leaves
// it: Postgres follows a renamed table or column in a view, so its CREATE
// VIEW statement and the columns it reads take the new names.
func (d *differ) renamedView(v *View) *View {
	out := *v
	out.Definition = append([]string(nil), v.Definition...)
	if len(out.Definition) > 0 {
		out.Definition[0] = renameInView(out.Definition[0], d.renames)
	}
	out.Reads = nil
	for _, r := range v.Reads {
		out.Reads = append(out.Reads, &ColumnRef{Table: d.renames.table(r.Table), Column: d.renames.column(r.Table, r.Column)})
	}
	sort.Slice(out.Reads, func(i, j int) bool {
		if out.Reads[i].Table != out.Reads[j].Table {
			return out.Reads[i].Table < out.Reads[j].Table
		}
		return out.Reads[i].Column < out.Reads[j].Column
	})
	return &out
}

// expandedGraphs are the version graphs between the phases: each graph of
// the previous model, its member tables and content columns as renamed,
// with the new model's members and content columns added, and each graph
// only the new model has. A graph keeps the previous model's schema epoch:
// commits made before the plan ran hash rows of the previous shape, and the
// contract's history hazards compare that epoch with the new one.
func (d *differ) expandedGraphs() []*Graph {
	var out []*Graph
	byName := map[string]*Graph{}
	for _, g := range d.from.Graphs {
		eg := &Graph{Name: g.Name, SchemaEpoch: g.SchemaEpoch}
		for _, member := range g.Members {
			eg.Members = append(eg.Members, &GraphMember{
				Table:   d.renames.table(member.Table),
				Content: d.renamedColumns(member.Table, member.Content),
			})
		}
		byName[g.Name] = eg
		out = append(out, eg)
	}
	for _, g := range d.to.Graphs {
		eg := byName[g.Name]
		if eg == nil {
			eg = &Graph{Name: g.Name, SchemaEpoch: g.SchemaEpoch}
			out = append(out, eg)
		}
		for _, member := range g.Members {
			var em *GraphMember
			for _, existing := range eg.Members {
				if existing.Table == member.Table {
					em = existing
				}
			}
			if em == nil {
				em = &GraphMember{Table: member.Table}
				eg.Members = append(eg.Members, em)
			}
			em.Content = union(em.Content, member.Content)
		}
	}
	for _, g := range out {
		for _, member := range g.Members {
			member.Content = union(member.Content, nil)
			if member.Content == nil {
				member.Content = []string{}
			}
		}
	}
	return out
}

package sqlmigrate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// diffFunctions matches functions by name, following the renames of the
// tables they are named after. A function whose definition changes is
// replaced in place, so the triggers that execute it stay.
func (d *differ) diffFunctions() {
	from := map[string]*Function{}
	for _, f := range d.from.Functions {
		from[f.Name] = f
	}
	to := map[string]bool{}
	for _, f := range d.to.Functions {
		to[f.Name] = true
	}
	matched := map[string]bool{}
	for _, tf := range d.to.Functions {
		prev := d.prevFunction(tf, from, to)
		ff := from[prev]
		if ff == nil {
			d.add(&change{op: opCreateFunction, phase: Expand, subject: functionSubject(tf.Name), function: tf})
			continue
		}
		matched[prev] = true
		d.functionTo[prev] = tf.Name
		if prev != tf.Name {
			d.add(&change{
				op: opRenameFunction, phase: Expand, subject: functionSubject(tf.Name),
				name: tf.Name, oldName: prev, oldFunc: ff,
			})
		}
		if ff.Definition != tf.Definition || ff.Arguments != tf.Arguments {
			c := d.add(&change{
				op: opReplaceFunction, phase: Expand, subject: functionSubject(tf.Name),
				function: tf, oldFunc: ff,
			})
			d.excludeHazard(c, tf)
		}
	}
	for _, ff := range d.from.Functions {
		if !matched[ff.Name] {
			d.droppedFunctions[ff.Name] = true
			d.add(&change{op: opDropFunction, phase: Contract, subject: functionSubject(ff.Name), oldFunc: ff})
		}
	}
}

// prevFunction returns the previous name of a function of the new model:
// for a function named after a renamed table ("order_capture_history"),
// the same function of the old table, when the previous model has it.
func (d *differ) prevFunction(tf *Function, from map[string]*Function, to map[string]bool) string {
	for prev, name := range d.renames.tableTo {
		tt := d.toTables[name]
		if tt == nil || tt.Origin != tf.Origin || !strings.HasPrefix(tf.Name, name+"_") {
			continue
		}
		candidate := prev + strings.TrimPrefix(tf.Name, name)
		if from[candidate] != nil && !to[candidate] {
			return candidate
		}
	}
	return tf.Name
}

// excludeHazard adds a history hazard to the replacement of a capture
// function whose table's @versioned({ exclude }) changes.
func (d *differ) excludeHazard(c *change, tf *Function) {
	for _, tt := range d.to.Tables {
		ft := d.fromTables[d.renames.prevTable(tt.Name)]
		if ft == nil || ft.History == "" || tt.History == "" || strings.Join(ft.HistoryExclude, ",") == strings.Join(tt.HistoryExclude, ",") {
			continue
		}
		if !d.executes(tt.Name, tf.Name) {
			continue
		}
		var changes []string
		if added := minus(tt.HistoryExclude, ft.HistoryExclude); len(added) > 0 {
			changes = append(changes, "leaves out "+strings.Join(added, ", "))
		}
		if removed := minus(ft.HistoryExclude, tt.HistoryExclude); len(removed) > 0 {
			changes = append(changes, "records "+strings.Join(removed, ", ")+" again")
		}
		c.addHazard(HazardHistory, "", fmt.Sprintf(
			"The history of %s now %s: images recorded before this change keep their old shape, and the plan does not scrub them.",
			tt.Name, strings.Join(changes, " and ")))
	}
}

// minus lists the names of a that b does not hold, in a's order.
func minus(a, b []string) []string {
	var out []string
	for _, name := range a {
		found := false
		for _, other := range b {
			if other == name {
				found = true
			}
		}
		if !found {
			out = append(out, name)
		}
	}
	return out
}

// executes reports whether a trigger of the new model on table executes
// function.
func (d *differ) executes(table, function string) bool {
	for _, t := range d.to.Triggers {
		if t.Table == table {
			if _, fn, ok := parseTrigger(t); ok && fn == function {
				return true
			}
		}
	}
	return false
}

// triggerPattern matches the CREATE TRIGGER statements sqlgen writes.
var triggerPattern = regexp.MustCompile(`^CREATE TRIGGER \S+\n  (.+) ON \S+\n  FOR EACH ROW EXECUTE FUNCTION (\S+)\(\)$`)

// parseTrigger returns when a trigger fires and the function it executes.
func parseTrigger(t *Trigger) (timing, function string, ok bool) {
	m := triggerPattern.FindStringSubmatch(t.Definition)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// diffTriggers matches triggers by table and name, following renames. A
// trigger whose table becomes versioned is created with the history seed.
func (d *differ) diffTriggers() {
	from := map[string]*Trigger{}
	for _, t := range d.from.Triggers {
		from[t.Table+"/"+t.Name] = t
	}
	matched := map[string]bool{}
	for _, tt := range d.to.Triggers {
		prevTable := d.renames.prevTable(tt.Table)
		prevName := tt.Name
		if prefix := "trg_" + tt.Table + "_"; prevTable != tt.Table && strings.HasPrefix(tt.Name, prefix) {
			prevName = "trg_" + prevTable + "_" + strings.TrimPrefix(tt.Name, prefix)
		}
		ft := from[prevTable+"/"+prevName]
		if ft == nil {
			prevName = tt.Name
			ft = from[prevTable+"/"+prevName]
		}
		seed := d.seeds[tt.Table]
		if ft == nil {
			if seed != nil {
				seed.seed.triggers = append(seed.seed.triggers, tt)
				continue
			}
			d.add(&change{
				op: opCreateTrigger, phase: Expand, subject: triggerSubject(tt.Table, tt.Name),
				table: tt.Table, trigger: tt,
			})
			continue
		}
		matched[ft.Table+"/"+ft.Name] = true
		if prevName != tt.Name {
			d.add(&change{
				op: opRenameTrigger, phase: Expand, subject: triggerSubject(tt.Table, tt.Name),
				table: tt.Table, name: tt.Name, oldName: prevName,
			})
		}
		if d.sameTrigger(ft, tt) {
			continue
		}
		if seed != nil {
			seed.seed.triggers = append(seed.seed.triggers, tt)
			seed.seed.replaced[tt.Name] = true
			continue
		}
		// A trigger whose old function the plan drops stops doing what the
		// previous version's servers rely on: contract.
		phase := Expand
		if _, fn, ok := parseTrigger(ft); ok && d.droppedFunctions[fn] {
			phase = Contract
		}
		d.add(&change{
			op: opReplaceTrigger, phase: phase, subject: triggerSubject(tt.Table, tt.Name),
			table: tt.Table, trigger: tt, oldTrigger: ft,
		})
	}
	for _, ft := range d.from.Triggers {
		if matched[ft.Table+"/"+ft.Name] {
			continue
		}
		table := d.renames.table(ft.Table)
		d.add(&change{
			op: opDropTrigger, phase: Contract, subject: triggerSubject(table, ft.Name),
			table: table, oldTrigger: ft,
		})
	}
	for _, seed := range d.seeds {
		sort.Slice(seed.seed.triggers, func(i, j int) bool {
			return triggerOrder(seed.seed.triggers[i]) < triggerOrder(seed.seed.triggers[j])
		})
	}
}

// triggerOrder puts a table's history triggers in create.sql's order.
func triggerOrder(t *Trigger) string {
	timing, _, _ := parseTrigger(t)
	switch timing {
	case "BEFORE UPDATE":
		return "0" + t.Name
	case "AFTER INSERT OR UPDATE":
		return "1" + t.Name
	}
	return "2" + t.Name
}

// sameTrigger reports whether a trigger of the previous model does what
// one of the new model does once renames are applied: it fires at the same
// time and executes the same function.
func (d *differ) sameTrigger(ft, tt *Trigger) bool {
	fromTiming, fromFn, ok1 := parseTrigger(ft)
	toTiming, toFn, ok2 := parseTrigger(tt)
	if !ok1 || !ok2 {
		return ft.Definition == tt.Definition
	}
	if name, ok := d.functionTo[fromFn]; ok {
		fromFn = name
	}
	return fromTiming == toTiming && fromFn == toFn
}

// pendingAlteration is a column type change or a generated column change
// before it is grouped with the others that share a view or a generated
// column.
type pendingAlteration struct {
	prevTable  string
	retype     *retype
	regenerate *regeneration
}

func (a *pendingAlteration) table() string {
	if a.retype != nil {
		return a.retype.table
	}
	return a.regenerate.table
}

func (a *pendingAlteration) before() *Column {
	if a.retype != nil {
		return a.retype.before
	}
	return a.regenerate.before
}

func (a *pendingAlteration) after() *Column {
	if a.retype != nil {
		return a.retype.after
	}
	return a.regenerate.after
}

func (a *pendingAlteration) subject() string {
	return columnSubject(a.table(), a.after().Name)
}

// groupAlterations turns the pending alterations into changes. Postgres
// refuses to change a column's type while a view or a generated column
// reads it, so each view that reads an altered column is dropped and
// created again in the step, and alterations that share a view or a
// generated column share a step. It returns the views those steps drop
// and create, by schema and name, so the views diff leaves them.
func (d *differ) groupAlterations() (dropped, created map[string]bool) {
	dropped, created = map[string]bool{}, map[string]bool{}
	sort.SliceStable(d.alterations, func(i, j int) bool {
		return d.alterations[i].subject() < d.alterations[j].subject()
	})
	parent := make([]int, len(d.alterations))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	owner := map[string]int{}
	views := make([][]*View, len(d.alterations))
	for i, a := range d.alterations {
		var resources []string
		for _, v := range d.from.Views {
			if readsColumn(v, a.prevTable, a.before().Name) {
				views[i] = append(views[i], v)
				resources = append(resources, "view:"+v.Schema+"."+v.Name)
			}
		}
		if a.regenerate != nil {
			resources = append(resources, "generated:"+a.table()+"."+a.after().Name)
		}
		if a.retype != nil {
			ft := d.fromTables[a.prevTable]
			for _, g := range ft.Columns {
				if g.Generated == "" {
					continue
				}
				for _, name := range identifiers(g.Generated) {
					if name == a.before().Name {
						resources = append(resources, "generated:"+a.table()+"."+d.renames.column(a.prevTable, g.Name))
					}
				}
			}
		}
		for _, r := range resources {
			if j, ok := owner[r]; ok {
				parent[find(i)] = find(j)
			} else {
				owner[r] = i
			}
		}
	}

	groups := map[int][]int{}
	var roots []int
	for i := range d.alterations {
		root := find(i)
		if _, ok := groups[root]; !ok {
			roots = append(roots, root)
		}
		groups[root] = append(groups[root], i)
	}
	sort.Ints(roots)
	toViews := map[string]*View{}
	for _, v := range d.to.Views {
		toViews[v.Schema+"."+v.Name] = v
	}
	for _, root := range roots {
		members := groups[root]
		// The step is named after its first type change, else its first
		// generated column.
		first := d.alterations[members[0]]
		for _, i := range members {
			if d.alterations[i].retype != nil {
				first = d.alterations[i]
				break
			}
		}
		c := &change{op: opRegenerateColumn, phase: Expand, subject: first.subject(), table: first.table(), alter: &alteration{}}
		seenViews := map[string]bool{}
		for _, i := range members {
			a := d.alterations[i]
			if a.retype != nil {
				c.op = opAlterColumnType
				c.alter.retypes = append(c.alter.retypes, a.retype)
				sub := &change{subject: a.subject(), phase: Expand}
				d.retypeHazards(sub, a.prevTable, a.retype)
				c.hazards = append(c.hazards, sub.hazards...)
			} else {
				c.alter.regenerate = append(c.alter.regenerate, a.regenerate)
			}
			for _, v := range views[i] {
				key := v.Schema + "." + v.Name
				if seenViews[key] {
					continue
				}
				seenViews[key] = true
				c.alter.dropViews = append(c.alter.dropViews, v)
				dropped[key] = true
				if tv := toViews[key]; tv != nil {
					c.alter.createViews = append(c.alter.createViews, tv)
					created[key] = true
					viewHazard(c, v, tv)
				} else {
					viewHazard(c, v, nil)
				}
			}
		}
		d.add(c)
	}
	return dropped, created
}

// readsColumn reports whether a view reads table.column.
func readsColumn(v *View, table, column string) bool {
	for _, r := range v.Reads {
		if r.Table == table && r.Column == column {
			return true
		}
	}
	return false
}

// diffViews matches projection views by schema and name. A view whose
// definition changes beyond what renames explain, or whose owner changes,
// is dropped and created again; Postgres follows a renamed table or column
// in a view on its own.
func (d *differ) diffViews(wrappedDrops, wrappedCreates map[string]bool) {
	from := map[string]*View{}
	for _, v := range d.from.Views {
		from[v.Schema+"."+v.Name] = v
	}
	for _, tv := range d.to.Views {
		key := tv.Schema + "." + tv.Name
		if wrappedCreates[key] {
			continue
		}
		fv := from[key]
		if fv == nil || wrappedDrops[key] {
			d.add(&change{op: opCreateView, phase: Expand, subject: viewSubject(tv), view: tv})
			continue
		}
		if fv.Owner == tv.Owner && d.sameView(fv, tv) {
			continue
		}
		c := d.add(&change{op: opReplaceView, phase: Expand, subject: viewSubject(tv), view: tv, oldView: fv})
		viewHazard(c, fv, tv)
	}
	to := map[string]bool{}
	for _, v := range d.to.Views {
		to[v.Schema+"."+v.Name] = true
	}
	for _, fv := range d.from.Views {
		key := fv.Schema + "." + fv.Name
		if to[key] || wrappedDrops[key] {
			continue
		}
		c := d.add(&change{op: opDropView, phase: Contract, subject: viewSubject(fv), oldView: fv})
		viewHazard(c, fv, nil)
	}
}

// sameView reports whether a view of the previous model, with the renames
// applied, is the view of the new model.
func (d *differ) sameView(fv, tv *View) bool {
	if len(fv.Definition) != len(tv.Definition) || len(fv.Definition) == 0 {
		return false
	}
	for i := 1; i < len(fv.Definition); i++ {
		if fv.Definition[i] != tv.Definition[i] {
			return false
		}
	}
	return renameInView(fv.Definition[0], d.renames) == tv.Definition[0]
}

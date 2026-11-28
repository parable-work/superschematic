package sqlmigrate

// diffRows seeds the rows @seedRows declares (#625): a row whose table and
// key the previous model does not hold is inserted in expand, with no
// hazard, since its insert leaves a row of its key alone. A row the
// previous model holds has no step, whatever its other values now are:
// once seeded it is the app's to edit, so a changed seed reaches only a
// database that does not hold its key, and a removed one stays. A row's
// change names no table, so a SQLite rebuild of its table never takes it
// in: it runs after the rebuild, at its own rank.
func (d *differ) diffRows() {
	from := map[string]bool{}
	for _, r := range d.from.Rows {
		from[r.Table+"/"+r.Key] = true
	}
	for _, tr := range d.to.Rows {
		if from[d.renames.prevTable(tr.Table)+"/"+tr.Key] {
			continue
		}
		d.add(&change{op: opSeedRow, phase: Expand, subject: seedRowSubject(tr.Table, tr.Key), row: tr})
	}
}

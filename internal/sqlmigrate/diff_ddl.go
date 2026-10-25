package sqlmigrate

import (
	"fmt"
	"slices"
	"strings"
)

// The CHECK constraints and the raw SQL a schema declares (D81).

// diffChecks matches the checks of a table both models have by their
// column, renamed, since a column has at most one. A check that loosens
// what the column takes goes in expand, where the servers of either
// version write within it: a dropped one, and an enum's that every
// previous member stays in. One that tightens it goes in contract, after
// the servers that may write outside it are gone, and fails when a row is
// outside it (data-dependent): a new one over a column the previous
// version has, and an enum's that drops a member. Any other change drops
// the old check in expand and adds the new one in contract. A check over a
// column the plan adds goes in expand; one over a column contract drops
// goes with the column.
func (d *differ) diffChecks(ft, tt *Table) {
	from := map[string]*Check{} // column, as the new model names it -> check
	for _, fc := range ft.Checks {
		from[d.renames.column(ft.Name, fc.Column)] = fc
	}
	for _, tc := range tt.Checks {
		fc := from[tc.Column]
		delete(from, tc.Column)
		subject := constraintSubject(tt.Name, tc.Name)
		what := checkWhat(tt, tc)
		switch {
		case fc == nil:
			newColumn := columnNamed(ft, d.renames.prevColumn(tt.Name, tc.Column)) == nil
			c := d.add(&change{
				op: opAddCheck, phase: Contract, subject: subject,
				table: tt.Name, check: tc, tableDef: tt, newColumn: newColumn,
			})
			if newColumn {
				c.phase = Expand
				continue
			}
			c.addHazard(HazardDataDependent, "", checkAddReason(tt, tc))
		case sameRule(fc, tc):
			if fc.Name != tc.Name {
				d.renameConstraint(tt.Name, fc.Name, tc.Name).check = tc
			}
		default:
			if fc.Name != tc.Name {
				d.renameConstraint(tt.Name, fc.Name, tc.Name).check = tc
			}
			c := &change{
				op: opReplaceCheck, phase: Expand, subject: subject,
				table: tt.Name, check: tc, oldCheck: fc, tableDef: tt,
			}
			switch {
			case len(fc.In) > 0 && len(tc.In) > 0 && subsetOf(fc.In, tc.In):
				// Every value the previous check took, the new one takes.
				d.add(c)
			case len(fc.In) > 0 && len(tc.In) > 0 && subsetOf(tc.In, fc.In):
				c.phase = Contract
				d.add(c).addHazard(HazardDataDependent, "", fmt.Sprintf(
					"%s takes fewer values: validating %s fails when a row of %s holds %s.", what, tc.Name, tt.Name, strings.Join(quoted(missing(fc.In, tc.In)), " or ")))
			default:
				d.add(&change{
					op: opDropCheck, phase: Expand, subject: subject,
					table: tt.Name, oldCheck: d.renamedCheck(ft.Name, fc, tc.Name),
				})
				d.add(&change{
					op: opAddCheck, phase: Contract, subject: subject,
					table: tt.Name, check: tc, oldCheck: fc, tableDef: tt,
				}).addHazard(HazardDataDependent, "", checkAddReason(tt, tc))
			}
		}
	}
	for _, fc := range sortedChecks(from) {
		if columnNamed(tt, d.renames.column(ft.Name, fc.Column)) == nil {
			// The column goes in contract, and its check with it.
			continue
		}
		d.add(&change{
			op: opDropCheck, phase: Expand, subject: constraintSubject(tt.Name, fc.Name),
			table: tt.Name, oldCheck: d.renamedCheck(ft.Name, fc, fc.Name),
		})
	}
}

// renamedCheck is a check of the previous model's table prevTable as
// expand leaves it: over its column's new name, under name.
func (d *differ) renamedCheck(prevTable string, c *Check, name string) *Check {
	out := *c
	out.Column = d.renames.column(prevTable, c.Column)
	out.Name = name
	return &out
}

func sortedChecks(m map[string]*Check) []*Check {
	out := make([]*Check, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *Check) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// checkAddReason is the data-dependent hazard of adding check c in
// contract, whether the column had another check before or none, so the
// plan from the model between the phases says the same.
func checkAddReason(t *Table, c *Check) string {
	return fmt.Sprintf("%s gets a CHECK in the database: validating %s fails when a row of %s holds %s.",
		checkWhat(t, c), c.Name, t.Name, checkBreach(c))
}

// checkWhat names the field a check holds to its rule.
func checkWhat(t *Table, c *Check) string {
	if col := columnNamed(t, c.Column); col != nil {
		return fieldOf(t, col)
	}
	return t.Name + "." + c.Column
}

// checkBreach says what a row that fails the check holds, ending a
// sentence.
func checkBreach(c *Check) string {
	if len(c.In) > 0 {
		return "a value other than " + strings.Join(quoted(c.In), ", ")
	}
	var parts []string
	number := func(f float64) string { return fmt.Sprint(f) }
	if c.Min != nil {
		parts = append(parts, "below "+number(*c.Min))
	}
	if c.Max != nil {
		parts = append(parts, "above "+number(*c.Max))
	}
	if c.MinLength != nil {
		parts = append(parts, fmt.Sprintf("shorter than %d characters", *c.MinLength))
	}
	if c.MaxLength != nil {
		parts = append(parts, fmt.Sprintf("longer than %d characters", *c.MaxLength))
	}
	return "a value " + strings.Join(parts, " or ")
}

func quoted(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = "'" + v + "'"
	}
	return out
}

// subsetOf reports whether every value of a is in b.
func subsetOf(a, b []string) bool {
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}

// missing lists the values of a that b lacks.
func missing(a, b []string) []string {
	var out []string
	for _, v := range a {
		if !slices.Contains(b, v) {
			out = append(out, v)
		}
	}
	return out
}

// diffRawSQL matches the pieces of raw SQL by id (D81). A new piece, and
// one whose checksum changes, runs in expand, at its place among the
// objects the plan creates; one the new model no longer declares runs its
// down in contract, before anything is dropped, the last to run first.
// Each step is raw-sql, since the planner cannot judge what the SQL does,
// except in a plan from an empty database, which create.sql is.
func (d *differ) diffRawSQL() {
	from := map[string]*RawSQL{}
	for _, p := range d.from.RawSQL {
		from[p.ID] = p
	}
	to := map[string]bool{}
	for i, p := range d.to.RawSQL {
		to[p.ID] = true
		fp := from[p.ID]
		if fp != nil && fp.Checksum == p.Checksum {
			continue
		}
		c := d.add(&change{op: opRunRawSQL, phase: Expand, subject: rawSQLSubject(p.ID), raw: p, oldRaw: fp, order: i})
		switch {
		case d.empty:
		case fp == nil:
			c.addHazard(HazardRawSQL, "", fmt.Sprintf(
				"The raw SQL %s (%s) runs for the first time; the planner cannot judge what it does.", p.ID, p.Origin))
		default:
			c.addHazard(HazardRawSQL, "", fmt.Sprintf(
				"The raw SQL %s (%s) changed: the step runs its statements again, over what the previous ones made; the planner cannot judge what they do.", p.ID, p.Origin))
		}
	}
	for i, p := range d.from.RawSQL {
		if to[p.ID] {
			continue
		}
		c := d.add(&change{op: opUndoRawSQL, phase: Contract, subject: rawSQLSubject(p.ID), oldRaw: p, order: -i})
		if len(p.Down) == 0 {
			c.addHazard(HazardRawSQL, "", fmt.Sprintf(
				"The raw SQL %s is no longer declared and has no down: nothing undoes what it made; drop that by hand.", p.ID))
			continue
		}
		c.addHazard(HazardRawSQL, "", fmt.Sprintf(
			"The raw SQL %s is no longer declared: the step runs its down; the planner cannot judge what it does.", p.ID))
	}
}

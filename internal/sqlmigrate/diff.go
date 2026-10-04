package sqlmigrate

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Diff plans the migration from one model to another. A nil from is an
// empty database. The returned plan is sealed ([Plan.Seal]). Diff is a pure
// function of its arguments.
func Diff(from, to *Model, opts Options) (*Plan, error) {
	if to == nil {
		return nil, errors.New("sqlmigrate: diff needs the model to plan to")
	}
	impl, err := dialectFor(to.Dialect)
	if err != nil {
		return nil, err
	}
	base := from
	if base == nil {
		base = &Model{Version: ModelVersion, Dialect: to.Dialect, Service: to.Service}
	}
	if base.Dialect != to.Dialect {
		return nil, fmt.Errorf("sqlmigrate: the previous model is for %s and the new one for %s", base.Dialect, to.Dialect)
	}
	if base.Service != to.Service {
		return nil, fmt.Errorf("sqlmigrate: the previous model is of service %s and the new one of %s", base.Service, to.Service)
	}

	d, err := newDiffer(base, to, opts, impl)
	if err != nil {
		return nil, err
	}
	if err := d.diff(); err != nil {
		return nil, err
	}
	steps, err := d.steps()
	if err != nil {
		return nil, err
	}

	toHash, err := to.Hash()
	if err != nil {
		return nil, err
	}
	toModel, err := to.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		Version: PlanVersion,
		Dialect: to.Dialect,
		Service: to.Service,
		To:      toHash,
		ToModel: toModel,
		Steps:   steps,
	}
	if from != nil {
		if plan.From, err = from.Hash(); err != nil {
			return nil, err
		}
	}
	for _, rn := range opts.Renames {
		plan.Renames = append(plan.Renames, rn.From+"="+rn.To)
	}
	if err := plan.Seal(); err != nil {
		return nil, err
	}
	return plan, nil
}

// differ holds one diff in progress.
type differ struct {
	from, to *Model
	opts     Options
	dialect  dialect
	renames  *renames

	fromTables, toTables map[string]*Table
	// dropped are the previous model's tables the new one does not have.
	dropped map[string]bool
	// functionTo maps a function of the previous model to its new name;
	// droppedFunctions are the ones the new model does not have.
	functionTo       map[string]string
	droppedFunctions map[string]bool

	// alterations are the column changes that may need views or generated
	// columns taken down around them; they become changes once grouped.
	alterations []*pendingAlteration
	// regenerated are the generated columns dropped and added again, by
	// table and column in the new model.
	regenerated map[string]bool
	// seeds are the new history tables of tables that become versioned, by
	// source table.
	seeds map[string]*change

	changes []*change
}

func newDiffer(from, to *Model, opts Options, impl dialect) (*differ, error) {
	r, err := resolveRenames(from, to, opts.Renames)
	if err != nil {
		return nil, err
	}
	return &differ{
		from:             from,
		to:               to,
		opts:             opts,
		dialect:          impl,
		renames:          r,
		fromTables:       tablesByName(from),
		toTables:         tablesByName(to),
		dropped:          map[string]bool{},
		functionTo:       map[string]string{},
		droppedFunctions: map[string]bool{},
		regenerated:      map[string]bool{},
		seeds:            map[string]*change{},
	}, nil
}

func (d *differ) add(c *change) *change {
	d.changes = append(d.changes, c)
	return c
}

// diff finds every change between the two models.
func (d *differ) diff() error {
	d.diffNamespaces()
	d.diffRenames()
	if err := d.diffTables(); err != nil {
		return err
	}
	d.diffFunctions()
	d.diffTriggers()
	d.diffViews(d.groupAlterations())
	return nil
}

// diffNamespaces creates the extensions and pool schemas the new model
// adds. Neither is ever dropped, as drop.sql leaves them.
func (d *differ) diffNamespaces() {
	have := map[string]bool{}
	for _, ext := range d.from.Extensions {
		have[ext] = true
	}
	for _, ext := range d.to.Extensions {
		if !have[ext] {
			d.add(&change{op: opCreateExtension, phase: Expand, subject: "extension/" + ext, name: ext})
		}
	}
	have = map[string]bool{}
	for _, schema := range d.from.Schemas {
		have[schema] = true
	}
	for _, schema := range d.to.Schemas {
		if !have[schema] {
			d.add(&change{op: opCreateSchema, phase: Expand, subject: "schema/" + schema, name: schema})
		}
	}
}

// diffRenames turns each table and column rename into a change.
func (d *differ) diffRenames() {
	for prev, name := range d.renames.tableTo {
		c := d.add(&change{
			op: opRenameTable, phase: Expand, subject: tableSubject(name),
			table: name, name: name, oldName: prev, tableDef: d.toTables[name],
		})
		reason := fmt.Sprintf("Table %s is renamed to %s: servers built from the previous version still use the old name.", prev, name)
		if because := d.renames.because["table:"+name]; because != "" {
			reason = fmt.Sprintf("Table %s is renamed to %s, following --rename %s: servers built from the previous version still use the old name.", prev, name, because)
		}
		c.addHazard(HazardCompat, "", reason)
		d.breaksReaders(c, prev, "", "renames its table to "+name)
	}
	for prevTable, columns := range d.renames.columnTo {
		table := d.renames.table(prevTable)
		for prev, name := range columns {
			ft, tt := d.fromTables[prevTable], d.toTables[table]
			c := d.add(&change{
				op: opRenameColumn, phase: Expand, subject: columnSubject(table, name),
				table: table, name: name, oldName: prev, column: columnNamed(tt, name),
			})
			reason := fmt.Sprintf("%s.%s is renamed to %s.%s%s: servers built from the previous version still use the old name.",
				prevTable, prev, table, name, originNote(columnNamed(tt, name)))
			if because := d.renames.because["column:"+table+"."+name]; because != "" {
				reason = fmt.Sprintf("%s.%s is renamed to %s.%s, following --rename %s: servers built from the previous version still use the old name.",
					prevTable, prev, table, name, because)
			}
			c.addHazard(HazardCompat, "", reason)
			d.breaksReaders(c, prevTable, prev, "renames it to "+table+"."+name)
			if ft.History != "" && tt.History != "" {
				c.addHazard(HazardHistory, "", fmt.Sprintf(
					"%s is versioned: images recorded before this change keep the value under %s, which the history readers no longer read.",
					table, prev))
			}
			d.graphContent(c, ft, columnNamed(ft, prev), tt, columnNamed(tt, name))
		}
	}
}

// diffTables matches the tables of the two models and diffs each pair.
func (d *differ) diffTables() error {
	matched := map[string]bool{}
	for _, tt := range d.to.Tables {
		ft := d.fromTables[d.renames.prevTable(tt.Name)]
		if ft == nil {
			d.createTable(tt)
			continue
		}
		matched[ft.Name] = true
		if err := d.diffTable(ft, tt); err != nil {
			return err
		}
	}
	var dropped []*Table
	for _, ft := range d.from.Tables {
		if !matched[ft.Name] {
			d.dropped[ft.Name] = true
			dropped = append(dropped, ft)
		}
	}
	d.dropTables(dropped)
	return nil
}

// createTable creates a table the previous model does not have, with its
// constraints and indexes in the plain forms: the table is empty.
func (d *differ) createTable(tt *Table) {
	d.add(&change{
		op: opCreateTable, phase: Expand, subject: tableSubject(tt.Name),
		table: tt.Name, created: true, tableDef: tt, partitioned: tt.PartitionBy != "",
	})
	for _, fk := range tt.ForeignKeys {
		d.add(&change{
			op: opAddForeignKey, phase: Expand, subject: constraintSubject(tt.Name, fk.Name),
			table: tt.Name, created: true, foreignKey: fk,
		})
	}
	for _, idx := range tt.Indexes {
		d.add(&change{
			op: opCreateIndex, phase: Expand, subject: indexSubject(tt.Name, idx.Name),
			table: tt.Name, created: true, index: idx, partitioned: tt.PartitionBy != "",
		})
	}
	// A table that is new but whose source table is not, a history table,
	// is filled from its source table in the step that adds its triggers.
	if tt.Kind == TableHistory {
		for _, source := range d.to.Tables {
			if source.History == tt.Name && d.fromTables[d.renames.prevTable(source.Name)] != nil {
				d.seeds[source.Name] = d.add(&change{
					op: opSeedHistory, phase: Expand, subject: tableSubject(tt.Name), table: source.Name,
					seed: &historySeed{source: source, history: tt, replaced: map[string]bool{}},
				})
			}
		}
	}
}

// diffTable diffs a table both models have. ft is the previous model's,
// tt the new one's.
func (d *differ) diffTable(ft, tt *Table) error {
	if ft.PartitionBy != tt.PartitionBy {
		return fmt.Errorf("sqlmigrate: table %s changes from PARTITION BY %q to %q, which a plan cannot express; change the table by hand and adopt the new model",
			tt.Name, ft.PartitionBy, tt.PartitionBy)
	}
	mapColumns := func(columns []string) []string {
		out := make([]string, len(columns))
		for i, c := range columns {
			out[i] = d.renames.column(ft.Name, c)
		}
		return out
	}

	// Primary key.
	switch {
	case ft.PrimaryKey == nil && tt.PrimaryKey == nil:
	case ft.PrimaryKey == nil || tt.PrimaryKey == nil ||
		strings.Join(mapColumns(ft.PrimaryKey.Columns), ",") != strings.Join(tt.PrimaryKey.Columns, ","):
		return fmt.Errorf("sqlmigrate: the primary key of table %s changes, which a plan cannot express; change the table by hand and adopt the new model", tt.Name)
	case ft.PrimaryKey.Name != tt.PrimaryKey.Name:
		d.renameConstraint(tt.Name, ft.PrimaryKey.Name, tt.PrimaryKey.Name)
	}

	// Partitions, matched by bound.
	for _, fp := range ft.Partitions {
		for _, tp := range tt.Partitions {
			if fp.Bound == tp.Bound && fp.Name != tp.Name {
				d.add(&change{
					op: opRenameTable, phase: Expand, subject: tableSubject(tp.Name),
					table: tp.Name, name: tp.Name, oldName: fp.Name,
				})
			}
		}
	}

	if err := d.diffColumns(ft, tt); err != nil {
		return err
	}
	d.diffUniques(ft, tt, mapColumns)
	d.diffForeignKeys(ft, tt, mapColumns)
	d.diffIndexes(ft, tt, mapColumns)

	if ft.Comment != tt.Comment {
		d.add(&change{
			op: opCommentOnTable, phase: Expand, subject: tableSubject(tt.Name),
			table: tt.Name, tableDef: tt,
		})
	}
	return nil
}

// diffColumns diffs the columns of a table both models have.
func (d *differ) diffColumns(ft, tt *Table) error {
	matched := map[string]bool{}
	var added, dropped []*Column
	retyped := map[string]bool{} // previous column names
	type genPair struct{ before, after *Column }
	var generated []genPair
	for _, tc := range tt.Columns {
		fc := columnNamed(ft, d.renames.prevColumn(tt.Name, tc.Name))
		if fc == nil {
			added = append(added, tc)
			continue
		}
		matched[fc.Name] = true
		if (fc.Generated == "") != (tc.Generated == "") {
			return fmt.Errorf("sqlmigrate: column %s.%s changes between a generated and a stored column, which a plan cannot express", tt.Name, tc.Name)
		}
		if tc.Generated != "" {
			generated = append(generated, genPair{fc, tc})
			continue
		}
		conv := d.dialect.convert(fc.Type, tc.Type)
		switch conv.kind {
		case convertImpossible:
			return fmt.Errorf("sqlmigrate: column %s.%s%s changes from %s to %s, which %s cannot convert; change the column by hand and adopt the new model",
				tt.Name, tc.Name, originNote(tc), fc.Type, tc.Type, d.dialect.name())
		case convertSame:
			d.diffDefault(tt, fc, tc)
		default:
			retyped[fc.Name] = true
			d.alterations = append(d.alterations, &pendingAlteration{
				prevTable: ft.Name, retype: &retype{table: tt.Name, before: fc, after: tc, conv: conv},
			})
		}
		d.diffNullability(tt, fc, tc)
	}

	// A generated column is dropped and added again when its expression
	// changes or a column it reads changes type.
	for _, g := range generated {
		reads := false
		for _, name := range identifiers(g.before.Generated) {
			if retyped[name] {
				reads = true
			}
		}
		if g.before.Generated == g.after.Generated && !reads {
			continue
		}
		d.regenerated[tt.Name+"."+g.after.Name] = true
		d.alterations = append(d.alterations, &pendingAlteration{
			prevTable: ft.Name, regenerate: &regeneration{table: tt.Name, before: g.before, after: g.after},
		})
	}

	for _, fc := range ft.Columns {
		if !matched[fc.Name] {
			dropped = append(dropped, fc)
		}
	}
	hint := ""
	if len(dropped) == 1 && len(added) == 1 && sameShape(dropped[0], added[0]) {
		hint = fmt.Sprintf(" It may be a rename: %s.%s is added with the same type, nullability and default. If it is, plan with --rename %s.%s=%s.%s.",
			tt.Name, added[0].Name, ft.Name, dropped[0].Name, tt.Name, added[0].Name)
	}
	for _, tc := range added {
		d.addColumn(ft, tt, tc)
	}
	for _, fc := range dropped {
		d.dropColumn(ft, tt, fc, hint)
	}
	return nil
}

// sameShape reports whether two columns differ only by name.
func sameShape(a, b *Column) bool {
	return a.Type == b.Type && a.Nullable == b.Nullable && a.Default == b.Default && a.Generated == b.Generated
}

func (d *differ) addColumn(ft, tt *Table, tc *Column) {
	c := d.add(&change{
		op: opAddColumn, phase: Expand, subject: columnSubject(tt.Name, tc.Name),
		table: tt.Name, column: tc, tableDef: tt,
	})
	if !tc.Nullable && tc.Default == "" && tc.Generated == "" {
		c.addHazard(HazardCompat, "", fmt.Sprintf(
			"%s is required and has no default: servers built from the previous version insert rows without it.", fieldOf(tt, tc)))
		c.addHazard(HazardDataDependent, "", fmt.Sprintf(
			"%s is required and has no default: adding it fails when %s has rows.", fieldOf(tt, tc), tt.Name))
	}
	d.graphContent(c, ft, nil, tt, tc)
}

func (d *differ) dropColumn(ft, tt *Table, fc *Column, hint string) {
	// The new servers insert rows without the column until contract drops
	// it, which a NOT NULL column without a default refuses.
	if !fc.Nullable && fc.Default == "" && fc.Generated == "" {
		d.add(&change{
			op: opDropNotNull, phase: Expand, subject: columnSubject(tt.Name, fc.Name),
			table: tt.Name, column: fc,
		})
	}
	c := d.add(&change{
		op: opDropColumn, phase: Contract, subject: columnSubject(tt.Name, fc.Name),
		table: tt.Name, column: fc,
	})
	// A generated column's values follow from the columns it reads.
	if fc.Generated == "" {
		c.addHazard(HazardDestructive, "", fmt.Sprintf("Dropping %s.%s deletes %s%s", tt.Name, fc.Name, valuesOf(ft, fc), hint))
	}
	d.breaksReaders(c, ft.Name, fc.Name, "drops it")
	d.graphContent(c, ft, fc, tt, nil)
}

// diffNullability loosens a column in expand and tightens it in contract.
func (d *differ) diffNullability(tt *Table, fc, tc *Column) {
	switch {
	case !fc.Nullable && tc.Nullable:
		d.add(&change{
			op: opDropNotNull, phase: Expand, subject: columnSubject(tt.Name, tc.Name),
			table: tt.Name, column: tc,
		})
	case fc.Nullable && !tc.Nullable:
		c := d.add(&change{
			op: opSetNotNull, phase: Contract, subject: columnSubject(tt.Name, tc.Name),
			table: tt.Name, column: tc, tableDef: tt,
		})
		c.addHazard(HazardDataDependent, "", fmt.Sprintf(
			"%s becomes required: the step fails when %s has a row where %s is null.", fieldOf(tt, tc), tt.Name, tc.Name))
	}
}

// diffDefault sets a new or changed default in expand and drops one in
// contract. A column whose type changes has its default handled with the
// type.
func (d *differ) diffDefault(tt *Table, fc, tc *Column) {
	switch {
	case fc.Default == tc.Default:
	case tc.Default != "":
		d.add(&change{
			op: opSetDefault, phase: Expand, subject: columnSubject(tt.Name, tc.Name),
			table: tt.Name, column: tc,
		})
	default:
		d.add(&change{
			op: opDropDefault, phase: Contract, subject: columnSubject(tt.Name, tc.Name),
			table: tt.Name, column: tc,
		})
	}
}

func (d *differ) renameConstraint(table, prev, name string) {
	d.add(&change{
		op: opRenameConstraint, phase: Expand, subject: constraintSubject(table, name),
		table: table, name: name, oldName: prev,
	})
}

// diffUniques matches unique constraints by their columns.
func (d *differ) diffUniques(ft, tt *Table, mapColumns func([]string) []string) {
	from := map[string]*Constraint{}
	for _, u := range ft.Uniques {
		from[strings.Join(mapColumns(u.Columns), ",")] = u
	}
	for _, u := range tt.Uniques {
		key := strings.Join(u.Columns, ",")
		if fu := from[key]; fu != nil {
			delete(from, key)
			if fu.Name != u.Name {
				d.renameConstraint(tt.Name, fu.Name, u.Name)
			}
			continue
		}
		c := d.add(&change{
			op: opAddUnique, phase: Expand, subject: constraintSubject(tt.Name, u.Name),
			table: tt.Name, constraint: u, tableDef: tt,
		})
		what := uniqueWhat(tt, u.Columns)
		c.addHazard(HazardCompat, "", fmt.Sprintf(
			"%s becomes unique: servers built from the previous version may write duplicates.", what))
		c.addHazard(HazardDataDependent, "", fmt.Sprintf(
			"Building the unique index fails when %s holds duplicate values of %s.", tt.Name, what))
	}
	for _, fu := range sortedConstraints(from) {
		d.add(&change{
			op: opDropUnique, phase: Contract, subject: constraintSubject(tt.Name, fu.Name),
			table: tt.Name, constraint: fu,
		})
	}
}

// diffForeignKeys matches foreign keys by their columns and the columns
// they reference, then by name.
func (d *differ) diffForeignKeys(ft, tt *Table, mapColumns func([]string) []string) {
	key := func(fk *ForeignKey, columns []string, refTable string, refColumns []string) string {
		return strings.Join(columns, ",") + "->" + refTable + "(" + strings.Join(refColumns, ",") + ")"
	}
	fromByKey, fromByName := map[string]*ForeignKey{}, map[string]*ForeignKey{}
	for _, fk := range ft.ForeignKeys {
		ref := d.renames.table(fk.RefTable)
		refColumns := make([]string, len(fk.RefColumns))
		for i, rc := range fk.RefColumns {
			refColumns[i] = d.renames.column(fk.RefTable, rc)
		}
		fromByKey[key(fk, mapColumns(fk.Columns), ref, refColumns)] = fk
	}
	used := map[string]bool{}
	var unmatched []*ForeignKey
	for _, fk := range tt.ForeignKeys {
		fromFK := fromByKey[key(fk, fk.Columns, fk.RefTable, fk.RefColumns)]
		if fromFK == nil || used[fromFK.Name] {
			unmatched = append(unmatched, fk)
			continue
		}
		used[fromFK.Name] = true
		if fromFK.Name != fk.Name {
			d.renameConstraint(tt.Name, fromFK.Name, fk.Name)
		}
		if fromFK.OnDelete != fk.OnDelete {
			d.add(&change{
				op: opReplaceFK, phase: Contract, subject: constraintSubject(tt.Name, fk.Name),
				table: tt.Name, foreignKey: fk, oldFK: fromFK,
			})
		}
	}
	for _, fk := range ft.ForeignKeys {
		if !used[fk.Name] {
			fromByName[fk.Name] = fk
		}
	}
	for _, fk := range unmatched {
		// A foreign key over columns the previous version already has
		// checks rows its servers wrote: contract, validated online.
		preexisting := true
		for _, col := range fk.Columns {
			if columnNamed(ft, d.renames.prevColumn(tt.Name, col)) == nil {
				preexisting = false
			}
		}
		phase := Expand
		if preexisting {
			phase = Contract
		}
		if fromFK := fromByName[fk.Name]; fromFK != nil {
			delete(fromByName, fk.Name)
			c := d.add(&change{
				op: opReplaceFK, phase: Contract, subject: constraintSubject(tt.Name, fk.Name),
				table: tt.Name, foreignKey: fk, oldFK: fromFK,
			})
			c.addHazard(HazardDataDependent, "", fkReason(tt, fk))
			continue
		}
		c := d.add(&change{
			op: opAddForeignKey, phase: phase, subject: constraintSubject(tt.Name, fk.Name),
			table: tt.Name, foreignKey: fk,
		})
		if preexisting {
			c.addHazard(HazardDataDependent, "", fkReason(tt, fk))
		}
	}
	names := make([]string, 0, len(fromByName))
	for name := range fromByName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d.add(&change{
			op: opDropForeignKey, phase: Contract, subject: constraintSubject(tt.Name, name),
			table: tt.Name, foreignKey: fromByName[name],
		})
	}
}

// diffIndexes matches indexes by their definition, then by name. An index
// over a generated column that is dropped and added again goes with it, so
// it is created again.
func (d *differ) diffIndexes(ft, tt *Table, mapColumns func([]string) []string) {
	key := func(idx *Index, columns []string) string {
		return fmt.Sprintf("%s|%s|%s|%t|%s", strings.Join(columns, ","), idx.Method, idx.Opclass, idx.Unique, idx.Where)
	}
	regenerated := func(columns []string) bool {
		for _, col := range columns {
			if d.regenerated[tt.Name+"."+col] {
				return true
			}
		}
		return false
	}
	fromByKey := map[string]*Index{}
	fromByName := map[string]*Index{}
	for _, idx := range ft.Indexes {
		columns := mapColumns(idx.Columns)
		if regenerated(columns) {
			continue
		}
		fromByKey[key(idx, columns)] = idx
		fromByName[idx.Name] = idx
	}
	var unmatched []*Index
	for _, idx := range tt.Indexes {
		if regenerated(idx.Columns) {
			unmatched = append(unmatched, idx)
			continue
		}
		fromIdx := fromByKey[key(idx, idx.Columns)]
		if fromIdx == nil || fromByName[fromIdx.Name] == nil {
			unmatched = append(unmatched, idx)
			continue
		}
		delete(fromByName, fromIdx.Name)
		if fromIdx.Name != idx.Name {
			d.add(&change{
				op: opRenameIndex, phase: Expand, subject: indexSubject(tt.Name, idx.Name),
				table: tt.Name, name: idx.Name, oldName: fromIdx.Name,
			})
		}
	}
	for _, idx := range unmatched {
		c := &change{
			op: opCreateIndex, phase: Expand, subject: indexSubject(tt.Name, idx.Name),
			table: tt.Name, index: idx, partitioned: tt.PartitionBy != "",
		}
		if fromIdx := fromByName[idx.Name]; fromIdx != nil && !regenerated(idx.Columns) {
			delete(fromByName, idx.Name)
			c.op = opReplaceIndex
		}
		d.add(c)
		if idx.Unique {
			what := uniqueWhat(tt, idx.Columns)
			c.addHazard(HazardCompat, "", fmt.Sprintf(
				"%s becomes unique: servers built from the previous version may write duplicates.", what))
			c.addHazard(HazardDataDependent, "", fmt.Sprintf(
				"Building the unique index fails when %s holds duplicate values of %s.", tt.Name, what))
		}
	}
	names := make([]string, 0, len(fromByName))
	for name := range fromByName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d.add(&change{
			op: opDropIndex, phase: Contract, subject: indexSubject(tt.Name, name),
			table: tt.Name, index: fromByName[name],
		})
	}
}

// dropTables drops the previous model's tables the new one does not have,
// in reverse dependency order: a table before the tables it references.
func (d *differ) dropTables(tables []*Table) {
	// A dropped table that has the same columns as exactly one new table
	// may be that table renamed.
	var created []*Table
	for _, tt := range d.to.Tables {
		if d.fromTables[d.renames.prevTable(tt.Name)] == nil && tt.Kind == TableEntity {
			created = append(created, tt)
		}
	}
	order := dropOrder(tables, d.dropped)
	position := map[string]int{}
	for i, ft := range order {
		position[ft.Name] = i
	}
	for i, ft := range order {
		// A table that references one dropped before it, in a reference
		// cycle, loses that foreign key first.
		for _, fk := range ft.ForeignKeys {
			if j, ok := position[fk.RefTable]; ok && j < i {
				d.add(&change{
					op: opDropForeignKey, phase: Contract, subject: constraintSubject(ft.Name, fk.Name),
					table: ft.Name, foreignKey: fk,
				})
			}
		}
		c := d.add(&change{
			op: opDropTable, phase: Contract, subject: tableSubject(ft.Name),
			table: ft.Name, tableDef: ft, order: i,
		})
		reason := fmt.Sprintf("Dropping table %s deletes its rows (%s).", ft.Name, ft.Origin)
		switch ft.Kind {
		case TableHistory:
			reason = fmt.Sprintf("Dropping %s deletes every recorded version of %s.", ft.Name, ft.Origin)
		case TableJoin:
			reason = fmt.Sprintf("Dropping join table %s deletes the links of %s.", ft.Name, ft.Origin)
		}
		if ft.Kind == TableEntity {
			var same []*Table
			for _, tt := range created {
				if sameColumns(ft, tt) {
					same = append(same, tt)
				}
			}
			if len(same) == 1 {
				reason += fmt.Sprintf(" It may be a rename: table %s has the same columns. If it is, plan with --rename %s=%s.",
					same[0].Name, ft.Name, same[0].Name)
			}
		}
		c.addHazard(HazardDestructive, "", reason)
		d.breaksReaders(c, ft.Name, "", "drops its table")
	}
}

// dropOrder sorts tables so that each comes before the tables it
// references, by name among equals. A reference cycle is broken in name
// order; dropTables drops the foreign keys that point back first.
func dropOrder(tables []*Table, dropped map[string]bool) []*Table {
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	referencedBy := map[string]int{} // dropped table -> dropped tables that reference it, not yet placed
	for _, t := range tables {
		for _, ref := range refsOf(t, dropped) {
			referencedBy[ref]++
		}
	}
	var out []*Table
	placed := map[string]bool{}
	for len(out) < len(tables) {
		progress := false
		for _, t := range tables {
			if placed[t.Name] || referencedBy[t.Name] > 0 {
				continue
			}
			placed[t.Name] = true
			out = append(out, t)
			for _, ref := range refsOf(t, dropped) {
				referencedBy[ref]--
			}
			progress = true
			break
		}
		if !progress {
			for _, t := range tables {
				if !placed[t.Name] {
					placed[t.Name] = true
					out = append(out, t)
					break
				}
			}
		}
	}
	return out
}

// refsOf lists the other dropped tables t references, once each.
func refsOf(t *Table, dropped map[string]bool) []string {
	seen := map[string]bool{}
	var refs []string
	for _, fk := range t.ForeignKeys {
		if fk.RefTable != t.Name && dropped[fk.RefTable] && !seen[fk.RefTable] {
			seen[fk.RefTable] = true
			refs = append(refs, fk.RefTable)
		}
	}
	return refs
}

// sameColumns reports whether two tables have the same columns, by name
// and shape, in any order.
func sameColumns(a, b *Table) bool {
	if len(a.Columns) != len(b.Columns) {
		return false
	}
	for _, ac := range a.Columns {
		bc := columnNamed(b, ac.Name)
		if bc == nil || !sameShape(ac, bc) {
			return false
		}
	}
	return true
}

func sortedConstraints(m map[string]*Constraint) []*Constraint {
	out := make([]*Constraint, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// identifiers lists the identifiers in a SQL expression as Postgres reads
// them, a bare one folded to lowercase: the columns a generated column
// reads.
func identifiers(expr string) []string {
	var out []string
	for _, tok := range tokenizeSQL(expr) {
		if tok.kind != tokIdent {
			continue
		}
		if strings.HasPrefix(tok.text, `"`) {
			out = append(out, tok.value)
		} else {
			out = append(out, strings.ToLower(tok.value))
		}
	}
	return out
}

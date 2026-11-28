package sqlgen

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// An @audience table's triggers on SQLite (D182), which has no trigger
// functions and cannot set NEW, and whose triggers do not fire a trigger
// already running (recursive_triggers is off unless a connection turns it
// on, and nothing can count on that: D1 sets no pragma for a migration). So
// no trigger counts on another to finish its work:
//
//   - trg_<table>_audience_insert, after each insert, refuses an audience or
//     an agent access that is none of the known values (RAISE(ABORT), the
//     message naming the column and the values) and sets the row's
//     resolved columns, as the Postgres function does: everyone is NULL,
//     private a JSON array of the owner alone, named the owner and the ids
//     of audienceNamed in order, inherit the parent's resolved audience, or
//     the owner's alone for a row with no parent; the agent access, the
//     least open of the row's and an inherited parent's.
//   - trg_<table>_audience_update, after each update whose row's resolved
//     columns are not what its audience resolves to, refuses the same and
//     sets them. Its WHEN is what stops it once they are, so it holds
//     whether or not a connection turns recursive triggers on.
//   - trg_<table>_audience_cascade, on a table another inherits from, after
//     an update that changes a row's resolved columns, sets each inheriting
//     child row's from the parent's, as the parent's row now holds them. A
//     child table's own cascade then fires for its children in turn. A table
//     that inherits from itself (a folder in a folder) cannot count on that,
//     since its cascade is the one running: its cascade sets every inheriting
//     row below the parent at once, through a recursive CTE, each the
//     parent's audience and the least open agent access on its way down,
//     and then each other child table's rows below any of them.
//
// The resolved audience is a JSON array of UUIDs in TEXT, as SQLite holds a
// UUID[] column (sqlmigrate's sqliteType), which a read compares the caller
// with through json_each. Each trigger reads the row it resolves as the
// table holds it, by its key, rather than NEW, which another trigger of the
// same write may have moved past.

// sqliteAudience is a table's audience columns quoted for SQLite.
type sqliteAudience struct {
	table, key, audience, named, agents, effective, effectiveAgents, owner string
	// parent, parentKey and parentColumn are empty for a table that
	// inherits from none.
	parent, parentKey, parentColumn string
	values                          []string
}

func (t AudienceTrigger) sqlite(quote func(string) string) sqliteAudience {
	q := func(c string) string {
		if c == "" {
			return ""
		}
		return quote(unquoted(c))
	}
	a := sqliteAudience{
		table: quote(t.Table), key: q(t.Key), audience: q(t.Audience), named: q(t.Named), agents: q(t.Agents),
		effective: q(t.Effective), effectiveAgents: q(t.EffectiveAgents), owner: q(t.Owner),
		parent: q(t.Parent), parentKey: q(t.ParentKey), parentColumn: q(t.ParentColumn),
		values: []string{ir.AudienceEveryone, ir.AudiencePrivate, ir.AudienceNamed},
	}
	if t.Parent != "" {
		a.values = append(a.values, ir.AudienceInherit)
	}
	return a
}

// own is the owner's alone, a JSON array of one id or none, of row.
func (a sqliteAudience) own(row string) string {
	return fmt.Sprintf("CASE WHEN %s.%s IS NULL THEN '[]' ELSE json_array(%s.%s) END", row, a.owner, row, a.owner)
}

// namedOf is the owner and audienceNamed's ids of row, in order.
func (a sqliteAudience) namedOf(row string) string {
	return fmt.Sprintf("(SELECT json_group_array(audience_member.value) FROM (SELECT %s.%s AS value WHERE %s.%s IS NOT NULL UNION ALL SELECT value FROM json_each(COALESCE(%s.%s, '[]')) WHERE value IS NOT NULL) AS audience_member)",
		row, a.owner, row, a.owner, row, a.named)
}

// parentColumnOf is a column of row's parent, NULL when it has none.
func (a sqliteAudience) parentColumnOf(row, column string) string {
	return fmt.Sprintf("(SELECT audience_parent.%s FROM %s AS audience_parent WHERE audience_parent.%s = %s.%s)", column, a.parent, a.parentKey, row, a.parentColumn)
}

// hasParent is whether row names a parent the parent table holds.
func (a sqliteAudience) hasParent(row string) string {
	return fmt.Sprintf("%s.%s IS NOT NULL AND EXISTS (SELECT 1 FROM %s AS audience_parent WHERE audience_parent.%s = %s.%s)", row, a.parentColumn, a.parent, a.parentKey, row, a.parentColumn)
}

// resolved is row's effective audience: NULL for everyone, else a JSON
// array of the ids it is visible to.
func (a sqliteAudience) resolved(row string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CASE %s.%s WHEN 'everyone' THEN NULL WHEN 'private' THEN %s WHEN 'named' THEN %s", row, a.audience, a.own(row), a.namedOf(row))
	if a.parent != "" {
		fmt.Fprintf(&b, " ELSE CASE WHEN %s THEN %s ELSE %s END END", a.hasParent(row), a.parentColumnOf(row, a.effective), a.own(row))
	} else {
		fmt.Fprintf(&b, " ELSE %s END", a.own(row))
	}
	return b.String()
}

// agentRank is an agent access's openness, 0 the most open, as
// ir.AgentAccesses orders them; NULL for none of them.
func agentRank(x string) string {
	var b strings.Builder
	b.WriteString("CASE " + x)
	for i, v := range ir.AgentAccesses {
		fmt.Fprintf(&b, " WHEN %s THEN %d", stringLiteral(v), i)
	}
	b.WriteString(" END")
	return b.String()
}

// resolvedAgents is row's effective agent access: its own, or an inherited
// parent's when that is less open.
func (a sqliteAudience) resolvedAgents(row string) string {
	if a.parent == "" {
		return row + "." + a.agents
	}
	parent := a.parentColumnOf(row, a.effectiveAgents)
	return fmt.Sprintf("CASE WHEN %s.%s = 'inherit' AND %s AND %s > %s THEN %s ELSE %s.%s END",
		row, a.audience, a.hasParent(row), agentRank(parent), agentRank(row+"."+a.agents), parent, row, a.agents)
}

// invalid is whether NEW's audience or agent access is none of the values.
func (a sqliteAudience) invalidAudience() string {
	return fmt.Sprintf("NEW.%s IS NULL OR NEW.%s NOT IN (%s)", a.audience, a.audience, literals(a.values))
}

func (a sqliteAudience) invalidAgents() string {
	return fmt.Sprintf("NEW.%s IS NULL OR NEW.%s NOT IN (%s)", a.agents, a.agents, literals(ir.AgentAccesses))
}

// SQLiteTriggers renders the table's audience on SQLite: the insert's and
// the update's triggers, each of which refuses an unknown audience or agent
// access and sets the row's resolved columns. quote quotes an identifier as
// the SQLite dialect does.
func (t AudienceTrigger) SQLiteTriggers(quote func(string) string) []SQLiteTrigger {
	a := t.sqlite(quote)
	refusals := fmt.Sprintf("  SELECT RAISE(ABORT, %s) WHERE %s;\n  SELECT RAISE(ABORT, %s) WHERE %s;\n",
		stringLiteral(fmt.Sprintf("%s.%s is none of %s", t.Table, unquoted(t.Audience), strings.Join(a.values, ", "))), a.invalidAudience(),
		stringLiteral(fmt.Sprintf("%s.%s is none of %s", t.Table, unquoted(t.Agents), strings.Join(ir.AgentAccesses, ", "))), a.invalidAgents())
	set := fmt.Sprintf("  UPDATE %s SET %s = %s,\n    %s = %s\n    WHERE %s.%s = NEW.%s;\n",
		a.table, a.effective, a.resolved(a.table), a.effectiveAgents, a.resolvedAgents(a.table), a.table, a.key, a.key)
	comment := fmt.Sprintf("  -- The @audience of %s (D156, D182): the row's audience resolved into\n  -- %s and %s, whatever writes the row.\n", t.Origin, unquoted(t.Effective), unquoted(t.EffectiveAgents))
	insert := fmt.Sprintf("CREATE TRIGGER %s AFTER INSERT ON %s FOR EACH ROW\nBEGIN\n%s%s%sEND",
		quote(t.TriggerName+"_insert"), a.table, comment, refusals, set)
	update := fmt.Sprintf("CREATE TRIGGER %s AFTER UPDATE ON %s FOR EACH ROW\nWHEN %s\n  OR %s\n  OR NEW.%s IS NOT %s\n  OR NEW.%s IS NOT %s\nBEGIN\n%s%s%sEND",
		quote(t.TriggerName+"_update"), a.table, a.invalidAudience(), a.invalidAgents(), a.effective, a.resolved("NEW"), a.effectiveAgents, a.resolvedAgents("NEW"), comment, refusals, set)
	return []SQLiteTrigger{
		{Name: t.TriggerName + "_insert", Table: t.Table, Definition: insert, Refuses: true},
		{Name: t.TriggerName + "_update", Table: t.Table, Definition: update, Refuses: true},
	}
}

// SQLiteBackfillSQL resolves every row's audience once, when the trigger is
// created on a table that holds rows: an update that sets nothing, which
// the update's trigger resolves.
func (t AudienceTrigger) SQLiteBackfillSQL(quote func(string) string) string {
	a := t.sqlite(quote)
	return fmt.Sprintf("UPDATE %s SET %s = %s", a.table, a.audience, a.audience)
}

// SQLiteTrigger renders the cascade on SQLite: after an update that changes
// a row's resolved columns, each inheriting child row's resolved again from
// it. triggers are the schema's audience triggers, which hold each child
// table's columns. quote quotes an identifier as the SQLite dialect does.
func (c AudienceCascade) SQLiteTrigger(triggers []AudienceTrigger, quote func(string) string) (SQLiteTrigger, error) {
	byTable := map[string]AudienceTrigger{}
	for _, t := range triggers {
		byTable[t.Table] = t
	}
	parent, ok := byTable[c.Table]
	if !ok {
		return SQLiteTrigger{}, fmt.Errorf("sqlgen: the audience cascade of %s has no audience trigger of its table", c.Table)
	}
	p := parent.sqlite(quote)
	var self *sqliteAudience
	var others []sqliteAudience
	for _, child := range c.Children {
		if child.Queue != nil {
			return SQLiteTrigger{}, fmt.Errorf("sqlgen: @audience on %s names a queue, which SQLite has no form of yet; resolve its rows in the writer's transaction (#734)", child.Origin)
		}
		t, ok := byTable[child.Table]
		if !ok {
			return SQLiteTrigger{}, fmt.Errorf("sqlgen: the audience cascade of %s names %s, which has no audience trigger", c.Table, child.Table)
		}
		a := t.sqlite(quote)
		if child.Table == c.Table {
			self = &a
			continue
		}
		others = append(others, a)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TRIGGER %s AFTER UPDATE ON %s FOR EACH ROW\nWHEN NEW.%s IS NOT OLD.%s OR NEW.%s IS NOT OLD.%s\nBEGIN\n",
		quote(c.TriggerName), p.table, p.effective, p.effective, p.effectiveAgents, p.effectiveAgents)
	fmt.Fprintf(&b, "  -- The children that inherit the audience of %s (D156, D182), resolved\n  -- again when its own resolved audience changes.\n", c.Origin)
	// below is the condition on a child table's foreign key column fk: a
	// child of the parent, or of an inheriting row below it.
	below := func(fk string) string { return fmt.Sprintf("%s = NEW.%s", fk, p.key) }
	if self != nil {
		// Every inheriting row below the parent, with the least open agent
		// access on its way down; UNION stops at a cycle.
		tree := fmt.Sprintf("WITH RECURSIVE audience_tree(k, g) AS (SELECT audience_child.%s, CASE WHEN %s > %s THEN audience_root.%s ELSE audience_child.%s END FROM %s AS audience_child JOIN %s AS audience_root ON audience_root.%s = NEW.%s WHERE audience_child.%s = NEW.%s AND audience_child.%s = 'inherit' UNION SELECT audience_child.%s, CASE WHEN %s > %s THEN audience_tree.g ELSE audience_child.%s END FROM %s AS audience_child JOIN audience_tree ON audience_child.%s = audience_tree.k WHERE audience_child.%s = 'inherit')",
			p.key, agentRank("audience_root."+p.effectiveAgents), agentRank("audience_child."+p.agents), p.effectiveAgents, p.agents, p.table, p.table, p.key, p.key, self.parentColumn, p.key, p.audience,
			p.key, agentRank("audience_tree.g"), agentRank("audience_child."+p.agents), p.agents, p.table, self.parentColumn, p.audience)
		fmt.Fprintf(&b, "  UPDATE %s SET %s = (SELECT audience_root.%s FROM %s AS audience_root WHERE audience_root.%s = NEW.%s),\n    %s = (%s SELECT g FROM audience_tree WHERE audience_tree.k = %s.%s ORDER BY %s DESC LIMIT 1)\n    WHERE %s.%s IN (%s SELECT k FROM audience_tree);\n",
			p.table, p.effective, p.effective, p.table, p.key, p.key,
			p.effectiveAgents, tree, p.table, p.key, agentRank("g"),
			p.table, p.key, tree)
		below = func(fk string) string {
			return fmt.Sprintf("(%s = NEW.%s OR %s IN (%s SELECT k FROM audience_tree))", fk, p.key, fk, tree)
		}
	}
	for _, a := range others {
		fmt.Fprintf(&b, "  UPDATE %s SET %s = %s,\n    %s = %s\n    WHERE %s.%s = 'inherit' AND %s;\n",
			a.table, a.effective, a.resolved(a.table), a.effectiveAgents, a.resolvedAgents(a.table), a.table, a.audience, below(a.table+"."+a.parentColumn))
	}
	b.WriteString("END")
	return SQLiteTrigger{Name: c.TriggerName, Table: c.Table, Definition: b.String()}, nil
}

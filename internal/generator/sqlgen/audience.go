package sqlgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// An @audience table's triggers (D156). The table holds, on each row, its
// audience (audience, audienceNamed, agentAccess) and the audience
// resolved (effectiveAudience, effectiveAgents), which the reads compare
// the caller with. The triggers keep the resolved columns, whatever writes
// the row, so a read stays one filter on the row:
//
//   - Audience, on every @audience table, before each insert and update,
//     refuses an audience or an agent access that is none of the known
//     values and sets the resolved columns: everyone is null, private the
//     owner alone, named the owner and audienceNamed, and inherit what the
//     parent row resolves to through the table's inheritFrom, or the owner
//     alone for a row with no parent, so a row a deleted parent leaves
//     behind is not shown to everyone. An inherited agent access is the
//     least open of the row's own and the parent's.
//   - AudienceCascade, on a table another inherits from, after each update
//     that changes the row's resolved columns, updates each child row that
//     inherits, so its own Audience trigger resolves it again from the
//     parent's new columns, and a grandchild's in turn.
//
// On Postgres each is a trigger function and the trigger that runs it. A
// cycle of parents is the database's to refuse, by the depth of the
// triggers' recursion. SQLite's forms (D182) are in audience_sqlite.go.

// audienceSuffix ends the name of an @audience table's function, after its
// table, and cascadeSuffix that of its cascade's; each trigger's is that
// name after trg_.
const (
	audienceSuffix = "_audience"
	cascadeSuffix  = "_audience_cascade"
)

// AudienceTrigger is the Audience trigger of one @audience table.
type AudienceTrigger struct {
	// Origin is the table's type.
	Origin string

	Table, QuotedTable string
	// Key is the table's key's quoted column, which the SQLite triggers
	// find the row they resolve by.
	Key string

	FunctionName string
	TriggerName  string

	// The quoted columns of the table's audience and of its owner.
	Audience, Named, Agents, Effective, EffectiveAgents, Owner string

	// Parent is the table inheritFrom names, with its key's quoted column,
	// and ParentColumn the row's foreign key to it; empty for a table that
	// inherits from none.
	Parent, ParentKey, ParentColumn string
}

// AudienceCascade is the cascade trigger of a table others inherit from:
// each child table, with the child's foreign key to the row.
type AudienceCascade struct {
	Origin string

	Table, QuotedTable string
	// Key is the table's key's quoted column, and Effective and
	// EffectiveAgents its resolved columns'.
	Key, Effective, EffectiveAgents string

	FunctionName string
	TriggerName  string

	Children []AudienceChild
}

// AudienceChild is a table that inherits from a cascade's table: its
// name, quoted name, its audience column and its foreign key to the parent.
type AudienceChild struct {
	Table                               string
	QuotedTable, Audience, ParentColumn string
}

// buildAudience builds the triggers of the schema's @audience tables from
// the tables as sqlgen built them, sorted by table.
func buildAudience(tableTypes []*ir.TypeDef, tables []Table) ([]AudienceTrigger, []AudienceCascade, error) {
	byType := map[string]*Table{}
	for i := range tables {
		// A type's own table: a join or a history table names its type
		// too, under another name.
		if td := typeNamed(tableTypes, tables[i].OriginalName); td != nil && tables[i].Name == sqlutil.TableName(td) {
			byType[tables[i].OriginalName] = &tables[i]
		}
	}
	var triggers []AudienceTrigger
	cascades := map[string]*AudienceCascade{}
	for _, td := range tableTypes {
		if td.Audience == nil {
			continue
		}
		table := byType[td.Name]
		if table == nil {
			return nil, nil, fmt.Errorf("sqlgen: @audience on %s: %s has no table", td.Name, td.Name)
		}
		column := func(field string) (string, error) {
			for i := range table.Columns {
				if table.Columns[i].Origin == td.Name+"."+field {
					return table.Columns[i].QuotedName, nil
				}
			}
			return "", fmt.Errorf("sqlgen: @audience on %s: table %s has no column of field %s", td.Name, table.Name, field)
		}
		t := AudienceTrigger{
			Origin:       td.Name,
			Table:        table.Name,
			QuotedTable:  table.QuotedName,
			Key:          table.PrimaryKey,
			FunctionName: table.Name + audienceSuffix,
			TriggerName:  "trg_" + table.Name + audienceSuffix,
		}
		var err error
		for _, c := range []struct {
			field string
			into  *string
		}{
			{ir.AudienceField, &t.Audience},
			{ir.AudienceNamedField, &t.Named},
			{ir.AgentAccessField, &t.Agents},
			{ir.EffectiveAudienceField, &t.Effective},
			{ir.EffectiveAgentsField, &t.EffectiveAgents},
			{ownerField(td), &t.Owner},
		} {
			if *c.into, err = column(c.field); err != nil {
				return nil, nil, err
			}
		}
		if from := td.Audience.InheritFrom; from != "" {
			fd := fieldOf(td, from)
			if fd == nil || fd.Relation == nil {
				return nil, nil, fmt.Errorf("sqlgen: @audience on %s: inheritFrom %s is no relation", td.Name, from)
			}
			parentType := typeNamed(tableTypes, fd.Relation.Type)
			parent := byType[fd.Relation.Type]
			if parent == nil || parentType == nil || parentType.Audience == nil {
				return nil, nil, fmt.Errorf("sqlgen: @audience on %s: inheritFrom %s names %s, which is no @audience table", td.Name, from, fd.Relation.Type)
			}
			if t.ParentColumn, err = column(from); err != nil {
				return nil, nil, err
			}
			t.Parent, t.ParentKey = parent.QuotedName, parent.PrimaryKey
			cascade := cascades[parent.Name]
			if cascade == nil {
				cascade = &AudienceCascade{
					Origin:       parentType.Name,
					Table:        parent.Name,
					QuotedTable:  parent.QuotedName,
					Key:          t.ParentKey,
					FunctionName: parent.Name + cascadeSuffix,
					TriggerName:  "trg_" + parent.Name + cascadeSuffix,
				}
				cascades[parent.Name] = cascade
			}
			cascade.Children = append(cascade.Children, AudienceChild{Table: t.Table, QuotedTable: t.QuotedTable, Audience: t.Audience, ParentColumn: t.ParentColumn})
		}
		for _, name := range []string{t.TriggerName, "trg_" + table.Name + cascadeSuffix} {
			if len(name) > sqlutil.PostgresIdentifierLimit {
				return nil, nil, fmt.Errorf("sqlgen: @audience on %s: its trigger on table %s is %s, %d bytes, over PostgreSQL's %d-byte limit; give the table a shorter name (D156)",
					td.Name, table.Name, name, len(name), sqlutil.PostgresIdentifierLimit)
			}
		}
		triggers = append(triggers, t)
	}
	// A cascade's resolved columns are its table's, which is @audience.
	var out []AudienceCascade
	for _, cascade := range cascades {
		for _, t := range triggers {
			if t.Table == cascade.Table {
				cascade.Effective, cascade.EffectiveAgents = t.Effective, t.EffectiveAgents
			}
		}
		out = append(out, *cascade)
	}
	slices.SortStableFunc(triggers, func(a, b AudienceTrigger) int { return strings.Compare(a.Table, b.Table) })
	slices.SortStableFunc(out, func(a, b AudienceCascade) int { return strings.Compare(a.Table, b.Table) })
	return triggers, out, nil
}

// ownerField is the field that holds an @audience table's owner: the one
// its owner names, a relation's by the relation's name, else createdBy.
func ownerField(td *ir.TypeDef) string {
	owner := td.Audience.Owner
	if owner == "" {
		return sqlutil.AuditCreatedBy
	}
	if fd := fieldOf(td, strings.TrimSuffix(owner, "Id")); fd != nil && fd.Relation != nil {
		return fd.Name
	}
	return owner
}

func fieldOf(td *ir.TypeDef, name string) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd != nil && fd.Name == name {
			return fd
		}
	}
	return nil
}

func typeNamed(types []*ir.TypeDef, name string) *ir.TypeDef {
	for _, td := range types {
		if td.Name == name {
			return td
		}
	}
	return nil
}

// Trigger is the Postgres trigger that executes the function before each
// insert and update of a row. It refuses an audience it does not know, so
// the planner places it as a refusing trigger is; a server of the
// previous version writes no audience and takes the column's default.
func (t AudienceTrigger) Trigger() Trigger {
	return Trigger{
		Name:        t.TriggerName,
		Timing:      "BEFORE INSERT OR UPDATE",
		Table:       t.Table,
		QuotedTable: t.QuotedTable,
		Function:    t.FunctionName,
	}
}

// BackfillSQL resolves every row's audience once, when the trigger is
// created on a table that holds rows: an update that sets nothing, which
// the trigger turns into the resolved columns.
func (t AudienceTrigger) BackfillSQL() string {
	return fmt.Sprintf("UPDATE %s SET %s = %s", t.QuotedTable, t.Audience, t.Audience)
}

// FunctionSQL renders the Postgres trigger function of the table's
// audience: it refuses an unknown audience or agent access with
// check_violation (23514) under the function's name, and sets the
// resolved columns.
func (t AudienceTrigger) FunctionSQL() string {
	var b strings.Builder
	using := fmt.Sprintf("USING ERRCODE = 'check_violation', CONSTRAINT = %s;", stringLiteral(t.FunctionName))
	audiences := []string{ir.AudienceEveryone, ir.AudiencePrivate, ir.AudienceNamed}
	if t.Parent != "" {
		audiences = append(audiences, ir.AudienceInherit)
	}
	b.WriteString("DECLARE\n  parent_audience UUID[];\n  parent_agents TEXT;\n  has_parent BOOLEAN := FALSE;\n  own UUID[];\n")
	fmt.Fprintf(&b, "BEGIN\n  -- The @audience of %s (D156): the row's audience resolved into\n  -- %s and %s, whatever writes the row.\n", t.Origin, t.Effective, t.EffectiveAgents)
	fmt.Fprintf(&b, "  IF NEW.%s IS NULL OR NEW.%s NOT IN (%s) THEN\n    RAISE EXCEPTION '%s.%s is none of %s: %%', NEW.%s\n      %s\n  END IF;\n",
		t.Audience, t.Audience, literals(audiences), raiseFormat(t.Table), raiseFormat(unquoted(t.Audience)), raiseFormat(strings.Join(audiences, ", ")), t.Audience, using)
	fmt.Fprintf(&b, "  IF NEW.%s IS NULL OR NEW.%s NOT IN (%s) THEN\n    RAISE EXCEPTION '%s.%s is none of %s: %%', NEW.%s\n      %s\n  END IF;\n",
		t.Agents, t.Agents, literals(ir.AgentAccesses), raiseFormat(t.Table), raiseFormat(unquoted(t.Agents)), raiseFormat(strings.Join(ir.AgentAccesses, ", ")), t.Agents, using)
	fmt.Fprintf(&b, "  own := array_remove(ARRAY[NEW.%s]::UUID[], NULL);\n", t.Owner)
	if t.Parent != "" {
		fmt.Fprintf(&b, "  IF NEW.%s = 'inherit' AND NEW.%s IS NOT NULL THEN\n", t.Audience, t.ParentColumn)
		fmt.Fprintf(&b, "    SELECT p.%s, p.%s INTO parent_audience, parent_agents\n      FROM %s p WHERE p.%s = NEW.%s;\n    has_parent := FOUND;\n  END IF;\n",
			t.Effective, t.EffectiveAgents, t.Parent, t.ParentKey, t.ParentColumn)
	}
	fmt.Fprintf(&b, "  NEW.%s := CASE NEW.%s\n", t.Effective, t.Audience)
	fmt.Fprintf(&b, "    WHEN 'everyone' THEN NULL\n")
	fmt.Fprintf(&b, "    WHEN 'private' THEN own\n")
	fmt.Fprintf(&b, "    WHEN 'named' THEN own || array_remove(COALESCE(NEW.%s, '{}'::UUID[]), NULL)\n", t.Named)
	b.WriteString("    -- inherit: the parent's, or the owner's alone with no parent.\n")
	b.WriteString("    ELSE CASE WHEN has_parent THEN parent_audience ELSE own END\n  END;\n")
	fmt.Fprintf(&b, "  NEW.%s := NEW.%s;\n", t.EffectiveAgents, t.Agents)
	b.WriteString("  -- An inherited agent access is the least open of the two.\n")
	fmt.Fprintf(&b, "  IF has_parent AND array_position(ARRAY[%s], parent_agents) > array_position(ARRAY[%s], NEW.%s) THEN\n    NEW.%s := parent_agents;\n  END IF;\n",
		literals(ir.AgentAccesses), literals(ir.AgentAccesses), t.Agents, t.EffectiveAgents)
	b.WriteString("  RETURN NEW;\nEND;\n")
	body := b.String()
	tag := dollarQuote(body)
	return fmt.Sprintf("CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS %s\n%s%s LANGUAGE plpgsql", sqlutil.QuoteIdentifier(t.FunctionName), tag, body, tag)
}

// Trigger is the Postgres trigger that executes the cascade after each
// update of a parent row.
func (c AudienceCascade) Trigger() Trigger {
	return Trigger{
		Name:        c.TriggerName,
		Timing:      "AFTER UPDATE",
		Table:       c.Table,
		QuotedTable: c.QuotedTable,
		Function:    c.FunctionName,
	}
}

// FunctionSQL renders the cascade: when an update changes the row's
// resolved columns, each child row that inherits is updated with no
// change, so its own trigger resolves it again.
func (c AudienceCascade) FunctionSQL() string {
	var b strings.Builder
	fmt.Fprintf(&b, "BEGIN\n  -- The children that inherit the audience of %s (D156), resolved\n  -- again when its own resolved audience changes.\n", c.Origin)
	fmt.Fprintf(&b, "  IF NEW.%s IS DISTINCT FROM OLD.%s OR NEW.%s IS DISTINCT FROM OLD.%s THEN\n", c.Effective, c.Effective, c.EffectiveAgents, c.EffectiveAgents)
	for _, child := range c.Children {
		fmt.Fprintf(&b, "    UPDATE %s SET %s = %s WHERE %s = NEW.%s AND %s = 'inherit';\n",
			child.QuotedTable, child.Audience, child.Audience, child.ParentColumn, c.Key, child.Audience)
	}
	b.WriteString("  END IF;\n  RETURN NULL;\nEND;\n")
	body := b.String()
	tag := dollarQuote(body)
	return fmt.Sprintf("CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS %s\n%s%s LANGUAGE plpgsql", sqlutil.QuoteIdentifier(c.FunctionName), tag, body, tag)
}

// literals is values as a list of SQL string literals.
func literals(values []string) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = stringLiteral(v)
	}
	return strings.Join(out, ", ")
}

// unquoted is a quoted identifier as its name.
func unquoted(quoted string) string {
	return strings.Trim(quoted, `"`)
}

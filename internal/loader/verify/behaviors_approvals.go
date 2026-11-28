package verify

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// approvalsBehavior is the name of the core's Approvals.
const approvalsBehavior = "Approvals"

// checkApprovalsConfig holds the config of an Approvals use a table
// implements to the table (D104, D200), in the core's words where the core
// sees the problem (superschematic_rules_behaviors::approvals): the Workflow
// its gates are of is the one its status names, or the table's one
// Workflow; each gate and each state its from names is one of that
// Workflow's states, from not naming the gate's own; each field a gate's
// resetOn, pinOn, excludeSubjectsIn and when name is an own field of the
// table; a gate that excludes the creator (excludeCreator absent or true)
// is on a table with a scalar createdBy to read the creator from
// (behaviorlayout.AuditFields), and one that excludes the creator's person
// on a table with an own createdFor. A gate that reads its requirements
// from rows names no requirement member and names its resetOn, its in is
// an own list field, and what only the IR shows is the build's
// (approvalsRowsProblem): its schema is a table of the schema that
// composes a behavior, so the core reads its rows, its link is a to-one
// relation of it, to this table when in is absent, and each requirement
// member it holds is of a type the core reads. What only the IR shows of a
// field is the build's too: a field a behavior owns, a relation named by
// its field and an audit field, none of which an update the core sees
// changes, and a field the core reads no value of (unread), whose changes
// it never sees. Each problem is `type <T>: behavior Approvals config:
// <reason>`, and like the core it reports the first: the status, then each
// gate in the canonical config's order, its state, its from, its resetOn,
// its pinOn, its requirements, then its creator. A config the configSchema
// refuses is reported by checkTypeBehaviors and left alone here.
func checkApprovalsConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		Status *string                               `json:"status"`
		Gates  map[string]map[string]json.RawMessage `json:"gates"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, fmt.Sprintf(format, args...))
	}
	workflow, why := flowOf(td, config.Status)
	if why != "" {
		fail("%s", why)
		return
	}
	all := strings.Join(workflow.states, ", ")
	var fields *tableFields
	ownField := func(state, member, name string) bool {
		if fields == nil {
			fields = tableFieldsOf(schema, td, uses)
		}
		class, owner, column, fd := fields.classify(name, false)
		switch class {
		case fieldOwn:
			if what := unread(column, fd); what != "" {
				fail("gate %q: %s names %q, which is %s", state, member, name, what)
				return false
			}
			return true
		case fieldOwned:
			fail("gate %q: %s names %q, a field %s owns: %s names the table's own fields, whose updates the core sees", state, member, name, owner, member)
		case fieldRelation:
			fail("gate %q: %s names %q, a relation: name the field that holds its key, %sId", state, member, name, name)
		case fieldAudit:
			fail("gate %q: %s names %q, an audit field the writes stamp, which no update changes", state, member, name)
		case fieldMissing:
			fail("gate %q: %s names %q, which is not a field of %s (its fields: %s)", state, member, name, td.Name, fields.names())
		}
		return false
	}
	creator, creatorRead := false, false
	for _, state := range slices.Sorted(maps.Keys(config.Gates)) {
		gate := config.Gates[state]
		var from, resetOn, pinOn, excluded []string
		var excludeCreator, excludeCreatorPerson *bool
		var when []struct {
			Field string `json:"field"`
		}
		var rows *struct {
			Schema string `json:"schema"`
			Link   string `json:"link"`
			In     string `json:"in"`
		}
		for member, into := range map[string]any{
			"from": &from, "resetOn": &resetOn, "pinOn": &pinOn, "excludeSubjectsIn": &excluded,
			"excludeCreator": &excludeCreator, "excludeCreatorPerson": &excludeCreatorPerson,
			"when": &when, "requirementsFrom": &rows,
		} {
			if raw, ok := gate[member]; ok && json.Unmarshal(raw, into) != nil {
				return
			}
		}
		if !workflow.has(state) {
			fail("gate %q is not a state of the type's Workflow (%s)", state, all)
			return
		}
		for _, source := range from {
			if source == state {
				fail("gate %q: from names %q itself, and no transition leads from a state to itself", state, state)
				return
			}
			if !workflow.has(source) {
				fail("gate %q: from state %q is not a state of the type's Workflow (%s)", state, source, all)
				return
			}
		}
		for _, name := range resetOn {
			if !ownField(state, "resetOn", name) {
				return
			}
		}
		for _, name := range pinOn {
			if !ownField(state, "pinOn", name) {
				return
			}
		}
		if rows != nil {
			for _, member := range approvalsRowMembers {
				if _, ok := gate[member]; ok {
					fail("gate %q reads its requirements from %s's rows, so it names no %s: each row holds its own", state, rows.Schema, member)
					return
				}
			}
			if _, ok := gate["resetOn"]; !ok {
				fail("gate %q reads its requirements from %s's rows and names no resetOn, so any update would consume every sign-off, the writes of the facts its rows read among them: name the fields whose change spends every sign-off, [] for none, and give each row its own resetOn", state, rows.Schema)
				return
			}
			if rows.In != "" {
				if !ownField(state, "requirementsFrom's in", rows.In) {
					return
				}
				if fd := sqlutil.FieldNamed(td, rows.In); fd == nil || !fd.TypeRef.IsArray {
					fail("gate %q: requirementsFrom's in names %q, which is no list: in names a list field of %s that holds the keys %s.%s points at", state, rows.In, td.Name, rows.Schema, rows.Link)
					return
				}
			}
			if why := approvalsRowsProblem(schema, td, rows.Schema, rows.Link, rows.In); why != "" {
				fail("gate %q: %s", state, why)
				return
			}
			continue
		}
		for _, name := range excluded {
			if !ownField(state, "excludeSubjectsIn", name) {
				return
			}
		}
		for i, condition := range when {
			if !ownField(state, fmt.Sprintf("when condition %d", i), condition.Field) {
				return
			}
		}
		if excludeCreatorPerson != nil && *excludeCreatorPerson && sqlutil.FieldNamed(td, approvalsCreatedFor) == nil {
			fail("gate %q: excludeCreatorPerson reads the person the creator acted for from createdFor, and %s has no field createdFor", state, td.Name)
			return
		}
		if excludeCreator == nil || *excludeCreator {
			if !creatorRead {
				creator, creatorRead = len(behaviorlayout.AuditFields(schema, td)) > 0, true
			}
			if !creator {
				fail("gate %q excludes the creator (excludeCreator is true when absent), and %s has no field createdBy to read the creator from: give %s a createdBy field, or set excludeCreator to false", state, td.Name, td.Name)
				return
			}
		}
	}
}

// The members a gate that reads its requirements from rows leaves to them
// (D200), in the order the core names them.
var approvalsRowMembers = []string{
	"callers", "count", "excludeCreator", "excludeCreatorPerson", "excludeSubjectsIn",
	"permission", "proof", "proofWithinMs", "recheckPermission", "when",
}

// approvalsCreatedFor is the own field excludeCreatorPerson reads (D200).
const approvalsCreatedFor = "createdFor"

// approvalsMemberTypes are the types the core reads a requirement row's
// members as (D200): an IR type a member's column may have, or "list" for
// a list of strings or of an enum's values, or "json" for any JSON a host
// hands as a value.
var approvalsMemberTypes = map[string][]string{
	"name":                 {"string"},
	"count":                {"Generic.Int64", "Generic.Int32", "integer", "number"},
	"permission":           {"string"},
	"callers":              {"list"},
	"proof":                {"string", "enum"},
	"proofWithinMs":        {"Generic.Int64", "Generic.Int32", "integer", "number"},
	"excludeCreator":       {"boolean"},
	"excludeCreatorPerson": {"boolean"},
	"excludeSubjectsIn":    {"list"},
	"resetOn":              {"list"},
	"recheckPermission":    {"boolean"},
	"when":                 {"json"},
	"archivedAt":           {"Temporal.DateTime"},
}

// approvalsRowsProblem is why a gate cannot read its requirements from
// the rows of the table named, by its to-one relation link, or "" (D200):
// the table composes a behavior, so the core reads its rows; the link is a
// to-one relation of it, to td when in is empty; it holds a permission; and
// each requirement member it holds is of a type the core reads.
func approvalsRowsProblem(schema *ir.Schema, td *ir.TypeDef, table, link, in string) string {
	rows := schema.Types[table]
	if rows == nil || rows.Role != ir.RoleDBTable || rows.JsonField {
		return fmt.Sprintf("requirementsFrom names %s, which is not a table of %s", table, schema.Name)
	}
	if len(rows.TableBehaviors()) == 0 {
		return fmt.Sprintf("requirementsFrom names %s, which composes no behavior, so the behaviors' core holds none of its rows: a gate reads its requirements from a table that composes one, Revisions among them (D103)", table)
	}
	field := fieldNamedIn(rows, link)
	if field == nil || field.Owned() || !sqlutil.IsTableRelation(schema, field) || field.TypeRef.IsArray || (in == "" && field.TypeRef.Name != td.Name) {
		target := td.Name
		if in != "" {
			target = "a table"
		}
		return fmt.Sprintf("requirementsFrom's link names %q, which is not a to-one relation of %s to %s", link, table, target)
	}
	if sqlutil.FieldNamed(rows, "permission") == nil {
		return fmt.Sprintf("requirementsFrom names %s, which has no field permission: each row names the permission its signers hold", table)
	}
	for _, member := range slices.Sorted(maps.Keys(approvalsMemberTypes)) {
		fd := sqlutil.FieldNamed(rows, member)
		if fd == nil {
			continue
		}
		if !approvalsMemberFits(schema, fd, approvalsMemberTypes[member]) {
			return fmt.Sprintf("requirementsFrom names %s, whose %s holds %s, which the core does not read as a requirement's %s", table, member, fd.TypeRef.Name, member)
		}
	}
	return ""
}

// approvalsMemberFits says whether a requirement row's field is of one of
// the kinds the core reads the member as.
func approvalsMemberFits(schema *ir.Schema, fd *ir.FieldDef, kinds []string) bool {
	name := fd.TypeRef.Name
	for _, kind := range kinds {
		switch kind {
		case "json":
			if fd.JsonField || name == "Generic.JSON" {
				return true
			}
		case "list":
			if fd.TypeRef.IsArray && (name == "string" || schema.Enums[name] != nil) {
				return true
			}
		case "enum":
			if !fd.TypeRef.IsArray && schema.Enums[name] != nil {
				return true
			}
		default:
			if !fd.TypeRef.IsArray && !fd.TypeRef.IsMap && name == kind {
				return true
			}
		}
	}
	return false
}

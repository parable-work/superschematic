package verify

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// approvalsBehavior is the name of the core's Approvals.
const approvalsBehavior = "Approvals"

// checkApprovalsConfig holds the config of an Approvals use a table
// implements to the table (D104), in the core's words where the core sees
// the problem (superschematic_rules_behaviors::approvals): the Workflow
// its gates are of is the one its status names, or the table's one
// Workflow; each gate and each state its from names is one of that
// Workflow's states, from not naming the gate's own; each field a gate's
// resetOn names is an own field of the table; and a gate that excludes the
// creator (excludeCreator absent or true) is on a table with a scalar
// createdBy to read the creator from (behaviorlayout.AuditFields). What only
// the IR shows is the build's: a resetOn field a behavior owns, a relation
// named by its field and an audit field, none of which an update the core
// sees changes, and a field the core reads no value of (unread), whose
// changes it never sees. Each problem is `type <T>: behavior Approvals config:
// <reason>`, and like the core it reports the first: the status, then each
// gate in the canonical config's order, its state, its from, its resetOn,
// then its creator. A config the configSchema refuses is reported by
// checkTypeBehaviors and left alone here.
func checkApprovalsConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		Status *string `json:"status"`
		Gates  map[string]struct {
			From           []string `json:"from"`
			ExcludeCreator *bool    `json:"excludeCreator"`
			ResetOn        []string `json:"resetOn"`
		} `json:"gates"`
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
	creator, creatorRead := false, false
	for _, state := range slices.Sorted(maps.Keys(config.Gates)) {
		gate := config.Gates[state]
		if !workflow.has(state) {
			fail("gate %q is not a state of the type's Workflow (%s)", state, all)
			return
		}
		for _, source := range gate.From {
			if source == state {
				fail("gate %q: from names %q itself, and no transition leads from a state to itself", state, state)
				return
			}
			if !workflow.has(source) {
				fail("gate %q: from state %q is not a state of the type's Workflow (%s)", state, source, all)
				return
			}
		}
		for _, name := range gate.ResetOn {
			if fields == nil {
				fields = tableFieldsOf(schema, td, uses)
			}
			class, owner, column, fd := fields.classify(name, false)
			switch class {
			case fieldOwn:
				if what := unread(column, fd); what != "" {
					fail("gate %q: resetOn names %q, which is %s", state, name, what)
					return
				}
			case fieldOwned:
				fail("gate %q: resetOn names %q, a field %s owns: resetOn names the table's own fields, whose updates the core sees", state, name, owner)
				return
			case fieldRelation:
				fail("gate %q: resetOn names %q, a relation: name the field that holds its key, %sId", state, name, name)
				return
			case fieldAudit:
				fail("gate %q: resetOn names %q, an audit field the writes stamp, which no update changes", state, name)
				return
			case fieldMissing:
				fail("gate %q: resetOn names %q, which is not a field of %s (its fields: %s)", state, name, td.Name, fields.names())
				return
			}
		}
		if gate.ExcludeCreator == nil || *gate.ExcludeCreator {
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

// checkEngineApprovalsConfig refuses, on an engine type, each member of
// Approvals' config that a table's Approvals alone takes (D104): each
// gate's resetOn then pinned, gates in the canonical config's order, then
// status. The declaration is shared, so the configSchema accepts them; the
// engine's Approvals refuses them too, and the build says so first, naming
// the table form. A config the configSchema refuses is reported by
// checkTypeBehaviors and left alone here.
func checkEngineApprovalsConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Gates  map[string]map[string]json.RawMessage `json:"gates"`
		Status json.RawMessage                       `json:"status"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	for _, state := range slices.Sorted(maps.Keys(config.Gates)) {
		if _, ok := config.Gates[state]["resetOn"]; ok {
			r.errorf(td.Owner, "type %s: @behavior Approvals names resetOn on the gate of %q, which a table's Approvals takes (implements Approvals<%s, C>, D104); an engine type's gates follow resetOnChange, so leave it out", td.Name, state, td.Name)
		}
		if _, ok := config.Gates[state]["pinned"]; ok {
			r.errorf(td.Owner, "type %s: @behavior Approvals names pinned on the gate of %q, which a table's Approvals takes (implements Approvals<%s, C>, D104); an engine type's sign-off names no version, so leave it out", td.Name, state, td.Name)
		}
	}
	if len(config.Status) > 0 {
		r.errorf(td.Owner, "type %s: @behavior Approvals names status, the Workflow field a table's Approvals reads (implements Approvals<%s, C>, D104); an engine type's Approvals reads its one Workflow, so leave status out", td.Name, td.Name)
	}
}

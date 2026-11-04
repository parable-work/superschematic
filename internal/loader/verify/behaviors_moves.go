package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The work queue's behaviors a table implements since D104's step 2 (its
// second batch): Lease, Retries, Presence and Budget.
const (
	leaseBehavior    = "Lease"
	retriesBehavior  = "Retries"
	presenceBehavior = "Presence"
	budgetBehavior   = "Budget"
)

// statusMove is a move of the status a work-queue behavior's config makes
// (D16): to transition, from one of from (Lease's onExpiry and escalate,
// Retries' then, Presence's onMissed and onBeat, Budget's escalate).
type statusMove struct {
	Transition string   `json:"transition"`
	From       []string `json:"from"`
}

// flowFor is the Workflow of td a config's moves read (D104), as the
// core's moves::flow_for reads it: the one named names, or the table's one
// Workflow; found is false for a table with no Workflow when named is nil,
// which a config with no move needs none of. Its problem is the core's
// refusal (flowOf), or "".
func flowFor(td *ir.TypeDef, named *string) (f flow, found bool, why string) {
	if named == nil && !slices.ContainsFunc(td.TableBehaviors(), func(use ir.BehaviorRef) bool { return use.Name == workflowBehavior }) {
		return flow{}, false, ""
	}
	f, why = flowOf(td, named)
	return f, why == "", why
}

// moveProblem holds a move of a config to the Workflow, in the core's words
// (moves::check_move, the engine's checkMove): its transition a state, and
// each from state a state, not the transition itself, with a transition of
// the Workflow from it to the transition. A Workflow whose states are not
// known passes: Workflow refuses its config.
func moveProblem(f flow, at string, move statusMove) string {
	if f.states == nil {
		return ""
	}
	states := strings.Join(f.states, ", ")
	if !f.has(move.Transition) {
		return fmt.Sprintf("%s.transition %q is not a state of the type's Workflow (%s)", at, move.Transition, states)
	}
	for _, from := range move.From {
		switch {
		case !f.has(from):
			return fmt.Sprintf("%s.from names %q, which is not a state of the type's Workflow (%s)", at, from, states)
		case from == move.Transition:
			return fmt.Sprintf("%s moves the status from %q to itself", at, from)
		case !f.leads(from, move.Transition):
			return fmt.Sprintf("%s: no transition of the type's Workflow leads from %q to %q", at, from, move.Transition)
		}
	}
	return ""
}

// noWorkflow is the core's refusal of a move on a table with no Workflow
// (moves::no_workflow, the engine's words, which name Workflow on a table
// too).
func noWorkflow(at string) string {
	return at + " moves the status through Workflow, which the type does not list"
}

// implementsLease reports whether td implements Lease, whose holder and
// acquire Retries and Budget read (D104).
func implementsLease(td *ir.TypeDef) bool {
	return slices.ContainsFunc(td.ImplementsBehaviors(), func(use ir.BehaviorRef) bool { return use.Name == leaseBehavior })
}

// fieldRule is how a work-queue config's field reference is held to the
// table (D104): where the config names it (maxHoldField, meter tokens:
// limitField) and its member (limitField), what the member names (the
// table's own field, the one that holds the principal), whether a field a
// behavior owns is refused as one only its operations change, what one
// value of it holds (one integer, one subject), and the types the core
// takes for it, by its spec column, with the core's words for another.
type fieldRule struct {
	at, member, names string
	operations        bool
	holds             string
	types             func(column string) bool
	wrong             string
}

// fieldProblem holds a field a work-queue config names to the table (D104):
// the core's refusal of a field the table's spec lacks, and of one whose
// type the rule does not take ("{at} "{f}" is not an integer field of
// {T}"); the build's, in its own words, of a field a behavior owns, a
// relation by its name, an audit field, a list or a map, and a field
// encrypted at rest, which the core reads no value of. It returns "" for a
// field the rule takes.
func fieldProblem(fields *tableFields, rule fieldRule, name string) string {
	class, owner, column, fd := fields.classify(name, false)
	switch class {
	case fieldOwned:
		why := ""
		if rule.operations {
			why = ", which only its operations change"
		}
		return fmt.Sprintf("%s %q is a field %s owns%s: %s names %s", rule.at, name, owner, why, rule.member, rule.names)
	case fieldRelation:
		return fmt.Sprintf("%s %q is a relation: name the field that holds its key, %sId", rule.at, name, name)
	case fieldAudit:
		return fmt.Sprintf("%s %q is an audit field the writes stamp: %s names %s", rule.at, name, rule.member, rule.names)
	case fieldMissing:
		return fmt.Sprintf("%s %q is not a field of %s (its fields: %s)", rule.at, name, fields.td.Name, fields.names())
	}
	switch {
	case fd != nil && (fd.TypeRef.IsArray || fd.TypeRef.IsMap):
		return fmt.Sprintf("%s %q is a list: %s names a field that holds %s", rule.at, name, rule.member, rule.holds)
	case fd != nil && fd.EncryptedAtRest:
		return fmt.Sprintf("%s %q is encrypted at rest: the core reads no value of it", rule.at, name)
	case rule.types != nil && !rule.types(column.Type):
		return fmt.Sprintf("%s %q %s of %s", rule.at, name, rule.wrong, fields.td.Name)
	}
	return ""
}

// implementedConfig is the config of td's implements use of behavior, and
// whether it has one.
func implementedConfig(td *ir.TypeDef, behavior string) (json.RawMessage, bool) {
	for _, ref := range td.ImplementsBehaviors() {
		if ref.Name == behavior {
			return ref.Config, true
		}
	}
	return nil, false
}

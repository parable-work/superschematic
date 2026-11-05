package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// reconcileBehavior is the name of the core's Reconcile, which the
// behaviors' core alone implements, on a field of a table (D104).
const reconcileBehavior = "Reconcile"

// checkReconcileConfig holds the config of a Reconcile use on a field of
// the table td to the table (D104), in the core's words
// (superschematic_rules_behaviors::reconcile): status names a Workflow use
// of td (flowFor); unreachable and staleAfterMs come together; unreachable
// is a state of that Workflow, which every state but a terminal one
// reaches along its transitions, and from which every other state can be
// reached. Each problem is `type <T>: field <f>: behavior Reconcile config:
// <reason>`, and like the core it reports the first. A Workflow whose
// states are not known passes the state rules: Workflow refuses its
// config. A config the configSchema refuses is reported by
// checkTypeBehaviors and left alone here.
func checkReconcileConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Status       string  `json:"status"`
		Unreachable  *string `json:"unreachable"`
		StaleAfterMs *int64  `json:"staleAfterMs"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	if why := reconcileProblem(td, config.Status, config.Unreachable, config.StaleAfterMs != nil); why != "" {
		r.errorf(td.Owner, "type %s: field %s: behavior %s config: %s", td.Name, ref.Field, ref.Name, why)
	}
}

// reconcileProblem is the first problem of a table's Reconcile config, or
// "".
func reconcileProblem(td *ir.TypeDef, status string, unreachable *string, stale bool) string {
	workflow, _, why := flowFor(td, &status)
	if why != "" {
		return why
	}
	switch {
	case unreachable != nil && !stale:
		return "unreachable names the state the stale deadline moves a row to, and the config gives no staleAfterMs to arm it"
	case unreachable == nil && stale:
		return "staleAfterMs arms a deadline that moves a row to unreachable, and the config names no unreachable state"
	case unreachable == nil || workflow.states == nil:
		return ""
	}
	u := *unreachable
	if !slices.Contains(workflow.states, u) {
		return fmt.Sprintf("unreachable %q is not a state of the type's Workflow (%s)", u, strings.Join(workflow.states, ", "))
	}
	for _, state := range workflow.states {
		if state == u || workflow.terminal(state) {
			continue
		}
		if !slices.Contains(workflow.reachable(state), u) {
			return fmt.Sprintf("unreachable %q cannot be reached from %q, which is not a terminal state: the stale deadline moves a row there from any state but a terminal one", u, state)
		}
	}
	from := workflow.reachable(u)
	for _, state := range workflow.states {
		if state != u && !slices.Contains(from, state) {
			return fmt.Sprintf("%q cannot be reached from unreachable %q: an observation moves a row from it to any state", state, u)
		}
	}
	return ""
}

// terminal reports whether no transition of the Workflow leaves state.
func (f flow) terminal(state string) bool {
	return !slices.ContainsFunc(f.transitions, func(t [2]string) bool { return t[0] == state })
}

// reachable is every state the Workflow's transitions lead to from a
// state, itself included, as the core's flow::reachable reads them (D104).
func (f flow) reachable(from string) []string {
	seen := []string{from}
	for next := 0; next < len(seen); next++ {
		for _, t := range f.transitions {
			if t[0] == seen[next] && !slices.Contains(seen, t[1]) {
				seen = append(seen, t[1])
			}
		}
	}
	return seen
}

// engineRefusal is the build's refusal of a behavior no engine implements
// on an engine type (D104), naming the table form the behaviors' core runs
// it in: implements <B><T, { ... }> for a behavior that names no value
// field, <value>: <B><{ ... }> for one anchored on a field, beside a
// Workflow field when it requires Workflow.
func engineRefusal(td *ir.TypeDef, behavior registry.Behavior) string {
	if behavior.Value == "" {
		return fmt.Sprintf("type %s: behavior %s is implemented by the behaviors' core alone, which runs it on a table, and no engine implements it: write implements %s<%s, { ... }> on a DB table (D104)", td.Name, behavior.Name, behavior.Name, td.Name)
	}
	beside := ""
	if slices.Contains(behavior.Requires, workflowBehavior) {
		beside = " beside a Workflow field"
	}
	return fmt.Sprintf("type %s: behavior %s is implemented by the behaviors' core alone, which runs it on a field of a table, and no engine implements it: write %s: %s<{ ... }>%s on a DB table (D104)", td.Name, behavior.Name, behavior.Value, behavior.Name, beside)
}

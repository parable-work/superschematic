package verify

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkPresenceConfig holds the config of a Presence use a table
// implements to the table and its database (D104), in the core's words
// where the core sees the problem (superschematic_rules_behaviors::presence):
// sweepMs, which a table's Presence does not take; principalField an own
// field that holds a subject; the Workflow onMissed, onBeat and ttlByState
// read, the one its status names or the table's one, each move held to it
// and each ttlByState state one of its states. What only the IR shows is
// the build's: a principalField a behavior owns, a relation by its name, an
// audit field, a list or a map and a field encrypted at rest; and each
// releaseLeases name a table of the database that implements Lease with an
// overridePermission, since the core's define check reads no other table.
// Each problem is `type <T>: behavior Presence config: <reason>`, and like
// the core it reports the first, in the core's order, then releaseLeases.
// A config the configSchema refuses is reported by checkTypeBehaviors and
// left alone here.
func checkPresenceConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		PrincipalField string           `json:"principalField"`
		OnMissed       *statusMove      `json:"onMissed"`
		OnBeat         *statusMove      `json:"onBeat"`
		ReleaseLeases  []string         `json:"releaseLeases"`
		SweepMs        json.RawMessage  `json:"sweepMs"`
		TTLByState     map[string]int64 `json:"ttlByState"`
		Status         *string          `json:"status"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	if why := presenceProblem(schema, td, uses, config.PrincipalField, config.OnMissed, config.OnBeat, config.ReleaseLeases, len(config.SweepMs) > 0, config.TTLByState, config.Status); why != "" {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
	}
}

// presenceProblem is the first problem of a table's Presence config, or "".
func presenceProblem(schema *ir.Schema, td *ir.TypeDef, uses map[string]registry.Behavior, principalField string,
	onMissed, onBeat *statusMove, releaseLeases []string, sweepMs bool, ttlByState map[string]int64, status *string) string {
	if sweepMs {
		return "sweepMs is the engine's runner's, which missed the instances past their deadline every sweepMs: on a table the API's @behaviorWorker runs each row's miss as its deadline falls due, so a table's Presence takes no sweepMs (D104)"
	}
	rule := fieldRule{at: "principalField", member: "principalField", names: "the table's own field that holds the principal", operations: true, holds: "one subject",
		types: func(t string) bool { return slices.Contains(subjectTypes, t) }, wrong: "is not a string field"}
	if why := fieldProblem(tableFieldsOf(schema, td, uses), rule, principalField); why != "" {
		return why
	}
	// The Workflow is read only when a move, ttlByState or status needs it,
	// as the core reads it, so a table with several and none of them is
	// not refused for the choice.
	if onMissed == nil && onBeat == nil && len(ttlByState) == 0 && status == nil {
		return releaseLeasesProblem(schema, releaseLeases)
	}
	workflow, found, why := flowFor(td, status)
	if why != "" {
		return why
	}
	if !found {
		switch {
		case onMissed != nil:
			return noWorkflow("onMissed")
		case onBeat != nil:
			return noWorkflow("onBeat")
		case len(ttlByState) > 0:
			return "ttlByState times the states of Workflow, which the type does not list"
		}
	} else {
		for _, move := range []struct {
			at   string
			rule *statusMove
		}{{"onMissed", onMissed}, {"onBeat", onBeat}} {
			if move.rule != nil {
				if why := moveProblem(workflow, move.at, *move.rule); why != "" {
					return why
				}
			}
		}
		for _, state := range slices.Sorted(maps.Keys(ttlByState)) {
			if !workflow.has(state) {
				return fmt.Sprintf("ttlByState: %q is not a state of the type's Workflow (%s)", state, strings.Join(workflow.states, ", "))
			}
		}
	}
	return releaseLeasesProblem(schema, releaseLeases)
}

// releaseLeasesProblem is the first releaseLeases name that is not a table
// of the database implementing Lease with an overridePermission, or "".
func releaseLeasesProblem(schema *ir.Schema, releaseLeases []string) string {
	for _, name := range releaseLeases {
		leased := schema.Types[name]
		if leased == nil || leased.Role != ir.RoleDBTable {
			return fmt.Sprintf("releaseLeases: %s is not a table of %s", name, schema.Name)
		}
		config, ok := implementedConfig(leased, leaseBehavior)
		if !ok {
			return fmt.Sprintf("releaseLeases: %s does not implement Lease, so it holds no lease to expire", name)
		}
		if leaseOverridePermission(config) == "" {
			return fmt.Sprintf("releaseLeases: %s's Lease names no overridePermission, which a miss's expiry of the principal's leases needs (D104)", name)
		}
	}
	return ""
}

// leaseOverridePermission is the overridePermission a Lease config names,
// or "".
func leaseOverridePermission(config json.RawMessage) string {
	var lease struct {
		OverridePermission string `json:"overridePermission"`
	}
	if len(config) == 0 || json.Unmarshal(config, &lease) != nil {
		return ""
	}
	return lease.OverridePermission
}

// checkEnginePresenceConfig refuses, on an engine type, ttlByState and
// status, which a table's Presence alone takes (D104). The declaration is
// shared, so the configSchema accepts them; the engine's Presence refuses
// them too, and the build says so first, naming the table form. A config
// the configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkEnginePresenceConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	if configNames(ref.Config, "ttlByState") {
		r.errorf(td.Owner, "type %s: @behavior Presence names ttlByState, which a table's Presence takes (implements Presence<%s, C>, D104); an engine type's instance stays present ttlMs after each beat, so leave it out", td.Name, td.Name)
	}
	if configNames(ref.Config, "status") {
		r.errorf(td.Owner, "type %s: @behavior Presence names status, the Workflow field a table's Presence reads (implements Presence<%s, C>, D104); an engine type's Presence reads its one Workflow, so leave status out", td.Name, td.Name)
	}
}

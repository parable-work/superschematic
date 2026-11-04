package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// holdTypes are the IR types Lease's maxHoldField may have, an integer
// field (D104): the core's superschematic_rules_behaviors::lease::HOLD_TYPES,
// which TestHoldTypesAreTheCores holds them to.
var holdTypes = []string{"Generic.Int64"}

// leaseDefaultTTL is how long a lease lasts when the config gives no ttlMs,
// as the core and the engine default it.
const leaseDefaultTTL = 60000

// checkLeaseConfig holds the config of a Lease use a table implements to
// the table (D104), in the core's words where the core sees the problem
// (superschematic_rules_behaviors::lease): heartbeatMs at most half of
// ttlMs; maxHoldField an own integer field; the Workflow onExpiry and
// escalate move, the one its status names or the table's one, each move
// held to it; escalate given with maxExpiries; each exempt entry another
// behavior's operation the table lists; and directOn and sweepMs, which a
// table's Lease does not take. What only the IR shows is the build's: a
// maxHoldField a behavior owns, a relation by its name, an audit field, a
// list or a map and a field encrypted at rest. Each problem is `type <T>:
// behavior Lease config: <reason>`, and like the core it reports the
// first, in the core's order. A config the configSchema refuses is
// reported by checkTypeBehaviors and left alone here.
func checkLeaseConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		TTLMs        *int64          `json:"ttlMs"`
		HeartbeatMs  *int64          `json:"heartbeatMs"`
		MaxHoldField *string         `json:"maxHoldField"`
		Status       *string         `json:"status"`
		OnExpiry     *statusMove     `json:"onExpiry"`
		Escalate     *statusMove     `json:"escalate"`
		MaxExpiries  *int64          `json:"maxExpiries"`
		Exempt       []string        `json:"exempt"`
		DirectOn     json.RawMessage `json:"directOn"`
		SweepMs      json.RawMessage `json:"sweepMs"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	if why := leaseProblem(schema, td, config.TTLMs, config.HeartbeatMs, config.MaxHoldField, config.Status, config.OnExpiry, config.Escalate,
		config.MaxExpiries, config.Exempt, len(config.DirectOn) > 0, len(config.SweepMs) > 0, uses); why != "" {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, why)
	}
}

// leaseProblem is the first problem of a table's Lease config, in the
// core's order, or "".
func leaseProblem(schema *ir.Schema, td *ir.TypeDef, ttlMs, heartbeatMs *int64, maxHoldField, status *string, onExpiry, escalate *statusMove,
	maxExpiries *int64, exempt []string, directOn, sweepMs bool, uses map[string]registry.Behavior) string {
	ttl := int64(leaseDefaultTTL)
	if ttlMs != nil {
		ttl = *ttlMs
	}
	heartbeat := ttl / 3
	if heartbeatMs != nil {
		heartbeat = *heartbeatMs
	}
	if heartbeat*2 > ttl {
		return fmt.Sprintf("heartbeatMs (%d) is more than half of ttlMs (%d): one late heartbeat would lose the lease", heartbeat, ttl)
	}
	if maxHoldField != nil {
		rule := fieldRule{at: "maxHoldField", member: "maxHoldField", names: "the table's own field", holds: "one integer",
			types: func(t string) bool { return slices.Contains(holdTypes, t) }, wrong: "is not an integer field"}
		if why := fieldProblem(tableFieldsOf(schema, td, uses), rule, *maxHoldField); why != "" {
			return why
		}
	}
	workflow, found, why := flowFor(td, status)
	if why != "" {
		return why
	}
	for _, move := range []struct {
		at   string
		rule *statusMove
	}{{"onExpiry", onExpiry}, {"escalate", escalate}} {
		if move.rule == nil {
			continue
		}
		if !found {
			return noWorkflow(move.at)
		}
		if why := moveProblem(workflow, move.at, *move.rule); why != "" {
			return why
		}
	}
	if escalate != nil && maxExpiries == nil {
		return "escalate applies at the expiry that reaches maxExpiries, which the config does not give"
	}
	var behaviors []string
	for _, use := range td.TableBehaviors() {
		if !slices.Contains(behaviors, use.Name) {
			behaviors = append(behaviors, use.Name)
		}
	}
	for _, entry := range exempt {
		behavior := entry
		if i := strings.LastIndex(entry, "."); i >= 0 {
			behavior = entry[:i]
		}
		if behavior == leaseBehavior {
			return fmt.Sprintf("exempt names %s: Lease's own operations check their callers themselves", entry)
		}
		if !slices.Contains(behaviors, behavior) {
			return fmt.Sprintf("exempt names %s, but the type does not list %s (it lists %s)", entry, behavior, strings.Join(behaviors, ", "))
		}
	}
	if directOn {
		return "directOn sends a directive when the target of a link moves on, a revision of Revisions or a release of Branches, which waits for Revisions and Branches on the core (D103, step 4)"
	}
	if sweepMs {
		return "sweepMs is how often the engine's runner swept the lapsed leases: a table's lease expires at its own deadline, which the API's behavior worker runs when it falls due (D103), so a table's Lease takes no sweepMs"
	}
	return ""
}

// checkEngineLeaseConfig refuses, on an engine type, status, which a
// table's Lease alone takes (D104). The declaration is shared, so the
// configSchema accepts it; the engine's Lease refuses it too, and the build
// says so first, naming the table form. A config the configSchema refuses
// is reported by checkTypeBehaviors and left alone here.
func checkEngineLeaseConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	if configNames(ref.Config, "status") {
		r.errorf(td.Owner, "type %s: @behavior Lease names status, the Workflow field a table's Lease moves (implements Lease<%s, C>, D104); an engine type's Lease moves its one Workflow, so leave status out", td.Name, td.Name)
	}
}

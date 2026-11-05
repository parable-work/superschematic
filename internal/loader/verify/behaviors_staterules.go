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

// stateRulesBehavior is the name of the core's StateRules.
const stateRulesBehavior = "StateRules"

// flow is the Workflow a table use of StateRules, Approvals or a work-queue
// behavior reads (D104): its use's id, its states, nil when its config holds
// no list of strings, which is Workflow's own to refuse, and its
// transitions, each from and to.
type flow struct {
	id          string
	states      []string
	transitions [][2]string
}

// flowOf is the Workflow of td a config reads: the one its status names,
// or the table's one Workflow. Its problem is the core's refusal
// (superschematic_rules_behaviors::flow), or "" when there is none.
func flowOf(td *ir.TypeDef, named *string) (flow, string) {
	var workflows []ir.BehaviorRef
	var ids []string
	for _, use := range td.TableBehaviors() {
		if use.Name == workflowBehavior {
			workflows = append(workflows, use)
			ids = append(ids, use.UseID())
		}
	}
	listed := "none"
	if len(ids) > 0 {
		listed = strings.Join(ids, ", ")
	}
	var use ir.BehaviorRef
	switch {
	case named != nil:
		i := slices.Index(ids, *named)
		if i < 0 {
			return flow{}, fmt.Sprintf("status %q is not a Workflow of the type (its Workflows: %s)", *named, listed)
		}
		use = workflows[i]
	case len(workflows) == 0:
		return flow{}, "it reads the status of Workflow, which the type does not list"
	case len(workflows) > 1:
		return flow{}, fmt.Sprintf("it reads the status of Workflow, and the type has several (%s): status names the field whose Workflow it reads", listed)
	default:
		use = workflows[0]
	}
	var config struct {
		States      []string `json:"states"`
		Transitions []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"transitions"`
	}
	if len(use.Config) > 0 && json.Unmarshal(use.Config, &config) != nil {
		config.States, config.Transitions = nil, nil
	}
	f := flow{id: use.UseID(), states: config.States}
	for _, transition := range config.Transitions {
		f.transitions = append(f.transitions, [2]string{transition.From, transition.To})
	}
	return f, ""
}

// leads reports whether a transition of the Workflow leads from one state
// to another.
func (f flow) leads(from, to string) bool {
	return slices.Contains(f.transitions, [2]string{from, to})
}

// has reports whether the Workflow lists state; every state, when its
// states are not known.
func (f flow) has(state string) bool {
	return f.states == nil || slices.Contains(f.states, state)
}

// checkStateRulesConfig holds the config of a StateRules use a table
// implements to the table (D104), in the core's words where the core sees
// the problem (superschematic_rules_behaviors::staterules): the Workflow
// it reads is the one its status names, or the table's one Workflow; each
// state is one of that Workflow's; each field a state requires or freezes
// is an own field of the table; and a state's by, in a state that freezes
// a field, names own fields that hold a subject and seats of the table's
// Seats, as a Workflow transition's by does (byProblem), a seat that names
// no take permission warned of once per entry. What only the IR shows is
// the build's:
// a field a behavior owns, a relation named by its field, an audit field
// but createdBy, which a state may require, and a field the core reads no
// value of (unread). Each problem is `type <T>: behavior StateRules config:
// <reason>`, and like the core it reports the first: the status, then each
// state in the canonical config's order, its required fields, its frozen
// ones, then its by. A state that freezes "all" on a table with a field
// the core reads no value of is warned of, never refused: the state
// freezes every field the core sees change, and leaves those unfrozen. A
// config the configSchema refuses is reported by checkTypeBehaviors and
// left alone here.
func checkStateRulesConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		Status *string `json:"status"`
		States map[string]struct {
			Required []string        `json:"required"`
			Frozen   json.RawMessage `json:"frozen"`
			By       []string        `json:"by"`
		} `json:"states"`
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
	fields := tableFieldsOf(schema, td, uses)
	held := func(at, name string) string {
		class, owner, column, fd := fields.classify(name, true)
		switch class {
		case fieldOwn:
			if what := unread(column, fd); what != "" {
				return fmt.Sprintf("%s: %s is %s", at, name, what)
			}
		case fieldOwned:
			return fmt.Sprintf("%s: %s is a field %s owns, which only its operations change: StateRules requires and freezes the table's own fields", at, name, owner)
		case fieldRelation:
			return fmt.Sprintf("%s: %s is a relation: name the field that holds its key, %sId", at, name, name)
		case fieldAudit:
			return fmt.Sprintf("%s: %s is an audit field the writes stamp, which no update changes", at, name)
		case fieldMissing:
			return fmt.Sprintf("%s: %s is not a field of %s (its fields: %s)", at, name, td.Name, fields.names())
		}
		return ""
	}
	for _, state := range slices.Sorted(maps.Keys(config.States)) {
		rule := config.States[state]
		if !workflow.has(state) {
			fail("states: %q is not a state of the type's Workflow (%s)", state, strings.Join(workflow.states, ", "))
			return
		}
		for _, name := range rule.Required {
			if why := held("states."+state+".required", name); why != "" {
				fail("%s", why)
				return
			}
		}
		var frozen []string
		if json.Unmarshal(rule.Frozen, &frozen) == nil {
			for _, name := range frozen {
				if why := held("states."+state+".frozen", name); why != "" {
					fail("%s", why)
					return
				}
			}
		} else if all := ""; json.Unmarshal(rule.Frozen, &all) == nil && all == "all" {
			if unfrozen := fields.unreadFields(); len(unfrozen) > 0 {
				them := pronoun(len(unfrozen))
				r.warnf(td.Owner, "type %s: behavior StateRules config: states.%s.frozen is \"all\", which leaves %s unfrozen, since the core reads no value of %s: name the fields %s freezes, or keep %s out of the updates a caller may make while the row is %s (D104)", td.Name, state, strings.Join(unfrozen, ", "), them, state, them, state)
			}
		}
		if len(rule.By) == 0 {
			continue
		}
		if len(rule.Frozen) == 0 {
			fail("states.%s.by lists who may change a field %s freezes, and %s freezes none", state, state, state)
			return
		}
		for _, entry := range rule.By {
			if why := byProblem(schema, td, fields.spec, entry); why != "" {
				fail("states.%s.by names %q, %s", state, entry, why)
				return
			}
		}
	}
	warned := map[string]bool{}
	for _, state := range slices.Sorted(maps.Keys(config.States)) {
		for _, entry := range config.States[state].By {
			if seat, takeless := takelessSeat(td, entry); takeless && !warned[entry] {
				warned[entry] = true
				r.warnf(td.Owner, "type %s: behavior StateRules config: states.%s.by names %q, and Seats' %s names no take permission: any caller may take it and so change what %s freezes; name a take permission for %s (D104)", td.Name, state, entry, seat, state, seat)
			}
		}
	}
}

// checkEngineStateRulesConfig refuses, on an engine type, each member of
// StateRules' config that a table's StateRules alone takes (D104): the by
// of each state's rules, in the canonical config's order, then status. The
// declaration is shared, so the configSchema accepts them; the engine's
// StateRules refuses them too, and the build says so first, naming the
// table form. A config the configSchema refuses is reported by
// checkTypeBehaviors and left alone here.
func checkEngineStateRulesConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		States map[string]map[string]json.RawMessage `json:"states"`
		Status json.RawMessage                       `json:"status"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	for _, state := range slices.Sorted(maps.Keys(config.States)) {
		if _, ok := config.States[state]["by"]; ok {
			r.errorf(td.Owner, "type %s: @behavior StateRules names by in the rules of %q, which a table's StateRules takes (implements StateRules<%s, C>, D104); an engine type's StateRules takes required, frozen and permission alone, so leave it out", td.Name, state, td.Name)
		}
	}
	if len(config.Status) > 0 {
		r.errorf(td.Owner, "type %s: @behavior StateRules names status, the Workflow field a table's StateRules reads (implements StateRules<%s, C>, D104); an engine type's StateRules reads its one Workflow, so leave status out", td.Name, td.Name)
	}
}

// pronoun is "it" for one field and "them" for several.
func pronoun(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

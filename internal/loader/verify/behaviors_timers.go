package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	ir "github.com/parable-work/superschematic/ir"
)

// timersBehavior is the name of the core's Timers.
const timersBehavior = "Timers"

// checkTimersConfig holds the config of a Timers use a table implements to
// the table (D103), in the core's words: the Workflow its deadlines read
// (an enters start, a then, an until) is the one its status names, or the
// table's one Workflow; a field start names an own field of the table
// that holds date-times; and each deadline's start, then and until are
// held to that Workflow as the core's parse_deadline holds them: an enters
// state, a then's transition and its from states and an until's states are
// states of it, a transition leads from each from state to the then's, a
// deadline that starts at a field names its then's from and takes no
// until.leaves, having no state of its own, and only a field start takes a
// negative afterMs. A config that breaks one of these would otherwise pass
// the build and fail every command on the table, the create among them,
// at the core's define check. Workflow's own rules stay the core's. Each
// problem is the core's define issue (superschematic_rules_behaviors::
// timers), `type <T>: behavior Timers config: <reason>`, and like the core
// it reports the first: the status, then each deadline in name order (the
// canonical config's, which the core reads), each place in the order
// start, afterMs, then, until. The own fields are
// the layout's (behaviorlayout.OwnFields), which the core's spec lists,
// so both name the same fields. A config the configSchema refuses is
// reported by checkTypeBehaviors and left alone here.
func checkTimersConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Status    *string                    `json:"status"`
		Deadlines map[string]json.RawMessage `json:"deadlines"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, fmt.Sprintf(format, args...))
	}
	var workflows []string
	for _, use := range td.TableBehaviors() {
		if use.Name == workflowBehavior {
			workflows = append(workflows, use.UseID())
		}
	}
	listed := "none"
	if len(workflows) > 0 {
		listed = strings.Join(workflows, ", ")
	}
	if config.Status != nil {
		named := false
		for _, workflow := range workflows {
			named = named || workflow == *config.Status
		}
		if !named {
			fail("status %q is not a Workflow of the type (its Workflows: %s)", *config.Status, listed)
			return
		}
	}
	// readsStatus is why a place that reads the status cannot: the type
	// lists no Workflow, or several and the config names none.
	readsStatus := func(at string) string {
		switch {
		case config.Status != nil:
			return ""
		case len(workflows) == 0:
			return fmt.Sprintf("%s reads the status of Workflow, which the type does not list", at)
		case len(workflows) > 1:
			return fmt.Sprintf("%s reads the status of Workflow, and the type has several (%s): status names the field whose Workflow it reads", at, listed)
		}
		return ""
	}
	// The Workflow the deadlines read, when the status names one or the
	// type lists one: its states, its initial state, the first unless the
	// config names one, and its transitions, as the core's flow_of reads
	// them. A Workflow whose config has another shape is Workflow's to
	// refuse, and its deadlines' states are then not checked.
	var timed *timersFlow
	if config.Status != nil || len(workflows) == 1 {
		timed = timersFlowOf(td, config.Status)
	}
	isState := func(at, state string) string {
		if timed == nil || slices.Contains(timed.states, state) {
			return ""
		}
		return fmt.Sprintf("%s %q is not a state of the type's Workflow (%s)", at, state, strings.Join(timed.states, ", "))
	}
	var own []behaviorlayout.Column
	ownRead := false
	// The canonical config's order: a deadline's name is never an integer,
	// so a JavaScript object's key order, which the core reads, is the
	// sorted one.
	names := make([]string, 0, len(config.Deadlines))
	for name := range config.Deadlines {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		at := "deadlines." + name
		var deadline struct {
			Start struct {
				Enters *string `json:"enters"`
				Field  *string `json:"field"`
			} `json:"start"`
			AfterMs *float64        `json:"afterMs"`
			Then    json.RawMessage `json:"then"`
			Until   json.RawMessage `json:"until"`
		}
		if json.Unmarshal(config.Deadlines[name], &deadline) != nil {
			continue
		}
		// The state the deadline starts in: its enters state, or the
		// Workflow's initial state for a create start. A field start has
		// none.
		startState := ""
		fieldStart := false
		switch {
		case deadline.Start.Enters != nil:
			if why := readsStatus(at + ".start.enters"); why != "" {
				fail("%s", why)
				return
			}
			if why := isState(at+".start.enters", *deadline.Start.Enters); why != "" {
				fail("%s", why)
				return
			}
			startState = *deadline.Start.Enters
		case deadline.Start.Field != nil:
			fieldStart = true
			if !ownRead {
				own, ownRead = behaviorlayout.OwnFields(schema, td), true
			}
			field := *deadline.Start.Field
			var found *behaviorlayout.Column
			known := make([]string, 0, len(own))
			for i := range own {
				known = append(known, own[i].Field)
				if own[i].Field == field {
					found = &own[i]
				}
			}
			fields := "none"
			if len(known) > 0 {
				fields = strings.Join(known, ", ")
			}
			if found == nil {
				fail("%s.start.field %q is not a field of %s (its fields: %s)", at, field, td.Name, fields)
				return
			}
			if found.Convert != behaviorlayout.ConvertEpochMillis {
				fail("%s.start.field %q is not a date-time field of %s: a deadline starts from the time a value holds", at, field, td.Name)
				return
			}
		default:
			if timed != nil {
				startState = timed.initial
			}
		}
		if deadline.AfterMs != nil && *deadline.AfterMs < 0 && !fieldStart {
			fail("%s.afterMs is negative, which only a field start takes: a deadline that starts at the create or at an entry is not due before it", at)
			return
		}
		for _, place := range []struct {
			name string
			raw  json.RawMessage
		}{{"then", deadline.Then}, {"until", deadline.Until}} {
			if len(place.raw) == 0 {
				continue
			}
			if why := readsStatus(at + "." + place.name); why != "" {
				fail("%s", why)
				return
			}
		}
		if why := timersThenProblem(timed, at, deadline.Then, fieldStart, startState); why != "" {
			fail("%s", why)
			return
		}
		if why := timersUntilProblem(isState, at, deadline.Until, fieldStart, startState); why != "" {
			fail("%s", why)
			return
		}
	}
}

// timersFlow is a Workflow config as Timers' deadlines read it: the core's
// timers::Flow.
type timersFlow struct {
	states      []string
	initial     string
	transitions [][2]string
}

// timersFlowOf reads the Workflow of td that named names, or its one
// Workflow, as the core's flow_of does: nil for a config of another shape
// than Workflow takes, which Workflow refuses.
func timersFlowOf(td *ir.TypeDef, named *string) *timersFlow {
	for _, use := range td.TableBehaviors() {
		if use.Name != workflowBehavior || (named != nil && use.UseID() != *named) {
			continue
		}
		var config struct {
			States      *[]string `json:"states"`
			Initial     *string   `json:"initial"`
			Transitions *[]struct {
				From *string `json:"from"`
				To   *string `json:"to"`
			} `json:"transitions"`
		}
		if len(use.Config) == 0 || json.Unmarshal(use.Config, &config) != nil || config.States == nil || config.Transitions == nil {
			return nil
		}
		f := &timersFlow{states: *config.States}
		switch {
		case config.Initial != nil:
			f.initial = *config.Initial
		case len(f.states) > 0:
			f.initial = f.states[0]
		}
		for _, edge := range *config.Transitions {
			if edge.From != nil && edge.To != nil {
				f.transitions = append(f.transitions, [2]string{*edge.From, *edge.To})
			}
		}
		return f
	}
	return nil
}

// timersThenProblem holds a deadline's then to the Workflow, in the words
// of the core's parse_deadline and its check_move: a then of a deadline
// that starts at a field names its from, since the deadline is in no state
// of its own; any other's from is the state it starts in unless it names
// one; and the transition and each from state are states of the Workflow,
// with a transition from each to it. It returns "" for a then the core
// takes, and for a Workflow whose config Workflow refuses.
func timersThenProblem(f *timersFlow, at string, raw json.RawMessage, fieldStart bool, startState string) string {
	if len(raw) == 0 {
		return ""
	}
	var then struct {
		Transition string    `json:"transition"`
		From       *[]string `json:"from"`
	}
	if json.Unmarshal(raw, &then) != nil {
		return ""
	}
	var from []string
	switch {
	case then.From != nil:
		from = *then.From
	case fieldStart:
		return at + ".then names no from: a deadline that starts at a field is in no state of its own, so then.from names the states its move leaves"
	case startState != "":
		from = []string{startState}
	}
	if f == nil {
		return ""
	}
	states := strings.Join(f.states, ", ")
	if !slices.Contains(f.states, then.Transition) {
		return fmt.Sprintf("%s.then.transition %q is not a state of the type's Workflow (%s)", at, then.Transition, states)
	}
	for _, state := range from {
		switch {
		case !slices.Contains(f.states, state):
			return fmt.Sprintf("%s.then.from names %q, which is not a state of the type's Workflow (%s)", at, state, states)
		case state == then.Transition:
			return fmt.Sprintf("%s.then moves the status from %q to itself", at, state)
		case !slices.Contains(f.transitions, [2]string{state, then.Transition}):
			return fmt.Sprintf("%s.then: no transition of the type's Workflow leads from %q to %q", at, state, then.Transition)
		}
	}
	return ""
}

// timersUntilProblem holds a deadline's until to the Workflow, in the
// words of the core's parse_deadline: until.leaves names the state the
// deadline starts in, which a deadline that starts at a field has none of,
// and until.enters names states other than it.
func timersUntilProblem(isState func(at, state string) string, at string, raw json.RawMessage, fieldStart bool, startState string) string {
	if len(raw) == 0 {
		return ""
	}
	var until struct {
		Leaves *string  `json:"leaves"`
		Enters []string `json:"enters"`
	}
	if json.Unmarshal(raw, &until) != nil {
		return ""
	}
	if until.Leaves != nil {
		leaves := *until.Leaves
		if fieldStart {
			return at + ".until.leaves: a deadline that starts at a field is in no state of its own to leave; until.enters names the states that cancel it"
		}
		if why := isState(at+".until.leaves", leaves); why != "" {
			return why
		}
		if startState != "" && startState != leaves {
			return fmt.Sprintf("%s.until.leaves %q is not the state the deadline starts in, %q, which until.leaves cancels it on leaving", at, leaves, startState)
		}
		return ""
	}
	for _, state := range until.Enters {
		if why := isState(at+".until.enters", state); why != "" {
			return why
		}
		if startState != "" && startState == state {
			return fmt.Sprintf("%s.until.enters names %q, the state the deadline starts in, which would cancel it as it starts", at, state)
		}
	}
	return ""
}

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

// limitsTypes are the IR types Retries' limitsField may have, an object
// field (D104): the core's superschematic_rules_behaviors::retries::LIMITS_TYPES,
// which TestLimitsTypesAreTheCores holds them to.
var limitsTypes = []string{"Generic.JSON"}

// retriesClass is one class of a Retries config as the build reads it: a
// capped class's backoff, or nothing for "terminal".
type retriesClass struct {
	Backoff *struct {
		FirstMs int64 `json:"firstMs"`
		MaxMs   int64 `json:"maxMs"`
	} `json:"backoff"`
}

// checkRetriesConfig holds the config of a Retries use a table implements
// to the table (D104), in the core's words where the core sees the problem
// (superschematic_rules_behaviors::retries): limitsField an own object
// field, resultField an own field; the Workflow its states are of, the one
// its status names or the table's one, with exhaustedState and from its
// states, from leading to exhaustedState; keyField an own scalar field;
// each backoff's maxMs at least its firstMs; retryInto's states and then's
// move held to the Workflow; something waiting for each wait (a backoff for
// then, a backoff or a window for retryInto, a retryInto, a then or Lease
// for a backoff, a retryInto or Lease for a window); and resultField, which
// a table's Retries does not take, last. What only the IR shows is the
// build's: a limitsField or keyField a behavior owns, a relation by its
// name, an audit field, a list or a map and a field encrypted at rest. Each
// problem is `type <T>: behavior Retries config: <reason>`, and like the
// core it reports the first, in the core's order (classes in the canonical
// config's). A config the configSchema refuses is reported by
// checkTypeBehaviors and left alone here.
func checkRetriesConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		Classes        map[string]json.RawMessage `json:"classes"`
		LimitsField    *string                    `json:"limitsField"`
		ResultField    *string                    `json:"resultField"`
		Status         *string                    `json:"status"`
		ExhaustedState string                     `json:"exhaustedState"`
		From           []string                   `json:"from"`
		KeyField       *string                    `json:"keyField"`
		RetryInto      []string                   `json:"retryInto"`
		Then           *statusMove                `json:"then"`
		Window         json.RawMessage            `json:"window"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, fmt.Sprintf(format, args...))
	}
	fields := tableFieldsOf(schema, td, uses)
	if config.LimitsField != nil {
		rule := fieldRule{at: "limitsField", member: "limitsField", names: "the table's own field", holds: "one object of caps",
			types: func(t string) bool { return slices.Contains(limitsTypes, t) }, wrong: "is not an object field"}
		if why := fieldProblem(fields, rule, *config.LimitsField); why != "" {
			fail("%s", why)
			return
		}
	}
	if config.ResultField != nil {
		if class, _, _, _ := fields.classify(*config.ResultField, true); class == fieldMissing {
			fail("resultField %q is not a field of %s (its fields: %s)", *config.ResultField, td.Name, fields.names())
			return
		}
	}
	workflow, why := flowOf(td, config.Status)
	if why != "" {
		fail("%s", why)
		return
	}
	states := strings.Join(workflow.states, ", ")
	to := config.ExhaustedState
	if !workflow.has(to) {
		fail("exhaustedState %q is not a state of the type's Workflow (%s)", to, states)
		return
	}
	for _, from := range config.From {
		switch {
		case !workflow.has(from):
			fail("from names %q, which is not a state of the type's Workflow (%s)", from, states)
			return
		case from == to:
			fail("from names exhaustedState %q, which exhaustion cannot move the status from", to)
			return
		case workflow.states != nil && !workflow.leads(from, to):
			fail("from: no transition of the type's Workflow leads from %q to %q", from, to)
			return
		}
	}
	if config.KeyField != nil {
		rule := fieldRule{at: "keyField", member: "keyField", names: "the table's own field", holds: "one scalar",
			types: func(t string) bool { return t != "Generic.JSON" }, wrong: "is a Generic.JSON field"}
		if why := fieldProblem(fields, rule, *config.KeyField); why != "" {
			if strings.HasSuffix(why, "is a Generic.JSON field of "+td.Name) {
				why += ": the value that keys the counts is one scalar"
			}
			fail("%s", why)
			return
		}
	}
	var backoff string
	for _, name := range slices.Sorted(maps.Keys(config.Classes)) {
		var class retriesClass
		if json.Unmarshal(config.Classes[name], &class) != nil || class.Backoff == nil {
			continue
		}
		if class.Backoff.MaxMs < class.Backoff.FirstMs {
			fail("classes.%s.backoff: maxMs (%d) is shorter than firstMs (%d)", name, class.Backoff.MaxMs, class.Backoff.FirstMs)
			return
		}
		if backoff == "" {
			backoff = name
		}
	}
	for _, state := range config.RetryInto {
		if !workflow.has(state) {
			fail("retryInto names %q, which is not a state of the type's Workflow (%s)", state, states)
			return
		}
		if state == to {
			fail("retryInto names exhaustedState %q, which an exhausted row moves to and stays in", state)
			return
		}
	}
	if config.Then != nil {
		if why := moveProblem(workflow, "then", *config.Then); why != "" {
			fail("%s", why)
			return
		}
	}
	window := len(config.Window) > 0
	leased := implementsLease(td)
	switch {
	case config.Then != nil && backoff == "":
		fail("then moves the row when its backoff ends, and no class sets a backoff")
		return
	case len(config.RetryInto) > 0 && backoff == "" && !window:
		fail("retryInto names the states an attempt starts in, which wait for a backoff or the window, and the config sets neither")
		return
	case backoff != "" && len(config.RetryInto) == 0 && config.Then == nil && !leased:
		fail("classes.%s.backoff sets when the row runs again (retryAt), which a move into retryInto, then and Lease's acquire wait for: the config names no retryInto and no then, and the type has no Lease", backoff)
		return
	case window && len(config.RetryInto) == 0 && !leased:
		fail("window caps the attempts that start, a move into retryInto or Lease's acquire: the config names no retryInto, and the type has no Lease")
		return
	}
	if config.ResultField != nil {
		fail("resultField writes a kept result to a field of the row through an update an operation makes, which waits for the core to run one (D103, step 4: Revisions)")
	}
}

// retriesEngineMembers are the members of Retries' config a table's
// Retries alone takes, in the order the build refuses them on an engine
// type (D104), each with what an engine type's Retries does instead.
var retriesEngineMembers = []struct{ member, instead string }{
	{"retryInto", "keeps no time"},
	{"then", "arms no deadline"},
	{"giveUpAfterMs", "exhausts by its caps alone"},
	{"window", "keeps no attempt's time"},
	{"keyField", "counts every attempt together"},
	{"status", "reads its one Workflow"},
}

// checkEngineRetriesConfig refuses, on an engine type, each member of
// Retries' config that a table's Retries alone takes (D104): each class's
// backoff, classes in the canonical config's order, then retryInto, then,
// giveUpAfterMs, window, keyField and status. The declaration is shared, so
// the configSchema accepts them; the engine's Retries refuses them too, and
// the build says so first, naming the table form. A config the
// configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkEngineRetriesConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Classes map[string]json.RawMessage `json:"classes"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(config.Classes)) {
		if configNames(config.Classes[name], "backoff") {
			r.errorf(td.Owner, "type %s: @behavior Retries names backoff on the class %q, which a table's Retries takes (implements Retries<%s, C>, D104); an engine type's Retries keeps no time, so leave it out", td.Name, name, td.Name)
		}
	}
	for _, m := range retriesEngineMembers {
		if configNames(ref.Config, m.member) {
			r.errorf(td.Owner, "type %s: @behavior Retries names %s, which a table's Retries takes (implements Retries<%s, C>, D104); an engine type's Retries %s, so leave it out", td.Name, m.member, td.Name, m.instead)
		}
	}
}

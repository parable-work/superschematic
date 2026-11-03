package verify

import (
	"encoding/json"
	"fmt"
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
// that holds date-times. These are the rules of Timers' config that read
// the IR, which the build can check before any command; its other rules
// (a state the Workflow lists, a transition that leads there) and
// Workflow's are the core's define check's, at the first command. Each
// problem is the core's define issue (superschematic_rules_behaviors::
// timers), `type <T>: behavior Timers config: <reason>`, and like the core
// it reports the first: the status, then each deadline in name order (the
// canonical config's, which the core reads), each place in the order
// start, then, until. The own fields are
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
			Then  json.RawMessage `json:"then"`
			Until json.RawMessage `json:"until"`
		}
		if json.Unmarshal(config.Deadlines[name], &deadline) != nil {
			continue
		}
		switch {
		case deadline.Start.Enters != nil:
			if why := readsStatus(at + ".start.enters"); why != "" {
				fail("%s", why)
				return
			}
		case deadline.Start.Field != nil:
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
	}
}

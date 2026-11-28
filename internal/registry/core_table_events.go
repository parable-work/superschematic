package registry

import (
	_ "embed"
	"encoding/json"
)

// commentsEvents are the events a table's Comments emits when its config
// sets events (#713): mentioned and replied, which the notifier raises into
// its inbox (D153). They sit beside the declaration file, since an engine's
// declaration parser refuses an events key and Comments' declaration is the
// engine's too; the core declares them in code.
//
//go:embed behaviors/comments.events.json
var commentsEvents []byte

// tableEvents are the events a table's use of a behavior emits beside the
// ones its declaration lists, by behavior, each with the config member that
// turns them on. Per-behavior code, as tableLeavesOut is, until step 5
// derives it from the behavior's class (D103).
var tableEvents = map[string]struct {
	member string
	events []BehaviorEvent
}{
	"Comments": {member: "events", events: mustEvents(commentsEvents)},
}

func mustEvents(data []byte) []BehaviorEvent {
	var events []BehaviorEvent
	if err := json.Unmarshal(data, &events); err != nil {
		panic("registry: core table events: " + err.Error())
	}
	return events
}

// UseEvents are the events a table's use of the behavior with config emits
// (D104): those its declaration lists, then those a table's use emits when
// its config turns them on (tableEvents: Comments' mentioned and replied
// with events: true, #713). A config that does not read turns none on.
func UseEvents(behavior Behavior, config json.RawMessage) []BehaviorEvent {
	extra, ok := tableEvents[behavior.Name]
	if !ok || len(config) == 0 {
		return behavior.Events
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(config, &members) != nil {
		return behavior.Events
	}
	var on bool
	if raw, set := members[extra.member]; !set || json.Unmarshal(raw, &on) != nil || !on {
		return behavior.Events
	}
	out := make([]BehaviorEvent, 0, len(behavior.Events)+len(extra.events))
	out = append(out, behavior.Events...)
	return append(out, extra.events...)
}

// TableEventKinds are the kinds of the events a table's use of the behavior
// name may emit beside its declaration's (tableEvents), in order; none for
// a behavior with none.
func TableEventKinds(name string) []string {
	var out []string
	for _, event := range tableEvents[name].events {
		out = append(out, event.Kind)
	}
	return out
}

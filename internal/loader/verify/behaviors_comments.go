package verify

import (
	"encoding/json"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// commentsBehavior is the conversation a table implements (D77, D103's
// step 4).
const commentsBehavior = "Comments"

// checkCommentsConfig holds a table's Comments config to what the
// behaviors' core runs, in the core's words and order: a member of a part
// it cannot run yet (streamed replies, claiming deliveries, code anchors;
// registry.TableRefusesMembers) is refused. An attachment block,
// and the config's attachments, the core takes since D154. The
// configSchema, which the declaration's own check holds a config to first,
// has held each member's shape.
func checkCommentsConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var members map[string]json.RawMessage
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &members) != nil {
		return
	}
	for _, refused := range registry.TableRefusesMembers(ref.Name) {
		if _, given := members[refused.Member]; given {
			r.errorf(td.Owner, "type %s: behavior %s config: %s: %s", td.Name, ref.Name, refused.Member, refused.Why)
			return
		}
	}
}

// checkEngineCommentsConfig refuses, on an engine type, events, which a
// table's Comments alone takes (#713): its mentioned and replied events
// reach the notifier through the table's event queue, and the engine's
// Comments records its operation events in its log. The declaration is
// shared, so the configSchema accepts it; the engine's Comments refuses it
// too, and the build says so first, naming the table form.
func checkEngineCommentsConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	if configNames(ref.Config, "events") {
		r.errorf(td.Owner, "type %s: @behavior Comments names events, which a table's Comments takes (implements Comments<%s, { events: true }>, #713); an engine type's Comments records its operation events in the engine's log, so leave it out", td.Name, td.Name)
	}
}

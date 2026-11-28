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

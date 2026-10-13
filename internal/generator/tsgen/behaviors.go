package tsgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
)

// renderBehaviors writes the declarations of types/behaviors.ts: for each
// type that composes behaviors, the aliases its behaviors' fields refer to
// and <T>Behaviors, typed by behaviortypes' TypeScript renderer as
// engine-client types them, with each behavior under its exact name. A
// type whose behaviors declare no field has an empty record.
func renderBehaviors(types []behaviortypes.Type) string {
	r := behaviortypes.TSRenderer{}
	var body strings.Builder
	for _, t := range types {
		for _, alias := range t.Aliases {
			body.WriteString("\n")
			if alias.Doc != "" {
				body.WriteString(behaviortypes.TSDoc(alias.Doc) + "\n")
			}
			fmt.Fprintf(&body, "export type %s = %s;\n", alias.Name, r.Render(alias.Schema, ""))
		}
		body.WriteString("\n")
		body.WriteString(behaviortypes.TSDoc(t.Structs[0].Doc) + "\n")
		if members := r.BehaviorMembers(t.Behaviors); members == "" {
			fmt.Fprintf(&body, "export type %s = Record<string, never>;\n", t.BehaviorsName)
		} else {
			fmt.Fprintf(&body, "export interface %s {\n%s}\n", t.BehaviorsName, members)
		}
	}
	if !r.UsesJSONObject {
		return body.String()
	}
	return fmt.Sprintf("\n/** A JSON object whose members the schema does not name. */\ntype %s = { [key: string]: unknown };\n", behaviortypes.JSONObjectType) + body.String()
}

package verify_test

import (
	"strings"
	"testing"
)

// TestEngineTypesRefuseTheTableMembers (D104): an engine type's
// @behavior("Constants", ...), ("StateRules", ...) or ("Approvals", ...)
// naming a member a table's alone takes, which the shared declaration's
// configSchema accepts, is refused, one problem per member, naming the
// table form, as the engine refuses each: Constants' oneWay; StateRules'
// by, per state in the canonical config's order, then status; Approvals'
// resetOn then pinned, per gate, then status. An engine type's configs
// without them load.
func TestEngineTypesRefuseTheTableMembers(t *testing.T) {
	note := func(behaviors ...map[string]any) func(doc, _ map[string]any) {
		return func(doc, _ map[string]any) {
			list := []any{map[string]any{"name": "Workflow", "config": map[string]any{
				"states": []any{"draft", "sent", "read"}, "transitions": []any{map[string]any{"from": "draft", "to": "sent"}, map[string]any{"from": "sent", "to": "read"}},
			}}}
			for _, behavior := range behaviors {
				list = append(list, behavior)
			}
			doc["types"].(map[string]any)["Note"] = map[string]any{"name": "Note", "role": "DBTable", "behaviors": list, "fields": []any{
				taskField("id", "Identity.UUID", map[string]any{"required": true, "key": true}),
				taskField("title", "string", map[string]any{"required": true}),
				taskField("author", "string", nil),
				taskField("count", "number", nil),
			}}
		}
	}
	constants := func(config map[string]any) map[string]any {
		config["fields"] = []any{"title"}
		return map[string]any{"name": "Constants", "config": config}
	}
	staterules := func(config map[string]any) map[string]any {
		return map[string]any{"name": "StateRules", "config": config}
	}
	approvals := func(config map[string]any) map[string]any {
		return map[string]any{"name": "Approvals", "config": config}
	}
	frozen := func(by ...any) map[string]any {
		rule := map[string]any{"frozen": []any{"title"}}
		if len(by) > 0 {
			rule["by"] = by
		}
		return rule
	}
	gate := func(members map[string]any) map[string]any {
		g := map[string]any{"count": 1, "permission": "notes.review", "excludeCreator": false}
		for k, v := range members {
			g[k] = v
		}
		return g
	}
	const (
		oneWay         = "type Note: @behavior Constants names oneWay, which a table's Constants takes (implements Constants<Note, C>, D104); an engine type's Constants takes fields and permission alone, so leave it out"
		byIn           = "type Note: @behavior StateRules names by in the rules of %q, which a table's StateRules takes (implements StateRules<Note, C>, D104); an engine type's StateRules takes required, frozen and permission alone, so leave it out"
		stateStatus    = "type Note: @behavior StateRules names status, the Workflow field a table's StateRules reads (implements StateRules<Note, C>, D104); an engine type's StateRules reads its one Workflow, so leave status out"
		resetOn        = "type Note: @behavior Approvals names resetOn on the gate of %q, which a table's Approvals takes (implements Approvals<Note, C>, D104); an engine type's gates follow resetOnChange, so leave it out"
		pinned         = "type Note: @behavior Approvals names pinned on the gate of %q, which a table's Approvals takes (implements Approvals<Note, C>, D104); an engine type's sign-off names no version, so leave it out"
		approvalStatus = "type Note: @behavior Approvals names status, the Workflow field a table's Approvals reads (implements Approvals<Note, C>, D104); an engine type's Approvals reads its one Workflow, so leave status out"
	)
	quoted := func(format, state string) string { return strings.Replace(format, "%q", `"`+state+`"`, 1) }
	for _, tc := range []struct {
		name string
		edit func(doc, _ map[string]any)
		want []string
	}{
		{"Constants' oneWay", note(constants(map[string]any{"oneWay": map[string]any{"count": "up"}})), []string{oneWay}},
		{"StateRules' by", note(staterules(map[string]any{"states": map[string]any{"sent": frozen("author"), "draft": frozen("author"), "read": frozen()}})),
			[]string{quoted(byIn, "draft"), quoted(byIn, "sent")}},
		{"StateRules' status", note(staterules(map[string]any{"states": map[string]any{"sent": frozen()}, "status": "status"})), []string{stateStatus}},
		{"StateRules' by and status", note(staterules(map[string]any{"states": map[string]any{"sent": frozen("author")}, "status": "status"})),
			[]string{quoted(byIn, "sent"), stateStatus}},
		{"Approvals' resetOn and pinned", note(approvals(map[string]any{"gates": map[string]any{
			"sent": gate(map[string]any{"resetOn": []any{}, "pinned": false}), "read": gate(map[string]any{"pinned": true}),
		}})), []string{quoted(pinned, "read"), quoted(resetOn, "sent"), quoted(pinned, "sent")}},
		{"Approvals' status", note(approvals(map[string]any{"gates": map[string]any{"sent": gate(nil)}, "status": "status"})), []string{approvalStatus}},
		{"all three", note(
			constants(map[string]any{"oneWay": map[string]any{"count": "up"}}),
			staterules(map[string]any{"states": map[string]any{"sent": frozen("author")}}),
			approvals(map[string]any{"gates": map[string]any{"sent": gate(map[string]any{"resetOn": []any{"title"}})}, "status": "status"}),
		), []string{oneWay, quoted(byIn, "sent"), quoted(resetOn, "sent"), approvalStatus}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTasks(t, tc.edit)
			if err == nil {
				t.Fatal("LoadService succeeded")
			}
			last := -1
			for _, want := range tc.want {
				at := strings.Index(err.Error(), want)
				if at < 0 || at < last {
					t.Fatalf("LoadService = %v, want %q, after the problems before it", err, want)
				}
				last = at
			}
			if got := strings.Count(err.Error(), "(implements "); got != len(tc.want) {
				t.Errorf("LoadService = %v, want %d problems", err, len(tc.want))
			}
		})
	}
	// An engine type's configs without them load.
	if _, err := loadTasks(t, note(
		constants(map[string]any{"permission": "notes.admin"}),
		staterules(map[string]any{"states": map[string]any{"sent": frozen()}, "permission": "notes.admin"}),
		approvals(map[string]any{"gates": map[string]any{"sent": gate(nil)}, "resetOnChange": false}),
	)); err != nil {
		t.Errorf("an engine type's configs without the table's members: %v", err)
	}
}

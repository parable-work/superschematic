package verify_test

import (
	"slices"
	"strings"
	"testing"
)

// TestWritableByLearnsTheFreezes (D104): the build's warning of an update
// that takes a field a by names learns Constants and StateRules. A Workflow
// by field the table's Constants lists in fields draws none, since no
// update changes it; one Constants does not list is warned of, its advice
// to freeze it there; a StateRules state's by field is warned of as a
// Workflow's is, naming the states whose freezes it lifts, unless
// Constants lists it; createdBy never is. One view carrying several draws
// one warning per use and field, the table's implements uses before its
// field uses. Each is a warning: the API loads.
func TestWritableByLearnsTheFreezes(t *testing.T) {
	view := func(fields ...string) string {
		var list []string
		for _, field := range fields {
			list = append(list, `{"name": "`+field+`", "typeRef": {"name": "Identity.UUID"}}`)
		}
		return `{"TaskEdit": {"name": "TaskEdit", "role": "APIView", "source": {"target": "tasks-db.Task", "omittedFromSource": ["id", "title", "plan", "proof", "urgent", "attempts"]}, "fields": [` + strings.Join(list, ", ") + `]}}`
	}
	// The set of the table's behavior operations has the loader read the
	// database, which resolves the view's target in a data format.
	const sets = `[{"name": "TaskActions", "operations": [], "behaviorOperations": {"type": "Task"}, "auth": true}, {"name": "TaskEdits", "operations": [{"name": "edit", "typeRef": {"name": "Task"}, "required": true, "httpMethod": "PATCH", "restPath": "tasks/{id}",
	  "arguments": [{"name": "id", "typeRef": {"name": "Identity.UUID"}, "required": true}, {"name": "input", "typeRef": {"name": "TaskEdit"}, "required": true}]}]}]`
	stateRules := func(field, states string) string {
		return "tasks-api: TaskEdits.edit takes Task." + field + " (TaskEdit is @source(Task)), and Task's StateRules names " + field + " in by for " + states + ": a caller who may call edit can set " + field + " to itself and then change the fields " + states + " " + map[bool]string{true: "freeze", false: "freezes"}[strings.Contains(states, " and ")] + ". Keep " + field + " out of the operation's input, or freeze it: list it in the fields of Task's Constants (D104)"
	}
	workflow := func(field, moves string) string {
		return "tasks-api: TaskEdits.edit takes Task." + field + " (TaskEdit is @source(Task)), and the Workflow on Task.status names " + field + " in by for " + moves + ": a caller who may call edit can set " + field + " to itself and then make that move. Keep " + field + " out of the operation's input, or freeze it: list it in the fields of Task's Constants (D104)"
	}
	states := func(task map[string]any) map[string]any { return config(task, "StateRules")["states"].(map[string]any) }
	for _, tc := range []struct {
		name   string
		edit   func(doc, task map[string]any)
		fields []string
		want   []string
	}{
		{"a Workflow by field Constants lists", nil, []string{"owner"}, nil},
		{"a StateRules by field", nil, []string{"reviewer"}, []string{stateRules("reviewer", "working")}},
		{"both, with one view", nil, []string{"owner", "reviewer"}, []string{stateRules("reviewer", "working")}},
		{"a Workflow by field Constants does not list", func(_, task map[string]any) {
			config(task, "Constants")["fields"] = []any{"title"}
		}, []string{"owner", "reviewer"}, []string{stateRules("reviewer", "working"), workflow("owner", "planned to working and working to proving")}},
		{"one field both name, in several states", func(_, task map[string]any) {
			states(task)["planned"] = map[string]any{"frozen": []any{"plan"}, "by": []any{"reviewer", "createdBy"}}
			transitions := config(task, "Workflow")["transitions"].([]any)
			transitions[3].(map[string]any)["by"] = []any{"reviewer"}
		}, []string{"reviewer"}, []string{stateRules("reviewer", "planned and working"), workflow("reviewer", "proving to done")}},
		{"a StateRules by field Constants lists", func(_, task map[string]any) {
			config(task, "Constants")["fields"] = []any{"owner", "reviewer"}
		}, []string{"owner", "reviewer"}, nil},
		{"createdBy", nil, []string{"createdBy"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := warningsOf(t, tc.edit, view(tc.fields...), sets); !slices.Equal(got, tc.want) {
				t.Errorf("warnings =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

package verify

import (
	"encoding/json"
	"testing"
)

// TestWorkflowRulesProblem: a Workflow's config is held at the build to
// the rules the core's WorkflowConfig::parse holds it to, in its words
// and its order, one case per message; a config the core takes has none.
func TestWorkflowRulesProblem(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"a state machine", `{"initial":"todo","outcomes":{"canceled":"neutral","done":"success"},"states":["todo","doing","done","canceled"],"transitions":[{"from":"todo","to":"doing"},{"from":"doing","to":"todo"},{"from":"doing","to":"done"},{"from":"todo","to":"canceled"}]}`, ""},
		{"no initial, no outcomes, no transitions", `{"states":["todo"],"transitions":[]}`, ""},
		{"a state no transition reaches", `{"states":["todo","done","lost"],"transitions":[{"from":"todo","to":"done"}]}`, ""},
		{"an initial that is no state", `{"initial":"open","states":["todo","done"],"transitions":[]}`,
			`the initial state "open" is not one of its states (todo, done)`},
		{"a from that is no state", `{"states":["todo","done"],"transitions":[{"from":"doing","to":"done"}]}`,
			`the transition from "doing" to "done" names "doing", which is not one of its states (todo, done)`},
		{"a to that is no state", `{"states":["todo","done"],"transitions":[{"from":"todo","to":"closed"}]}`,
			`the transition from "todo" to "closed" names "closed", which is not one of its states (todo, done)`},
		{"a transition to itself", `{"states":["todo","done"],"transitions":[{"from":"todo","to":"todo"}]}`,
			`a transition from "todo" to itself changes nothing`},
		{"a transition twice", `{"states":["todo","done"],"transitions":[{"from":"todo","to":"done"},{"from":"todo","to":"done","permission":"x"}]}`,
			`the transition from "todo" to "done" is listed twice`},
		{"an outcome of no state", `{"outcomes":{"closed":"success"},"states":["todo","done"],"transitions":[{"from":"todo","to":"done"}]}`,
			`outcomes names "closed", which is not one of its states (todo, done)`},
		{"an outcome of a state a transition leaves", `{"outcomes":{"todo":"neutral"},"states":["todo","done"],"transitions":[{"from":"todo","to":"done"}]}`,
			`outcomes names "todo", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome`},
		// finished names states a transition leaves, each with its
		// outcome (D113), after the outcomes, by name.
		{"a finished state a transition leaves", `{"finished":{"done":"success"},"outcomes":{"canceled":"neutral"},"states":["todo","done","canceled"],"transitions":[{"from":"todo","to":"done"},{"from":"done","to":"todo"},{"from":"todo","to":"canceled"}]}`, ""},
		{"a finished state that is no state", `{"finished":{"closed":"success"},"states":["todo","done"],"transitions":[{"from":"todo","to":"done"},{"from":"done","to":"todo"}]}`,
			`finished names "closed", which is not one of its states (todo, done)`},
		{"a finished state no transition leaves", `{"finished":{"done":"success"},"states":["todo","done"],"transitions":[{"from":"todo","to":"done"}]}`,
			`finished names "done", which is a terminal state: no transition leaves it, and outcomes names a terminal state's outcome`},
		{"an outcome before a finished state", `{"finished":{"closed":"success"},"outcomes":{"todo":"neutral"},"states":["todo","done"],"transitions":[{"from":"todo","to":"done"}]}`,
			`outcomes names "todo", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome`},
		{"finished in name order", `{"finished":{"todo":"neutral","done":"success","closed":"success"},"states":["todo","done"],"transitions":[{"from":"todo","to":"done"}]}`,
			`finished names "closed", which is not one of its states (todo, done)`},
		// The first, in the core's order: the initial state, each
		// transition as listed, then the outcomes by name.
		{"the first of several", `{"initial":"open","outcomes":{"closed":"success"},"states":["todo","done"],"transitions":[{"from":"todo","to":"todo"}]}`,
			`the initial state "open" is not one of its states (todo, done)`},
		{"a transition before an outcome", `{"outcomes":{"closed":"success"},"states":["todo","done"],"transitions":[{"from":"todo","to":"todo"}]}`,
			`a transition from "todo" to itself changes nothing`},
		{"outcomes in name order", `{"outcomes":{"todo":"neutral","closed":"success"},"states":["todo","done"],"transitions":[{"from":"todo","to":"done"}]}`,
			`outcomes names "closed", which is not one of its states (todo, done)`},
		// What the configSchema reports is left to it.
		{"no config", ``, ""},
		{"a config that is no object", `["todo"]`, ""},
	} {
		if got := workflowRulesProblem(json.RawMessage(tc.config)); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// transitionMoves reads a config's transitions as "from>to" and, for one
// with other members, "from>to permission by".
func transitionMoves(t *testing.T, config json.RawMessage) []string {
	t.Helper()
	var read struct {
		Transitions []struct {
			From       string   `json:"from"`
			To         string   `json:"to"`
			Permission string   `json:"permission"`
			By         []string `json:"by"`
		} `json:"transitions"`
	}
	if err := json.Unmarshal(config, &read); err != nil {
		t.Fatalf("the expanded config holds a transition that is not a from and a to: %v\n%s", err, config)
	}
	var out []string
	for _, tr := range read.Transitions {
		move := tr.From + ">" + tr.To
		if tr.Permission != "" || len(tr.By) > 0 {
			move += " " + tr.Permission + " " + strings.Join(tr.By, ",")
		}
		out = append(out, move)
	}
	return out
}

// TestExpandTransitionListsWritesOutTheListedStates: a transition whose
// from or to lists states is one transition per pair, in the lists' order,
// each with the transition's other members; a state both lists hold moves
// to the others; and the config's other members stay as they were (D109).
func TestExpandTransitionListsWritesOutTheListedStates(t *testing.T) {
	for _, tc := range []struct {
		name        string
		transitions string
		want        []string
	}{
		{"a list of from states", `[{"from":["backlog","todo"],"to":"started"}]`, []string{"backlog>started", "todo>started"}},
		{"a list of to states", `[{"from":"started","to":["done","canceled"]}]`, []string{"started>done", "started>canceled"}},
		{"both, with the other members on each", `[{"from":["backlog","todo"],"to":["done","canceled"],"permission":"issues.close","by":["assigneeId"]}]`,
			[]string{"backlog>done issues.close assigneeId", "backlog>canceled issues.close assigneeId", "todo>done issues.close assigneeId", "todo>canceled issues.close assigneeId"}},
		{"one list in both is every move among its states", `[{"from":["backlog","todo","started"],"to":["backlog","todo","started"]}]`,
			[]string{"backlog>todo", "backlog>started", "todo>backlog", "todo>started", "started>backlog", "started>todo"}},
		{"beside plain transitions, in the config's order", `[{"from":"done","to":"backlog"},{"from":["backlog","todo"],"to":"done"},{"from":"todo","to":"backlog"}]`,
			[]string{"done>backlog", "backlog>done", "todo>done", "todo>backlog"}},
		{"a list of one state", `[{"from":["backlog"],"to":"done"}]`, []string{"backlog>done"}},
	} {
		config, err := ExpandTransitionLists(json.RawMessage(`{"initial":"backlog","outcomes":{"canceled":"neutral"},"states":["backlog","todo","started","done","canceled"],"transitions":` + tc.transitions + `}`))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := transitionMoves(t, config); strings.Join(got, "; ") != strings.Join(tc.want, "; ") {
			t.Errorf("%s:\n got %v\nwant %v", tc.name, got, tc.want)
		}
		var rest map[string]json.RawMessage
		if err := json.Unmarshal(config, &rest); err != nil {
			t.Fatal(err)
		}
		if string(rest["initial"]) != `"backlog"` || string(rest["outcomes"]) != `{"canceled":"neutral"}` || string(rest["states"]) != `["backlog","todo","started","done","canceled"]` || len(rest) != 4 {
			t.Errorf("%s: the config's other members changed: %s", tc.name, config)
		}
	}
}

// TestExpandTransitionListsLeavesWhatItDoesNotRead: a config of plain
// transitions is returned byte for byte, two that repeat each other among
// them, which the core refuses; so is a config the configSchema refuses.
func TestExpandTransitionListsLeavesWhatItDoesNotRead(t *testing.T) {
	for name, config := range map[string]string{
		"plain transitions":            `{ "states": ["a", "b"], "transitions": [{ "from": "a", "to": "b" }] }`,
		"plain transitions, repeated":  `{"states":["a","b"],"transitions":[{"from":"a","to":"b"},{"from":"a","to":"b"}]}`,
		"a from that is no state":      `{"states":["a","b"],"transitions":[{"from":7,"to":["a","b"]}]}`,
		"a list that holds no state":   `{"states":["a","b"],"transitions":[{"from":["a",7],"to":"b"}]}`,
		"a transition with no to":      `{"states":["a","b"],"transitions":[{"from":["a","b"]}]}`,
		"transitions that are no list": `{"states":["a","b"],"transitions":{"from":["a"],"to":"b"}}`,
		"no object":                    `["a","b"]`,
	} {
		got, err := ExpandTransitionLists(json.RawMessage(config))
		if err != nil || string(got) != config {
			t.Errorf("%s: %v\n got %s\nwant %s", name, err, got, config)
		}
	}
}

// TestExpandTransitionListsRefusesWhatOnlyTheShortFormGetsWrong, one case
// per message.
func TestExpandTransitionListsRefusesWhatOnlyTheShortFormGetsWrong(t *testing.T) {
	for _, tc := range []struct {
		name        string
		transitions string
		want        string
	}{
		{"an empty list", `[{"from":[],"to":"done"}]`, `the transition from [] to "done" lists no state in from`},
		{"an empty to", `[{"from":"todo","to":[]}]`, `the transition from "todo" to [] lists no state in to`},
		{"a state twice in a list", `[{"from":["todo","backlog","todo"],"to":"done"}]`, `the transition from ["todo", "backlog", "todo"] to "done" lists "todo" twice in from`},
		{"lists that leave no move", `[{"from":["todo"],"to":["todo"]}]`, `the transition from ["todo"] to ["todo"] makes no move: a state both lists hold moves to the others, and there are none`},
		{"a move a list and a plain transition both make", `[{"from":"todo","to":"done"},{"from":["backlog","todo"],"to":"done"}]`,
			`the transition from "todo" to "done" is listed twice: by the transition from "todo" to "done", and by the transition from ["backlog", "todo"] to "done"`},
		{"a move two lists make", `[{"from":["backlog","todo"],"to":"done"},{"from":"todo","to":["started","done"]}]`,
			`the transition from "todo" to "done" is listed twice: by the transition from ["backlog", "todo"] to "done", and by the transition from "todo" to ["started", "done"]`},
	} {
		_, err := ExpandTransitionLists(json.RawMessage(`{"states":["backlog","todo","started","done"],"transitions":` + tc.transitions + `}`))
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s:\n got %v\nwant %s", tc.name, err, tc.want)
		}
	}
}

// TestOnlyATablesWorkflowExpandsItsTransitions: a use on a field or by
// implements is written out; an engine type's @behavior, and any other
// behavior, is left as it is.
func TestOnlyATablesWorkflowExpandsItsTransitions(t *testing.T) {
	const short = `{"states":["a","b"],"transitions":[{"from":["a"],"to":"b"}]}`
	for _, tc := range []struct {
		name     string
		ref      BehaviorRef
		expanded bool
	}{
		{"on a field", BehaviorRef{Name: "Workflow", Field: "status"}, true},
		{"by implements", BehaviorRef{Name: "Workflow", Implements: true}, true},
		{"an engine type's", BehaviorRef{Name: "Workflow"}, false},
		{"another behavior", BehaviorRef{Name: "Reconcile", Field: "observed"}, false},
	} {
		ref := tc.ref
		ref.Config = json.RawMessage(short)
		if err := ref.ExpandTransitions(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if got := string(ref.Config) != short; got != tc.expanded {
			t.Errorf("%s: config %s, expanded %t, want %t", tc.name, ref.Config, got, tc.expanded)
		}
	}
	var none *BehaviorRef
	if err := none.ExpandTransitions(); err != nil {
		t.Error(err)
	}
	empty := BehaviorRef{Name: "Workflow", Field: "status"}
	if err := empty.ExpandTransitions(); err != nil || empty.Config != nil {
		t.Errorf("a use with no config: %v, %s", err, empty.Config)
	}
}

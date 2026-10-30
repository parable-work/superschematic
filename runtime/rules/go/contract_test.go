package rules_test

import (
	"encoding/json"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

// The contract's JSON, as superschematic-rules reads and writes it: each
// action kind's exact members, each anchor and answer both ways, each step
// and outcome read, one of a kind the binding does not know kept whole,
// and a row write's null key.

func marshal(t *testing.T, value any) string {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %+v: %v", value, err)
	}
	return string(out)
}

func TestContractJSON(t *testing.T) {
	t.Run("actions", func(t *testing.T) {
		from := &rules.TypeSpec{Name: "Order"}
		cases := []struct {
			action rules.Action
			want   string
		}{
			{rules.Action{Kind: rules.ActionDefine}, `{"kind":"define"}`},
			{rules.Action{Kind: rules.ActionPublish}, `{"kind":"publish","from":null}`},
			{rules.Action{Kind: rules.ActionPublish, From: from}, `{"kind":"publish","from":{"name":"Order"}}`},
			{rules.Action{Kind: rules.ActionCreate}, `{"kind":"create","id":null,"own":{},"params":{}}`},
			{
				rules.Action{Kind: rules.ActionCreate, ID: "o1", Own: map[string]json.RawMessage{"title": json.RawMessage(`"Desk"`)}, Params: json.RawMessage(`{"status":{}}`)},
				`{"kind":"create","id":"o1","own":{"title":"Desk"},"params":{"status":{}}}`,
			},
			{rules.Action{Kind: rules.ActionUpdate, Key: "o1"}, `{"kind":"update","key":"o1","patch":{}}`},
			{rules.Action{Kind: rules.ActionUpdate, Key: "o1", Patch: map[string]json.RawMessage{"title": json.RawMessage(`null`)}}, `{"kind":"update","key":"o1","patch":{"title":null}}`},
			{rules.Action{Kind: rules.ActionDelete, Key: "o1"}, `{"kind":"delete","key":"o1"}`},
			{rules.Action{Kind: rules.ActionOperation, Key: "o1", UseID: "status", Operation: "transition"}, `{"kind":"operation","key":"o1","useId":"status","operation":"transition","params":{}}`},
			{
				rules.Action{Kind: rules.ActionOperation, Key: "o1", UseID: "status", Operation: "transition", Params: json.RawMessage(`{"to":"paid"}`)},
				`{"kind":"operation","key":"o1","useId":"status","operation":"transition","params":{"to":"paid"}}`,
			},
			{rules.Action{Kind: rules.ActionDeadline, Key: "o1", UseID: "timers", Name: "expire", At: 5000}, `{"kind":"deadline","key":"o1","useId":"timers","name":"expire","at":5000,"payload":null}`},
			{rules.Action{Kind: rules.ActionDeadline, Key: "o1", UseID: "timers", Name: "expire", At: 5000, Payload: json.RawMessage(`{"n":1}`)}, `{"kind":"deadline","key":"o1","useId":"timers","name":"expire","at":5000,"payload":{"n":1}}`},
		}
		for _, c := range cases {
			got := marshal(t, c.action)
			if got != c.want {
				t.Errorf("%s: got %s, want %s", c.action.Kind, got, c.want)
			}
			var back rules.Action
			if err := json.Unmarshal([]byte(got), &back); err != nil {
				t.Fatalf("%s: read back: %v", c.action.Kind, err)
			}
			if again := marshal(t, back); again != c.want {
				t.Errorf("%s: read back as %s, want %s", c.action.Kind, again, c.want)
			}
		}
		if _, err := json.Marshal(rules.Action{Kind: "touch"}); err == nil {
			t.Error("an action of no known kind marshalled")
		}
		var unknown rules.Action
		if err := json.Unmarshal([]byte(`{"kind":"touch"}`), &unknown); err == nil {
			t.Error("an action of no known kind read")
		}
		command := rules.Command{Caller: rules.Caller{Subject: "alice"}, Now: 1, Action: rules.Action{Kind: rules.ActionDelete, Key: "o1"}}
		if got := marshal(t, command); got != `{"caller":{"subject":"alice"},"now":1,"action":{"kind":"delete","key":"o1"}}` {
			t.Errorf("a command with no request = %s", got)
		}
	})

	t.Run("anchors", func(t *testing.T) {
		if got := marshal(t, rules.Anchor{}); got != `"type"` {
			t.Errorf("the type anchor = %s", got)
		}
		if got := marshal(t, rules.Anchor{Field: "status"}); got != `{"field":"status"}` {
			t.Errorf("a field anchor = %s", got)
		}
		var use rules.UseSpec
		if err := json.Unmarshal([]byte(`{"behavior":"Workflow","anchor":{"field":"status"}}`), &use); err != nil || use.Anchor.Field != "status" {
			t.Errorf("read a field anchor: %+v, %v", use, err)
		}
		use = rules.UseSpec{}
		if err := json.Unmarshal([]byte(`{"behavior":"Workflow","anchor":"type","config":null}`), &use); err != nil || use.Anchor.Field != "" || string(use.Config) != "null" {
			t.Errorf("read the type anchor and a null config: %+v, %v", use, err)
		}
		if got := marshal(t, use); got != `{"behavior":"Workflow","config":null,"anchor":"type"}` {
			t.Errorf("a null config given = %s", got)
		}
		use = rules.UseSpec{}
		if err := json.Unmarshal([]byte(`{"behavior":"Workflow"}`), &use); err != nil || use.Anchor.Field != "" || use.Config != nil {
			t.Errorf("read no anchor and no config: %+v, %v", use, err)
		}
		if got := marshal(t, use); got != `{"behavior":"Workflow","anchor":"type"}` {
			t.Errorf("no config given = %s", got)
		}
		for _, bad := range []string{`"field"`, `{"field":""}`, `3`} {
			var anchor rules.Anchor
			if err := json.Unmarshal([]byte(bad), &anchor); err == nil {
				t.Errorf("read the anchor %s", bad)
			}
		}
	})

	t.Run("answers", func(t *testing.T) {
		cases := []struct {
			answer rules.Answer
			want   string
		}{
			{rules.Answer{Kind: rules.ReadKindRow}, `{"row":null}`},
			{rules.Answer{Kind: rules.ReadKindRow, Row: &rules.Row{Own: map[string]json.RawMessage{"title": json.RawMessage(`"Desk"`)}, Owned: map[string]map[string]json.RawMessage{"status": {"status": json.RawMessage(`"draft"`)}}, Version: 2}}, `{"row":{"own":{"title":"Desk"},"owned":{"status":{"status":"draft"}},"version":2}}`},
			{rules.Answer{Kind: rules.ReadKindHasRows, HasRows: true}, `{"hasRows":true}`},
			{rules.Answer{Kind: rules.ReadKindHasRows}, `{"hasRows":false}`},
			{rules.Answer{Kind: rules.ReadKindSpec}, `{"spec":null}`},
			{rules.Answer{Kind: rules.ReadKindSpec, Spec: &rules.TypeSpec{Name: "Order"}}, `{"spec":{"name":"Order"}}`},
		}
		for _, c := range cases {
			got := marshal(t, c.answer)
			if got != c.want {
				t.Errorf("%s: got %s, want %s", c.answer.Kind, got, c.want)
			}
			var back rules.Answer
			if err := json.Unmarshal([]byte(got), &back); err != nil {
				t.Fatalf("%s: read back: %v", c.answer.Kind, err)
			}
			if again := marshal(t, back); again != c.want {
				t.Errorf("%s: read back as %s", c.answer.Kind, again)
			}
		}
		answered := rules.Answered{Request: rules.ReadRequest{Kind: rules.ReadKindHasRows, Type: "Order"}, Answer: rules.Answer{Kind: rules.ReadKindHasRows}, Held: 1}
		if got := marshal(t, answered); got != `{"request":{"kind":"hasRows","type":"Order"},"answer":{"hasRows":false},"held":1}` {
			t.Errorf("an answered read = %s", got)
		}
	})

	t.Run("steps", func(t *testing.T) {
		var read rules.Step
		if err := json.Unmarshal([]byte(`{"read":[{"kind":"row","type":"Order","key":"o1"},{"kind":"hasRows","type":"Order"}]}`), &read); err != nil {
			t.Fatal(err)
		}
		if len(read.Read) != 2 || read.Read[1] != (rules.ReadRequest{Kind: rules.ReadKindHasRows, Type: "Order"}) || read.Lock != nil || read.Done != nil || read.Raw != nil {
			t.Errorf("a read step = %+v", read)
		}
		var lock rules.Step
		if err := json.Unmarshal([]byte(`{"lock":["Stock/s1"]}`), &lock); err != nil || len(lock.Lock) != 1 || lock.Lock[0] != "Stock/s1" || lock.Read != nil {
			t.Errorf("a lock step = %+v, %v", lock, err)
		}
		var empty rules.Step
		if err := json.Unmarshal([]byte(`{"read":[]}`), &empty); err != nil || empty.Read == nil || len(empty.Read) != 0 {
			t.Errorf("an empty read step = %+v, %v", empty, err)
		}
		var refused rules.Step
		if err := json.Unmarshal([]byte(`{"done":{"refused":{"code":"vetoed","status":409,"class":"BehaviorVetoError","message":"no","details":{"vetoCode":"x"}}}}`), &refused); err != nil {
			t.Fatal(err)
		}
		if refused.Done == nil || refused.Done.Refused == nil || refused.Done.Refused.Code != rules.CodeVetoed || string(refused.Done.Refused.Details) != `{"vetoCode":"x"}` {
			t.Errorf("a refused step = %+v", refused.Done)
		}
		var changed rules.Step
		if err := json.Unmarshal([]byte(`{"done":{"changed":{"rows":[{"kind":"update","type":"Order","key":"o1","expectedVersion":3,"own":{},"owned":{"status":{"status":"paid","enteredAt":2000}}}],"subRows":[],"events":[],"deadlines":[],"result":{"from":"draft","to":"paid"}}}}`), &changed); err != nil {
			t.Fatal(err)
		}
		changes := changed.Done.Changed
		if changes == nil || len(changes.Rows) != 1 || changes.Rows[0].ExpectedVersion != 3 || string(changes.Rows[0].Owned["status"]["enteredAt"]) != "2000" || string(changes.Result) != `{"from":"draft","to":"paid"}` {
			t.Errorf("a changed step = %+v", changes)
		}
		if len(changes.Rows[0].Raw) == 0 {
			t.Error("a row write kept no JSON")
		}
		var wait rules.Step
		if err := json.Unmarshal([]byte(`{"wait":1}`), &wait); err != nil {
			t.Fatal(err)
		}
		if wait.Read != nil || wait.Lock != nil || wait.Done != nil || string(wait.Raw) != `{"wait":1}` {
			t.Errorf("a step of another kind = %+v", wait)
		}
		if got := marshal(t, wait); got != `{"wait":1}` {
			t.Errorf("a step of another kind writes %s", got)
		}
		var other rules.Step
		if err := json.Unmarshal([]byte(`{"done":{"postponed":true}}`), &other); err != nil {
			t.Fatal(err)
		}
		if other.Done == nil || other.Done.Refused != nil || other.Done.Changed != nil || string(other.Done.Raw) != `{"postponed":true}` {
			t.Errorf("an outcome of another kind = %+v", other.Done)
		}
	})

	t.Run("row writes", func(t *testing.T) {
		var write rules.RowWrite
		if err := json.Unmarshal([]byte(`{"kind":"create","type":"Order","key":null,"own":{"title":"Desk"},"owned":{}}`), &write); err != nil {
			t.Fatal(err)
		}
		if write.Kind != rules.RowCreate || write.Key != nil || string(write.Own["title"]) != `"Desk"` {
			t.Errorf("a create without an id = %+v", write)
		}
		if got := marshal(t, write); got != `{"kind":"create","type":"Order","key":null,"own":{"title":"Desk"}}` {
			t.Errorf("a create without an id writes %s", got)
		}
		var touch rules.RowWrite
		if err := json.Unmarshal([]byte(`{"kind":"touch","type":"Order","key":"o1"}`), &touch); err != nil || touch.Kind != "touch" || string(touch.Raw) != `{"kind":"touch","type":"Order","key":"o1"}` {
			t.Errorf("a write of another kind = %+v, %v", touch, err)
		}
	})
}

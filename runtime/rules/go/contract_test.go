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
			{
				rules.Action{Kind: rules.ActionChange, Key: "t1", Change: &rules.RowChange{Write: "insert", At: 1000000, After: &rules.RowImage{Own: map[string]json.RawMessage{"title": json.RawMessage(`"t1"`)}}}},
				`{"kind":"change","key":"t1","change":{"write":"insert","at":1000000,"after":{"own":{"title":"t1"},"owned":{}}}}`,
			},
			{
				rules.Action{Kind: rules.ActionChange, Key: "t1", Change: &rules.RowChange{Write: "update", At: 1001000,
					Before: &rules.RowImage{Own: map[string]json.RawMessage{"projectId": json.RawMessage(`"p1"`)}, Owned: map[string]map[string]json.RawMessage{"Workflow": {"status": json.RawMessage(`"todo"`)}}},
					After:  &rules.RowImage{Own: map[string]json.RawMessage{"projectId": json.RawMessage(`"p1"`)}, Owned: map[string]map[string]json.RawMessage{"Workflow": {"status": json.RawMessage(`"doing"`)}}}}},
				`{"kind":"change","key":"t1","change":{"write":"update","at":1001000,"before":{"own":{"projectId":"p1"},"owned":{"Workflow":{"status":"todo"}}},"after":{"own":{"projectId":"p1"},"owned":{"Workflow":{"status":"doing"}}}}}`,
			},
			{rules.Action{Kind: rules.ActionChange, Key: "t1", Change: &rules.RowChange{Write: "delete", At: 7}}, `{"kind":"change","key":"t1","change":{"write":"delete","at":7}}`},
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
		if _, err := json.Marshal(rules.Action{Kind: rules.ActionChange, Key: "t1"}); err == nil {
			t.Error("a change with no write marshalled")
		}
		var bare rules.Action
		if err := json.Unmarshal([]byte(`{"kind":"change","key":"t1"}`), &bare); err == nil {
			t.Error("a change with no write read")
		}
		command := rules.Command{Caller: rules.Caller{Subject: "alice"}, Now: 1, Action: rules.Action{Kind: rules.ActionDelete, Key: "o1"}}
		if got := marshal(t, command); got != `{"caller":{"subject":"alice"},"now":1,"action":{"kind":"delete","key":"o1"}}` {
			t.Errorf("a command with no request = %s", got)
		}
		// A command's preconditions, each use's entry by its use id (D16,
		// D104): written when it has any, its entries' JSON as given, and
		// read back with each entry's text kept.
		command.Preconditions = rules.Preconditions{"Lease": struct {
			Token int64 `json:"token"`
		}{3}}
		presented := `{"caller":{"subject":"alice"},"now":1,"action":{"kind":"delete","key":"o1"},"preconditions":{"Lease":{"token":3}}}`
		if got := marshal(t, command); got != presented {
			t.Errorf("a command presenting a precondition = %s, want %s", got, presented)
		}
		var back rules.Command
		if err := json.Unmarshal([]byte(`{"caller":{"subject":"alice"},"now":1,"action":{"kind":"delete","key":"o1"},"preconditions":{"Lease":{"token":3.0},"Other":null}}`), &back); err != nil {
			t.Fatal(err)
		}
		if got := marshal(t, back); got != `{"caller":{"subject":"alice"},"now":1,"action":{"kind":"delete","key":"o1"},"preconditions":{"Lease":{"token":3.0},"Other":null}}` {
			t.Errorf("a command's preconditions read back as %s", got)
		}
		var none rules.Command
		if err := json.Unmarshal([]byte(`{"caller":{"subject":"alice"},"now":1,"action":{"kind":"delete","key":"o1"},"preconditions":null}`), &none); err != nil || none.Preconditions != nil {
			t.Errorf("null preconditions read as %+v, %v", none.Preconditions, err)
		}
		if err := json.Unmarshal([]byte(`{"caller":{"subject":"alice"},"now":1,"action":{"kind":"delete","key":"o1"},"preconditions":[1]}`), &none); err == nil {
			t.Error("preconditions that are no object read")
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
			{rules.Answer{Kind: rules.ReadKindSpec, Spec: &rules.TypeSpec{Name: "Ticket", Fields: []rules.FieldSpec{{Name: "title", Type: "string"}, {Name: "visitAt", Type: "Temporal.DateTime", DateTime: true}}}}, `{"spec":{"name":"Ticket","fields":[{"name":"title","type":"string"},{"name":"visitAt","type":"Temporal.DateTime","dateTime":true}]}}`},
			{rules.Answer{Kind: rules.ReadKindSubRows}, `{"subRows":[]}`},
			{rules.Answer{Kind: rules.ReadKindSubRows, SubRows: []rules.SubRow{{Key: "idle"}, {Key: "respond", Values: map[string]json.RawMessage{"dueAt": json.RawMessage(`2500`), "fired": json.RawMessage(`false`)}}}}, `{"subRows":[{"key":"idle","values":{}},{"key":"respond","values":{"dueAt":2500,"fired":false}}]}`},
			{rules.Answer{Kind: rules.ReadKindLinked}, `{"linked":{"rows":[],"more":false}}`},
			{rules.Answer{Kind: rules.ReadKindLinked, Linked: &rules.LinkedRows{Rows: []rules.KeyedRow{{Key: "t1", Row: rules.Row{Own: map[string]json.RawMessage{"projectId": json.RawMessage(`"p1"`)}, Version: 3}}}, More: true}}, `{"linked":{"rows":[{"key":"t1","row":{"own":{"projectId":"p1"},"version":3}}],"more":true}}`},
			{rules.Answer{Kind: rules.ReadKindSpec, Spec: &rules.TypeSpec{Name: "Task", Fields: []rules.FieldSpec{{Name: "projectId", Type: "Identity.UUID", Relation: &rules.RelationSpec{Name: "project", Type: "Project"}}}}}, `{"spec":{"name":"Task","fields":[{"name":"projectId","type":"Identity.UUID","relation":{"name":"project","type":"Project"}}]}}`},
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
		subRows := rules.ReadRequest{Kind: rules.ReadKindSubRows, Type: "Ticket", Key: "t1", UseID: "Timers", Table: "deadlines"}
		if got := marshal(t, subRows); got != `{"kind":"subRows","type":"Ticket","key":"t1","useId":"Timers","table":"deadlines"}` {
			t.Errorf("a read of sub-table rows = %s", got)
		}
		for _, text := range []string{
			`{"kind":"linked","type":"Task","field":"projectId","key":"p1","limit":500}`,
			`{"kind":"linked","type":"Task","field":"projectId","key":"p1","after":"t9","limit":500}`,
			`{"kind":"linked","type":"Task","field":"projectId","key":"p1","limit":0}`,
		} {
			var linked rules.ReadRequest
			if err := json.Unmarshal([]byte(text), &linked); err != nil {
				t.Fatal(err)
			}
			if got := marshal(t, linked); got != text {
				t.Errorf("a linked read reads back as %s, want %s", got, text)
			}
		}
		// A holding read (D104): its use, its owned field and the value as
		// its JSON, its limit always.
		for _, text := range []string{
			`{"kind":"holding","type":"Job","useId":"Lease","field":"holder","value":"wren","limit":500}`,
			`{"kind":"holding","type":"Job","useId":"Lease","field":"holder","value":"wren","after":"j9","limit":500}`,
			`{"kind":"holding","type":"Job","useId":"Lease","field":"token","value":3,"limit":0}`,
			`{"kind":"holding","type":"Job","useId":"Lease","field":"ended","value":{"reason":"ttl"},"limit":1}`,
		} {
			var holding rules.ReadRequest
			if err := json.Unmarshal([]byte(text), &holding); err != nil {
				t.Fatal(err)
			}
			if got := marshal(t, holding); got != text {
				t.Errorf("a holding read reads back as %s, want %s", got, text)
			}
			if again := holding; again != holding {
				t.Errorf("a holding read compares unequal to its copy")
			}
		}
		if _, err := json.Marshal(rules.ReadRequest{Kind: rules.ReadKindHolding, Type: "Job", UseID: "Lease", Field: "holder", Value: "{"}); err == nil {
			t.Error("a holding read of a value that is no JSON marshalled")
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

	t.Run("sub-table rows and deadlines", func(t *testing.T) {
		const text = `{"rows":[],"subRows":[` +
			`{"row":{"type":"Ticket","key":"t1"},"useId":"Timers","table":"deadlines","key":"respond","change":{"kind":"insert","values":{"dueAt":2500}}},` +
			`{"row":{"type":"Ticket","key":"t1"},"useId":"Timers","table":"deadlines","key":"respond","change":{"kind":"update","values":{}}},` +
			`{"row":{"type":"Ticket","key":"t1"},"useId":"Timers","table":"deadlines","key":"idle","change":{"kind":"delete"}}],` +
			`"events":[],"deadlines":[` +
			`{"kind":"arm","row":{"type":"Ticket","key":"t1"},"useId":"Timers","name":"respond","at":0,"payload":null},` +
			`{"kind":"clear","row":{"type":"Ticket","key":"t1"},"useId":"Timers","name":"idle"}],"result":null}`
		var changes rules.ChangeSet
		if err := json.Unmarshal([]byte(text), &changes); err != nil {
			t.Fatal(err)
		}
		if len(changes.SubRows) != 3 || changes.SubRows[0].Change.Kind != rules.SubRowInsert || *changes.SubRows[0].Row.Key != "t1" || changes.Deadlines[0].Kind != rules.DeadlineArm || changes.Deadlines[1].Name != "idle" {
			t.Errorf("a change set = %+v", changes)
		}
		if got := marshal(t, changes); got != text {
			t.Errorf("a change set writes\n%s\nwant\n%s", got, text)
		}
		var snooze rules.DeadlineChange
		if err := json.Unmarshal([]byte(`{"kind":"snooze","name":"x"}`), &snooze); err != nil || string(snooze.Raw) != `{"kind":"snooze","name":"x"}` || marshal(t, snooze) != `{"kind":"snooze","name":"x"}` {
			t.Errorf("a deadline change of another kind = %+v, %v", snooze, err)
		}
		var touch rules.SubRowChange
		if err := json.Unmarshal([]byte(`{"kind":"touch"}`), &touch); err != nil || marshal(t, touch) != `{"kind":"touch"}` {
			t.Errorf("a sub-table row change of another kind = %+v, %v", touch, err)
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
		var gone rules.RowWrite
		if err := json.Unmarshal([]byte(`{"kind":"gone","type":"Task","key":"t1"}`), &gone); err != nil || gone.Kind != rules.RowGone || gone.Type != "Task" || *gone.Key != "t1" {
			t.Errorf("a gone row = %+v, %v", gone, err)
		}
		if got := marshal(t, gone); got != `{"kind":"gone","type":"Task","key":"t1"}` {
			t.Errorf("a gone row writes %s", got)
		}
	})
}

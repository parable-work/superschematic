package rules_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

// The binding over the static archive: each operation of the C ABI, as
// rules-wasm's own tests run them, and the native core from many
// goroutines at once.

func order(config string) string {
	return `{"name": "Order", "uses": [{"behavior": "Workflow", "config": ` + config + `, "anchor": {"field": "status"}}]}`
}

const flow = `{"states": ["draft", "paid"], "transitions": [{"from": "draft", "to": "paid"}]}`

// transitionInput is rules-wasm's own test's run: a transition of Order
// o1, which first asks for the row.
var transitionInput = `{"spec": ` + order(flow) + `,
	"command": {"caller": {"subject": "sam"}, "now": 1000, "request": "r1",
		"action": {"kind": "operation", "key": "o1", "useId": "status", "operation": "transition", "params": {"to": "paid"}}}}`

func TestNativeRunReturnsTheCoresStep(t *testing.T) {
	out, err := rules.RunJSON([]byte(transitionInput))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, out, `{"read": [{"kind": "row", "type": "Order", "key": "o1"}]}`)

	var spec rules.TypeSpec
	if err := json.Unmarshal([]byte(order(flow)), &spec); err != nil {
		t.Fatal(err)
	}
	step, err := rules.Native().Run(spec, rules.Command{
		Caller:  rules.Caller{Subject: "sam"},
		Now:     1000,
		Request: "r1",
		Action:  rules.Action{Kind: rules.ActionOperation, Key: "o1", UseID: "status", Operation: "transition", Params: json.RawMessage(`{"to": "paid"}`)},
	}, rules.Answers{})
	if err != nil {
		t.Fatal(err)
	}
	if len(step.Read) != 1 || step.Read[0] != (rules.ReadRequest{Kind: rules.ReadKindRow, Type: "Order", Key: "o1"}) || step.Lock != nil || step.Done != nil {
		t.Fatalf("step = %+v, want one read of Order o1", step)
	}
}

func TestNativeDeclarationsAreTheFiles(t *testing.T) {
	out, err := rules.DeclarationsJSON()
	if err != nil {
		t.Fatal(err)
	}
	var files []map[string]any
	if err := json.Unmarshal(out, &files); err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 || files[0]["name"] != "Workflow" {
		t.Fatalf("declarations = %s, want Workflow first", out)
	}
	declarations, err := rules.Native().Declarations()
	if err != nil {
		t.Fatal(err)
	}
	workflow := declarations[0]
	if workflow.Name != "Workflow" || workflow.Value != "status" {
		t.Fatalf("Workflow's declaration = %+v, want its value field status", workflow)
	}
	var enteredAt *rules.DeclaredField
	for i := range workflow.Fields {
		if workflow.Fields[i].Name == "enteredAt" {
			enteredAt = &workflow.Fields[i]
		}
	}
	if enteredAt == nil || enteredAt.Type != "Temporal.DateTime" {
		t.Fatalf("Workflow's enteredAt = %+v, want a Temporal.DateTime", enteredAt)
	}
	var transition *rules.DeclaredOperation
	for i := range workflow.Operations {
		if workflow.Operations[i].Name == "transition" {
			transition = &workflow.Operations[i]
		}
	}
	if transition == nil || !transition.Writes || len(transition.ParamsSchema) == 0 {
		t.Fatalf("Workflow's transition = %+v, want an operation that writes, with its parameters' schema", transition)
	}
}

func TestNativeCheckRefusesAConfigTheCoreRefuses(t *testing.T) {
	out, err := rules.CheckJSON([]byte(`{"specs": [` + order(flow) + `, ` + order(`{"states": []}`) + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil || len(raw) != 2 || string(raw[0]) != "null" {
		t.Fatalf("check = %s, want null then a refusal", out)
	}

	var specs []rules.TypeSpec
	if err := json.Unmarshal([]byte(`[`+order(flow)+`, `+order(`{"states": []}`)+`]`), &specs); err != nil {
		t.Fatal(err)
	}
	refusals, err := rules.Native().Check(specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 2 || refusals[0] != nil || refusals[1] == nil {
		t.Fatalf("refusals = %+v, want nil then one", refusals)
	}
	if refusals[1].Code != rules.CodeInvalidSchema || refusals[1].Status != 422 || refusals[1].Class != "SchemaDocumentError" {
		t.Fatalf("refusal = %+v, want invalid_schema, 422, SchemaDocumentError", refusals[1])
	}
	if empty, err := rules.Native().Check(nil); err != nil || len(empty) != 0 {
		t.Fatalf("Check(nil) = %v, %v, want no refusals", empty, err)
	}
}

func TestAnInputTheCoreCannotReadIsAnError(t *testing.T) {
	_, err := rules.RunJSON([]byte(`{not json`))
	var refused *rules.Error
	if !errors.As(err, &refused) || refused.Code != "invalid_json" {
		t.Fatalf("err = %v, want an Error of invalid_json", err)
	}
	if err.Error() != "rules: invalid_json: "+refused.Message {
		t.Fatalf("err = %q, want rules: <code>: <message>", err)
	}
	_, err = rules.CheckJSON([]byte(`{"specs": 1}`))
	if !errors.As(err, &refused) || refused.Code != "invalid_input" {
		t.Fatalf("err = %v, want an Error of invalid_input", err)
	}
	_, err = rules.RunJSON(nil)
	if !errors.As(err, &refused) || refused.Code != "invalid_json" {
		t.Fatalf("err = %v, want an empty input refused as invalid_json", err)
	}
}

func TestNativeIsSafeAcrossGoroutines(t *testing.T) {
	want, err := rules.RunJSON([]byte(transitionInput))
	if err != nil {
		t.Fatal(err)
	}
	var specs []rules.TypeSpec
	if err := json.Unmarshal([]byte(`[`+order(flow)+`, `+order(`{"states": []}`)+`]`), &specs); err != nil {
		t.Fatal(err)
	}
	const goroutines, runs = 64, 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan string, goroutines*runs)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			input := []byte(transitionInput)
			for range runs {
				out, err := rules.RunJSON(input)
				if err != nil {
					failures <- err.Error()
					continue
				}
				if string(out) != string(want) {
					failures <- string(out)
				}
				if _, err := rules.Native().Declarations(); err != nil {
					failures <- err.Error()
				}
				refusals, err := rules.Native().Check(specs)
				if err != nil {
					failures <- err.Error()
				} else if len(refusals) != 2 || refusals[0] != nil || refusals[1] == nil || refusals[1].Code != rules.CodeInvalidSchema {
					failures <- fmt.Sprintf("check = %+v", refusals)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Errorf("an answer differed: %s", failure)
	}
}

// assertJSON fails unless got and want are the same JSON, member order
// aside.
func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want %s: %v", want, err)
	}
	gt, _ := json.Marshal(g)
	wt, _ := json.Marshal(w)
	if string(gt) != string(wt) {
		t.Fatalf("got %s, want %s", gt, wt)
	}
}

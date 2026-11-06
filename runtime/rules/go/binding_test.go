package rules_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
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
	if len(step.Read) != 1 || !reflect.DeepEqual(step.Read[0], rules.ReadRequest{Kind: rules.ReadKindRow, Type: "Order", Key: "o1"}) || step.Lock != nil || step.Done != nil {
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
	// The core registers its behaviors in one order (D104), the four only
	// it implements last; Reconcile's value field is optional, its column
	// NULL before the behavior sets it, Schedules and Pool each declare the
	// one event they emit, run and recheck, whose payload's schema the
	// declaration holds, Pool's pick and Lease's expireHolder run on the
	// table as a whole, and Budget implements the contract Capacity, which
	// Pool asks.
	var names []string
	byName := map[string]rules.Declaration{}
	for _, declaration := range declarations {
		names = append(names, declaration.Name)
		byName[declaration.Name] = declaration
	}
	if want := []string{"Workflow", "Reactions", "Constants", "StateRules", "Approvals", "Timers", "Lease", "Presence", "Budget", "Retries", "Seats", "Reconcile", "Schedules", "Pool"}; !slices.Equal(names, want) {
		t.Fatalf("the core declares %q, want %q", names, want)
	}
	reconcile := byName["Reconcile"]
	if reconcile.Value != "observed" || len(reconcile.Fields) == 0 || reconcile.Fields[0].Name != "observed" || !reconcile.Fields[0].Optional {
		t.Fatalf("Reconcile's declaration = %+v, want its value field observed, optional", reconcile)
	}
	for name, kind := range map[string]string{"Schedules": "run", "Pool": "recheck"} {
		events := byName[name].Events
		if len(events) != 1 || events[0].Kind != kind || len(events[0].PayloadSchema) == 0 {
			t.Fatalf("%s's events = %+v, want the one event %s, with its payload's schema", name, events, kind)
		}
	}
	for _, declaration := range declarations {
		if len(declaration.Events) > 0 && declaration.Name != "Schedules" && declaration.Name != "Pool" {
			t.Fatalf("%s declares events %+v, and only Schedules and Pool emit any", declaration.Name, declaration.Events)
		}
	}
	scopes := map[string]string{}
	for _, declaration := range declarations {
		for _, operation := range declaration.Operations {
			if operation.Scope == "schema" {
				scopes[declaration.Name+"."+operation.Name] = operation.Scope
			}
		}
	}
	if want := map[string]string{"Lease.expireHolder": "schema", "Pool.pick": "schema"}; !maps.Equal(scopes, want) {
		t.Fatalf("the operations of a table as a whole = %v, want %v", scopes, want)
	}
	if got := byName["Budget"].Contracts; len(got) != 1 || got[0].Name != "Capacity" || got[0].Role != "implements" {
		t.Fatalf("Budget's contracts = %+v, want it to implement Capacity", got)
	}
	if got := byName["Pool"].Contracts; len(got) != 1 || got[0].Name != "Capacity" || got[0].Role != "asks" {
		t.Fatalf("Pool's contracts = %+v, want it to ask Capacity", got)
	}
	for _, field := range declarations[0].Fields {
		if field.Optional {
			t.Fatalf("Workflow's field %s is optional, and the behavior sets each when a row is created", field.Name)
		}
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

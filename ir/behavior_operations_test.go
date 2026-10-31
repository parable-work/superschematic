package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestBehaviorOperationsMarshal: a @behaviorOperations set and an
// operation the loader added for it write their keys in JSON and YAML and
// read back the same (D103); a set or an operation without them writes
// none, so a schema without the decorator is the same IR bytes.
func TestBehaviorOperationsMarshal(t *testing.T) {
	set := &OperationSet{
		Name:               "OrderActions",
		Operations:         []*FieldDef{},
		BehaviorOperations: &BehaviorOperationsConfig{Type: "Order"},
		Auth:               true,
		Permissions:        []string{"orders.write"},
	}
	op := &FieldDef{
		Name:       "statusTransition",
		TypeRef:    TypeRef{Name: "OrderStatusTransitionResult"},
		HTTPMethod: "POST",
		Origin:     OriginBehavior,
		BehaviorOperation: &BehaviorOperation{
			Database: "orders-db", Type: "Order", Key: "id", Use: "status", Behavior: "Workflow",
			Operation: "transition", Writes: true, Permissions: []string{"orders.ship"},
		},
	}

	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"behaviorOperations":{"type":"Order"}`, `"auth":true`, `"permissions":["orders.write"]`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("set JSON %s lacks %s", data, want)
		}
	}
	if strings.Contains(string(data), `"public"`) {
		t.Errorf("set JSON %s writes public false", data)
	}
	var setBack OperationSet
	if err := json.Unmarshal(data, &setBack); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&setBack, set) || !setBack.IsBehaviorOperations() {
		t.Errorf("set read back = %+v, want %+v", setBack, set)
	}

	data, err = json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"behaviorOperation":{"database":"orders-db","type":"Order","key":"id","use":"status","behavior":"Workflow","operation":"transition","writes":true,"permissions":["orders.ship"]}`; !strings.Contains(string(data), want) {
		t.Errorf("operation JSON %s lacks %s", data, want)
	}
	var opBack FieldDef
	if err := json.Unmarshal(data, &opBack); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opBack.BehaviorOperation, op.BehaviorOperation) {
		t.Errorf("operation read back = %+v, want %+v", opBack.BehaviorOperation, op.BehaviorOperation)
	}

	text, err := yaml.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	var yamlBack OperationSet
	if err := yaml.Unmarshal(text, &yamlBack); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&yamlBack, set) {
		t.Errorf("set read back from YAML = %+v, want %+v\n%s", yamlBack, set, text)
	}

	plain, err := json.Marshal(&OperationSet{Name: "Orders", Operations: []*FieldDef{{Name: "list", HTTPMethod: "GET"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"behaviorOperations", "behaviorOperation", "auth", "permissions", "public"} {
		if strings.Contains(string(plain), `"`+key+`"`) {
			t.Errorf("a plain set writes %s: %s", key, plain)
		}
	}
	var none *OperationSet
	if none.IsBehaviorOperations() || (&OperationSet{}).IsBehaviorOperations() {
		t.Error("a nil or plain set is a @behaviorOperations set")
	}
}

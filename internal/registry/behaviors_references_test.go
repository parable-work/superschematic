package registry

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// A ConfigPath reaches a config's values by member, list item and object
// member, the last in name order, and names each by where it is.
func TestConfigValues(t *testing.T) {
	var config any
	if err := json.Unmarshal([]byte(`{
		"transitions": [{"from": "a", "to": "b", "permission": "orders.ship"}, {"from": "b", "to": "c"}],
		"gates": {"review": {"permission": "tasks.review"}, "approve": {"permission": "tasks.approve"}},
		"overridePermission": "orders.override"
	}`), &config); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want []ConfigValue
	}{
		{"overridePermission", []ConfigValue{{At: "overridePermission", Value: "orders.override"}}},
		{"transitions[].permission", []ConfigValue{{At: "transitions.permission", Value: "orders.ship"}}},
		{"gates.*.permission", []ConfigValue{{At: "gates.approve.permission", Value: "tasks.approve"}, {At: "gates.review.permission", Value: "tasks.review"}}},
		{"absent", nil},
	} {
		got := ConfigValues(config, tc.path)
		if !slices.EqualFunc(got, tc.want, func(a, b ConfigValue) bool { return a.At == b.At && a.Value == b.Value }) {
			t.Errorf("ConfigValues(%s) = %+v, want %+v", tc.path, got, tc.want)
		}
	}
}

// An operation that states its permissions lists the strings its paths
// reach in the use's config, sorted and each once (D103's step 5).
func TestOperationPermissions(t *testing.T) {
	op := BehaviorOperation{Name: "transition", Permissions: []string{"transitions[].permission", "overridePermission"}}
	config := json.RawMessage(`{"transitions": [{"permission": "orders.ship"}, {"permission": "orders.ship"}, {}], "overridePermission": "orders.admin"}`)
	if got, want := OperationPermissions(op, config), []string{"orders.admin", "orders.ship"}; !slices.Equal(got, want) {
		t.Errorf("OperationPermissions = %v, want %v", got, want)
	}
	if got := OperationPermissions(BehaviorOperation{Name: "list"}, config); got != nil {
		t.Errorf("an operation that states none checks %v", got)
	}
}

// The core's declarations that state which operation checks which
// permission name paths their config schemas have, and Workflow's
// transition checks its transitions' permissions and its
// overridePermission.
func TestCoreOperationsStateTheirPermissions(t *testing.T) {
	reg := New(naming.Default())
	workflow, ok := reg.Behavior("Workflow")
	if !ok || !workflow.StatesPermissions() {
		t.Fatal("Workflow states no operation's permissions")
	}
	if got, want := workflow.Operations[0].Permissions, []string{"transitions[].permission", "overridePermission"}; !slices.Equal(got, want) {
		t.Errorf("transition checks %v, want %v", got, want)
	}
	timers, _ := reg.Behavior("Timers")
	for _, op := range timers.Operations {
		checks := len(op.Permissions) > 0
		if want := op.Name != "fireTimer"; checks != want {
			t.Errorf("Timers' %s checks %v, want a permission: %v", op.Name, op.Permissions, want)
		}
	}
}

// A declaration's references and its operations' permissions name members
// its config schema has, each reference of a kind the build reads.
func TestBehaviorPathsAreHeldToTheConfigSchema(t *testing.T) {
	declaration := func(extra string) json.RawMessage {
		return json.RawMessage(`{"name": "test.Thing", "configSchema": {"type": "object", "properties": {
			"status": {"type": "string"},
			"gates": {"type": "object", "additionalProperties": {"type": "object", "properties": {"permission": {"type": "string"}}}},
			"rules": {"type": "array", "items": {"type": "object", "properties": {"when": {"oneOf": [{"type": "object", "properties": {"enters": {"type": "string"}}}]}}}}
		}}` + extra + `}`)
	}
	for _, tc := range []struct {
		name, extra, want string
	}{
		{"paths it has", `, "references": [{"path": "status", "kind": "workflow"}, {"path": "rules[].when.enters", "kind": "state"}], "operations": [{"name": "sign", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": {}, "permissions": ["gates.*.permission"]}]`, ""},
		{"a member it lacks", `, "references": [{"path": "phase", "kind": "workflow"}]`, "reference phase: the config schema has no member phase"},
		{"a kind the build does not read", `, "references": [{"path": "status", "kind": "table"}]`, `has kind "table"`},
		{"a type on no field", `, "references": [{"path": "status", "kind": "state", "type": "Temporal.DateTime"}]`, "which only a field's does"},
		{"a malformed segment", `, "operations": [{"name": "sign", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": {}, "permissions": ["gates.**"]}]`, `segment "**"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := New(naming.Default())
			err := reg.RegisterBehavior(BehaviorSpec{Extension: "test", Declaration: declaration(tc.extra)})
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("RegisterBehavior: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("RegisterBehavior: %v, want an error that says %q", err, tc.want)
			}
		})
	}
}

package schemafile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/registry"
)

// The schema-file JSON Schema holds a type's display to the registered
// @display spec's Args (D48): its $defs replace the reflected ones, and a
// reflected struct that names other members than the registered schema
// fails the generation instead of drifting from it.
func TestDisplayDefsAreTheRegisteredArgs(t *testing.T) {
	reg := core()
	spec, ok := reg.Decorator("display", registry.TargetType)
	if !ok {
		t.Fatal("no @display")
	}
	var args struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(spec.Args, &args); err != nil {
		t.Fatal(err)
	}
	data, err := Definition()
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TypeDisplay", "DisplayState"} {
		registered := args.Defs[name].(map[string]any)
		emitted := root.Defs[name]
		for _, key := range []string{"minProperties", "additionalProperties", "description"} {
			if emitted[key] == nil || !jsonEqual(t, emitted[key], registered[key]) {
				t.Errorf("$defs/%s %s = %v, want %v", name, key, emitted[key], registered[key])
			}
		}
	}

	defs := map[string]any{
		"TypeDisplay":  map[string]any{"properties": map[string]any{"noun": map[string]any{}}},
		"DisplayState": map[string]any{"properties": map[string]any{}},
	}
	err = replaceDisplayDefs(defs, reg)
	if err == nil || !strings.HasPrefix(err.Error(), "@display Args $defs/TypeDisplay has the properties [createLabel noun plural states summaryFields titleField transitions], but ir.TypeDisplay has [noun]") {
		t.Fatalf("replaceDisplayDefs = %v", err)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(x) == string(y)
}

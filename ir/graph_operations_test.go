package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestGraphOperationsMarshal: a @graphOperations set and an operation the
// loader added for it write their keys in JSON and YAML and read back the
// same (D138); a set or an operation without them writes none, so a schema
// without the decorator is the same IR bytes.
func TestGraphOperationsMarshal(t *testing.T) {
	set := &OperationSet{
		Name:       "RecipeOperations",
		Operations: []*FieldDef{},
		GraphOperations: &GraphOperationsConfig{
			Type:               "Recipe",
			Operations:         []string{"branch", "save", "release"},
			Permissions:        map[string][]string{"release": {"recipes.release"}},
			By:                 []string{"createdBy"},
			OverridePermission: "recipes.admin",
		},
		Permissions: []string{"recipes.write"},
	}
	op := &FieldDef{
		Name:       "save",
		TypeRef:    TypeRef{Name: "RecipeGraphSaveResult"},
		HTTPMethod: "PUT",
		Origin:     OriginGraphOperations,
		GraphOperation: &GraphOperation{
			Database: "recipes-db", Graph: "Recipe", Type: "Recipe", Key: "id", Operation: "save",
			By: []string{"createdBy"}, OverridePermission: "recipes.admin",
		},
	}

	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"graphOperations":{"type":"Recipe","operations":["branch","save","release"],"permissions":{"release":["recipes.release"]},"by":["createdBy"],"overridePermission":"recipes.admin"}`; !strings.Contains(string(data), want) {
		t.Errorf("set JSON %s lacks %s", data, want)
	}
	var setBack OperationSet
	if err := json.Unmarshal(data, &setBack); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&setBack, set) || !setBack.IsGraphOperations() || !setBack.TakesSetClause() {
		t.Errorf("set read back = %+v, want %+v", setBack, set)
	}

	data, err = json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"graphOperation":{"database":"recipes-db","graph":"Recipe","type":"Recipe","key":"id","operation":"save","by":["createdBy"],"overridePermission":"recipes.admin"}`; !strings.Contains(string(data), want) {
		t.Errorf("operation JSON %s lacks %s", data, want)
	}
	var opBack FieldDef
	if err := json.Unmarshal(data, &opBack); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opBack.GraphOperation, op.GraphOperation) {
		t.Errorf("operation read back = %+v, want %+v", opBack.GraphOperation, op.GraphOperation)
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

	plain, err := json.Marshal(&OperationSet{Name: "Recipes", Operations: []*FieldDef{{Name: "list", HTTPMethod: "GET"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"graphOperations", "graphOperation"} {
		if strings.Contains(string(plain), `"`+key+`"`) {
			t.Errorf("a plain set writes %s: %s", key, plain)
		}
	}
	var none *OperationSet
	if none.IsGraphOperations() || (&OperationSet{}).IsGraphOperations() || (&OperationSet{}).TakesSetClause() {
		t.Error("a nil or plain set is a @graphOperations set")
	}
}

// TestGraphOperationsServes: a set that lists no operation serves every
// one, and one that lists some serves those.
func TestGraphOperationsServes(t *testing.T) {
	all := &GraphOperationsConfig{Type: "Recipe"}
	for _, name := range GraphOperationNames {
		if !all.Serves(name) {
			t.Errorf("a set that narrows nothing does not serve %s", name)
		}
	}
	if all.Serves("publish") {
		t.Error("a set serves publish, which is no operation of a graph")
	}
	some := &GraphOperationsConfig{Type: "Recipe", Operations: []string{"released"}}
	if !some.Serves("released") || some.Serves("save") {
		t.Errorf("a set narrowed to released serves %v", some.Operations)
	}
	for _, owned := range GraphOperationsOwned {
		if !all.Serves(owned) {
			t.Errorf("%s is under the row rule and is no operation", owned)
		}
	}
}

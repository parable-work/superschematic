package schemafile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The schema-file JSON Schema holds an operation's page to the registered
// @paginated spec's Args (D60): its $defs/OperationPage replaces the
// reflected one, and a reflected struct that names other members than the
// registered schema fails the generation instead of drifting from it.
func TestPageDefsAreTheRegisteredArgs(t *testing.T) {
	reg := core()
	spec, ok := reg.Decorator("paginated", registry.TargetOperation)
	if !ok {
		t.Fatal("no @paginated")
	}
	var args struct {
		Defs map[string]map[string]any `json:"$defs"`
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
	emitted, registered := root.Defs["OperationPage"], args.Defs["OperationPage"]
	for _, key := range []string{"additionalProperties", "description", "type"} {
		if emitted[key] == nil || !jsonEqual(t, emitted[key], registered[key]) {
			t.Errorf("$defs/OperationPage %s = %v, want %v", key, emitted[key], registered[key])
		}
	}
	for _, member := range []string{"defaultLimit", "maxLimit"} {
		prop := emitted["properties"].(map[string]any)[member].(map[string]any)
		if prop["minimum"] != 1.0 || prop["maximum"] != 1000.0 || prop["type"] != "integer" {
			t.Errorf("$defs/OperationPage/properties/%s = %v", member, prop)
		}
	}
	if prop := emitted["properties"].(map[string]any)["defaultSort"].(map[string]any); prop["type"] != "string" || prop["pattern"] != ir.PageSortPattern {
		t.Errorf("$defs/OperationPage/properties/defaultSort = %v, want a string of the pattern %s", prop, ir.PageSortPattern)
	}

	defs := map[string]any{"OperationPage": map[string]any{"properties": map[string]any{"limit": map[string]any{}}}}
	err = replacePageDefs(defs, reg)
	if err == nil || !strings.HasPrefix(err.Error(), "@paginated Args $defs/OperationPage has the properties [defaultLimit defaultSort maxLimit], but ir.OperationPage has [limit]") {
		t.Fatalf("replacePageDefs = %v", err)
	}
}

// The merge fills what a page leaves unsaid, so a data form reads to the
// IR the TypeScript form does: @paginated() is page: {}, and
// @paginated({ maxLimit: 10 }) pages ten at a time.
func TestMergeFillsPageDefaults(t *testing.T) {
	doc, err := Decode([]byte(`{"kind": "API", "name": "catalog", "operationSets": [{"name": "Catalog", "operations": [
		{"name": "listItems", "typeRef": {"name": "String", "isArray": true}, "httpMethod": "GET", "page": {}},
		{"name": "listTags", "typeRef": {"name": "String", "isArray": true}, "httpMethod": "GET", "page": {"maxLimit": 10}},
		{"name": "listSkus", "typeRef": {"name": "String", "isArray": true}, "httpMethod": "GET", "page": {"defaultLimit": 2, "maxLimit": 5}},
		{"name": "getItem", "typeRef": {"name": "String"}, "httpMethod": "GET"}]}]}`), "catalog.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema := ir.NewSchema("catalog", ir.SchemaKindAPI)
	if errs := Merge(doc, schema, "catalog.schema.json"); len(errs) > 0 {
		t.Fatal(errs)
	}
	want := map[string]*ir.OperationPage{
		"listItems": {DefaultLimit: 20, MaxLimit: 100},
		"listTags":  {DefaultLimit: 10, MaxLimit: 10},
		"listSkus":  {DefaultLimit: 2, MaxLimit: 5},
		"getItem":   nil,
	}
	for _, op := range schema.OperationSets[0].Operations {
		got, w := op.Page, want[op.Name]
		if (got == nil) != (w == nil) || (got != nil && *got != *w) {
			t.Errorf("%s page = %+v, want %+v", op.Name, got, w)
		}
	}
}

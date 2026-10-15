package toolsutil

import (
	"encoding/json"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// A paged endpoint's tool (D60) takes cursor and limit beside its own
// arguments, both optional and bounded as the OpenAPI document bounds
// them; it returns the page, {items, next}; and its description says how
// to ask for the next page. An endpoint that does not page is unchanged.
func TestPagedToolParametersReturnsAndDescription(t *testing.T) {
	page := &apigen.PageInfo{DefaultLimit: 2, MaxLimit: 5, Operation: "Catalog.listItems"}
	query := []ToolQueryArg{{Name: "inStock", Type: "boolean"}}
	scalars := map[string]apigen.ScalarJSONSchemaInfo{"boolean": {Type: "boolean"}}

	unpaged := BuildParametersSchema(nil, query, false, "", nil, nil, nil, nil, false, scalars, byName, apigen.DefaultToolKeys(), nil)
	paged := BuildParametersSchema(nil, query, false, "", nil, nil, nil, nil, false, scalars, byName, apigen.DefaultToolKeys(), page)
	if len(unpaged.Properties) != 1 || len(paged.Properties) != 3 || len(paged.Required) != 0 {
		t.Fatalf("parameters: unpaged %v, paged %v (required %v)", unpaged.Properties, paged.Properties, paged.Required)
	}
	encoded, err := json.Marshal(paged.Properties)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cursor":{"type":"string","description":"The ` + "`next`" + ` of the previous page; absent for the first page.","pattern":"^[A-Za-z0-9_-]+$","minLength":1,"maxLength":1024},` +
		`"inStock":{"type":"boolean"},` +
		`"limit":{"type":"integer","description":"The most items the page holds: 1 to 5, 2 when absent.","minimum":1,"maximum":5}}`
	if string(encoded) != want {
		t.Fatalf("paged properties =\n%s\nwant\n%s", encoded, want)
	}

	returns, err := json.Marshal(BuildEndpointReturnSchema("ItemView", 1, page, scalars))
	if err != nil {
		t.Fatal(err)
	}
	wantReturns := `{"type":"object","description":"A page of ItemView","properties":{` +
		`"items":{"type":"array","description":"Array of ItemView","items":{"type":"object","description":"ItemView object"}},` +
		`"next":{"description":"The cursor of the next page, to send as cursor; null after the last page.","type":["string","null"]}},` +
		`"required":["items","next"]}`
	if string(returns) != wantReturns {
		t.Fatalf("paged returns =\n%s\nwant\n%s", returns, wantReturns)
	}
	if got, want := BuildEndpointReturnSchema("ItemView", 1, nil, scalars), BuildReturnSchemaAtDepth("ItemView", 1, scalars); !jsonEqual(t, got, want) {
		t.Fatalf("unpaged returns = %+v, want %+v", got, want)
	}

	if got := PagedDescription("The items.", page); got != "The items. "+PageSentence {
		t.Fatalf("paged description = %q", got)
	}
	if got := PagedDescription("The items.", nil); got != "The items." {
		t.Fatalf("unpaged description = %q", got)
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

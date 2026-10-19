package toolsutil_test

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestToolSchemasGoldenRefs pins the tool schemas of fixture-refs-api: a
// value that refers to an item gives its reference under
// x-superschematic-reference, after its scalar key, with the type's name
// as target and the options operation's tool name (D61): a field of the
// objects in a list (basket lines' itemId), a field with every member
// said (giftSku), a query list (ids, the lookup, on the list) and a path
// argument (getItem's itemId). The search argument, q, says
// x-superschematic-search (D62). Regenerate with:
// go test ./internal/generator/toolsutil -run TestToolSchemasGoldenRefs -update
func TestToolSchemasGoldenRefs(t *testing.T) {
	const fixture = "fixture-refs-api"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: fixture,
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	tools := buildToolSchemas(t, output)
	byName := map[string]toolSchema{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	list, get, place := byName["catalog.listItems"], byName["catalog.getItem"], byName["catalog.placeBasket"]
	byID := &ir.ToolSchemaReference{Target: "ItemView", Options: "catalog.listItems", Value: "id", Label: "name", Lookup: "ids"}
	bySku := &ir.ToolSchemaReference{Target: "ItemView", Options: "catalog.listItems", Value: "sku", Label: "name"}
	for _, check := range []struct {
		where string
		got   *ir.ToolSchemaReference
		want  *ir.ToolSchemaReference
	}{
		{"listItems(ids)", list.Parameters.Properties["ids"].Reference, byID},
		{"getItem(itemId)", get.Parameters.Properties["itemId"].Reference, byID},
		{"placeBasket giftSku", place.Parameters.Properties["giftSku"].Reference, bySku},
		{"placeBasket lines[].itemId", place.Parameters.Properties["lines"].Items.Properties["itemId"].Reference, byID},
		{"placeBasket lines[].quantity", place.Parameters.Properties["lines"].Items.Properties["quantity"].Reference, nil},
		{"listItems(ids) items", list.Parameters.Properties["ids"].Items.Reference, nil},
	} {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Errorf("%s refers as %+v, want %+v", check.where, check.got, check.want)
		}
	}
	if !list.Parameters.Properties["q"].Search || list.Parameters.Properties["inStock"].Search {
		t.Error("listItems: q is not the search, or inStock is")
	}
	compareToolSchemasGolden(t, fixture, tools)
}

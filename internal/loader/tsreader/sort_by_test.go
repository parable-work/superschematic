package tsreader

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// sortByService is an API whose Catalog lists Item, with the method and
// the declarations given.
func sortByService(t *testing.T, decls, method string) string {
	t.Helper()
	return decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { Identity } from "superscalar";
import { HttpMethod, paginated, rest, SortBy } from "@superschematic/api";
export abstract class Item {
  id: Identity.UUID;
  name: string;
  sku: Identity.Slug;
}
export abstract class Other {
  id: Identity.UUID;
}
` + decls + `
export class Catalog {
` + method + `
}
`})
}

// TestSortByLoads: SortBy<Item, Fields> is an optional query argument of
// text whose sort lists Fields in order, with the tuple readonly or not,
// and @paginated's defaultSort is the page's (D60, amended).
func TestSortByLoads(t *testing.T) {
	dir := sortByService(t, "", `  @rest(HttpMethod.GET, "items")
  @paginated({ defaultLimit: 2, maxLimit: 5, defaultSort: "-sku" })
  listItems(sort: SortBy<Item, ["sku", "name"]>): Item[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "items/by")
  @paginated({ defaultSort: "id" })
  listBy(order: SortBy<Item, readonly ["id"]>): Item[] {
    throw new Error("schema declaration only");
  }`)
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	ops := schema.OperationSets[0].Operations
	if got, want := ops[0].Arguments, []*ir.ArgumentDef{{Name: "sort", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Sort: []string{"sku", "name"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("listItems' arguments = %+v, want %+v", got[0], want[0])
	}
	if got, want := *ops[0].Page, (ir.OperationPage{DefaultLimit: 2, MaxLimit: 5, DefaultSort: "-sku"}); got != want {
		t.Errorf("listItems' page = %+v, want %+v", got, want)
	}
	if got := ops[1].Arguments[0]; got.Name != "order" || !reflect.DeepEqual(got.Sort, []string{"id"}) || !got.IsQuery || got.Required {
		t.Errorf("listBy's argument = %+v", got)
	}
	if got, want := *ops[1].Page, (ir.OperationPage{DefaultLimit: 20, MaxLimit: 100, DefaultSort: "id"}); got != want {
		t.Errorf("listBy's page = %+v, want %+v", got, want)
	}
}

// TestSortByRefusals: each of the reader's SortBy errors, which only the
// TypeScript form can make, and the item it sorts against the one the
// operation lists, which only this form names.
func TestSortByRefusals(t *testing.T) {
	list := func(sort string) string {
		return `  @rest(HttpMethod.GET, "items")
  @paginated({ defaultSort: "id" })
  listItems(sort: ` + sort + `): Item[] {
    throw new Error("schema declaration only");
  }`
	}
	usage := `SortBy<Item, Fields> needs the item type and a tuple of its field names: SortBy<OrderView, ["placedAt"]>`
	for _, tc := range []struct {
		name, decls, method, want string
	}{
		{"an item that is no object type", "", list(`SortBy<string, []>`), usage},
		{"an item that is a list", "", list(`SortBy<Item[], []>`), usage},
		{"fields that are no tuple", "", list(`SortBy<Item, (keyof Item & string)[]>`), usage},
		{"a field that is no string literal", "", list(`SortBy<Item, [keyof Item & string]>`), "SortBy's fields are string literals, each a field of Item"},
		{"a named tuple member", "", list(`SortBy<Item, [first: "id"]>`), "SortBy's fields are string literals, each a field of Item"},
		{"a field of a class", "export abstract class Line {\n  sort: SortBy<Item, [\"id\"]>;\n}", "", "SortBy marks an operation's argument; a field is no sort"},
		{"a field of an object type", "export type Line = {\n  sort: SortBy<Item, [\"id\"]>;\n};", "", "SortBy marks an operation's argument; a field is no sort"},
		{"a result", "", `  @rest(HttpMethod.GET, "items")
  listItems(): SortBy<Item, ["id"]> {
    throw new Error("schema declaration only");
  }`, "SortBy marks an operation's argument, not its result"},
		{"another item than the operation lists", "", list(`SortBy<Other, ["id"]>`), "Catalog.listItems: SortBy<Other, …> sorts Other, and listItems lists Item"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := LoadService(sortByService(t, tc.decls, tc.method))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want %q", err, tc.want)
			}
		})
	}
}

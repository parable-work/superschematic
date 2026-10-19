package loader

import (
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestPageVerifyFindingsReachTheTypeScriptAuthor: the loader holds a
// @paginated operation of a TypeScript service to the page's rules (D60):
// a GET returning a list, no argument named as a page parameter, and no
// operation named as its SDK iterator; and a SortBy to a page, an item
// with an id, and a defaultSort to a SortBy (D60, amended).
func TestPageVerifyFindingsReachTheTypeScriptAuthor(t *testing.T) {
	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-paged-api"))
	if err == nil {
		t.Fatal("expected verification errors")
	}
	msg := err.Error()
	for _, want := range []string{
		"Catalog.searchItems: @paginated needs a GET operation, not POST",
		"Catalog.listItems: @paginated adds the query parameters cursor and limit, and an implementation takes them as page; the operation declares an argument named limit",
		"Catalog.listItemsAll: an SDK names listItemsAll the paged Catalog.listItems's iterator; rename one of them",
		"Catalog.firstItem: @paginated needs a list result, not Item",
		"Catalog.sortedItems: sort argument sort orders a page, and the operation has no @paginated",
		"Catalog.sortedItems: a sort breaks ties by id, and Item declares no id",
		"Catalog.orderedItems: @paginated defaultSort name orders a sort argument, and the operation declares none (SortBy)",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
}

// TestPageLimitsAreCheckedInTheDataForms: a page's default above its
// maximum, which the JSON Schema cannot say, is refused in a data form as
// @paginated's Apply refuses it in TypeScript.
func TestPageLimitsAreCheckedInTheDataForms(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.yaml": "name: catalog\nkind: API\noutputs: {}\n",
		"src/catalog.schema.yaml": `name: catalog
kind: API
operationSets:
  - name: Catalog
    operations:
      - name: listTags
        typeRef: { name: string, isArray: true }
        required: true
        page: { defaultLimit: 50, maxLimit: 10 }
        httpMethod: GET
        restPath: tags
`,
	})
	_, err := LoadService(dir)
	if err == nil || !strings.Contains(err.Error(), "Catalog.listTags: @paginated defaultLimit 50 is more than its maxLimit 10") {
		t.Fatalf("LoadService = %v", err)
	}
}

// TestSortsAreCheckedInTheDataForms: a data form's sort argument meets the
// rules the TypeScript form's SortBy does (D60, amended): its page, its
// item's fields and its page's defaultSort.
func TestSortsAreCheckedInTheDataForms(t *testing.T) {
	schema := func(arguments, page string) string {
		return `name: catalog
kind: API
types:
  ItemView:
    name: ItemView
    role: EmbeddedStruct
    fields:
      - name: id
        typeRef: { name: string }
        required: true
      - name: name
        typeRef: { name: string }
        required: true
      - name: inStock
        typeRef: { name: boolean }
        required: true
operationSets:
  - name: Catalog
    operations:
      - name: listItems
        typeRef: { name: ItemView, isArray: true }
        required: true
        arguments:
` + arguments + page + `
        httpMethod: GET
        restPath: items
`
	}
	sort := func(fields string) string {
		return `          - name: sort
            typeRef: { name: string }
            isQuery: true
            sort: ` + fields + "\n"
	}
	for _, tc := range []struct {
		name, arguments, page, want string
	}{
		{"a sort without a page", sort("[name]"), "", "Catalog.listItems: sort argument sort orders a page, and the operation has no @paginated"},
		{"a sort by a boolean", sort("[name, inStock]"), "        page: { defaultSort: name }",
			"Catalog.listItems: sort argument sort names ItemView.inStock, which holds boolean; a page is sorted by one text, number or time"},
		{"a defaultSort of another field", sort("[name]"), "        page: { defaultSort: -id }",
			"Catalog.listItems: @paginated defaultSort -id is not one of sort's values: name, -name"},
		{"a sort without a defaultSort", sort("[name]"), "        page: {}",
			"Catalog.listItems: sort argument sort needs @paginated's defaultSort, the order of a request that sends none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.yaml":      "name: catalog\nkind: API\noutputs: {}\n",
				"src/catalog.schema.yaml": schema(tc.arguments, tc.page),
			})
			_, err := LoadService(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want %q", err, tc.want)
			}
		})
	}

	dir := writeService(t, map[string]string{
		"schema.config.yaml":      "name: catalog\nkind: API\noutputs: {}\n",
		"src/catalog.schema.yaml": schema(sort("[name, id]"), "        page: { defaultSort: -name }"),
	})
	loaded, err := LoadService(dir)
	if err != nil {
		t.Fatalf("a sort that verifies: %v", err)
	}
	op := loaded.OperationSets[0].Operations[0]
	if op.Page.DefaultSort != "-name" || ir.SortArgument(op) == nil || strings.Join(ir.SortArgument(op).Sort, ",") != "name,id" {
		t.Fatalf("loaded page %+v, sort %+v", op.Page, ir.SortArgument(op))
	}
}

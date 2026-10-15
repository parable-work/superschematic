package loader

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPageVerifyFindingsReachTheTypeScriptAuthor: the loader holds a
// @paginated operation of a TypeScript service to the page's rules (D60):
// a GET returning a list, no argument named as a page parameter, and no
// operation named as its SDK iterator.
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

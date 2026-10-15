package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTSWriterEmitsRefersToAndSearchQuery: the TypeScript writer wraps a
// referring value's base type in RefersTo, with only the config members
// the reference writes and a list's suffix outside it, and writes a search
// argument as SearchQuery, not the QueryParam<Nullable<T>> it implies
// (D61, D62). Reading it back gives the same IR; the corpus covers the
// other legs.
func TestTSWriterEmitsRefersToAndSearchQuery(t *testing.T) {
	schema, err := loader.LoadService(dataFixtures + "/fixture-refs-api-yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "catalog.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`import { HttpMethod, QueryParam, RefersTo, SearchQuery, rest } from "@superschematic/api";`,
		`giftSku: Nullable<RefersTo<Identity.Slug, ItemView, { options: "Catalog.listItems", value: "sku", label: "name" }>>;`,
		`itemId: RefersTo<Identity.UUID, ItemView>;`,
		`listItems(q: SearchQuery<Validate<string, { maxLength: 50 }>>, ids: QueryParam<Nullable<Validate<RefersTo<Identity.UUID, ItemView>, { listMax: 5 }>[]>>, inStock: QueryParam<Nullable<boolean>>): ItemView[] {`,
		`getItem(itemId: RefersTo<Identity.UUID, ItemView>): ItemView {`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("TS output missing\n%s\n\ngot:\n%s", want, got)
		}
	}
	if got, want := normalizeIR(t, writeAndReload(t, schema, FormatTS)), normalizeIR(t, schema); got != want {
		t.Fatalf("IR changed after a YAML -> TS round trip\nwant:\n%s\ngot:\n%s", want, got)
	}

	// A search argument that is not an optional query parameter is none
	// SearchQuery can write.
	for _, arg := range []*ir.ArgumentDef{
		{Name: "q", TypeRef: ir.TypeRef{Name: "string"}, Search: true},
		{Name: "q", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Required: true, Search: true},
	} {
		doc := &schemafile.Document{Name: "catalog", Kind: ir.SchemaKindAPI, OperationSets: []*ir.OperationSet{{Name: "Catalog", Operations: []*ir.FieldDef{
			{Name: "search", HTTPMethod: "GET", RestPath: "search", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true, Arguments: []*ir.ArgumentDef{arg}},
		}}}}
		if _, err := Write(doc, FormatTS); err == nil || !strings.Contains(err.Error(), "Catalog.search(q): a search argument that is not an optional query parameter has no TypeScript authoring form") {
			t.Errorf("Write(ts) of %+v = %v", arg, err)
		}
	}
}

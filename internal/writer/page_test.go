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

// TestTSWriterEmitsPaginated: the TypeScript writer emits @paginated with
// both limits, as the loader filled them in from a YAML page that left
// them out, and a sorted page's defaultSort and SortBy argument (D60,
// amended), and reading it back gives the same IR.
func TestTSWriterEmitsPaginated(t *testing.T) {
	schema, err := loader.LoadService(dataFixtures + "/fixture-paged-api-yaml")
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
		`import { HttpMethod, QueryParam, SortBy, paginated, rest } from "@superschematic/api";`,
		"  @rest(HttpMethod.GET, \"items\")\n  @paginated({ defaultLimit: 2, maxLimit: 5, defaultSort: \"name\" })\n  listItems(",
		`sort: SortBy<ItemView, ["name", "sku"]>): ItemView[] {`,
		"  @rest(HttpMethod.GET, \"tags\")\n  @paginated({ defaultLimit: 20, maxLimit: 100 })\n  listTags(",
	} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("TS output missing\n%s\n\ngot:\n%s", want, got)
		}
	}
	if strings.Count(string(got), "@paginated") != 2 {
		t.Fatalf("TS output pages an operation it should not:\n%s", got)
	}
	if got, want := normalizeIR(t, writeAndReload(t, schema, FormatTS)), normalizeIR(t, schema); got != want {
		t.Fatalf("IR changed after a YAML -> TS round trip\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// TestTSWriterRefusesASortSortByCannotWrite: SortBy is an optional text
// query parameter that names its fields alone, so a sort argument that is
// not one, or that carries a default or a rule, has no TypeScript form.
func TestTSWriterRefusesASortSortByCannotWrite(t *testing.T) {
	name := "name"
	maxLength := 5
	for _, tc := range []struct {
		arg  *ir.ArgumentDef
		want string
	}{
		{&ir.ArgumentDef{Name: "sort", TypeRef: ir.TypeRef{Name: "string"}, Sort: []string{"name"}},
			"Catalog.listItems(sort): a sort argument that is not an optional query parameter has no TypeScript authoring form"},
		{&ir.ArgumentDef{Name: "sort", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Required: true, Sort: []string{"name"}},
			"Catalog.listItems(sort): a sort argument that is not an optional query parameter has no TypeScript authoring form"},
		{&ir.ArgumentDef{Name: "sort", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Default: &name, Sort: []string{"name"}},
			"Catalog.listItems(sort): a sort argument is SortBy's text alone"},
		{&ir.ArgumentDef{Name: "sort", TypeRef: ir.TypeRef{Name: "number"}, IsQuery: true, Sort: []string{"name"}},
			"Catalog.listItems(sort): a sort argument is SortBy's text alone"},
		{&ir.ArgumentDef{Name: "sort", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, ValidateMaxLength: &maxLength, Sort: []string{"name"}},
			"Catalog.listItems(sort): a sort argument is SortBy's text alone"},
	} {
		doc := &schemafile.Document{Name: "catalog", Kind: ir.SchemaKindAPI, OperationSets: []*ir.OperationSet{{Name: "Catalog", Operations: []*ir.FieldDef{
			{Name: "listItems", HTTPMethod: "GET", RestPath: "items", TypeRef: ir.TypeRef{Name: "ItemView", IsArray: true}, Required: true,
				Arguments: []*ir.ArgumentDef{tc.arg}, Page: &ir.OperationPage{DefaultLimit: 20, MaxLimit: 100, DefaultSort: "name"}},
		}}}}
		if _, err := Write(doc, FormatTS); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Write(ts) of %+v = %v, want %q", tc.arg, err, tc.want)
		}
	}
}

package apigen_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// generatePagedAPI is the API output of fixture-paged-api, whose
// listItems, listTags and listParts page (D60), and whose listParts takes
// a filter (D106).
func generatePagedAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	return generatePagedAPIOf(t, loadPagedAPI(t))
}

func loadPagedAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-paged-api"))
	if err != nil {
		t.Fatalf("load fixture-paged-api: %v", err)
	}
	return schema
}

// generatePagedAPIOf is generatePagedAPI of schema, fixture-paged-api as
// the caller loaded and edited it.
func generatePagedAPIOf(t *testing.T, schema *ir.Schema) *apigen.APIOutput {
	t.Helper()
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-paged-api",
		ModulePath:  "example.com/schemas/api/fixture-paged-api",
		TypesModule: "example.com/schemas/types/go/fixture-paged-api",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return output
}

// TestPagedEndpoints: a paged operation's endpoint carries its page, whose
// cursor and limit are not among its query parameters, and its result
// stays the list; every other endpoint has no page. listItems's sort is a
// query parameter, marked with its values and default, and its page's
// sort (D60, amended), which the implementation's arguments leave out.
func TestPagedEndpoints(t *testing.T) {
	output := generatePagedAPI(t)
	pages := map[string]apigen.PageInfo{}
	for _, endpoint := range output.Endpoints {
		if endpoint.Page == nil {
			continue
		}
		pages[endpoint.Name] = *endpoint.Page
		if !endpoint.OutputIsArray {
			t.Errorf("%s: OutputIsArray = false", endpoint.Name)
		}
		for _, param := range append(endpoint.QueryParams, endpoint.ScalarArgs...) {
			if param.Name == "cursor" || param.Name == "limit" {
				t.Errorf("%s: the page's %s is an argument", endpoint.Name, param.Name)
			}
		}
	}
	sort := &apigen.PageSort{Param: "sort", Fields: []string{"name", "sku"}, Default: "name"}
	partsSort := &apigen.PageSort{Param: "sort", Fields: []string{"name", "quantity"}, Default: "name"}
	want := map[string]apigen.PageInfo{
		"listItems": {DefaultLimit: 2, MaxLimit: 5, Operation: "Catalog.listItems", Sort: sort},
		"listTags":  {DefaultLimit: 20, MaxLimit: 100, Operation: "Catalog.listTags"},
		"listParts": {DefaultLimit: 2, MaxLimit: 5, Operation: "Catalog.listParts", Sort: partsSort},
	}
	if !reflect.DeepEqual(pages, want) {
		t.Fatalf("pages = %+v, want %+v", pages, want)
	}
	for _, endpoint := range output.Endpoints {
		if endpoint.Name != "listItems" {
			continue
		}
		var names, args []string
		var marked *apigen.SortParam
		for _, param := range endpoint.QueryParams {
			names = append(names, param.Name)
			if param.Sort != nil {
				marked = param.Sort
			}
		}
		for _, param := range endpoint.ArgQueryParams() {
			args = append(args, param.Name)
		}
		wantSort := &apigen.SortParam{Fields: []string{"name", "sku"}, Values: []string{"name", "-name", "sku", "-sku"}, Default: "name"}
		if !reflect.DeepEqual(names, []string{"ids", "inStock", "sort"}) || !reflect.DeepEqual(args, []string{"ids", "inStock"}) || !reflect.DeepEqual(marked, wantSort) {
			t.Errorf("listItems: query params %v, the implementation's %v, sort %+v", names, args, marked)
		}
		if got, want := endpoint.Page.GoSpec(), `paging.Spec{DefaultLimit: 2, MaxLimit: 5, Sort: &paging.SortSpec{Param: "sort", Fields: []string{"name", "sku"}, Default: paging.Sort{Field: "name"}}}`; got != want {
			t.Errorf("GoSpec = %s, want %s", got, want)
		}
	}
	descending := &apigen.PageInfo{DefaultLimit: 20, MaxLimit: 100, Sort: &apigen.PageSort{Param: "order", Fields: []string{"rating"}, Default: "-rating"}}
	if got, want := descending.GoSpec(), `paging.Spec{DefaultLimit: 20, MaxLimit: 100, Sort: &paging.SortSpec{Param: "order", Fields: []string{"rating"}, Default: paging.Sort{Field: "rating", Desc: true}}}`; got != want {
		t.Errorf("GoSpec = %s, want %s", got, want)
	}
	if !output.HasPagedEndpoints() {
		t.Fatal("HasPagedEndpoints = false")
	}
}

// TestFilteredEndpoint: an operation's FilterBy is its endpoint's Filter
// (D106), the argument's name, the filter's type name and a field per
// field of FilterBy, each with its kind, its operators, the shape of its
// typed filter and one value as a query argument named filter[field]. The
// filter is no query parameter and no argument of the implementation's
// own: listParts keeps its sort alone. Every other endpoint has none.
func TestFilteredEndpoint(t *testing.T) {
	output := generatePagedAPI(t)
	if !output.HasFilteredEndpoints {
		t.Fatal("HasFilteredEndpoints = false")
	}
	var listParts *apigen.EndpointInfo
	for i, endpoint := range output.Endpoints {
		switch {
		case endpoint.Name == "listParts":
			listParts = &output.Endpoints[i]
		case endpoint.Filter != nil:
			t.Errorf("%s has a filter: %+v", endpoint.Name, endpoint.Filter)
		}
	}
	if listParts == nil || listParts.Filter == nil {
		t.Fatal("fixture-paged-api's listParts has no filter")
	}
	filter := listParts.Filter
	if filter.Argument != "filter" || filter.GoName != "Filter" || filter.TypeName != "ListPartsFilter" || filter.Operation != "Catalog.listParts" || listParts.GoFilterTypeName() != "CatalogListPartsFilter" {
		t.Errorf("filter = %+v, its Go type %s", filter, listParts.GoFilterTypeName())
	}
	var names []string
	for _, param := range listParts.QueryParams {
		names = append(names, param.Name)
	}
	if !reflect.DeepEqual(names, []string{"sort"}) || len(listParts.ArgQueryParams()) != 0 || len(listParts.ScalarArgs) != 0 {
		t.Errorf("listParts: query params %v, the implementation's %v, scalar args %v", names, listParts.ArgQueryParams(), listParts.ScalarArgs)
	}

	type field struct {
		name, kind, operators, shape, valueType, goFilterType string
		nullable                                              bool
	}
	ordered := "eq ne in notIn gt gte lt lte"
	want := []field{
		{"id", ir.FilterKindUUID, "eq ne in notIn", apigen.FilterShapeEquals, "Identity.UUID", "filterparse.Equals[types.IdentityUUID]", false},
		{"name", ir.FilterKindString, "eq ne in notIn contains", apigen.FilterShapeText, "string", "filterparse.Text[string]", false},
		{"kind", ir.FilterKindEnum, "eq ne in notIn", apigen.FilterShapeEquals, "PartKind", "filterparse.Equals[types.PartKind]", false},
		{"quantity", ir.FilterKindInt, ordered, apigen.FilterShapeOrdered, "Generic.Int64", "filterparse.Ordered[int64]", false},
		{"weight", ir.FilterKindFloat, ordered, apigen.FilterShapeOrdered, "number", "filterparse.Ordered[float64]", false},
		{"price", ir.FilterKindDecimal, ordered, apigen.FilterShapeOrdered, "Generic.Decimal", "filterparse.Ordered[types.GenericDecimal]", false},
		{"discontinued", ir.FilterKindBool, "eq ne in notIn", apigen.FilterShapeEquals, "boolean", "filterparse.Equals[bool]", false},
		{"madeAt", ir.FilterKindDateTime, ordered, apigen.FilterShapeOrdered, "Temporal.DateTime", "filterparse.Ordered[types.TemporalDateTime]", false},
		{"retiredOn", ir.FilterKindDate, ordered + " isNull", apigen.FilterShapeOrdered, "Temporal.Date", "filterparse.Ordered[types.TemporalDate]", true},
		{"supplierId", ir.FilterKindUUID, "eq ne in notIn isNull", apigen.FilterShapeEquals, "Identity.UUID", "filterparse.Equals[types.IdentityUUID]", true},
	}
	var got []field
	for _, f := range filter.Fields {
		got = append(got, field{f.Name, f.Kind, strings.Join(f.Operators, " "), f.Shape(), f.Value.Type, f.GoFilterType(), f.Nullable()})
		// One value is a query argument at filter[field], never a list, and
		// never held to a body's rule for an absent member.
		if f.Value.Name != "filter["+f.Name+"]" || f.Value.IsArray || strings.Contains(f.Value.NewArg(), "bodyargs.Required()") {
			t.Errorf("%s: its value is %s, %s", f.Name, f.Value.Name, f.Value.NewArg())
		}
		if f.Takes(ir.FilterOpContains) != (f.Kind == ir.FilterKindString) || strings.Join(f.ValueOperators(), " ") != strings.TrimSuffix(strings.Replace(strings.Join(f.Operators, " "), " contains", "", 1), " isNull") {
			t.Errorf("%s: value operators %v of %v", f.Name, f.ValueOperators(), f.Operators)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields =\n%+v\nwant\n%+v", got, want)
	}
	if !reflect.DeepEqual(filter.FieldNames(), []string{"id", "name", "kind", "quantity", "weight", "price", "discontinued", "madeAt", "retiredOn", "supplierId"}) {
		t.Errorf("FieldNames = %v", filter.FieldNames())
	}
	if spec := filter.GoSpec(); !strings.HasPrefix(spec, `filterparse.Spec{Fields: []filterparse.FieldSpec{{Name: "id", Operators: []filterparse.Operator{"eq", "ne", "in", "notIn"}}, {Name: "name", Operators: []filterparse.Operator{"eq", "ne", "in", "notIn", "contains"}}, `) ||
		!strings.HasSuffix(spec, `{Name: "supplierId", Operators: []filterparse.Operator{"eq", "ne", "in", "notIn", "isNull"}}}}`) {
		t.Errorf("GoSpec = %s", spec)
	}
}

// TestAFilterTheLoaderRefusesIsAGeneratorError: IR built without the
// loader holds a filter to its rules in apigen too (D106): an argument and
// a field, each field with the kind, the type and the operators the loader
// fills, on a GET whose result is a list.
func TestAFilterTheLoaderRefusesIsAGeneratorError(t *testing.T) {
	const unfilled = "has no kind, type and operators of that kind; the loader fills them (D106)"
	for name, tc := range map[string]struct {
		edit func(op *ir.FieldDef)
		want string
	}{
		"no argument":          {func(op *ir.FieldDef) { op.Filter.Argument = "" }, "Catalog.listParts: its filter names no argument or no field"},
		"no field":             {func(op *ir.FieldDef) { op.Filter.Fields = nil }, "Catalog.listParts: its filter names no argument or no field"},
		"an unnamed field":     {func(op *ir.FieldDef) { op.Filter.Fields[0].Name = "" }, "Catalog.listParts: its filter holds an unnamed field"},
		"a field of no kind":   {func(op *ir.FieldDef) { op.Filter.Fields[1].Kind = "" }, "filter field name " + unfilled},
		"a field of no type":   {func(op *ir.FieldDef) { op.Filter.Fields[2].TypeRef = nil }, "filter field kind " + unfilled},
		"another kind's order": {func(op *ir.FieldDef) { op.Filter.Fields[2].Operators = ir.FilterOperators(ir.FilterKindInt, false) }, "filter field kind " + unfilled},
		"an operator left out": {func(op *ir.FieldDef) { op.Filter.Fields[3].Operators = []string{ir.FilterOpEq} }, "filter field quantity " + unfilled},
		"not a GET":            {func(op *ir.FieldDef) { op.HTTPMethod = "POST" }, "operation Catalog.listParts: a filter is an argument of a GET whose result is a list (D106)"},
	} {
		t.Run(name, func(t *testing.T) {
			schema := loadPagedAPI(t)
			var listParts *ir.FieldDef
			for _, set := range schema.OperationSets {
				for _, op := range set.Operations {
					if op.Name == "listParts" {
						listParts = op
					}
				}
			}
			if listParts == nil || listParts.Filter == nil {
				t.Fatal("fixture-paged-api's listParts has no filter")
			}
			tc.edit(listParts)
			_, err := apigen.Generate(schema, apigen.Options{
				Provider:    sessionauth.Provider{},
				SchemaName:  "fixture-paged-api",
				ModulePath:  "example.com/schemas/api/fixture-paged-api",
				TypesModule: "example.com/schemas/types/go/fixture-paged-api",
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Generate = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestASortTheLoaderRefusesIsAGeneratorError: IR built without the loader
// holds a sort argument to its rules in apigen too (D60, amended): an
// optional query parameter, the operation's one, of a @paginated operation
// whose defaultSort is one of the sort's values.
func TestASortTheLoaderRefusesIsAGeneratorError(t *testing.T) {
	for name, edit := range map[string]func(op *ir.FieldDef, sort *ir.ArgumentDef){
		"a required sort":                  func(_ *ir.FieldDef, sort *ir.ArgumentDef) { sort.Required = true },
		"a sort that is no query":          func(_ *ir.FieldDef, sort *ir.ArgumentDef) { sort.IsQuery = false },
		"a sort without a defaultSort":     func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.Page.DefaultSort = "" },
		"a defaultSort of another field":   func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.Page.DefaultSort = "-inStock" },
		"a defaultSort of no sort's value": func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.Page.DefaultSort = "--name" },
		"a second sort": func(op *ir.FieldDef, sort *ir.ArgumentDef) {
			second := *sort
			second.Name = "order"
			op.Arguments = append([]*ir.ArgumentDef{&second}, op.Arguments...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			schema := loadPagedAPI(t)
			var listItems *ir.FieldDef
			for _, set := range schema.OperationSets {
				for _, op := range set.Operations {
					if op.Name == "listItems" {
						listItems = op
					}
				}
			}
			if listItems == nil || ir.SortArgument(listItems) == nil {
				t.Fatal("fixture-paged-api's listItems has no sort")
			}
			edit(listItems, ir.SortArgument(listItems))
			_, err := apigen.Generate(schema, apigen.Options{
				Provider:    sessionauth.Provider{},
				SchemaName:  "fixture-paged-api",
				ModulePath:  "example.com/schemas/api/fixture-paged-api",
				TypesModule: "example.com/schemas/types/go/fixture-paged-api",
			})
			if err == nil || !strings.Contains(err.Error(), "a sort argument is the one optional query parameter of a @paginated operation whose defaultSort is one of its values") {
				t.Fatalf("Generate = %v, want the sort's refusal", err)
			}
		})
	}
}

// TestPagedOpenAPIGolden pins the OpenAPI document of fixture-paged-api: a
// paged operation's cursor and limit follow its own query parameters, with
// their bounds, and its data is the page, {items, next}, written inline;
// listParts's filter is one deepObject parameter between them (D106).
// Regenerate with:
// go test ./internal/generator/apigen -run TestPagedOpenAPIGolden -update
func TestPagedOpenAPIGolden(t *testing.T) {
	got := []byte(generatePagedAPI(t).OpenAPISpecRaw)
	goldenPath := filepath.Join("testdata", "golden", "fixture-paged-api", "openapi.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("openapi.json differs from %s; run with -update and review the diff\ngot:\n%s", goldenPath, got)
	}
}

// TestWriteAPIGoldenPaged pins the Go server of fixture-paged-api (D60): a
// paged operation's method takes page paging.Request last and returns
// paging.Page of its list's element, its route reads the page with
// paging.Read after its own query parameters and answers the page as the
// envelope's data, and its scaffolds return the empty page. listParts's
// method takes its filter, CatalogListPartsFilter, before the page, and
// its route reads and decodes the filter before it reads the page (D106).
// Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenPaged -update
func TestWriteAPIGoldenPaged(t *testing.T) {
	output := generatePagedAPI(t)
	outDir := t.TempDir()
	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/fixture-paged-api"); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	if _, err := apigen.WriteScaffolds(output, filepath.Join(outDir, "scaffolds")); err != nil {
		t.Fatalf("write scaffolds: %v", err)
	}
	if _, err := apigen.WriteImplementationScaffold(output, filepath.Join(outDir, "impl")); err != nil {
		t.Fatalf("write implementation scaffold: %v", err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", "fixture-paged-api"), []string{
		"interfaces.go",
		"routes.go",
		"scaffolds/catalog/list_items.go",
		"scaffolds/catalog/list_parts.go",
		"scaffolds/catalog/list_tags.go",
		"impl/implementation.go",
	})
}

// TestPagedRoutesServePages generates the Go types and API modules of
// fixture-paged-api, with its scaffolds and its implementation scaffold
// inside the API module, copies testdata/paged_routes_test.go and the HTTP
// runtimes' page and filter vectors into it, and runs go build, go vet and
// go test there: the routes page an in-memory catalog over httptest, first
// page, next, last page with a null next, filters on each page, the
// limit's bounds, the items sorted each way with the next bound to the
// sort (D60, amended), and each refusal of a limit, a sort or a cursor as
// page_parity.json words it; listParts filters by a field of each kind
// and every operator, beside a sort and a cursor, hands the implementation
// the typed filter, refuses a field, an operator and a malformed key as
// filter_parity.json words them, and answers a value its field's type
// refuses with a 400 at filter[field] (D106); the scaffolds implement the
// interface and answer 501.
func TestPagedRoutesServePages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	apiDir := writePagedAPIModule(t, loadPagedAPI(t))
	for _, file := range []struct{ from, to string }{
		{filepath.Join("testdata", "paged_routes_test.go"), "paged_routes_test.go"},
		{filepath.Join(testpaths.Local(t).HTTPRuntimeGo, "..", "testdata", "page_parity.json"), filepath.Join("testdata", "page_parity.json")},
		{filepath.Join(testpaths.Local(t).HTTPRuntimeGo, "..", "testdata", "filter_parity.json"), filepath.Join("testdata", "filter_parity.json")},
	} {
		data, err := os.ReadFile(file.from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(apiDir, file.to)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(apiDir, file.to), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGoAPIModule(t, apiDir)
}

// TestFilteredRoutesBesideQueryParametersCompile builds and vets
// fixture-paged-api with listItems also filtered (D106), under an argument
// named where, its scaffolds and its implementation scaffold: the method
// takes its query parameters, then the filter, then the page, and the
// route reads the filters before it reads the page, so the filters apply
// to each page. The wire's parameter is filter whatever the argument is
// called.
func TestFilteredRoutesBesideQueryParametersCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema := loadPagedAPI(t)
	found := false
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.Name == "listItems" && op.Page != nil {
				// As the loader fills a FilterBy<ItemView, ["id", "name",
				// "inStock"]>.
				op.Filter = &ir.OperationFilter{Argument: "where", Fields: []*ir.FilterField{
					{Name: "id", Kind: ir.FilterKindUUID, Operators: ir.FilterOperators(ir.FilterKindUUID, false), TypeRef: &ir.TypeRef{Name: "Identity.UUID"}},
					{Name: "name", Kind: ir.FilterKindString, Operators: ir.FilterOperators(ir.FilterKindString, false), TypeRef: &ir.TypeRef{Name: "string"}},
					{Name: "inStock", Kind: ir.FilterKindBool, Operators: ir.FilterOperators(ir.FilterKindBool, false), TypeRef: &ir.TypeRef{Name: "boolean"}},
				}}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("fixture-paged-api has no paged listItems")
	}
	apiDir := writePagedAPIModule(t, schema)
	data, err := os.ReadFile(filepath.Join(apiDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	_, handler, ok := strings.Cut(string(data), "func createCatalogListItemsHandler(")
	if !ok {
		t.Fatalf("routes.go has no handler of listItems:\n%s", data)
	}
	handler, _, _ = strings.Cut(handler, "\nfunc create")
	filters := strings.Index(handler, `filterRequest, ok := filterparse.Read(w, r, filterparse.Spec{Fields: []filterparse.FieldSpec{{Name: "id", Operators: []filterparse.Operator{"eq", "ne", "in", "notIn"}}, {Name: "name", Operators: []filterparse.Operator{"eq", "ne", "in", "notIn", "contains"}}, {Name: "inStock", Operators: []filterparse.Operator{"eq", "ne", "in", "notIn"}}}})`)
	page := strings.Index(handler, "page, ok := paging.Read(w, r, paging.Spec{DefaultLimit: 2, MaxLimit: 5, Sort: ")
	if filters < 0 || page < filters || !strings.Contains(handler, "impl.ListItems(r.Context(), Ids, InStock, filters, page)") {
		t.Errorf("listItems's handler does not read the filters, then the page, then pass both:\n%s", handler)
	}
	interfaces, err := os.ReadFile(filepath.Join(apiDir, "interfaces.go"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "ListItems(ctx context.Context, ids []types.IdentityUUID, inStock *bool, where CatalogListItemsFilter, page paging.Request) (paging.Page[types.ItemView], error)"; !strings.Contains(string(interfaces), want) {
		t.Errorf("interfaces.go lacks %s:\n%s", want, interfaces)
	}
	runGoAPIModule(t, apiDir)
}

// writePagedAPIModule writes the Go types and API modules of schema, a
// fixture-paged-api, with its scaffolds under scaffolds/ and its
// implementation scaffold under impl/ inside the API module, and returns
// the API module's directory.
func writePagedAPIModule(t *testing.T, schema *ir.Schema) string {
	t.Helper()
	apiDir := writeGoAPIModule(t, schema, "fixture-paged-api")
	output := generatePagedAPIOf(t, schema)
	if _, err := apigen.WriteScaffolds(output, filepath.Join(apiDir, "scaffolds")); err != nil {
		t.Fatal(err)
	}
	if _, err := apigen.WriteImplementationScaffold(output, filepath.Join(apiDir, "impl")); err != nil {
		t.Fatal(err)
	}
	return apiDir
}

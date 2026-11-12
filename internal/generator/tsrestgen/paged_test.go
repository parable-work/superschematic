package tsrestgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

const pagedAPI = "fixture-paged-api"

// pagedFixture is fixture-paged-api with apigen's endpoints for it:
// catalog.listItems pages ItemView two to a page, up to five, beside two
// query arguments; catalog.listTags pages strings with the default limits
// and declares no argument; catalog.listParts pages PartView, sorted, and
// takes a filter on a field of every kind a filter compares (D106);
// getItem and placeBasket do not page.
func pagedFixture(t *testing.T) apiFixture {
	t.Helper()
	return pagedFixtureWith(t, nil)
}

// pagedFixtureWith is pagedFixture with listParts rewritten first, when
// rewrite is set.
func pagedFixtureWith(t *testing.T, rewrite func(listParts *ir.FieldDef)) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedAPI))
	if err != nil {
		t.Fatalf("load %s: %v", pagedAPI, err)
	}
	if rewrite != nil {
		for _, set := range schema.OperationSets {
			for _, op := range set.Operations {
				if op.Name == "listParts" {
					rewrite(op)
				}
			}
		}
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: pagedAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: pagedAPI, schema: schema, endpoints: endpoints}
}

// TestWriteAPIGoldenPaged pins the package for fixture-paged-api (D60).
// Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenPaged -update
func TestWriteAPIGoldenPaged(t *testing.T) {
	checkGolden(t, generateFixture(t, pagedFixture(t)), pagedAPI)
}

// TestGeneratePagedShape: a @paginated operation carries its limits, and
// listItems its sort (D60, amended), takes its page as an argument even
// when it declares none, and answers a Page of its list's elements; its
// cursor, limit and sort are not query parameters of its own. The
// operations that do not page are as before, and an API without a paged
// operation imports nothing of the page.
func TestGeneratePagedShape(t *testing.T) {
	output := generateFixture(t, pagedFixture(t))
	if !output.HasPages || !output.HasSortedPages {
		t.Errorf("HasPages = %t, HasSortedPages = %t for fixture-paged-api", output.HasPages, output.HasSortedPages)
	}
	byName := endpointsByName(output)
	for _, tc := range []struct {
		name         string
		page         *apigen.PageInfo
		output       string
		queryParams  int
		hasArguments bool
	}{
		{"listItems", &apigen.PageInfo{DefaultLimit: 2, MaxLimit: 5, Operation: "Catalog.listItems", Sort: &apigen.PageSort{Param: "sort", Fields: []string{"name", "sku"}, Default: "name"}}, "Page<ItemView>", 2, true},
		{"listTags", &apigen.PageInfo{DefaultLimit: 20, MaxLimit: 100, Operation: "Catalog.listTags"}, "Page<string>", 0, true},
		{"listParts", &apigen.PageInfo{DefaultLimit: 2, MaxLimit: 5, Operation: "Catalog.listParts", Sort: &apigen.PageSort{Param: "sort", Fields: []string{"name", "quantity"}, Default: "name"}}, "Page<PartView>", 0, true},
		{"getItem", nil, "ItemView", 0, true},
		{"placeBasket", nil, "BasketView", 0, true},
	} {
		ep, ok := byName[tc.name]
		if !ok {
			t.Fatalf("no endpoint %s", tc.name)
		}
		switch {
		case tc.page == nil && ep.Page != nil:
			t.Errorf("%s Page = %+v, want none", tc.name, *ep.Page)
		case tc.page != nil && !reflect.DeepEqual(ep.Page, tc.page):
			t.Errorf("%s Page = %+v, want %+v", tc.name, ep.Page, *tc.page)
		}
		if ep.OutputType != tc.output {
			t.Errorf("%s OutputType = %q, want %q", tc.name, ep.OutputType, tc.output)
		}
		if len(ep.QueryParams) != tc.queryParams {
			t.Errorf("%s query params = %+v, want %d", tc.name, ep.QueryParams, tc.queryParams)
		}
		if ep.HasArgs() != tc.hasArguments {
			t.Errorf("%s HasArgs = %t, want %t", tc.name, ep.HasArgs(), tc.hasArguments)
		}
	}
	if byName["listItems"].PageSpecLiteral() != "{ defaultLimit: 2, maxLimit: 5, sort: { param: 'sort', fields: ['name', 'sku'], default: 'name' } }" ||
		byName["listTags"].PageSpecLiteral() != "{ defaultLimit: 20, maxLimit: 100 }" {
		t.Errorf("page specs = %s, %s", byName["listItems"].PageSpecLiteral(), byName["listTags"].PageSpecLiteral())
	}
	if generateFixtureAPI(t).HasPages {
		t.Error("HasPages = true for fixture-api, which declares no @paginated operation")
	}
}

// TestGeneratePagedFilterShape: an operation with a FilterBy carries its
// filter (D106), each field with its operators, the shape of its filter,
// the TypeScript type of one of its values and that value's spec as a
// query parameter named filter[<field>], never required. The filter is no
// query parameter of the operation, and its argument is one of the
// method's. The operations without one carry none, and an API without a
// filtered operation imports nothing of the filter.
func TestGeneratePagedFilterShape(t *testing.T) {
	output := generateFixture(t, pagedFixture(t))
	if !output.HasFilters {
		t.Error("HasFilters = false for fixture-paged-api")
	}
	byName := endpointsByName(output)
	for _, name := range []string{"listItems", "listTags", "getItem", "placeBasket"} {
		if byName[name].Filter != nil {
			t.Errorf("%s Filter = %+v, want none", name, *byName[name].Filter)
		}
	}
	filter := byName["listParts"].Filter
	if filter == nil {
		t.Fatal("listParts has no Filter")
	}
	if filter.Argument != "filter" || filter.TSName != "filter" || filter.TypeName != "ListPartsFilter" {
		t.Errorf("listParts filter = %s, %s, %s", filter.Argument, filter.TSName, filter.TypeName)
	}
	type field struct{ name, member, spec string }
	var got []field
	for _, f := range filter.Fields {
		got = append(got, field{f.Name, f.Type(), f.FieldSpecLiteral()})
	}
	equality, ordered := "['eq', 'in']", "['eq', 'in', 'gt', 'gte', 'lt', 'lte']"
	want := []field{
		{"id", "EqualsFilter<IdentityUUID>", "{ name: 'id', kind: 'uuid', operators: " + equality + ", value: { name: 'filter[id]', kind: 'uuid', required: false } }"},
		{"name", "TextFilter<string>", "{ name: 'name', kind: 'string', operators: ['eq', 'in', 'contains'], value: { name: 'filter[name]', kind: 'string', required: false } }"},
		{"kind", "EqualsFilter<PartKind>", "{ name: 'kind', kind: 'enum', operators: " + equality + ", value: { name: 'filter[kind]', kind: 'enum', required: false, enumValues: ['bolt', 'nut', 'washer'] } }"},
		{"quantity", "OrderedFilter<number>", "{ name: 'quantity', kind: 'int', operators: " + ordered + ", value: { name: 'filter[quantity]', kind: 'integer', required: false, scalar: { name: 'Generic.Int64', min: -9007199254740991, max: 9007199254740991 } } }"},
		{"weight", "OrderedFilter<number>", "{ name: 'weight', kind: 'float', operators: " + ordered + ", value: { name: 'filter[weight]', kind: 'number', required: false } }"},
		{"price", "OrderedFilter<GenericDecimal>", "{ name: 'price', kind: 'decimal', operators: " + ordered + ", value: { name: 'filter[price]', kind: 'string', required: false, scalar: { name: 'Generic.Decimal', pattern: '^[+-]?[0-9]+(\\\\.[0-9]+)?$' } } }"},
		{"discontinued", "EqualsFilter<boolean>", "{ name: 'discontinued', kind: 'bool', operators: " + equality + ", value: { name: 'filter[discontinued]', kind: 'boolean', required: false } }"},
		{"madeAt", "OrderedFilter<Date>", "{ name: 'madeAt', kind: 'datetime', operators: " + ordered + ", value: { name: 'filter[madeAt]', kind: 'datetime', required: false } }"},
		{"retiredOn", "OrderedFilter<TemporalDate>", "{ name: 'retiredOn', kind: 'date', operators: ['eq', 'in', 'gt', 'gte', 'lt', 'lte', 'isNull'], value: { name: 'filter[retiredOn]', kind: 'string', required: false, scalar: { name: 'Temporal.Date', maxLength: 40 } } }"},
		{"supplierId", "EqualsFilter<IdentityUUID>", "{ name: 'supplierId', kind: 'uuid', operators: ['eq', 'in', 'isNull'], value: { name: 'filter[supplierId]', kind: 'uuid', required: false } }"},
	}
	if !reflect.DeepEqual(got, want) {
		for i := range max(len(got), len(want)) {
			var g, w field
			if i < len(got) {
				g = got[i]
			}
			if i < len(want) {
				w = want[i]
			}
			if g != w {
				t.Errorf("filter field %d:\n got %+v\nwant %+v", i, g, w)
			}
		}
	}
	if got, want := output.InterfaceRuntimeTypes(), []string{"EqualsFilter", "OperationResult", "OrderedFilter", "Page", "PageRequest", "RequestContext", "TextFilter"}; !slices.Equal(got, want) {
		t.Errorf("interfaces.ts imports %v from the runtime, want %v", got, want)
	}
	if got, want := output.OperationTableType(), "OperationSpec & { readonly filter?: FilterSpec; readonly page?: PageSpec }"; got != want {
		t.Errorf("the operation table's type = %q, want %q", got, want)
	}
	if got := output.FilterTypeNames(); !slices.Equal(got, []string{"ListPartsFilter"}) {
		t.Errorf("router.ts imports the filter types %v, want ListPartsFilter", got)
	}
	// The types of a filter's values are interfaces.ts's to name, not
	// router.ts's.
	for _, scalar := range []string{"GenericDecimal", "TemporalDate"} {
		if !slices.Contains(output.ScalarImports, scalar) || slices.Contains(output.RouterScalarImports, scalar) {
			t.Errorf("%s: interfaces.ts imports %v and router.ts %v", scalar, output.ScalarImports, output.RouterScalarImports)
		}
	}

	plain := generateFixtureAPI(t)
	if plain.HasFilters || len(plain.FilteredEndpoints()) != 0 || plain.OperationTableType() != "OperationSpec" {
		t.Errorf("fixture-api, which declares no FilterBy: HasFilters = %t, table type %q", plain.HasFilters, plain.OperationTableType())
	}
	if strings.Contains(plain.RuntimeImport(), "Filter") || slices.ContainsFunc(plain.InterfaceRuntimeTypes(), func(name string) bool { return strings.Contains(name, "Filter") }) {
		t.Errorf("fixture-api imports a filter: %s; %v", plain.RuntimeImport(), plain.InterfaceRuntimeTypes())
	}
}

// listPartsTool makes listParts a visible MCP tool, list_parts, as its
// @mcp and @docs would.
func listPartsTool(op *ir.FieldDef) {
	op.MCP = &ir.OperationMCP{
		Handle:     "list_parts",
		Invocation: ir.MCPInvocation{Key: apigen.DefaultToolInvocationKey, Value: apigen.ToolInvocationAuto},
	}
	op.Docs = &ir.OperationDocs{
		Title:         "List parts",
		Description:   "Lists the catalog's parts.",
		Capability:    "catalog.parts.list",
		Lifecycle:     ir.DocsLifecycleActive,
		Visibility:    ir.DocsVisibilityPublic,
		MappingStatus: ir.DocsMappingStatusMapped,
		ReplayMode:    ir.DocsReplayModeReadOnly,
	}
}

// TestGeneratePagedFilterArgument: the filter travels under filter on the
// wire whatever its argument is named (D106). An operation whose FilterBy
// argument is where hands its implementation args.where, and its MCP
// tool's where argument goes to the route's query as the bracket form
// under filter, beside the sort, the cursor and the limit, which are query
// parameters.
func TestGeneratePagedFilterArgument(t *testing.T) {
	for _, argument := range []string{"filter", "where"} {
		fixture := pagedFixtureWith(t, func(op *ir.FieldDef) {
			listPartsTool(op)
			op.Filter.Argument = argument
		})
		fixture.mcp = true
		output := generateMCPFixture(t, fixture)
		outDir := t.TempDir()
		if err := WriteAPI(output, outDir); err != nil {
			t.Fatal(err)
		}
		read := func(name string) string {
			data, err := os.ReadFile(filepath.Join(outDir, name))
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
		for file, lines := range map[string][]string{
			"interfaces.ts": {"  " + argument + ": ListPartsFilter;", "args." + argument + " holds a member for each of id, name,"},
			"router.ts": {
				"const filter = readFilter(ctx.query, operationSpecs.listParts.filter) as ListPartsFilter;",
				"            " + argument + ": filter,",
				"{ name: 'id', kind: 'uuid', operators: ['eq', 'in'], value: { name: 'filter[id]', kind: 'uuid', required: false } },",
			},
		} {
			source := read(file)
			for _, line := range lines {
				if !strings.Contains(source, line) {
					t.Errorf("%s of a filter argument %s lacks %q:\n%s", file, argument, line, source)
				}
			}
		}
		var listed []MCPToolArgument
		for _, tool := range mcpToolsOf(t, outDir) {
			if tool.Name == "list_parts" {
				listed = tool.Arguments
				var schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				}
				if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
					t.Fatal(err)
				}
				if _, ok := schema.Properties[argument]; !ok {
					t.Errorf("list_parts's input schema has no %s: %s", argument, tool.InputSchema)
				}
			}
		}
		want := []MCPToolArgument{
			{Key: "sort", In: "query", Name: "sort"},
			{Key: argument, In: "filter", Name: "filter"},
			{Key: "cursor", In: "query", Name: "cursor"},
			{Key: "limit", In: "query", Name: "limit"},
		}
		if mustJSON(t, listed) != mustJSON(t, want) {
			t.Errorf("list_parts's arguments = %s, want %s", mustJSON(t, listed), mustJSON(t, want))
		}
	}
}

// TestGeneratedPagedRouter type-checks the generated package for
// fixture-paged-api with a typed implementation of its catalog staged
// beside it, so tsc holds a paged method to args.page and a Page result,
// and a filtered one to args.filter, its filter's type (D106), then serves
// that implementation through the generated router under bun:
// the first page, each next to the last, whose next is null, the filters
// on every page, the items sorted each way with each next bound to the sort
// (D60, amended), a page of a primitive, a limit past the max, a sort not
// offered, a cursor of another sort and a forged cursor refused with
// page_parity.json's 400s, and every reader vector of the two routes'
// limits and sorts answered over HTTP as the runtime's reader answers it;
// and listParts's filter: equality on a field of each kind, in, a range,
// contains and isNull, with a sort and a cursor, and a field, an operator
// and a value its type refuses each a 400 at filter[field] that never
// reaches the implementation.
func TestGeneratedPagedRouter(t *testing.T) {
	tree := materializeAPI(t, pagedFixture(t))
	tree.stage(t, "paged_implementation.ts")
	tree.typeCheck(t)
	tree.env = append(tree.env, "PAGE_PARITY="+filepath.Join(testpaths.RepoRoot(t), "runtime", "http", "testdata", "page_parity.json"))
	tree.runTest(t, "paged_runtime.test.ts")
}

// TestGeneratedPagedMCPFilter serves fixture-paged-api with outputs.api.mcp
// on and listParts a tool, under bun: a tools/call's filter argument, an
// object with a member per field, reaches the route as the bracket form
// (D106), so the implementation takes it typed as a request's is, beside
// the sort, the limit and the cursor, and the route refuses a field, an
// operator and a value as it refuses any caller's.
func TestGeneratedPagedMCPFilter(t *testing.T) {
	fixture := pagedFixtureWith(t, listPartsTool)
	fixture.mcp = true
	tree := materializeAPI(t, fixture)
	tree.stage(t, "paged_implementation.ts")
	tree.typeCheck(t)
	tree.runTest(t, "paged_mcp_runtime.test.ts")
}

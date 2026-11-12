package sdkgen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// filterParityFile is runtime/http/testdata/filter_parity.json, whose
// queries an SDK's filter writer is held to (D106).
func filterParityFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(testpaths.RepoRoot(t), "runtime", "http", "testdata", "filter_parity.json")
}

// TestFilteredEndpointsTakeATypedFilter pins the TypeScript type of a
// filtered endpoint's filter (D106): named for the namespace and the
// operation, a member per field in FilterBy's order, each a value of the
// field's kind or an object of the operators that kind takes, isNull on a
// field that may be null alone. The method takes it in its query object,
// under the argument's name, and withFilter writes it.
func TestFilteredEndpointsTakeATypedFilter(t *testing.T) {
	_, apiOutput, parseable := loadPagedAPI(t, sdktest.AddFindPartsOperation)
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !sdkOutput.HasFilteredEndpoints || len(sdkOutput.Namespaces) != 1 || !sdkOutput.Namespaces[0].HasFilteredEndpoints {
		t.Fatalf("HasFilteredEndpoints = %t, namespaces %d", sdkOutput.HasFilteredEndpoints, len(sdkOutput.Namespaces))
	}
	ns := sdkOutput.Namespaces[0]
	if !containsString(ns.Imports, "PartKind") {
		t.Errorf("the namespace does not import the enum its filter names: %v", ns.Imports)
	}

	const (
		ordered = "eq?: T; in?: T[]; gt?: T; gte?: T; lt?: T; lte?: T"
		equals  = "eq?: T; in?: T[]"
	)
	member := func(value, operators string) string {
		return value + " | { " + strings.ReplaceAll(operators, "T", value) + " }"
	}
	wantMembers := []FilterMember{
		{"id", member("string", equals)},
		{"name", member("string", equals+"; contains?: string")},
		{"kind", member("PartKind", equals)},
		{"quantity", member("number", ordered)},
		{"weight", member("number", ordered)},
		{"price", member("string", ordered)},
		{"discontinued", member("boolean", equals)},
		{"madeAt", member("string", ordered)},
		{"retiredOn", member("string", ordered+"; isNull?: boolean")},
		{"supplierId", member("string", equals+"; isNull?: boolean")},
	}
	want := map[string]struct{ filterType, param, queryType, queryExpr string }{
		"listParts": {
			"CatalogListPartsFilter", "filter",
			"{ filter?: CatalogListPartsFilter; [name: string]: any }", "withFilter(params, 'filter')",
		},
		"findParts": {
			"CatalogFindPartsFilter", "where",
			"{ where?: CatalogFindPartsFilter; [name: string]: any }", "withFilter(params, 'where')",
		},
	}
	filtered := ns.FilteredEndpoints()
	if len(filtered) != len(want) {
		t.Fatalf("filtered endpoints = %d, want %d", len(filtered), len(want))
	}
	for _, ep := range ns.Endpoints {
		w, ok := want[ep.Name]
		if !ok {
			if ep.Filter != nil || ep.FilterType != "" || ep.FilterMembers() != nil || ep.QueryType() != "any" || ep.QueryExpr("params") != "params" {
				t.Errorf("%s, which takes no filter, has %s (%s)", ep.Name, ep.FilterType, ep.QueryType())
			}
			continue
		}
		if ep.FilterType != w.filterType || ep.FilterParam() != w.param || ep.QueryType() != w.queryType || ep.QueryExpr("params") != w.queryExpr || !ep.HasQuery() {
			t.Errorf("%s = (%s, %s, %s, %s, query %t), want %+v", ep.Name, ep.FilterType, ep.FilterParam(), ep.QueryType(), ep.QueryExpr("params"), ep.HasQuery(), w)
		}
		if got := ep.FilterMembers(); !reflect.DeepEqual(got, wantMembers) {
			t.Errorf("%s's filter has the members\n%+v\nwant\n%+v", ep.Name, got, wantMembers)
		}
	}
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// TestFilteredToolsTakeTheFilter: a filtered endpoint's tool (D106) takes
// the filter as the object toolsutil.FilterProperty builds, under the
// argument's name, typed in tools/index.ts as the SDK's own type; its
// binding sends it in the method's query object, where invokeTool passes
// it for the method to write as the bracket form; and its description
// names the fields, after the page's sentence when it pages.
func TestFilteredToolsTakeTheFilter(t *testing.T) {
	_, apiOutput, parseable := loadPagedAPI(t, sdktest.AddFindPartsOperation)
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	tools, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatalf("GenerateTools: %v", err)
	}
	if !tools.HasFilters || !reflect.DeepEqual(tools.FilterTypes, []string{"CatalogFindPartsFilter", "CatalogListPartsFilter"}) {
		t.Fatalf("HasFilters = %t, filter types %v", tools.HasFilters, tools.FilterTypes)
	}
	const fields = "id, name, kind, quantity, weight, price, discontinued, madeAt, retiredOn, supplierId"
	for _, test := range []struct {
		tool, param, filterType string
		query                   []string
		invocation, sentence    string
	}{
		{
			tool: "catalog.listParts", param: "filter", filterType: "CatalogListPartsFilter",
			query: []string{"sort", "filter", "cursor", "limit"},
			invocation: "{ sort: (params as CatalogListPartsParams).sort, filter: (params as CatalogListPartsParams).filter, " +
				"cursor: (params as CatalogListPartsParams).cursor, limit: (params as CatalogListPartsParams).limit }",
			sentence: toolsutil.SortedPageSentence("sort") + " Filters: `filter` keeps the items its members match, a member per field (" + fields +
				"), each a value or an object of operators; pass the same `filter` with each `cursor`.",
		},
		{
			tool: "catalog.findParts", param: "where", filterType: "CatalogFindPartsFilter",
			query:      []string{"where"},
			invocation: "{ where: (params as CatalogFindPartsParams).where }",
			sentence: "Every part the caller's filters find. Filters: `where` keeps the items its members match, a member per field (" + fields +
				"), each a value or an object of operators.",
		},
	} {
		t.Run(test.tool, func(t *testing.T) {
			tool := tools.GetToolByName(test.tool)
			if tool == nil {
				t.Fatalf("no tool %s", test.tool)
			}
			if tool.Filter == nil || tool.FilterParam != test.param || tool.FilterType != test.filterType {
				t.Fatalf("filter = (%v, %s, %s)", tool.Filter, tool.FilterParam, tool.FilterType)
			}
			if !strings.HasSuffix(tool.Description, test.sentence) {
				t.Errorf("description = %q", tool.Description)
			}
			property, ok := tool.Parameters.Properties[test.param]
			if !ok || property.Type != "object" || len(property.Properties) != len(tool.Filter.Fields) {
				t.Fatalf("parameters hold the filter as %+v", property)
			}
			for _, required := range tool.Parameters.Required {
				if required == test.param {
					t.Errorf("the filter is required: %v", tool.Parameters.Required)
				}
			}
			if got := tool.ParamTypes[test.param]; got != test.filterType {
				t.Errorf("the filter is typed %s, want %s", got, test.filterType)
			}
			var query []string
			for _, argument := range tool.MCPBinding.Arguments {
				if argument.Kind != "query" {
					continue
				}
				if argument.Required {
					t.Errorf("query argument %+v", argument)
				}
				if argument.ToolParameter == test.param && argument.Target != "query."+test.param {
					t.Errorf("the filter's argument is %+v", argument)
				}
				query = append(query, argument.ToolParameter)
			}
			var sources []string
			for _, methodArg := range tool.MCPBinding.MethodArgs {
				if methodArg.Kind == "query" {
					sources = methodArg.Sources
				}
			}
			if !reflect.DeepEqual(query, test.query) || !reflect.DeepEqual(sources, test.query) {
				t.Errorf("query arguments %v, sources %v, want %v", query, sources, test.query)
			}
			if got := buildInvocationArgs(*tool, tools.Namespaces[0]); got != test.invocation {
				t.Errorf("invocation =\n%s\nwant\n%s", got, test.invocation)
			}
		})
	}
}

// TestFiltersChangeNoOtherSDKFile: the filter's writer, its types and the
// README's passage are written only for an SDK with a filtered endpoint,
// and no SDK keeps the untyped FilterParam that FilterBy replaced (D106).
func TestFiltersChangeNoOtherSDKFile(t *testing.T) {
	outDir := t.TempDir()
	writeNestedArraysSDK(t, outDir)
	for _, name := range []string{"types.ts", "client.ts", "index.ts", "namespaces/grid.ts", "README.md", "tools/index.ts", "tools/schema.json"} {
		source, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{"withFilter", "filterParams", "FilterParam", "### Filters", "filter[", "type?: string | string[]"} {
			if strings.Contains(string(source), marker) {
				t.Errorf("%s of an SDK without a filter carries %q", name, marker)
			}
		}
	}

	_, apiOutput, parseable := loadPagedAPI(t)
	pagedDir := t.TempDir()
	writePagedSDK(t, apiOutput, parseable, pagedDir)
	for name, markers := range map[string][]string{
		"types.ts":              nil,
		"client.ts":             {"export function filterParams(", "export function withFilter("},
		"index.ts":              {"export type { CatalogListPartsFilter } from './namespaces/catalog';"},
		"namespaces/catalog.ts": {"export interface CatalogListPartsFilter {", "params: withFilter(params, 'filter')"},
		"README.md":             {"### Filters", "sdk.catalog.listParts({"},
		"tools/index.ts":        {"import type { FixturePagedApiSDK, CatalogListPartsFilter } from '../index';", "filter?: CatalogListPartsFilter;", "type?: string | string[];"},
	} {
		source := string(readRendered(t, pagedDir, name))
		for _, marker := range markers {
			if !strings.Contains(source, marker) {
				t.Errorf("%s lacks %q", name, marker)
			}
		}
		if strings.Contains(source, "FilterParam") {
			t.Errorf("%s still declares FilterParam", name)
		}
	}
}

package sdkgen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

const pagedService = "fixture-paged-api"

// loadPagedAPI loads fixture-paged-api: Catalog.listItems pages ItemView
// two to a page, up to five, beside its ids and inStock query parameters,
// sorted by its sort argument, name or sku, name unless the caller says
// (D60, amended), Catalog.listTags pages strings with the default limits
// (D60), and Catalog.listParts pages PartView, sorted by name or quantity
// and kept to the parts its filter finds, a field of every kind (D106).
// Each edit then changes the schema (sdktest.AddShelfItemsOperation).
func loadPagedAPI(t *testing.T, edits ...func(*ir.Schema) error) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	return loadPagedAPIWithHooks(t, nil, edits...)
}

// loadPagedAPIWithHooks is loadPagedAPI with the tool hooks the api
// generator runs.
func loadPagedAPIWithHooks(t *testing.T, hooks []apigen.ToolHook, edits ...func(*ir.Schema) error) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedService))
	if err != nil {
		t.Fatalf("load %s: %v", pagedService, err)
	}
	for _, edit := range edits {
		if err := edit(schema); err != nil {
			t.Fatal(err)
		}
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: pagedService,
		Clock:      nestedArraysClock,
		ToolHooks:  hooks,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: pagedService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// writePagedSDK writes the TypeScript SDK of apiOutput into dir.
func writePagedSDK(t *testing.T, apiOutput *apigen.APIOutput, parseable map[string]bool, dir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, dir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkOutput
}

// TestWriteSDKGoldenPaged pins every file of the TypeScript SDK for
// fixture-paged-api (D60): a paged method returns Page<T>, its <name>All
// iterates, and the tools take cursor and limit and return the page.
// Regenerate with:
// go test ./internal/generator/sdkgen -run TestWriteSDKGoldenPaged -update
func TestWriteSDKGoldenPaged(t *testing.T) {
	_, apiOutput, parseable := loadPagedAPI(t)
	outDir := t.TempDir()
	writePagedSDK(t, apiOutput, parseable, outDir)
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", pagedService))
}

// TestPagedEndpointsReturnAPage pins the TypeScript types of the paged
// endpoints: Page<T> as the method's result, T as the iterator's, and the
// query object a paged endpoint takes even without query parameters.
func TestPagedEndpointsReturnAPage(t *testing.T) {
	_, apiOutput, parseable := loadPagedAPI(t)
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !sdkOutput.HasPagedEndpoints || len(sdkOutput.Namespaces) != 1 {
		t.Fatalf("HasPagedEndpoints = %t, namespaces %d", sdkOutput.HasPagedEndpoints, len(sdkOutput.Namespaces))
	}
	ns := sdkOutput.Namespaces[0]
	if !ns.HasPagedEndpoints || ns.PageType != "Page" {
		t.Fatalf("namespace pages %t as %q", ns.HasPagedEndpoints, ns.PageType)
	}
	want := map[string]struct {
		output, item string
		query        bool
	}{
		"getItem":     {"ItemView", "ItemView", false},
		"listItems":   {"Page<ItemView>", "ItemView", true},
		"listParts":   {"Page<PartView>", "PartView", true},
		"listTags":    {"Page<string>", "string", true},
		"placeBasket": {"BasketView", "BasketView", false},
	}
	for _, ep := range ns.Endpoints {
		w := want[ep.Name]
		if ep.TSOutputType() != w.output || ep.TSItemType() != w.item || ep.HasQuery() != w.query {
			t.Errorf("%s = (%s, %s, query %t), want (%s, %s, %t)", ep.Name, ep.TSOutputType(), ep.TSItemType(), ep.HasQuery(), w.output, w.item, w.query)
		}
	}
}

// TestPageTypeYieldsToASchemaTypeNamedPage: a namespace that imports a
// schema type named Page names the SDK's page SDKPage.
func TestPageTypeYieldsToASchemaTypeNamedPage(t *testing.T) {
	ns := &NamespaceInfo{Endpoints: []EndpointInfo{
		{Name: "listPages", OutputType: "Page", OutputIsArray: true, Page: &apigen.PageInfo{DefaultLimit: 1, MaxLimit: 2}},
		{Name: "getPage", OutputType: "Page"},
	}}
	setPageType(ns, map[string]bool{"Page": true})
	if ns.PageType != "SDKPage" || ns.Endpoints[0].TSOutputType() != "SDKPage<Page>" || ns.Endpoints[1].TSOutputType() != "Page" {
		t.Fatalf("page type %q, outputs %s and %s", ns.PageType, ns.Endpoints[0].TSOutputType(), ns.Endpoints[1].TSOutputType())
	}
}

// TestPagedToolsTakeCursorAndLimitAndReturnThePage: a paged endpoint's
// tool (D60) binds cursor and limit to the query, invokeTool passes them in
// the method's query object, and the tool returns the page toolsutil
// builds. A sorted page's sort (D60, amended) is a query argument like any
// other, typed as the union of its values, and its description asks for
// the same sort with each next.
func TestPagedToolsTakeCursorAndLimitAndReturnThePage(t *testing.T) {
	_, apiOutput, parseable := loadPagedAPI(t, sdktest.AddShelfItemsOperation)
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	tools, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatalf("GenerateTools: %v", err)
	}
	if !tools.HasPagedReturns {
		t.Fatal("HasPagedReturns = false")
	}
	for _, test := range []struct {
		tool, item string
		query      []string
		invocation string
		sorted     bool
	}{
		{
			tool: "catalog.listItems", item: "ItemView",
			query: []string{"ids", "inStock", "sort", "cursor", "limit"},
			invocation: "{ ids: (params as CatalogListItemsParams).ids, inStock: (params as CatalogListItemsParams).inStock, " +
				"sort: (params as CatalogListItemsParams).sort, cursor: (params as CatalogListItemsParams).cursor, limit: (params as CatalogListItemsParams).limit }",
			sorted: true,
		},
		{
			tool: "catalog.listTags", item: "string",
			query:      []string{"cursor", "limit"},
			invocation: "{ cursor: (params as CatalogListTagsParams).cursor, limit: (params as CatalogListTagsParams).limit }",
		},
		{
			tool: "catalog.listShelfItems", item: "ItemView",
			query: []string{"cursor", "limit"},
			invocation: "(params as CatalogListShelfItemsParams).shelfId, " +
				"{ maker: (params as CatalogListShelfItemsParams).maker, color: (params as CatalogListShelfItemsParams).color }, " +
				"{ cursor: (params as CatalogListShelfItemsParams).cursor, limit: (params as CatalogListShelfItemsParams).limit }",
		},
	} {
		t.Run(test.tool, func(t *testing.T) {
			tool := tools.GetToolByName(test.tool)
			if tool == nil {
				t.Fatalf("no tool %s", test.tool)
			}
			if got, want := tool.Returns, toolsutil.BuildPageReturnSchema(test.item, apiOutput.Scalars); !reflect.DeepEqual(got, want) {
				t.Errorf("returns = %+v, want %+v", got, want)
			}
			sentence := toolsutil.PageSentence
			if test.sorted {
				sentence = toolsutil.SortedPageSentence("sort")
			}
			if !strings.HasSuffix(tool.Description, sentence) {
				t.Errorf("description = %q", tool.Description)
			}
			sort, sorted := tool.Parameters.Properties["sort"]
			if sorted != test.sorted || sorted != tool.Parameters.Holds().Sort {
				t.Errorf("parameters hold a sort: %t, want %t", sorted, test.sorted)
			}
			if sorted {
				if !sort.Sort || !reflect.DeepEqual(sort.Enum, []string{"name", "-name", "sku", "-sku"}) {
					t.Errorf("sort = %+v", sort)
				}
				if got, want := tool.ParamTypes["sort"], "'name' | '-name' | 'sku' | '-sku'"; got != want {
					t.Errorf("sort is typed %s, want %s", got, want)
				}
			}
			for _, name := range []string{"cursor", "limit"} {
				if _, ok := tool.Parameters.Properties[name]; !ok || tool.ParamTypes[name] == "" {
					t.Errorf("parameters have no %s: %v", name, tool.Parameters.Properties)
				}
			}
			var query []string
			for _, argument := range tool.MCPBinding.Arguments {
				if argument.Kind == "query" {
					if argument.Target != "query."+argument.ToolParameter || argument.Required {
						t.Errorf("query argument %+v", argument)
					}
					query = append(query, argument.ToolParameter)
				}
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
			ns := tools.Namespaces[0]
			if got := buildInvocationArgs(*tool, ns); got != test.invocation {
				t.Errorf("invocation =\n%s\nwant\n%s", got, test.invocation)
			}
		})
	}
}

// TestPagedToolDocumentsRoundTripThroughIRWireTypes: tools/schema.json of a
// paged API, whose returns carry properties, decodes into ir.ToolManifest
// with unknown fields refused and encodes back to the same document, as
// tools/mcp-binding.json does into ir.ToolBindingManifest.
func TestPagedToolDocumentsRoundTripThroughIRWireTypes(t *testing.T) {
	_, apiOutput, parseable := loadPagedAPI(t)
	outDir := t.TempDir()
	writePagedSDK(t, apiOutput, parseable, outDir)

	rendered := readRendered(t, outDir, "tools/schema.json")
	var manifest ir.ToolManifest
	decodeDisallowingUnknownFields(t, rendered, &manifest)
	tool, found := manifestTool(manifest, "catalog.listItems")
	if !found || tool.Returns.Properties["next"].Type.Missing() || !reflect.DeepEqual(tool.Returns.Required, []string{"items", "next"}) {
		t.Fatalf("catalog.listItems returns = %+v", tool.Returns)
	}
	// The sort (D60, amended) decodes with its mark and its values.
	if sort := tool.Parameters.Properties["sort"]; !sort.Sort || !sort.Type.Is("string") ||
		!reflect.DeepEqual(sort.Enum, []any{"name", "-name", "sku", "-sku"}) {
		t.Errorf("catalog.listItems sort decoded as %+v", sort)
	}
	if tags, _ := manifestTool(manifest, "catalog.listTags"); len(tags.Parameters.Properties) != 2 {
		t.Errorf("catalog.listTags, which does not sort, takes %v", tags.Parameters.Properties)
	}
	assertManifestRoundTrip(t, rendered, manifest, len(manifest.Tools))

	binding := readRendered(t, outDir, "tools/mcp-binding.json")
	var bindings ir.ToolBindingManifest
	decodeDisallowingUnknownFields(t, binding, &bindings)
	assertManifestRoundTrip(t, binding, bindings, len(bindings.Tools))
}

// TestToolHookRenamesTheSortKey: the sort key a tool hook leaves (D60,
// amended) is the one every TypeScript tool document writes and
// tools/index.ts types, and the digest hashes; none keeps the core key.
func TestToolHookRenamesTheSortKey(t *testing.T) {
	rename := apigen.ToolHook{Name: "vendor-sort", Edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
		tools.Keys.Sort = "x-vendor-sort"
		return nil
	}}
	_, core, parseable := loadPagedAPI(t)
	_, renamed, _ := loadPagedAPIWithHooks(t, []apigen.ToolHook{rename})
	coreDir, renamedDir := t.TempDir(), t.TempDir()
	writePagedSDK(t, core, parseable, coreDir)
	writePagedSDK(t, renamed, parseable, renamedDir)

	for _, name := range []string{"tools/index.ts", "tools/schema.json"} {
		if rendered := string(readRendered(t, renamedDir, name)); strings.Contains(rendered, apigen.DefaultToolSortKey) {
			t.Errorf("%s still carries the core key %s", name, apigen.DefaultToolSortKey)
		}
	}
	for dir, key := range map[string]string{coreDir: apigen.DefaultToolSortKey, renamedDir: "x-vendor-sort"} {
		schema := string(readRendered(t, dir, "tools/schema.json"))
		if want := `"type":"string","` + key + `":true}`; !strings.Contains(schema, want) {
			t.Errorf("tools/schema.json lacks %s", want)
		}
		if want := "'" + key + "'?: boolean;"; !strings.Contains(string(readRendered(t, dir, "tools/index.ts")), want) {
			t.Errorf("tools/index.ts lacks %s", want)
		}
	}

	// The ir wire types spell the core's keys, so a renamed document is
	// read as plain JSON.
	digests := func(dir string) map[string]string {
		t.Helper()
		var manifest struct {
			Tools []struct {
				Name              string `json:"name"`
				InputSchemaDigest string `json:"inputSchemaDigest"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(readRendered(t, dir, "tools/schema.json"), &manifest); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, tool := range manifest.Tools {
			out[tool.Name] = tool.InputSchemaDigest
		}
		return out
	}
	before, after := digests(coreDir), digests(renamedDir)
	for name, digest := range before {
		if changed := after[name] != digest; changed != (name == "catalog.listItems" || name == "catalog.listParts") {
			t.Errorf("%s's digest changed: %t; only the sorted tools' should", name, changed)
		}
	}
}

// TestPagesChangeNoOtherSDKFile: Page, pageItems and the paged readme and
// tool shapes are written only for an SDK with a paged endpoint.
func TestPagesChangeNoOtherSDKFile(t *testing.T) {
	outDir := t.TempDir()
	writeNestedArraysSDK(t, outDir)
	for _, name := range []string{"types.ts", "client.ts", "namespaces/grid.ts", "README.md", "tools/index.ts", "tools/schema.json"} {
		source, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{"Page<", "pageItems", "Paged Lists", "AsyncIterable", `"properties": {` + "\n          \"items\""} {
			if strings.Contains(string(source), marker) {
				t.Errorf("%s of an SDK without pages carries %q", name, marker)
			}
		}
	}
}

// TestPagedSDKCompilesAndRuns type-checks the TypeScript SDK of
// fixture-paged-api, with Catalog.listShelfItems added, against its
// generated types package, then runs test_paged_api.js against a server
// that pages as D60's routers do: the first page, the next and the last,
// the limit refused before any request, the iterators collecting every
// item lazily, stopping at a null next and ending with an error, and
// invokeTool passing cursor and limit. The server sorts listItems as D60's
// amendment says, so the iterator and invokeTool are seen sending the sort
// with every page, and the SDK sends a sort as it is, for the server to
// refuse one it does not offer. The server keeps listParts to the parts
// the request's filters find (D106), read with a reader of its own, and
// Catalog.findParts is added, a list with a filter alone under another
// name: the typed filter is seen on the wire for each kind and operator,
// ne and notIn among them (D112), which keep a part whose field is null,
// beside a sort and with every page, through invokeTool, and as
// runtime/http/testdata/filter_parity.json's queries write it. tsc holds
// filterUsage, a caller of the filtered methods, to the filters' types.
func TestPagedSDKCompilesAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}

	schema, apiOutput, parseable := loadPagedAPI(t, sdktest.AddShelfItemsOperation, sdktest.AddFindPartsOperation)
	typesDir := writeTypesPackage(t, bunPath, schema, pagedService, tempRoot)
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", pagedService)
	writePagedSDK(t, apiOutput, parseable, sdkDir)
	if err := os.WriteFile(filepath.Join(sdkDir, "filter_usage.ts"), []byte(filterUsage), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}
	requireToolsBuilt(t, sdkDir)

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_paged_api.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir, "FILTER_PARITY="+filterParityFile(t))
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("test_paged_api.js failed: %v\n%s", err, out)
	}
}

// filterUsage is a caller of fixture-paged-api's filtered methods, which
// tsc compiles with the SDK (D106): a filter of every field type-checks,
// through the method, its iterator and invokeTool, and each line under a
// @ts-expect-error is one the filter's type refuses, an operator its
// field's kind does not take, a field FilterBy does not name and a value
// of another type. tsc fails on a @ts-expect-error line it accepts.
const filterUsage = `import { FixturePagedApiSDK } from './index';
import type { CatalogFindPartsFilter, CatalogListPartsFilter } from './index';
import { invokeTool } from './tools';
import { PartKind } from '@schemas/fixture-paged-api-types';

export async function filterUsage(sdk: FixturePagedApiSDK): Promise<string[]> {
  const filter: CatalogListPartsFilter = {
    id: '00000000-0000-4000-8000-000000000001',
    name: { contains: 'x', in: ['axle', 'bracket'], ne: 'clamp' },
    kind: { in: [PartKind.Bolt, PartKind.Nut], notIn: [PartKind.Washer] },
    quantity: { gte: 1, lt: 9, ne: 7, notIn: [5, 6] },
    weight: 2.5,
    price: { gte: '1.00', notIn: ['9.99'] },
    discontinued: false,
    madeAt: { gt: '2026-01-01T00:00:00Z' },
    retiredOn: { isNull: true },
    supplierId: { isNull: false, ne: '7c1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f' },
  };
  const page = await sdk.catalog.listParts({ filter, sort: 'name', limit: 2 });
  const names = page.items.map((part) => part.name);
  for await (const part of sdk.catalog.listPartsAll({ filter })) {
    names.push(part.name);
  }
  const where: CatalogFindPartsFilter = { kind: PartKind.Washer };
  names.push(...(await sdk.catalog.findParts({ where })).map((part) => part.name));
  await invokeTool(sdk, 'catalog.listParts', { filter, limit: 1 });
  await invokeTool(sdk, 'catalog.findParts', { where });

  // @ts-expect-error a number takes no contains
  await sdk.catalog.listParts({ filter: { quantity: { contains: '1' } } });
  // @ts-expect-error a field that is never null takes no isNull
  await sdk.catalog.listParts({ filter: { name: { isNull: true } } });
  // @ts-expect-error an enum is compared for equality alone
  await sdk.catalog.listParts({ filter: { kind: { gt: PartKind.Bolt } } });
  // @ts-expect-error the filter holds the fields FilterBy names
  await sdk.catalog.listParts({ filter: { sku: 'axle-sku' } });
  // @ts-expect-error a number's value is a number
  await sdk.catalog.listParts({ filter: { quantity: '1' } });
  // @ts-expect-error an in takes a list
  await sdk.catalog.listPartsAll({ filter: { id: { in: '00000000-0000-4000-8000-000000000001' } } });
  // @ts-expect-error a notIn takes a list
  await sdk.catalog.listParts({ filter: { kind: { notIn: PartKind.Bolt } } });
  // @ts-expect-error a notIn's values are the field's
  await sdk.catalog.listParts({ filter: { discontinued: { notIn: ['true'] } } });
  // @ts-expect-error a ne takes one value
  await sdk.catalog.listParts({ filter: { quantity: { ne: [1, 2] } } });
  // @ts-expect-error a ne's value is the field's
  await sdk.catalog.findParts({ where: { kind: { ne: 'bolt' } } });
  // @ts-expect-error the tool's filter is the method's
  await invokeTool(sdk, 'catalog.findParts', { where: { quantity: { contains: '1' } } });
  return names;
}
`

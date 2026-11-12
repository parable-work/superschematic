package gosdkgen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestFilteredMethodsTakeATypedFilter pins the Go SDK's filtered endpoints
// (D106): the query struct holds the filter under the argument's name, a
// struct named as the query struct is, whose members are the HTTP
// runtime's typed filters, Equals, Ordered or Text as the field's kind
// says, of the Go type an item of a list query parameter of the field's
// type has: the types package's enum and scalars, as the Go server's
// filter holds them. An endpoint whose only query is its filter still
// takes a query struct.
func TestFilteredMethodsTakeATypedFilter(t *testing.T) {
	_, apiOutput := loadPagedAPI(t, sdktest.AddFindPartsOperation)
	sdkOutput, err := Generate(apiOutput, pagedSDKModule, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !sdkOutput.HasFilteredEndpoints {
		t.Fatal("HasFilteredEndpoints = false")
	}
	wantFields := []FilterField{
		{"id", "Id", "types.IdentityUUID", "filterparse.Equals[types.IdentityUUID]"},
		{"name", "Name", "string", "filterparse.Text[string]"},
		{"kind", "Kind", "types.PartKind", "filterparse.Equals[types.PartKind]"},
		{"quantity", "Quantity", "int64", "filterparse.Ordered[int64]"},
		{"weight", "Weight", "float64", "filterparse.Ordered[float64]"},
		{"price", "Price", "types.GenericDecimal", "filterparse.Ordered[types.GenericDecimal]"},
		{"discontinued", "Discontinued", "bool", "filterparse.Equals[bool]"},
		{"madeAt", "MadeAt", "types.TemporalDateTime", "filterparse.Ordered[types.TemporalDateTime]"},
		{"retiredOn", "RetiredOn", "types.TemporalDate", "filterparse.Ordered[types.TemporalDate]"},
		{"supplierId", "SupplierId", "types.IdentityUUID", "filterparse.Equals[types.IdentityUUID]"},
	}
	want := map[string]struct {
		structName, goName, queryStruct string
		queryParams                     int
	}{
		"ListParts": {"CatalogListPartsFilter", "Filter", "CatalogListPartsQueryParams", 1},
		"FindParts": {"CatalogFindPartsFilter", "Where", "CatalogFindPartsQueryParams", 0},
	}
	for _, ep := range sdkOutput.Namespaces[0].Endpoints {
		w, ok := want[ep.MethodName]
		if !ok {
			if ep.Filter != nil || ep.FilterStructName != "" || ep.FilterGoName != "" || ep.FilterFields != nil {
				t.Errorf("%s, which takes no filter, has %s", ep.MethodName, ep.FilterStructName)
			}
			continue
		}
		delete(want, ep.MethodName)
		if ep.Filter == nil || ep.FilterStructName != w.structName || ep.FilterGoName != w.goName ||
			ep.QueryStructName != w.queryStruct || !ep.HasQueryParams || len(ep.QueryParams) != w.queryParams {
			t.Errorf("%s = (%s, %s, %s, query struct %t, %d query parameters), want %+v",
				ep.MethodName, ep.FilterStructName, ep.FilterGoName, ep.QueryStructName, ep.HasQueryParams, len(ep.QueryParams), w)
		}
		if !reflect.DeepEqual(ep.FilterFields, wantFields) {
			t.Errorf("%s's filter has the members\n%+v\nwant\n%+v", ep.MethodName, ep.FilterFields, wantFields)
		}

		// A namespace of the endpoint alone imports filterparse and, for
		// its filter's JSON, fmt; the SDK's own runtime only when the
		// endpoint has a query parameter or pages.
		needsFmt, needsURL, needsRuntime, _, _, _, needsFilterparse := namespaceImportFlags(NamespaceInfo{Endpoints: []EndpointInfo{ep}})
		if !needsURL || !needsFilterparse || !needsFmt || needsRuntime != (ep.MethodName == "ListParts") {
			t.Errorf("%s alone imports net/url %t, filterparse %t, fmt %t, runtime %t", ep.MethodName, needsURL, needsFilterparse, needsFmt, needsRuntime)
		}
	}
	if len(want) > 0 {
		t.Errorf("missing endpoints %v", want)
	}
}

// TestRuntimeReplacesFollowTheFilter: an SDK with a filtered endpoint
// (D106) builds on the HTTP runtime, so in a project with [paths] its
// go.mod replaces the HTTP runtime and the modules that runtime requires
// from a checkout, but one the types module's go.mod already replaces; an
// SDK without a filtered endpoint replaces none.
func TestRuntimeReplacesFollowTheFilter(t *testing.T) {
	_, apiOutput := loadPagedAPI(t)
	sdkOutput, err := Generate(apiOutput, pagedSDKModule, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := sdkOutput.RuntimeReplaces(); got != nil {
		t.Fatalf("no path is set, yet the SDK replaces %v", got)
	}
	root := t.TempDir()
	sdkDir := filepath.Join(root, "out", "sdk", "go", pagedService)
	paths := naming.LocalPaths{
		HTTPRuntimeGo:   filepath.Join(root, "runtime", "http", "go"),
		SchemaRuntimeGo: filepath.Join(root, "runtime", "schema", "go"),
		SchemaIR:        filepath.Join(root, "ir"),
	}
	if err := SetReplacePaths(sdkOutput, paths, sdkDir); err != nil {
		t.Fatal(err)
	}
	n := sdkOutput.Naming
	want := []ModuleReplace{
		{n.HTTPRuntimeGoModule, "../../../../runtime/http/go"},
		{n.SchemaRuntimeGoModule, "../../../../runtime/schema/go"},
		{n.SchemaIRGoModule, "../../../../ir"},
	}
	if got := sdkOutput.RuntimeReplaces(); !reflect.DeepEqual(got, want) {
		t.Errorf("replaces = %v, want %v", got, want)
	}
	sdkOutput.TypeModuleReplaces = []ModuleReplace{{n.SchemaIRGoModule, "../../../../ir"}}
	if got := sdkOutput.RuntimeReplaces(); !reflect.DeepEqual(got, want[:2]) {
		t.Errorf("beside the types module's replace of the schema IR, replaces = %v, want %v", got, want[:2])
	}

	_, plain := loadNestedArraysAPI(t)
	plainOutput, err := Generate(plain, "example.com/schemas/sdk/go/fixture-nested-arrays-api", "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := SetReplacePaths(plainOutput, paths, sdkDir); err != nil {
		t.Fatal(err)
	}
	if got := plainOutput.RuntimeReplaces(); plainOutput.HasFilteredEndpoints || got != nil {
		t.Errorf("an SDK without a filter replaces %v", got)
	}
}

// TestFiltersChangeNoOtherSDKFile: the filter's struct, its reader and the
// HTTP runtime it builds on are written only for an SDK with a filtered
// endpoint (D106), and no SDK keeps the untyped filters parameter that
// FilterBy replaced.
func TestFiltersChangeNoOtherSDKFile(t *testing.T) {
	_, apiOutput := loadNestedArraysAPI(t)
	sdkDir := t.TempDir()
	writeNestedArraysSDK(t, apiOutput, sdkDir, t.TempDir())
	for _, name := range []string{"go.mod", "runtime/runtime.go", "namespaces/common.go", "namespaces/grid.go", "README.md"} {
		source, err := os.ReadFile(filepath.Join(sdkDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{"filterparse", "filterMember", "runtime/http/go", "Filtered lists", "## Filters"} {
			if strings.Contains(string(source), marker) {
				t.Errorf("%s of an SDK without a filter carries %s", name, marker)
			}
		}
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "golden", pagedService, "namespaces", "catalog.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leftover := range []string{"filters []filterparse.Filter,", "filterparse.Serialize"} {
		if strings.Contains(string(golden), leftover) {
			t.Errorf("the paged SDK still carries %s", leftover)
		}
	}
}

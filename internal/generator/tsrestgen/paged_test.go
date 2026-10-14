package tsrestgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

const pagedAPI = "fixture-paged-api"

// pagedFixture is fixture-paged-api with apigen's endpoints for it:
// catalog.listItems pages ItemView two to a page, up to five, beside two
// query arguments; catalog.listTags pages strings with the default limits
// and declares no argument; getItem and placeBasket do not page.
func pagedFixture(t *testing.T) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedAPI))
	if err != nil {
		t.Fatalf("load %s: %v", pagedAPI, err)
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

// TestGeneratePagedShape: a @paginated operation carries its limits, takes
// its page as an argument even when it declares none, and answers a Page of
// its list's elements; its cursor and limit are not query parameters of its
// own. The operations that do not page are as before, and an API without a
// paged operation imports nothing of the page.
func TestGeneratePagedShape(t *testing.T) {
	output := generateFixture(t, pagedFixture(t))
	if !output.HasPages {
		t.Error("HasPages = false for fixture-paged-api")
	}
	byName := endpointsByName(output)
	for _, tc := range []struct {
		name         string
		page         *apigen.PageInfo
		output       string
		queryParams  int
		hasArguments bool
	}{
		{"listItems", &apigen.PageInfo{DefaultLimit: 2, MaxLimit: 5, Operation: "Catalog.listItems"}, "Page<ItemView>", 2, true},
		{"listTags", &apigen.PageInfo{DefaultLimit: 20, MaxLimit: 100, Operation: "Catalog.listTags"}, "Page<string>", 0, true},
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
		case tc.page != nil && (ep.Page == nil || *ep.Page != *tc.page):
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
	if generateFixtureAPI(t).HasPages {
		t.Error("HasPages = true for fixture-api, which declares no @paginated operation")
	}
}

// TestGeneratedPagedRouter type-checks the generated package for
// fixture-paged-api with a typed implementation of its catalog staged
// beside it, so tsc holds a paged method to args.page and a Page result,
// then serves that implementation through the generated router under bun:
// the first page, each next to the last, whose next is null, the filters
// on every page, a page of a primitive, a limit past the max and a forged
// cursor refused with page_parity.json's 400s, and every reader vector of
// the two routes' limits answered over HTTP as the runtime's reader answers
// it.
func TestGeneratedPagedRouter(t *testing.T) {
	tree := materializeAPI(t, pagedFixture(t))
	tree.stage(t, "paged_implementation.ts")
	tree.typeCheck(t)
	tree.env = append(tree.env, "PAGE_PARITY="+filepath.Join(testpaths.RepoRoot(t), "runtime", "http", "testdata", "page_parity.json"))
	tree.runTest(t, "paged_runtime.test.ts")
}

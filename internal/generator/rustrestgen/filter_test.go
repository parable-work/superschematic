package rustrestgen

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// filterAlone keeps fixture-paged-api's catalog.listParts alone, without
// its page and its sort: a list that filters (D106) and does not page.
func filterAlone(schema *ir.Schema) {
	for _, set := range schema.OperationSets {
		set.Operations = slices.DeleteFunc(set.Operations, func(op *ir.FieldDef) bool { return op.Name != "listParts" })
		for _, op := range set.Operations {
			op.Page = nil
			op.Arguments = slices.DeleteFunc(op.Arguments, func(arg *ir.ArgumentDef) bool { return arg.Sort != nil })
		}
	}
}

// TestAFilteredListWithoutAPage: a list with a FilterBy argument that does
// not page has an Args struct that holds its filter alone, which the
// router's decode reads from the query and check holds to the route's
// rules; its method returns the list, the interfaces name the runtime for
// the filter's types, and the router names no page.
func TestAFilteredListWithoutAPage(t *testing.T) {
	output := generateRustAPI(t, pagedService, false, "", nil, filterAlone)
	if len(output.Endpoints) != 1 || !output.HasFilters() || output.HasPages() {
		t.Fatalf("endpoints = %d, HasFilters = %v, HasPages = %v", len(output.Endpoints), output.HasFilters(), output.HasPages())
	}
	endpoint := output.Endpoints[0]
	if !endpoint.HasArgs() || !endpoint.ReadsQuery() || endpoint.OutputRustType != "Vec<types::PartView>" {
		t.Errorf("listParts: HasArgs = %v, ReadsQuery = %v, output = %s", endpoint.HasArgs(), endpoint.ReadsQuery(), endpoint.OutputRustType)
	}
	if err := SetReplacePaths(output, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, "/repo/schemas/dist/api/"+pagedService); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	for file, wants := range map[string][]string{
		"interfaces.rs": {
			"use crate::runtime;\n",
			"    /// holds at once. `CatalogListPartsFilter::default()` filters by nothing.\n    pub filter: CatalogListPartsFilter,\n}",
			"    /// The parts, in order of their names unless the caller sorts them by\n    ///\n    /// It filters its list (FilterBy, D106)",
			"    /// member among them.\n    async fn list_parts(&self, ctx: RequestContext, args: CatalogListPartsArgs) -> Result<Vec<types::PartView>, ApiError>;",
		},
		"router.rs": {
			"/// router reads them: path, query, the filter, input, then body fields.\n",
			"fn decode_catalog_list_parts(query: &QueryValues) -> Result<CatalogListPartsArgs, ApiError> {\n    Ok(CatalogListPartsArgs {\n        filter: decode_catalog_list_parts_filter(query)?,\n    })\n}",
			"    /// rules, the filter by its fields', then the input by its type's, a\n    /// refusal the 400 the router answers.\n    pub fn check(&self) -> Result<(), ApiError> {\n        self.filter.check()?;\n        Ok(())\n    }",
			"        .list_parts(ctx, args)\n        .await;\n",
		},
	} {
		text, err := os.ReadFile(filepath.Join(dir, "src", file))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(text), want) {
				t.Errorf("%s does not contain %q", file, want)
			}
		}
		if strings.Contains(string(text), "PageSpec") || strings.Contains(string(text), "PageRequest") {
			t.Errorf("%s names a page", file)
		}
	}
}

// TestFilterFieldsOfOneRustName: two fields of a filter that are one Rust
// identifier, and a filter whose argument is another argument's field, are
// refused, naming both.
func TestFilterFieldsOfOneRustName(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedService))
	if err != nil {
		t.Fatalf("load %s: %v", pagedService, err)
	}
	listParts := func() *ir.FieldDef {
		for _, set := range schema.OperationSets {
			for _, op := range set.Operations {
				if op.Name == "listParts" {
					return op
				}
			}
		}
		t.Fatal("fixture-paged-api has no listParts")
		return nil
	}()
	for _, field := range listParts.Filter.Fields {
		if field.Name == "madeAt" {
			twin := *field
			twin.Name = "made_at"
			listParts.Filter.Fields = append(listParts.Filter.Fields, &twin)
		}
	}
	_, err = generateFrom(schema, apiSource{}, Options{SchemaName: pagedService})
	if want := "operation catalog.listParts filter fields madeAt and made_at are both the Rust field made_at"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want one containing %q", err, want)
	}

	listParts.Filter.Fields = listParts.Filter.Fields[:len(listParts.Filter.Fields)-1]
	listParts.Filter.Argument = "page"
	_, err = generateFrom(schema, apiSource{}, Options{SchemaName: pagedService})
	if want := "operation catalog.listParts arguments its page and page are both the Rust field page"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want one containing %q", err, want)
	}
}

// TestAFilteredListWithoutAPageServesItsFilter runs cargo test on the
// crate of fixture-paged-api's catalog.listParts alone, without its page
// (filterAlone), with filterAloneRouterTest: the router answers the list
// its filter keeps, refuses a field the FilterBy does not name and a value
// its field's type refuses with the 400 at filter[field], and leaves a
// page's parameters alone; and check refuses a built filter the same.
func TestAFilteredListWithoutAPageServesItsFilter(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedService))
	if err != nil {
		t.Fatalf("load %s: %v", pagedService, err)
	}
	filterAlone(schema)
	cargoTestAPICrate(t, pagedService, schema, "filter_alone", filterAloneRouterTest)
}

// filterAloneRouterTest is tests/filter_alone.rs of the crate of
// fixture-paged-api's catalog.listParts without its page, with API_CRATE
// and RUNTIME_CRATE replaced by the crates' module names. Its catalog
// holds two parts, which it keeps by their kind.
const filterAloneRouterTest = `use std::sync::Arc;

use API_CRATE::runtime::filter::Equals;
use API_CRATE::{build_router, types, CatalogImplementation, CatalogListPartsArgs, CatalogListPartsFilter, Implementations};
use RUNTIME_CRATE::{ApiError, RequestContext};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::{Request, StatusCode};
use serde_json::{json, Value};
use tower::ServiceExt;

fn part(index: usize, name: &str, kind: &str) -> types::PartView {
    serde_json::from_value(json!({
        "id": format!("00000000-0000-4000-8000-00000000001{index}"),
        "name": name,
        "kind": kind,
        "quantity": 10,
        "weight": 1.5,
        "price": "2.50",
        "discontinued": false,
        "madeAt": "2026-01-01T00:00:00Z",
        "retiredOn": null,
        "supplierId": null,
    }))
    .unwrap()
}

struct Catalog;

#[async_trait]
impl CatalogImplementation for Catalog {
    async fn list_parts(&self, _ctx: RequestContext, args: CatalogListPartsArgs) -> Result<Vec<types::PartView>, ApiError> {
        let parts = vec![part(1, "Anchor bolt", "bolt"), part(2, "Hex nut", "nut")];
        let Some(kind) = args.filter.kind else {
            return Ok(parts);
        };
        let kept = |part: &types::PartView| kind.eq.is_none_or(|eq| eq == part.kind) && (kind.in_.is_empty() || kind.in_.contains(&part.kind));
        Ok(parts.into_iter().filter(kept).collect())
    }
}

async fn get(uri: &str) -> (StatusCode, Value) {
    let request = Request::get(uri).body(Body::empty()).unwrap();
    let response = build_router(Implementations { catalog: Arc::new(Catalog) }).oneshot(request).await.unwrap();
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, serde_json::from_slice(&bytes).unwrap())
}

// The names of the list the router answers.
fn names(body: &Value) -> Vec<&str> {
    body["data"].as_array().unwrap().iter().map(|part| part["name"].as_str().unwrap()).collect()
}

#[tokio::test]
async fn a_list_that_does_not_page_keeps_what_its_filter_finds() {
    for (query, want) in [
        ("", vec!["Anchor bolt", "Hex nut"]),
        ("?filter[kind]=nut", vec!["Hex nut"]),
        ("?filter[kind][in]=bolt,nut", vec!["Anchor bolt", "Hex nut"]),
        ("?filter[kind]=washer", vec![]),
        // A page's parameters are no arguments of a list that does not page.
        ("?limit=1&cursor=e30&sort=-name&filter[kind]=bolt", vec!["Anchor bolt"]),
    ] {
        let (status, body) = get(&format!("/api/parts{query}")).await;
        assert_eq!(status, StatusCode::OK, "{query}: {body}");
        assert_eq!(names(&body), want, "{query}");
    }
}

#[tokio::test]
async fn a_refused_filter_is_the_400_at_its_field() {
    for (query, parameter, reason) in [
        ("filter[kind]=gear", "filter[kind]", "expected one of bolt, nut, washer"),
        ("filter[kind][lt]=nut", "filter[kind]", "takes no operator lt; expected one of eq, in"),
        ("filter[quantity]=many", "filter[quantity]", "expected an integer"),
        ("filter[madeAt]=yesterday", "filter[madeAt]", "expected an RFC 3339 timestamp"),
        ("filter[id]=not-a-uuid", "filter[id]", "expected a UUID"),
    ] {
        let (status, body) = get(&format!("/api/parts?{query}")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{query}: {body}");
        assert_eq!(body["detail"], format!("Invalid query parameter {parameter}: {reason}"), "{query}");
        assert_eq!(body["details"]["parameter"], parameter, "{query}");
    }

    // check refuses a built filter as the router refuses the request.
    let (status, body) = get("/api/parts?filter[kind][isNull]=true").await;
    let filter = CatalogListPartsFilter { kind: Some(Equals { is_null: Some(true), ..Default::default() }), ..Default::default() };
    let refused = CatalogListPartsArgs { filter }.check().unwrap_err();
    assert_eq!((status, &body["detail"]), (refused.status, &json!(refused.message)));
    assert!(CatalogListPartsArgs { filter: CatalogListPartsFilter::default() }.check().is_ok());
}
`

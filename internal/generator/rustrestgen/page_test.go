package rustrestgen

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
)

// pagedService is the core fixture whose lists page (D60): catalog.listItems
// pages ItemView two to a page, up to five, beside two query arguments,
// sorted by name or sku (D60, amended), catalog.listTags pages strings by
// the default limits with no argument, and catalog.listParts pages
// PartView, sorted by name or quantity, kept to what its FilterBy argument
// finds, a field of every kind a filter compares (D106).
const pagedService = "fixture-paged-api"

// TestWriteRustAPIPagedGolden pins the crate of fixture-paged-api. A paged
// operation's Args carry page, which the router reads after its query
// arguments by the Args' PAGE and check holds to it, and its method returns
// runtime::page::Page of the list's element; a sorted one's PAGE holds its
// sort, which the Args carry in page alone, the router's decode unbinds
// and its handler binds the next to; a filtered one's Args carry its
// filter, a struct of the operation with a member per field, which the
// router reads by the Args' FILTER before the page and check holds to it
// (D106), a date's and a decimal's value by the scalar crate's parse, which
// the crate then depends on; an operation that does not page is as before.
// Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIPagedGolden -update
func TestWriteRustAPIPagedGolden(t *testing.T) {
	output := generateRustAPI(t, pagedService, false, "", nil)
	if !output.HasPages() {
		t.Fatal("HasPages is false for fixture-paged-api")
	}
	pages := map[string]string{}
	for _, endpoint := range output.Endpoints {
		if endpoint.Page != nil {
			pages[endpoint.Name] = endpoint.OutputRustType
			if !endpoint.HasArgs() {
				t.Errorf("%s pages but has no Args", endpoint.Name)
			}
		}
	}
	want := map[string]string{
		"listItems": "runtime::page::Page<types::ItemView>",
		"listParts": "runtime::page::Page<types::PartView>",
		"listTags":  "runtime::page::Page<String>",
	}
	if !maps.Equal(pages, want) {
		t.Errorf("paged results = %v, want %v", pages, want)
	}
	if !output.HasSortedPages() {
		t.Error("HasSortedPages is false for fixture-paged-api")
	}
	if !output.HasFilters() {
		t.Error("HasFilters is false for fixture-paged-api")
	}
	for _, endpoint := range output.Endpoints {
		for _, arg := range endpoint.QueryArgs {
			if arg.Name == "sort" || arg.Name == "filter" {
				t.Errorf("%s: %s is a query field of its Args", endpoint.Name, arg.Name)
			}
		}
		if filtered := endpoint.Filter != nil; filtered != (endpoint.Name == "listParts") {
			t.Errorf("%s: Filter = %+v", endpoint.Name, endpoint.Filter)
		}
	}

	generated := writeGoldenAPI(t, pagedService, output)
	for file, wants := range map[string][]string{
		"src/interfaces.rs": {
			"use crate::runtime;\n",
			"    pub in_stock: Option<bool>,\n    /// The page asked for (D60)",
			"    pub page: runtime::page::PageRequest,\n}",
			"async fn list_items(&self, ctx: RequestContext, args: CatalogListItemsArgs) -> Result<runtime::page::Page<types::ItemView>, ApiError>;",
			"async fn list_tags(&self, ctx: RequestContext, args: CatalogListTagsArgs) -> Result<runtime::page::Page<String>, ApiError>;",
			"async fn get_item(&self, ctx: RequestContext, args: CatalogGetItemArgs) -> Result<types::ItemView, ApiError>;",
			"    pub filter: CatalogListPartsFilter,\n    /// The page asked for (D60)",
			"#[derive(Debug, Clone, Default)]\npub struct CatalogListPartsFilter {\n",
			"    /// Filters by `id`: eq, ne, in, notIn.\n    pub id: Option<runtime::filter::Equals<types::IdentityUUID>>,\n",
			"    pub name: Option<runtime::filter::Text<String>>,\n",
			"    pub kind: Option<runtime::filter::Equals<types::PartKind>>,\n",
			"    pub quantity: Option<runtime::filter::Ordered<types::GenericInt64>>,\n",
			"    pub weight: Option<runtime::filter::Ordered<f64>>,\n",
			"    pub price: Option<runtime::filter::Ordered<types::GenericDecimal>>,\n",
			"    pub discontinued: Option<runtime::filter::Equals<bool>>,\n",
			"    pub made_at: Option<runtime::filter::Ordered<types::TemporalDateTime>>,\n",
			"    /// Filters by `retiredOn`: eq, ne, in, notIn, gt, gte, lt, lte, isNull.\n    pub retired_on: Option<runtime::filter::Ordered<types::TemporalDate>>,\n",
			"    pub supplier_id: Option<runtime::filter::Equals<types::IdentityUUID>>,\n}",
			"    /// It filters its list (FilterBy, D106): keep the items every member\n    /// of `args.filter` that is set holds for",
			"async fn list_parts(&self, ctx: RequestContext, args: CatalogListPartsArgs) -> Result<runtime::page::Page<types::PartView>, ApiError>;",
		},
		"Cargo.toml": {
			// The scalar crate, whose parse reads a filter's date and decimal.
			"\nsuperscalar = { path = \"../../../../third_party/superscalar/crates/core\" }\n",
		},
		"src/router.rs": {
			"use superschematic_http_runtime::page::{PageSpec, SortSpec};\n",
			`pub const PAGE: PageSpec = PageSpec::new(2, 5).sorted(SortSpec::new("sort", &["name", "sku"], "name"));`,
			"pub const PAGE: PageSpec = PageSpec::new(20, 100);",
			"        page: CatalogListItemsArgs::PAGE.unbind(CatalogListItemsArgs::PAGE.query(query)?)?,\n",
			"        page: CatalogListTagsArgs::PAGE.query(query)?,\n",
			"    let page = args.page.clone();\n",
			"        .await\n        .and_then(|listed| listed.bind(&page));\n",
			"        Self::PAGE.check(&self.page)?;\n",
			"fn decode_catalog_list_tags(query: &QueryValues) -> Result<CatalogListTagsArgs, ApiError> {",
			"use superschematic_http_runtime::filter::{self, FieldSpec, FilterSpec, Operator};\n",
			`static CATALOG_LIST_PARTS_FILTER_KIND: ParamSpec = ParamSpec::new("filter[kind]", ParamKind::Enum).required().enum_values(&["bolt", "nut", "washer"]);`,
			`static CATALOG_LIST_PARTS_FILTER_MADE_AT: ParamSpec = ParamSpec::new("filter[madeAt]", ParamKind::DateTime).required().check(types::validators::scalars::validate_temporal_date_time);`,
			// A date and a decimal are read by the scalar core's parse.
			`static CATALOG_LIST_PARTS_FILTER_RETIRED_ON: ParamSpec = ParamSpec::new("filter[retiredOn]", ParamKind::Date).required().parse(parse_temporal_date);`,
			`static CATALOG_LIST_PARTS_FILTER_PRICE: ParamSpec = ParamSpec::new("filter[price]", ParamKind::Decimal).required().parse(parse_generic_decimal);`,
			"fn parse_temporal_date(text: &str) -> Result<String, Vec<schema::ValidationError>> {\n    scalar_parse(\"Temporal.Date\", text)\n}",
			"    let registry = superscalar::Registry::builtin();\n",
			"        filter: decode_catalog_list_parts_filter(query)?,\n        page: CatalogListPartsArgs::PAGE.unbind(CatalogListPartsArgs::PAGE.query(query)?)?,\n",
			"    let filters = CatalogListPartsArgs::FILTER.query(query)?;\n",
			`        quantity: filter::decode_ordered(&filters, "quantity", &CATALOG_LIST_PARTS_FILTER_QUANTITY)?,` + "\n",
			`        name: filter::decode_text(&filters, "name", &CATALOG_LIST_PARTS_FILTER_NAME)?,` + "\n",
			`        supplier_id: filter::decode_equals(&filters, "supplierId", &CATALOG_LIST_PARTS_FILTER_SUPPLIER_ID)?,` + "\n",
			"    pub const FILTER: FilterSpec = FilterSpec::new(&[\n" + `        FieldSpec::new("id", &[Operator::Eq, Operator::Ne, Operator::In, Operator::NotIn]),` + "\n",
			`        FieldSpec::new("retiredOn", &[Operator::Eq, Operator::Ne, Operator::In, Operator::NotIn, Operator::Gt, Operator::Gte, Operator::Lt, Operator::Lte, Operator::IsNull]),` + "\n",
			`            by.check(&CatalogListPartsArgs::FILTER, "madeAt", &CATALOG_LIST_PARTS_FILTER_MADE_AT)?;` + "\n",
			"        self.filter.check()?;\n        Self::PAGE.check(&self.page)?;\n",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(generated[file], want) {
				t.Errorf("%s does not contain %q", file, want)
			}
		}
	}
}

// TestPagedScaffoldsNameTheRuntime: a paged method's scaffolds, the
// namespace's and the implementation crate's (D57), return the page by the
// API crate's runtime re-export, which they import; a namespace without a
// paged method does not.
func TestPagedScaffoldsNameTheRuntime(t *testing.T) {
	output := generateRustAPI(t, pagedService, false, "", nil)
	dir := t.TempDir()
	if _, err := WriteScaffolds(output, dir); err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		t.Helper()
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(text)
	}
	const use = "use schemas_fixture_paged_api_api::runtime;\n"
	for path, wants := range map[string][]string{
		filepath.Join(dir, "catalog", "implementation.rs"): {use, "async fn list_items(&self, ctx: RequestContext, args: CatalogListItemsArgs) -> Result<runtime::page::Page<types::ItemView>, ApiError> {"},
		filepath.Join(dir, "catalog", "list_items.rs"):     {use, ") -> Result<runtime::page::Page<types::ItemView>, ApiError> {"},
		filepath.Join(dir, "catalog", "list_tags.rs"):      {use, "    _args: CatalogListTagsArgs,\n) -> Result<runtime::page::Page<String>, ApiError> {"},
		filepath.Join(dir, "catalog", "list_parts.rs"):     {use, "    _args: CatalogListPartsArgs,\n) -> Result<runtime::page::Page<types::PartView>, ApiError> {"},
		filepath.Join(dir, "README.md"):                    {"- A list with a `FilterBy` argument (D106) takes what the request filters\n  by in its Args, under the argument's name: a `<Operation>Filter`"},
	} {
		text := read(path)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not contain %q:\n%s", path, want, text)
			}
		}
	}
	if text := read(filepath.Join(dir, "catalog", "get_item.rs")); strings.Contains(text, "::runtime;") {
		t.Errorf("get_item.rs, which does not page, names the runtime:\n%s", text)
	}

	if err := output.SetDeps(DepsInfo{}); err != nil {
		t.Fatal(err)
	}
	implDir := t.TempDir()
	if _, err := WriteImplementationScaffold(output, implDir, t.TempDir(), naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, naming.Default()); err != nil {
		t.Fatal(err)
	}
	lib := read(filepath.Join(implDir, ImplementationLib))
	for _, want := range []string{use, "-> Result<runtime::page::Page<types::ItemView>, ApiError> {"} {
		if !strings.Contains(lib, want) {
			t.Errorf("the implementation's lib.rs does not contain %q:\n%s", want, lib)
		}
	}
}

// TestPagedListServesAPageAtATime runs cargo test on fixture-paged-api's
// crate with pagedRouterTest, with the namespace's scaffolds and the
// implementation crate's lib.rs (D57) compiled beside it: the router
// serves five items two to a page, each page's next reading back as the
// cursor of the one after it and the last page's null; a limit past the
// maximum or below 1, and a cursor that is not of the alphabet, are the 400
// Args::check refuses the same arguments with; a cursor the implementation
// cannot read is the 400 "cursor is not one this operation returned"; the
// items sort by name or sku each way, ties broken by id, the next bound to
// the sort, and a sort not offered and a cursor of another sort are the
// 400s an in-process call gets too (D60, amended); and the in-process
// call, through the operation's OperationInfo, check, PAGE.unbind and
// Page::bind, returns the page the router answers, so a cursor from either
// pages the other.
//
// catalog.listParts filters (D106): through the router, a filter on a
// field of each kind keeps the parts it finds, by equality, in, their
// negations ne and notIn (D112), which keep a part whose field is null, a
// range, contains and isNull, every filter holding at once, beside a sort
// and a cursor, which carries no filter; a field or an operator the FilterBy
// does not take, and a value its field's type refuses, a date and a
// decimal by their scalars' parse, are the 400 at filter[field]; and the
// in-process call takes the typed filter, which
// check refuses as the router refuses a request that says the same.
func TestPagedListServesAPageAtATime(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedService))
	if err != nil {
		t.Fatalf("load %s: %v", pagedService, err)
	}
	// The test names serde's traits, to write a filter's values as the
	// wire does.
	options := cargoOptions{devDependencies: `serde = "1.0.228"`}
	cargoTestAPICrateWith(t, pagedService, schema, "paged", pagedRouterTest, options, func(apiDir string, output *APIOutput) error {
		if _, err := WriteScaffolds(output, filepath.Join(apiDir, "scaffolds")); err != nil {
			return err
		}
		// The implementation crate's lib.rs needs the crate's Deps.
		if err := output.SetDeps(DepsInfo{}); err != nil {
			return err
		}
		if err := WriteAPI(output, apiDir); err != nil {
			return err
		}
		_, err := WriteImplementationScaffold(output, filepath.Join(apiDir, "implementation"), apiDir, naming.LocalPaths{HTTPRuntimeRust: filepath.Join(apiDir, "runtime")}, naming.Default())
		return err
	})
}

// pagedRouterTest is tests/paged.rs of fixture-paged-api's crate, with
// API_CRATE and RUNTIME_CRATE replaced by the crates' module names. Its
// catalog holds five items, their names in id order and two of their skus
// the same; a cursor's key is the last item's sort field and id. It holds
// five parts too, which catalog.listParts keeps by its filter (D106),
// member for member, each compared as the wire writes it.
const pagedRouterTest = `#[path = "../scaffolds/mod.rs"]
mod scaffolds;

// The implementation crate's lib.rs, whose Handlers hold what no test
// reads.
#[allow(dead_code)]
#[path = "../implementation/src/lib.rs"]
mod implementation;

use std::sync::Arc;

use std::cmp::Ordering;

use API_CRATE::operations::{CATALOG_LIST_ITEMS, CATALOG_LIST_PARTS, CATALOG_LIST_TAGS};
use API_CRATE::runtime::filter::{Equals, Ordered, Text};
use API_CRATE::runtime::page::{refused_cursor, Cursor, Page, PageRequest, Sort};
use API_CRATE::{
    build_router, types, CatalogGetItemArgs, CatalogImplementation, CatalogListItemsArgs, CatalogListPartsArgs,
    CatalogListPartsFilter, CatalogListTagsArgs, CatalogPlaceBasketArgs, Deps, Implementations,
};
use RUNTIME_CRATE::{ApiError, RequestContext};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::{Request, StatusCode};
use serde_json::{json, Value};
use tower::ServiceExt;

const IDS: [&str; 5] = [
    "00000000-0000-4000-8000-000000000001",
    "00000000-0000-4000-8000-000000000002",
    "00000000-0000-4000-8000-000000000003",
    "00000000-0000-4000-8000-000000000004",
    "00000000-0000-4000-8000-000000000005",
];

const SKUS: [&str; 5] = ["sku-2", "sku-1", "sku-5", "sku-2", "sku-4"];

fn item(index: usize) -> types::ItemView {
    serde_json::from_value(json!({
        "id": IDS[index],
        "name": format!("Item {}", index + 1),
        "sku": SKUS[index],
        "inStock": index.is_multiple_of(2),
    }))
    .unwrap()
}

// The catalog's indexes in a sort's order, ties broken by id in the same
// direction.
fn sorted(sort: &Sort) -> Vec<usize> {
    let value = |index: usize| match sort.field() {
        "sku" => SKUS[index].to_owned(),
        _ => format!("Item {}", index + 1),
    };
    let mut order: Vec<usize> = (0..IDS.len()).collect();
    order.sort_by(|&a, &b| {
        let by = value(a).cmp(&value(b)).then(IDS[a].cmp(IDS[b]));
        if sort.desc() { by.reverse() } else { by }
    });
    order
}

// An item's key under a sort: its sort field's value and its id.
fn key(index: usize, sort: &Sort) -> Value {
    let value = serde_json::to_value(item(index)).unwrap();
    json!({sort.field(): value[sort.field()], "id": value["id"]})
}

fn by_name() -> Sort {
    Sort::parse("name").unwrap()
}

const PART_IDS: [&str; 5] = [
    "00000000-0000-4000-8000-000000000011",
    "00000000-0000-4000-8000-000000000012",
    "00000000-0000-4000-8000-000000000013",
    "00000000-0000-4000-8000-000000000014",
    "00000000-0000-4000-8000-000000000015",
];

const SUPPLIERS: [&str; 2] = ["00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2"];

// An in whose second value is no UUID.
const ONE_BAD_SUPPLIER: &str = "filter[supplierId][in]=00000000-0000-4000-8000-0000000000a1,not-a-uuid";

// The parts: a field of every kind a filter compares, two of them of one
// quantity, two retired and two with no supplier.
fn part(index: usize) -> types::PartView {
    let (name, kind, quantity, weight, price, discontinued, retired_on, supplier) = [
        ("Anchor bolt", "bolt", 10, 1.5, "2.50", false, None, Some(SUPPLIERS[0])),
        ("Hex nut", "nut", 200, 0.2, "0.10", false, Some("2026-06-30"), None),
        ("Lock washer", "washer", 50, 0.05, "0.05", true, None, Some(SUPPLIERS[0])),
        ("Wing nut", "nut", 10, 0.3, "0.75", false, Some("2026-09-30"), Some(SUPPLIERS[1])),
        ("Carriage bolt", "bolt", 75, 2.25, "3.00", true, None, None),
    ][index];
    serde_json::from_value(json!({
        "id": PART_IDS[index],
        "name": name,
        "kind": kind,
        "quantity": quantity,
        "weight": weight,
        "price": price,
        "discontinued": discontinued,
        "madeAt": format!("2026-0{}-01T00:00:00Z", index + 1),
        "retiredOn": retired_on,
        "supplierId": supplier,
    }))
    .unwrap()
}

// A value as the wire writes it.
fn wire<T: serde::Serialize>(value: &T) -> Value {
    serde_json::to_value(value).unwrap()
}

// How two values of a field compare, as the wire writes them: numbers by
// their value, text and booleans by their own order. A null compares with
// nothing.
fn compare(a: &Value, b: &Value) -> Option<Ordering> {
    match (a, b) {
        (Value::Number(a), Value::Number(b)) => a.as_f64()?.partial_cmp(&b.as_f64()?),
        (Value::String(a), Value::String(b)) => Some(a.cmp(b)),
        (Value::Bool(a), Value::Bool(b)) => Some(a.cmp(b)),
        _ => None,
    }
}

// A decimal's text as its number, so 0.10 is 0.1; any other value as it
// is.
fn number(value: Value) -> Value {
    value.as_str().and_then(|text| text.parse::<f64>().ok()).map_or(value, Value::from)
}

// A field's filter of any shape, its values as the wire writes them.
#[derive(Default)]
struct By {
    eq: Option<Value>,
    ne: Option<Value>,
    in_: Vec<Value>,
    not_in: Vec<Value>,
    gt: Option<Value>,
    gte: Option<Value>,
    lt: Option<Value>,
    lte: Option<Value>,
    contains: Option<String>,
    is_null: Option<bool>,
}

impl By {
    fn equals<T: serde::Serialize>(by: &Option<Equals<T>>) -> Option<Self> {
        let by = by.as_ref()?;
        Some(Self {
            eq: by.eq.as_ref().map(wire),
            ne: by.ne.as_ref().map(wire),
            in_: by.in_.iter().map(wire).collect(),
            not_in: by.not_in.iter().map(wire).collect(),
            is_null: by.is_null,
            ..Self::default()
        })
    }

    fn ordered<T: serde::Serialize>(by: &Option<Ordered<T>>) -> Option<Self> {
        let by = by.as_ref()?;
        Some(Self {
            eq: by.eq.as_ref().map(wire),
            ne: by.ne.as_ref().map(wire),
            in_: by.in_.iter().map(wire).collect(),
            not_in: by.not_in.iter().map(wire).collect(),
            gt: by.gt.as_ref().map(wire),
            gte: by.gte.as_ref().map(wire),
            lt: by.lt.as_ref().map(wire),
            lte: by.lte.as_ref().map(wire),
            is_null: by.is_null,
            ..Self::default()
        })
    }

    fn text<T: serde::Serialize>(by: &Option<Text<T>>) -> Option<Self> {
        let by = by.as_ref()?;
        Some(Self {
            eq: by.eq.as_ref().map(wire),
            ne: by.ne.as_ref().map(wire),
            in_: by.in_.iter().map(wire).collect(),
            not_in: by.not_in.iter().map(wire).collect(),
            contains: by.contains.clone(),
            is_null: by.is_null,
            ..Self::default()
        })
    }

    // The filter of a decimal, each value its number.
    fn numbers(self) -> Self {
        Self {
            eq: self.eq.map(number),
            ne: self.ne.map(number),
            in_: self.in_.into_iter().map(number).collect(),
            not_in: self.not_in.into_iter().map(number).collect(),
            gt: self.gt.map(number),
            gte: self.gte.map(number),
            lt: self.lt.map(number),
            lte: self.lte.map(number),
            ..self
        }
    }

    // Whether every operator set holds for a field's value. ne and not_in
    // hold for a value that is none of theirs, a null among them (D112).
    fn holds(&self, value: &Value) -> bool {
        let is = |other: &Option<Value>, wanted: &[Ordering]| {
            other.as_ref().is_none_or(|other| compare(value, other).is_some_and(|order| wanted.contains(&order)))
        };
        let equals = |other: &Value| compare(value, other) == Some(Ordering::Equal);
        is(&self.eq, &[Ordering::Equal])
            && !self.ne.as_ref().is_some_and(equals)
            && (self.in_.is_empty() || self.in_.iter().any(equals))
            && !self.not_in.iter().any(equals)
            && is(&self.gt, &[Ordering::Greater])
            && is(&self.gte, &[Ordering::Greater, Ordering::Equal])
            && is(&self.lt, &[Ordering::Less])
            && is(&self.lte, &[Ordering::Less, Ordering::Equal])
            && self.contains.as_ref().is_none_or(|text| {
                value.as_str().is_some_and(|value| value.to_lowercase().contains(&text.to_lowercase()))
            })
            && self.is_null.is_none_or(|is_null| value.is_null() == is_null)
    }
}

// Whether a part is one the filter keeps: every member set holds.
fn kept(part: &types::PartView, filter: &CatalogListPartsFilter) -> bool {
    let mut part = wire(part);
    part["price"] = number(part["price"].take());
    [
        ("id", By::equals(&filter.id)),
        ("name", By::text(&filter.name)),
        ("kind", By::equals(&filter.kind)),
        ("quantity", By::ordered(&filter.quantity)),
        ("weight", By::ordered(&filter.weight)),
        ("price", By::ordered(&filter.price).map(By::numbers)),
        ("discontinued", By::equals(&filter.discontinued)),
        ("madeAt", By::ordered(&filter.made_at)),
        ("retiredOn", By::ordered(&filter.retired_on)),
        ("supplierId", By::equals(&filter.supplier_id)),
    ]
    .iter()
    .all(|(field, by)| by.as_ref().is_none_or(|by| by.holds(&part[*field])))
}

// The parts' indexes in a sort's order, ties broken by id in the same
// direction.
fn sorted_parts(sort: &Sort) -> Vec<usize> {
    let mut order: Vec<usize> = (0..PART_IDS.len()).collect();
    order.sort_by(|&a, &b| {
        let (left, right) = (wire(&part(a)), wire(&part(b)));
        let by = compare(&left[sort.field()], &right[sort.field()]).unwrap().then(PART_IDS[a].cmp(PART_IDS[b]));
        if sort.desc() { by.reverse() } else { by }
    });
    order
}

// A part's key under a sort: its sort field's value and its id.
fn part_key(index: usize, sort: &Sort) -> Value {
    let value = wire(&part(index));
    json!({sort.field(): value[sort.field()], "id": value["id"]})
}

struct Catalog;

#[async_trait]
impl CatalogImplementation for Catalog {
    async fn list_items(&self, _ctx: RequestContext, args: CatalogListItemsArgs) -> Result<Page<types::ItemView>, ApiError> {
        // The router, or the in-process call, fills the default sort in.
        let sort = args.page.sort.clone().expect("the page's sort");
        let order = sorted(&sort);
        // The position is the last item's key; a cursor of another shape is
        // refused by decode, and one naming no item by the implementation.
        let after = match &args.page.cursor {
            None => None,
            Some(cursor) => {
                let key: Value = cursor.decode()?;
                Some(order.iter().position(|&index| serde_json::to_value(item(index)).unwrap()["id"] == key["id"]).ok_or_else(refused_cursor)?)
            }
        };
        let rows: Vec<(usize, types::ItemView)> = order
            .iter()
            .enumerate()
            .filter(|(at, _)| after.is_none_or(|after| *at > after))
            .map(|(_, &index)| (index, item(index)))
            .filter(|(_, item)| args.in_stock.is_none_or(|in_stock| item.in_stock == in_stock))
            .take(usize::try_from(args.page.limit).unwrap() + 1)
            .collect();
        Ok(Page::from_rows(rows, args.page.limit, |(index, _)| key(*index, &sort))?.map(|(_, item)| item))
    }
    async fn get_item(&self, _ctx: RequestContext, _args: CatalogGetItemArgs) -> Result<types::ItemView, ApiError> {
        Ok(item(0))
    }
    async fn list_tags(&self, _ctx: RequestContext, args: CatalogListTagsArgs) -> Result<Page<String>, ApiError> {
        assert_eq!(args.page, PageRequest::first(20));
        Ok(Page::last(vec!["red".to_owned(), "blue".to_owned()]))
    }
    async fn list_parts(&self, _ctx: RequestContext, args: CatalogListPartsArgs) -> Result<Page<types::PartView>, ApiError> {
        let sort = args.page.sort.clone().expect("the page's sort");
        let order = sorted_parts(&sort);
        let after = match &args.page.cursor {
            None => None,
            Some(cursor) => {
                let key: Value = cursor.decode()?;
                Some(order.iter().position(|&index| wire(&part(index))["id"] == key["id"]).ok_or_else(refused_cursor)?)
            }
        };
        // The filter keeps a part or not on every page: a cursor carries
        // no filter (D60).
        let rows: Vec<(usize, types::PartView)> = order
            .iter()
            .enumerate()
            .filter(|(at, _)| after.is_none_or(|after| *at > after))
            .map(|(_, &index)| (index, part(index)))
            .filter(|(_, part)| kept(part, &args.filter))
            .take(usize::try_from(args.page.limit).unwrap() + 1)
            .collect();
        Ok(Page::from_rows(rows, args.page.limit, |(index, _)| part_key(*index, &sort))?.map(|(_, part)| part))
    }
    async fn place_basket(&self, _ctx: RequestContext, _args: CatalogPlaceBasketArgs) -> Result<types::BasketView, ApiError> {
        Err(ApiError::not_implemented("place_basket"))
    }
}

fn implementations() -> Implementations {
    Implementations { catalog: Arc::new(Catalog) }
}

async fn get(implementations: Implementations, uri: &str) -> (StatusCode, Value) {
    let request = Request::get(uri).body(Body::empty()).unwrap();
    let response = build_router(implementations).oneshot(request).await.unwrap();
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, serde_json::from_slice(&bytes).unwrap())
}

// The in-process call of catalog.listItems: the caller admitted by the
// route's rule (it needs none), the arguments checked and the page
// unbound, then the implementation called with the context the route
// would build, and its next bound to the sort.
async fn list_in_process(mut args: CatalogListItemsArgs) -> Result<Page<types::ItemView>, ApiError> {
    const { assert!(!CATALOG_LIST_ITEMS.requires_auth) };
    args.check()?;
    args.page = CatalogListItemsArgs::PAGE.unbind(args.page)?;
    let request = args.page.clone();
    implementations().catalog.list_items(CATALOG_LIST_ITEMS.context(None), args).await?.bind(&request)
}

fn args(page: PageRequest) -> CatalogListItemsArgs {
    CatalogListItemsArgs { ids: None, in_stock: None, page }
}

// The in-process call of catalog.listParts, as list_in_process calls
// catalog.listItems: check holds the filter to the route's rules.
async fn parts_in_process(mut args: CatalogListPartsArgs) -> Result<Page<types::PartView>, ApiError> {
    args.check()?;
    args.page = CatalogListPartsArgs::PAGE.unbind(args.page)?;
    let request = args.page.clone();
    implementations().catalog.list_parts(CATALOG_LIST_PARTS.context(None), args).await?.bind(&request)
}

// The whole list of parts a filter keeps, in name order.
fn all_parts(filter: CatalogListPartsFilter) -> CatalogListPartsArgs {
    CatalogListPartsArgs { filter, page: PageRequest::first(5) }
}

// The ids of the parts at indexes, as the wire writes them.
fn part_ids(indexes: &[usize]) -> Vec<Value> {
    indexes.iter().map(|&index| wire(&part(index))["id"].clone()).collect()
}

// A value of a field's type, of its JSON.
fn value<T: serde::de::DeserializeOwned>(json: Value) -> T {
    serde_json::from_value(json).unwrap()
}

// The ids of a page's items, as the wire writes them.
fn ids(page: &Value) -> Vec<Value> {
    page["items"].as_array().unwrap().iter().map(|item| item["id"].clone()).collect()
}

// The ids of the catalog's items from start to end, as the wire writes them.
fn wire_ids(range: std::ops::Range<usize>) -> Vec<Value> {
    range.map(|index| serde_json::to_value(item(index)).unwrap()["id"].clone()).collect()
}

// The problem the router answers, as status, code, detail and details,
// beside the same members of the in-process refusal.
fn same_refusal(status: StatusCode, body: &Value, refused: ApiError) {
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    assert_eq!(refused.status, status);
    let refused = json!({"code": refused.code, "detail": refused.message, "details": refused.details.as_deref()});
    assert_eq!(refused, json!({"code": body["code"], "detail": body["detail"], "details": body["details"]}));
}

#[tokio::test]
async fn a_list_pages_by_its_cursor_to_the_last_page() {
    let (status, body) = get(implementations(), "/api/items").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    let first = &body["data"];
    assert_eq!(ids(first), wire_ids(0..2));
    let next = first["next"].as_str().unwrap().to_owned();
    // The next names the default sort, around the implementation's key.
    let own = Cursor::parse(&next).unwrap().unbind(&by_name()).unwrap();
    assert_eq!(own.decode::<Value>().unwrap(), key(1, &by_name()));
    assert_eq!(body["meta"].as_object().unwrap().keys().collect::<Vec<_>>(), ["requestId"]);

    // The in-process call returns the page the router answers.
    let page = list_in_process(args(CatalogListItemsArgs::PAGE.first())).await.unwrap();
    assert_eq!(serde_json::to_value(&page).unwrap(), *first);

    let (status, body) = get(implementations(), &format!("/api/items?cursor={next}")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    let second = &body["data"];
    assert_eq!(ids(second), wire_ids(2..4));
    let page = list_in_process(args(PageRequest::after(page.next.unwrap(), 2))).await.unwrap();
    assert_eq!(serde_json::to_value(&page).unwrap(), *second);

    let last_cursor = second["next"].as_str().unwrap();
    let (status, body) = get(implementations(), &format!("/api/items?cursor={last_cursor}&limit=5")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(body["data"], json!({"items": [serde_json::to_value(item(4)).unwrap()], "next": null}));
    let page = list_in_process(args(PageRequest::after(page.next.unwrap(), 5))).await.unwrap();
    assert_eq!(page, Page::last(vec![item(4)]));

    // The whole list at the maximum, and a filter applied per page.
    let (_, body) = get(implementations(), "/api/items?limit=5").await;
    assert_eq!((ids(&body["data"]), &body["data"]["next"]), (wire_ids(0..5), &Value::Null));
    let (_, body) = get(implementations(), "/api/items?inStock=true&limit=1").await;
    assert_eq!(ids(&body["data"]), wire_ids(0..1));
    let next = body["data"]["next"].as_str().unwrap().to_owned();
    let (_, body) = get(implementations(), &format!("/api/items?inStock=true&limit=1&cursor={next}")).await;
    assert_eq!(ids(&body["data"]), wire_ids(2..3));

    // A page of a primitive, at the default limits, with no argument.
    let (status, body) = get(implementations(), "/api/tags").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(body["data"], json!({"items": ["red", "blue"], "next": null}));
    assert_eq!(CatalogListTagsArgs::PAGE.first(), PageRequest::first(20));
    assert_eq!(CATALOG_LIST_TAGS.path, "/api/tags");
}

#[tokio::test]
async fn a_limit_out_of_bounds_is_refused_as_check_refuses_it() {
    let (status, body) = get(implementations(), "/api/items?limit=6").await;
    assert_eq!(body["detail"], "Invalid query parameter limit: must be at most 5");
    assert_eq!(body["details"]["parameter"], "limit");
    same_refusal(status, &body, args(PageRequest::first(6)).check().unwrap_err());
    same_refusal(status, &body, list_in_process(args(PageRequest::first(6))).await.unwrap_err());

    let (status, body) = get(implementations(), "/api/items?limit=0").await;
    same_refusal(status, &body, args(PageRequest::first(0)).check().unwrap_err());

    let (status, body) = get(implementations(), "/api/tags?limit=101").await;
    same_refusal(status, &body, CatalogListTagsArgs { page: PageRequest::first(101) }.check().unwrap_err());
    let (status, body) = get(implementations(), "/api/items?limit=2.5").await;
    assert_eq!((status, body["details"]["reason"].as_str()), (StatusCode::BAD_REQUEST, Some("expected an integer")));

    // A query argument is checked before the page, by the router and check.
    let six = [IDS[0]; 6].join(",");
    let (status, body) = get(implementations(), &format!("/api/items?ids={six}&limit=6")).await;
    assert_eq!(body["details"]["parameter"], "ids");
    let ids = vec![serde_json::from_value(json!(IDS[0])).unwrap(); 6];
    let refused = CatalogListItemsArgs { ids: Some(ids), in_stock: None, page: PageRequest::first(6) }.check().unwrap_err();
    same_refusal(status, &body, refused);
}

#[tokio::test]
async fn a_cursor_this_operation_did_not_return_is_refused() {
    // Of the alphabet, but JSON of another shape: the implementation's
    // decode refuses it, in a request and in-process alike.
    let (status, body) = get(implementations(), "/api/items?cursor=e30").await;
    assert_eq!(body["detail"], "cursor is not one this operation returned");
    assert_eq!(body["details"]["parameter"], "cursor");
    let forged = Cursor::parse("e30").unwrap();
    same_refusal(status, &body, list_in_process(args(PageRequest::after(forged, 2))).await.unwrap_err());

    // A key of the right type that names no item, bound to the sort.
    let unknown = Cursor::encode(&json!({"name": "Item 9", "id": "no such item"})).unwrap().bind(&by_name()).unwrap();
    let (status, body) = get(implementations(), &format!("/api/items?cursor={unknown}")).await;
    same_refusal(status, &body, refused_cursor());
    same_refusal(status, &body, list_in_process(args(PageRequest::after(unknown, 2))).await.unwrap_err());

    // The implementation's own cursor names no sort: the router's unbind
    // refuses it, as the in-process call's does.
    let own = Cursor::encode(&key(1, &by_name())).unwrap();
    let (status, body) = get(implementations(), &format!("/api/items?cursor={own}")).await;
    same_refusal(status, &body, refused_cursor());
    same_refusal(status, &body, list_in_process(args(PageRequest::after(own, 2))).await.unwrap_err());

    // Not of the alphabet: the router's reader and check refuse it alike.
    let (status, body) = get(implementations(), "/api/items?cursor=e30%3D").await;
    assert_eq!(body["details"]["reason"], "does not match the required pattern");
    let padded: Cursor = serde_json::from_value(json!("e30=")).unwrap();
    same_refusal(status, &body, args(PageRequest::after(padded, 2)).check().unwrap_err());
}

#[tokio::test]
async fn a_list_sorts_each_way_and_its_next_names_the_sort() {
    for (text, want) in [
        ("name", [0, 1, 2, 3, 4]),
        ("-name", [4, 3, 2, 1, 0]),
        ("sku", [1, 0, 3, 4, 2]),
        ("-sku", [2, 4, 3, 0, 1]),
    ] {
        let sort = Sort::parse(text).unwrap();
        assert_eq!(sorted(&sort), want, "{text}");
        let wire: Vec<Value> = want.iter().flat_map(|&index| wire_ids(index..index + 1)).collect();

        // Over HTTP, each next to the last, under the sort.
        let mut seen = Vec::new();
        let mut query = format!("/api/items?sort={text}");
        loop {
            let (status, body) = get(implementations(), &query).await;
            assert_eq!(status, StatusCode::OK, "{body}");
            seen.extend(ids(&body["data"]));
            let Some(next) = body["data"]["next"].as_str() else { break };
            let envelope = String::from_utf8(base64_url(next)).unwrap();
            assert!(envelope.starts_with(&format!(r#"{{"sort":"{text}","key":"#)), "{envelope}");
            query = format!("/api/items?sort={text}&cursor={next}");
        }
        assert_eq!(seen, wire, "{text}");

        // In-process, the request built from the values a form holds.
        let mut seen = Vec::new();
        let mut page = CatalogListItemsArgs::PAGE.sorted_request(None, None, Some(text)).unwrap();
        loop {
            let listed = list_in_process(args(page.clone())).await.unwrap();
            seen.extend(ids(&serde_json::to_value(&listed).unwrap()));
            let Some(next) = listed.next else { break };
            page = PageRequest::after(next, 2).sorted(sort.clone());
        }
        assert_eq!(seen, wire, "{text}");
    }

    // A cursor from the router pages the in-process call, and back.
    let (_, body) = get(implementations(), "/api/items?sort=-sku").await;
    let next = Cursor::parse(body["data"]["next"].as_str().unwrap()).unwrap();
    let descending = Sort::parse("-sku").unwrap();
    let second = list_in_process(args(PageRequest::after(next.clone(), 2).sorted(descending.clone()))).await.unwrap();
    let (_, body) = get(implementations(), &format!("/api/items?sort=-sku&cursor={next}")).await;
    assert_eq!(serde_json::to_value(&second).unwrap(), body["data"]);
    let third = second.next.unwrap();
    let (_, body) = get(implementations(), &format!("/api/items?sort=-sku&cursor={third}")).await;
    assert_eq!(ids(&body["data"]), wire_ids(1..2));

    // A cursor of another sort, the default among them, is refused alike.
    for (query, sort) in [("sort=sku&", Some("sku")), ("", None)] {
        let (status, body) = get(implementations(), &format!("/api/items?{query}cursor={next}")).await;
        assert_eq!(body["detail"], "cursor is not one this sort returned");
        assert_eq!(body["details"]["reason"], "returned under another sort");
        let page = CatalogListItemsArgs::PAGE.sorted_request(Some(next.as_str()), None, sort).unwrap();
        same_refusal(status, &body, list_in_process(args(page)).await.unwrap_err());
    }

    // A sort not offered is the parameter's 400, from the router, check and
    // a form's values alike, before the cursor's sort.
    let (status, body) = get(implementations(), &format!("/api/items?sort=price&cursor={next}")).await;
    assert_eq!(body["detail"], "Invalid query parameter sort: expected one of name, -name, sku, -sku");
    assert_eq!(body["details"]["parameter"], "sort");
    let price = Sort::parse("price").unwrap();
    same_refusal(status, &body, args(PageRequest::after(next, 2).sorted(price)).check().unwrap_err());
    same_refusal(status, &body, CatalogListItemsArgs::PAGE.sorted_request(None, None, Some("price")).unwrap_err());
}

#[tokio::test]
async fn a_list_keeps_the_parts_its_filters_find() {
    // The parts in name order: 0 Anchor bolt, 4 Carriage bolt, 1 Hex nut,
    // 2 Lock washer, 3 Wing nut.
    let cases: [(String, &[usize]); 46] = [
        (String::new(), &[0, 4, 1, 2, 3]),
        // Equality on a field of each kind.
        (format!("filter[id]={}", PART_IDS[3]), &[3]),
        ("filter[name]=Hex+nut".to_owned(), &[1]),
        ("filter[kind]=nut".to_owned(), &[1, 3]),
        ("filter[kind][eq]=nut".to_owned(), &[1, 3]),
        ("filter[quantity]=10".to_owned(), &[0, 3]),
        ("filter[weight]=0.2".to_owned(), &[1]),
        ("filter[price]=0.10".to_owned(), &[1]),
        ("filter[discontinued]=true".to_owned(), &[4, 2]),
        ("filter[madeAt]=2026-03-01T00:00:00Z".to_owned(), &[2]),
        ("filter[retiredOn]=2026-06-30".to_owned(), &[1]),
        (format!("filter[supplierId]={}", SUPPLIERS[0]), &[0, 2]),
        // in, its values trimmed.
        (format!("filter[id][in]={},{}", PART_IDS[0], PART_IDS[3]), &[0, 3]),
        ("filter[kind][in]=bolt,+washer".to_owned(), &[0, 4, 2]),
        ("filter[quantity][in]=75,200".to_owned(), &[4, 1]),
        // ne keeps the parts eq leaves out (D112), one whose field is null
        // among them.
        ("filter[kind][ne]=nut".to_owned(), &[0, 4, 2]),
        ("filter[name][ne]=Hex+nut".to_owned(), &[0, 4, 2, 3]),
        ("filter[quantity][ne]=10".to_owned(), &[4, 1, 2]),
        ("filter[price][ne]=0.1".to_owned(), &[0, 4, 2, 3]),
        ("filter[discontinued][ne]=true".to_owned(), &[0, 1, 3]),
        (format!("filter[supplierId][ne]={}", SUPPLIERS[0]), &[4, 1, 3]),
        ("filter[retiredOn][ne]=2026/06/30".to_owned(), &[0, 4, 2, 3]),
        // notIn keeps the parts in leaves out, its values trimmed.
        (format!("filter[id][notIn]={},{}", PART_IDS[0], PART_IDS[3]), &[4, 1, 2]),
        ("filter[kind][notIn]=bolt,+washer".to_owned(), &[1, 3]),
        ("filter[quantity][notIn]=75,200".to_owned(), &[0, 2, 3]),
        ("filter[price][notIn]=0.1,3".to_owned(), &[0, 2, 3]),
        (format!("filter[supplierId][notIn]={},{}", SUPPLIERS[0], SUPPLIERS[1]), &[4, 1]),
        // A range, on a number, a decimal, a time and a date.
        ("filter[quantity][gte]=50&filter[quantity][lt]=200".to_owned(), &[4, 2]),
        ("filter[weight][gt]=1".to_owned(), &[0, 4]),
        ("filter[price][lte]=0.10".to_owned(), &[1, 2]),
        ("filter[madeAt][gte]=2026-03-01T00:00:00Z".to_owned(), &[4, 2, 3]),
        ("filter[retiredOn][lt]=2026-07-01".to_owned(), &[1]),
        // A date and a decimal in another of their forms: the filter holds
        // each one's canonical text.
        ("filter[retiredOn]=2026/06/30".to_owned(), &[1]),
        ("filter[retiredOn][in]=30+June+2026,2026-09-30T12:00:00Z".to_owned(), &[1, 3]),
        ("filter[price][in]=0.1,3".to_owned(), &[4, 1]),
        // contains, in any case.
        ("filter[name][contains]=NUT".to_owned(), &[1, 3]),
        ("filter[name][contains]=r+b".to_owned(), &[0]),
        // isNull, on the fields that may be null.
        ("filter[retiredOn][isNull]=true".to_owned(), &[0, 4, 2]),
        ("filter[retiredOn][isNull]=false".to_owned(), &[1, 3]),
        ("filter[supplierId][isNull]=true".to_owned(), &[4, 1]),
        // Every filter holds at once.
        ("filter[kind]=nut&filter[quantity][lt]=100".to_owned(), &[3]),
        ("filter[kind]=nut&filter[discontinued]=true".to_owned(), &[]),
        ("filter[kind][ne]=nut&filter[kind][notIn]=washer".to_owned(), &[0, 4]),
        (format!("filter[supplierId][ne]={}&filter[supplierId][isNull]=false", SUPPLIERS[0]), &[3]),
        ("filter[kind]=nut&filter[kind][ne]=nut".to_owned(), &[]),
        // The brackets percent-encoded.
        ("filter%5Bkind%5D%5Bin%5D=nut".to_owned(), &[1, 3]),
    ];
    for (query, want) in cases {
        let (status, body) = get(implementations(), &format!("/api/parts?limit=5&{query}")).await;
        assert_eq!(status, StatusCode::OK, "{query}: {body}");
        assert_eq!(ids(&body["data"]), part_ids(want), "{query}");
        assert_eq!(body["data"]["next"], Value::Null, "{query}");
    }

    // The in-process call returns the page the router answers.
    let nuts = CatalogListPartsFilter {
        kind: Some(Equals { eq: Some(types::PartKind::Nut), ..Default::default() }),
        quantity: Some(Ordered { lt: Some(100), ..Default::default() }),
        ..Default::default()
    };
    let (_, body) = get(implementations(), "/api/parts?limit=5&filter[kind]=nut&filter[quantity][lt]=100").await;
    let page = parts_in_process(all_parts(nuts)).await.unwrap();
    assert_eq!(wire(&page), body["data"]);
    let named = CatalogListPartsFilter {
        name: Some(Text { contains: Some("WASHER".to_owned()), ..Default::default() }),
        retired_on: Some(Ordered { is_null: Some(true), ..Default::default() }),
        supplier_id: Some(Equals { in_: vec![value(json!(SUPPLIERS[0])), value(json!(SUPPLIERS[1]))], ..Default::default() }),
        ..Default::default()
    };
    let page = parts_in_process(all_parts(named)).await.unwrap();
    assert_eq!(ids(&wire(&page)), part_ids(&[2]));
    let page = parts_in_process(all_parts(CatalogListPartsFilter::default())).await.unwrap();
    assert_eq!(ids(&wire(&page)), part_ids(&[0, 4, 1, 2, 3]));

    // ne and not_in, which keep the part of no supplier.
    let others = CatalogListPartsFilter {
        kind: Some(Equals { ne: Some(types::PartKind::Nut), ..Default::default() }),
        supplier_id: Some(Equals { not_in: vec![value(json!(SUPPLIERS[0]))], ..Default::default() }),
        ..Default::default()
    };
    let query = format!("/api/parts?limit=5&filter[kind][ne]=nut&filter[supplierId][notIn]={}", SUPPLIERS[0]);
    let (_, body) = get(implementations(), &query).await;
    let page = parts_in_process(all_parts(others)).await.unwrap();
    assert_eq!(wire(&page), body["data"]);
    assert_eq!(ids(&wire(&page)), part_ids(&[4]));
}

#[tokio::test]
async fn a_filtered_list_pages_under_its_sort() {
    // The bolts and nuts by quantity, descending, two to a page: 200, 75,
    // then the two of 10, the later id first.
    let query = "/api/parts?filter[kind][in]=bolt,nut&sort=-quantity";
    let (status, body) = get(implementations(), query).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    let first = body["data"].clone();
    assert_eq!(ids(&first), part_ids(&[1, 4]));
    let next = first["next"].as_str().unwrap().to_owned();
    let envelope = String::from_utf8(base64_url(&next)).unwrap();
    assert!(envelope.starts_with(r#"{"sort":"-quantity","key":"#), "{envelope}");

    // The next page is asked with the same filters (D60).
    let (status, body) = get(implementations(), &format!("{query}&cursor={next}")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(ids(&body["data"]), part_ids(&[3, 0]));
    assert_eq!(body["data"]["next"], Value::Null);

    // In-process, the filter built and the page from a form's values.
    let filter = CatalogListPartsFilter {
        kind: Some(Equals { in_: vec![types::PartKind::Bolt, types::PartKind::Nut], ..Default::default() }),
        ..Default::default()
    };
    let page = CatalogListPartsArgs::PAGE.sorted_request(None, None, Some("-quantity")).unwrap();
    let listed = parts_in_process(CatalogListPartsArgs { filter: filter.clone(), page }).await.unwrap();
    assert_eq!(wire(&listed), first);
    let page = CatalogListPartsArgs::PAGE.sorted_request(Some(&next), None, Some("-quantity")).unwrap();
    let listed = parts_in_process(CatalogListPartsArgs { filter, page }).await.unwrap();
    assert_eq!(wire(&listed), body["data"]);

    // A cursor carries no filter: another filter reads on from its place.
    let (_, body) = get(implementations(), &format!("/api/parts?filter[kind]=nut&sort=-quantity&cursor={next}")).await;
    assert_eq!(ids(&body["data"]), part_ids(&[3]));
    let (_, body) = get(implementations(), &format!("/api/parts?sort=-quantity&cursor={next}&limit=5")).await;
    assert_eq!(ids(&body["data"]), part_ids(&[2, 3, 0]));
}

#[tokio::test]
async fn a_filter_the_route_does_not_take_is_refused_at_its_field() {
    let cases = [
        // A field, an operator and a shape the route's FilterBy does not
        // take: the reader's refusals.
        (
            "filter[color]=red",
            "filter[color]",
            "is not a field this operation filters by; expected one of id, name, kind, quantity, weight, price, discontinued, madeAt, retiredOn, supplierId",
        ),
        ("filter[kind][gt]=bolt", "filter[kind]", "takes no operator gt; expected one of eq, ne, in, notIn"),
        ("filter[kind][neq]=bolt", "filter[kind]", "takes no operator neq; expected one of eq, ne, in, notIn"),
        ("filter[kind][notin]=bolt", "filter[kind]", "takes no operator notin; expected one of eq, ne, in, notIn"),
        ("filter[id][isNull]=true", "filter[id]", "takes no operator isNull; expected one of eq, ne, in, notIn"),
        ("filter[quantity][contains]=1", "filter[quantity]", "takes no operator contains; expected one of eq, ne, in, notIn, gt, gte, lt, lte"),
        ("filter[kind]=nut&filter[kind][eq]=bolt", "filter[kind]", "holds eq more than once"),
        ("filter[kind][ne]=nut&filter[kind][ne]=bolt", "filter[kind]", "holds ne more than once"),
        ("filter[retiredOn][isNull]=1", "filter[retiredOn]", "isNull expects true or false"),
        ("filter[kind][in]=,", "filter[kind]", "in names no value"),
        ("filter[kind][notIn]=,", "filter[kind]", "notIn names no value"),
        ("filter[name][contains]=", "filter[name]", "contains needs text"),
        ("filter=nut", "filter", "expected filter[field] or filter[field][operator]"),
        ("filter[kind][in][0]=nut", "filter[kind][in][0]", "expected filter[field] or filter[field][operator]"),
        // A value its field's type refuses, as an item of a query list of
        // that type is refused.
        ("filter[quantity]=many", "filter[quantity]", "expected an integer"),
        ("filter[quantity][in]=1,2.5", "filter[quantity]", "expected an integer"),
        ("filter[quantity][gt]=9007199254740993", "filter[quantity]", "must be at most 9007199254740991"),
        ("filter[weight][lt]=heavy", "filter[weight]", "expected a number"),
        ("filter[kind]=gear", "filter[kind]", "expected one of bolt, nut, washer"),
        ("filter[kind][in]=bolt,gear", "filter[kind]", "expected one of bolt, nut, washer"),
        ("filter[kind][ne]=gear", "filter[kind]", "expected one of bolt, nut, washer"),
        ("filter[kind][notIn]=bolt,gear", "filter[kind]", "expected one of bolt, nut, washer"),
        ("filter[quantity][notIn]=1,2.5", "filter[quantity]", "expected an integer"),
        ("filter[retiredOn][ne]=yesterday", "filter[retiredOn]", "expected a date"),
        // nope alone is a UUID's base62 text, so it is one.
        ("filter[id]=not-a-uuid", "filter[id]", "expected a UUID"),
        (ONE_BAD_SUPPLIER, "filter[supplierId]", "expected a UUID"),
        ("filter[madeAt]=yesterday", "filter[madeAt]", "expected an RFC 3339 timestamp"),
        ("filter[madeAt][gte]=yesterday", "filter[madeAt]", "expected an RFC 3339 timestamp"),
        // A date and a decimal, by their scalars' parse.
        ("filter[retiredOn]=yesterday", "filter[retiredOn]", "expected a date"),
        ("filter[retiredOn][in]=2026-06-30,20260630", "filter[retiredOn]", "expected a date"),
        ("filter[price]=cheap", "filter[price]", "expected a decimal number"),
        ("filter[price][gte]=1.2.3", "filter[price]", "expected a decimal number"),
        ("filter[discontinued]=maybe", "filter[discontinued]", "expected a boolean"),
        // The filter is read before the page, its reader's refusal and
        // then a value's, and its reader before any value.
        (
            "filter[secret]=x&limit=0",
            "filter[secret]",
            "is not a field this operation filters by; expected one of id, name, kind, quantity, weight, price, discontinued, madeAt, retiredOn, supplierId",
        ),
        ("filter[madeAt]=yesterday&limit=0", "filter[madeAt]", "expected an RFC 3339 timestamp"),
        ("filter[kind]=gear&limit=99&sort=price&cursor=e30%3D", "filter[kind]", "expected one of bolt, nut, washer"),
        ("filter[kind]=gear&filter[weight][contains]=1", "filter[weight]", "takes no operator contains; expected one of eq, ne, in, notIn, gt, gte, lt, lte"),
    ];
    for (query, parameter, reason) in cases {
        let (status, body) = get(implementations(), &format!("/api/parts?{query}")).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{query}: {body}");
        assert_eq!(body["code"], "bad_request", "{query}");
        assert_eq!(body["detail"], format!("Invalid query parameter {parameter}: {reason}"), "{query}");
        let details = &body["details"];
        assert_eq!((&details["location"], &details["parameter"], &details["reason"]), (&json!("query"), &json!(parameter), &json!(reason)), "{query}");
        assert_eq!(details.get("path"), None, "{query}");
    }

    // With no filter to refuse, the page is.
    let (status, body) = get(implementations(), "/api/parts?filter[kind]=nut&limit=0").await;
    assert_eq!((status, &body["details"]["parameter"]), (StatusCode::BAD_REQUEST, &json!("limit")), "{body}");
    // nope is an id, of no part.
    let (status, body) = get(implementations(), "/api/parts?filter[id]=nope").await;
    assert_eq!((status, &body["data"]["items"]), (StatusCode::OK, &json!([])), "{body}");
}

#[tokio::test]
async fn check_refuses_a_built_filter_as_the_router_refuses_a_requests() {
    assert!(CatalogListPartsFilter::default().check().is_ok());
    let names: Vec<&str> = CatalogListPartsArgs::FILTER.fields().iter().map(|field| field.name()).collect();
    assert_eq!(names, ["id", "name", "kind", "quantity", "weight", "price", "discontinued", "madeAt", "retiredOn", "supplierId"]);

    // A value past its scalar's range.
    let (status, body) = get(implementations(), "/api/parts?filter[quantity][gt]=9007199254740993").await;
    let filter = CatalogListPartsFilter {
        quantity: Some(Ordered { gt: Some(9_007_199_254_740_993), ..Default::default() }),
        ..Default::default()
    };
    same_refusal(status, &body, filter.check().unwrap_err());
    same_refusal(status, &body, all_parts(filter.clone()).check().unwrap_err());
    same_refusal(status, &body, parts_in_process(all_parts(filter)).await.unwrap_err());

    // An in past its most values.
    let many = vec!["7"; 101].join(",");
    let (status, body) = get(implementations(), &format!("/api/parts?filter[price][in]={many}")).await;
    assert_eq!(body["details"]["reason"], "in must hold at most 100 values");
    let filter = CatalogListPartsFilter {
        price: Some(Ordered { in_: vec![value(json!("7")); 101], ..Default::default() }),
        ..Default::default()
    };
    same_refusal(status, &body, all_parts(filter).check().unwrap_err());

    // A not_in past its most values, and a ne its type refuses.
    let (status, body) = get(implementations(), &format!("/api/parts?filter[price][notIn]={many}")).await;
    assert_eq!(body["details"]["reason"], "notIn must hold at most 100 values");
    let filter = CatalogListPartsFilter {
        price: Some(Ordered { not_in: vec![value(json!("7")); 101], ..Default::default() }),
        ..Default::default()
    };
    same_refusal(status, &body, all_parts(filter).check().unwrap_err());
    let (status, body) = get(implementations(), "/api/parts?filter[quantity][ne]=9007199254740993").await;
    let filter = CatalogListPartsFilter {
        quantity: Some(Ordered { ne: Some(9_007_199_254_740_993), ..Default::default() }),
        ..Default::default()
    };
    same_refusal(status, &body, all_parts(filter).check().unwrap_err());

    // A date that is none, which a caller can build: its type is text.
    let (status, body) = get(implementations(), "/api/parts?filter[retiredOn][lt]=yesterday").await;
    assert_eq!(body["details"]["errors"][0]["validator"], "parse", "{body}");
    let filter = CatalogListPartsFilter {
        retired_on: Some(Ordered { lt: Some("yesterday".to_owned()), ..Default::default() }),
        ..Default::default()
    };
    same_refusal(status, &body, all_parts(filter).check().unwrap_err());
    let filter = CatalogListPartsFilter {
        retired_on: Some(Ordered { lt: Some("2026-07-01".to_owned()), ..Default::default() }),
        ..Default::default()
    };
    assert!(all_parts(filter).check().is_ok());

    // is_null on a field that is never null, which the reader refuses.
    let (status, body) = get(implementations(), "/api/parts?filter[id][isNull]=false").await;
    let filter = CatalogListPartsFilter { id: Some(Equals { is_null: Some(false), ..Default::default() }), ..Default::default() };
    same_refusal(status, &body, all_parts(filter).check().unwrap_err());

    // A contains of no text.
    let (status, body) = get(implementations(), "/api/parts?filter[name][contains]=").await;
    let filter = CatalogListPartsFilter { name: Some(Text { contains: Some(String::new()), ..Default::default() }), ..Default::default() };
    same_refusal(status, &body, all_parts(filter).check().unwrap_err());

    // The filter is checked before the page, as the router reads it.
    let (status, body) = get(implementations(), "/api/parts?filter[id][isNull]=false&limit=99").await;
    let filter = CatalogListPartsFilter { id: Some(Equals { is_null: Some(false), ..Default::default() }), ..Default::default() };
    same_refusal(status, &body, CatalogListPartsArgs { filter, page: PageRequest::first(99) }.check().unwrap_err());
}

// The bytes of an unpadded base64url text.
fn base64_url(text: &str) -> Vec<u8> {
    const ALPHABET: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    let (mut bits, mut count, mut out) = (0u32, 0, Vec::new());
    for byte in text.bytes() {
        bits = (bits << 6) | ALPHABET.iter().position(|&c| c == byte).unwrap() as u32;
        count += 6;
        if count >= 8 {
            count -= 8;
            out.push((bits >> count) as u8);
            bits &= (1 << count) - 1;
        }
    }
    out
}

#[tokio::test]
async fn the_scaffolds_return_the_page_type() {
    let scaffolded = Implementations { catalog: Arc::new(scaffolds::catalog::Implementation::new()) };
    let (status, body) = get(scaffolded, "/api/items").await;
    assert_eq!(status, StatusCode::NOT_IMPLEMENTED, "{body}");

    let deps = Deps { span: tracing::Span::none() };
    let handlers = implementation::create(deps).await.unwrap();
    let (status, body) = get(Implementations::new(handlers), "/api/tags").await;
    assert_eq!(status, StatusCode::NOT_IMPLEMENTED, "{body}");

    // A filtered list's scaffold takes its Args, the filter decoded.
    let scaffolded = Implementations { catalog: Arc::new(scaffolds::catalog::Implementation::new()) };
    let (status, body) = get(scaffolded, "/api/parts?filter[kind]=nut").await;
    assert_eq!(status, StatusCode::NOT_IMPLEMENTED, "{body}");
}
`

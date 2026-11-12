package topcoat_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/registry"
)

// pagedService is the core's fixture whose lists page, and whose parts
// filter by a field of every kind a filter compares (D106).
const pagedService = "fixture-paged-api"

// TestAFilteredListsFormFiltersIt builds fixture-paged-api's crates with
// the pages on and runs cargo clippy, then cargo test, with
// filterFormAppTest. The parts' argument form has a control per field of
// the list's FilterBy (D106): through the parts' page, a new form renders
// each, an enum's a group of its members, text a text box, a boolean a
// select of Yes and No, and a number, a date and a time their inputs; the
// form reads the query's bracket form, an `in` sent once with commas or
// once per choice and an equality as one choice, shows the filters in
// effect, and hands the operation the typed filter, each control's
// operator set; a blank control, a field the form has no control for and
// an operator it does not write filter by nothing; the pager's links keep
// the filter, each one pair of the bracket form; and a value its field's
// type refuses, in the form's parse or the route's check, and the
// operation's own refusal at `filter[field]`, land on the field's control.
func TestAFilteredListsFormFiltersIt(t *testing.T) {
	cargoTestCrateWith(t, pagedService, filterFormAppTest, crateOptions{outputs: pagesOutputs()})
}

// TestAFilteredListsFormHoldsItsFilter reads fixture-paged-api's crate: the
// parts' form holds a member per field of the filter, reads each from the
// query's bracket form by the operator its control writes, builds the
// typed filter in its parse and writes each as one pair; its component
// names each control as the query parameter it sends and shows the errors
// at `filter[field]`; forms.rs reads a bracket's key as a step of a name;
// and a call without a form, a procedure's, sends no filter.
func TestAFilteredListsFormHoldsItsFilter(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), pagedService, pagesOutputs())
	for file, wants := range map[string][]string{
		"forms.rs": {
			"    /// What `kind` is filtered by (D106), the query's `filter[kind][in]`:\n    /// the values chosen, any one of them.\n    pub filter_kind: Vec<String>,\n",
			"    /// What `name` is filtered by (D106), the query's `filter[name][contains]`:\n    /// text it holds, in any case.\n    pub filter_name: Option<String>,\n",
			"    /// What `quantity` is filtered by (D106), the query's `filter[quantity]`:\n    /// the value it equals.\n    pub filter_quantity: Option<String>,\n",
			"            filter_kind: filter_choices(posted.get(\"filter\").get(\"kind\")),\n",
			"            filter_name: posted.get(\"filter\").get(\"name\").get(\"contains\").first(),\n",
			"            filter_quantity: filter_equal(posted.get(\"filter\").get(\"quantity\")),\n",
			"        let filter_kind = list::<Option<Vec<crate::api::types::PartKind>>>(&mut errors, \"filter[kind]\", &self.filter_kind, text);\n",
			"        let filter_quantity = single::<Option<crate::api::types::GenericInt64>>(&mut errors, \"filter[quantity]\", integer(self.filter_quantity.as_deref()));\n",
			"        let filter_discontinued = single::<Option<bool>>(&mut errors, \"filter[discontinued]\", yes_no(self.filter_discontinued.as_deref()));\n",
			"            filter: crate::api::CatalogListPartsFilter {\n",
			"                name: self.filter_name.clone().map(|contains| crate::api::runtime::filter::Text { contains: Some(contains), ..Default::default() }),\n",
			"                kind: filter_kind.map(|in_| crate::api::runtime::filter::Equals { in_, ..Default::default() }),\n",
			"                retired_on: filter_retired_on.map(|eq| crate::api::runtime::filter::Ordered { eq: Some(eq), ..Default::default() }),\n",
			"            pairs.push((\"filter[kind][in]\".to_owned(), self.filter_kind.join(\",\")));\n",
			"            pairs.push((\"filter[name][contains]\".to_owned(), value.clone()));\n",
			"                \"filter\" => matches!(rest, [Segment::Key(field), ..] if matches!(field.as_str(), \"id\" | \"name\" | \"kind\" | \"quantity\" | \"weight\" | \"price\" | \"discontinued\" | \"madeAt\" | \"retiredOn\" | \"supplierId\")),\n",
			"fn filter_choices(field: &Posted) -> Vec<String> {\n",
			"fn filter_equal(field: &Posted) -> Option<String> {\n",
			"                Err(_) if !digits.is_empty() && !digits.contains(['.', '[']) => Segment::Key(digits.to_owned()),\n",
			`<label class="ss-choice"><input type="checkbox" name="filter[kind][in]" value="nut" checked=(checked(&form.filter_kind, "nut"))>"Nut"</label>`,
			"            for message in errors.under(\"filter[kind]\") {\n",
			`<input id="catalog-list-parts-filter-name" name="filter[name][contains]" type="text" value=(form.filter_name.clone()) aria-invalid=(errors.invalid("filter[name]"))>`,
			`<input id="catalog-list-parts-filter-retired-on" name="filter[retiredOn]" type="date" value=(form.filter_retired_on.clone()) aria-invalid=(errors.invalid("filter[retiredOn]"))>`,
		},
		// A call without a form sends no filter.
		"procedures.rs": {
			"        Ok(crate::api::CatalogListPartsArgs {\n            filter: Default::default(),\n            page: crate::api::CatalogListPartsArgs::PAGE\n",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(files[file], want) {
				t.Errorf("fixture-paged-api's %s lacks %q", file, want)
			}
		}
	}
	// The items' form, of a list without a FilterBy, holds none.
	items := files["forms.rs"]
	start := strings.Index(items, "pub struct CatalogListItemsArgsForm {")
	end := strings.Index(items, "pub struct CatalogListPartsArgsForm {")
	if start < 0 || end < start || strings.Contains(items[start:end], "filter[") || strings.Contains(items[start:end], "pub filter_") {
		t.Error("fixture-paged-api's items' form, of a list without a FilterBy, names a filter")
	}

	// A crate without a filtered list reads no bracket's key.
	console, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	for _, unwanted := range []string{"fn filter_choices", "fn filter_equal", "Segment::Key(digits.to_owned())"} {
		if strings.Contains(console["forms.rs"], unwanted) {
			t.Errorf("fixture-console-api's forms.rs, which filters no list, contains %q", unwanted)
		}
	}
}

// filterFormAppTest is tests/app.rs of fixture-paged-api's Topcoat crate
// with the pages on. Its catalog answers the parts with one part, named
// for the filter it was asked with, and another page after it; a filter
// whose name holds boom is its own refusal, at the route's parameter.
const filterFormAppTest = `use std::sync::Arc;

use async_trait::async_trait;
use schemas_fixture_paged_api_topcoat::api::runtime::page::{Cursor, Page};
use schemas_fixture_paged_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_paged_api_topcoat::api::{
    types, CatalogGetItemArgs, CatalogImplementation, CatalogListItemsArgs, CatalogListPartsArgs, CatalogListPartsFilter,
    CatalogListTagsArgs, CatalogPlaceBasketArgs, Implementations,
};
use schemas_fixture_paged_api_topcoat::forms::CatalogListPartsArgsForm;
use schemas_fixture_paged_api_topcoat::RouterBuilderFixturePagedApiExt;
use serde_json::{json, Value};
use topcoat::router::{to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

const PART: &str = "00000000-0000-4000-8000-000000000011";
const SUPPLIER: &str = "00000000-0000-4000-8000-0000000000a1";

// What one member of a filter holds: each operator the form writes that is
// set, its values as the wire writes them, and "more" for any other.
fn member(field: &str, eq: Value, in_: Value, contains: Option<&str>, more: bool) -> String {
    let mut said = field.to_owned();
    if !eq.is_null() {
        said += &format!(" eq={eq}");
    }
    if in_.as_array().is_some_and(|values| !values.is_empty()) {
        said += &format!(" in={in_}");
    }
    if let Some(text) = contains {
        said += &format!(" contains={text}");
    }
    if more {
        said += " more";
    }
    said
}

// What a filter holds, member by member; "no filter" for one that filters
// by nothing.
fn said(filter: &CatalogListPartsFilter) -> String {
    let mut members = Vec::new();
    if let Some(by) = &filter.id {
        members.push(member("id", json!(by.eq), json!(by.in_), None, by.is_null.is_some()));
    }
    if let Some(by) = &filter.name {
        members.push(member("name", json!(by.eq), json!(by.in_), by.contains.as_deref(), by.is_null.is_some()));
    }
    if let Some(by) = &filter.kind {
        members.push(member("kind", json!(by.eq), json!(by.in_), None, by.is_null.is_some()));
    }
    if let Some(by) = &filter.quantity {
        let more = by.gt.is_some() || by.gte.is_some() || by.lt.is_some() || by.lte.is_some() || by.is_null.is_some();
        members.push(member("quantity", json!(by.eq), json!(by.in_), None, more));
    }
    if let Some(by) = &filter.weight {
        let more = by.gt.is_some() || by.gte.is_some() || by.lt.is_some() || by.lte.is_some() || by.is_null.is_some();
        members.push(member("weight", json!(by.eq), json!(by.in_), None, more));
    }
    if let Some(by) = &filter.price {
        let more = by.gt.is_some() || by.gte.is_some() || by.lt.is_some() || by.lte.is_some() || by.is_null.is_some();
        members.push(member("price", json!(by.eq), json!(by.in_), None, more));
    }
    if let Some(by) = &filter.discontinued {
        members.push(member("discontinued", json!(by.eq), json!(by.in_), None, by.is_null.is_some()));
    }
    if let Some(by) = &filter.made_at {
        let more = by.gt.is_some() || by.gte.is_some() || by.lt.is_some() || by.lte.is_some() || by.is_null.is_some();
        members.push(member("madeAt", json!(by.eq), json!(by.in_), None, more));
    }
    if let Some(by) = &filter.retired_on {
        let more = by.gt.is_some() || by.gte.is_some() || by.lt.is_some() || by.lte.is_some() || by.is_null.is_some();
        members.push(member("retiredOn", json!(by.eq), json!(by.in_), None, more));
    }
    if let Some(by) = &filter.supplier_id {
        members.push(member("supplierId", json!(by.eq), json!(by.in_), None, by.is_null.is_some()));
    }
    if members.is_empty() {
        return "no filter".to_owned();
    }
    members.join("; ")
}

// An id as the wire writes it.
fn wire_id(text: &str) -> Value {
    json!(serde_json::from_value::<types::IdentityUUID>(json!(text)).unwrap())
}

struct Shop;

#[async_trait]
impl CatalogImplementation for Shop {
    async fn list_items(&self, _ctx: RequestContext, _args: CatalogListItemsArgs) -> Result<Page<types::ItemView>, ApiError> {
        Ok(Page::last(Vec::new()))
    }
    async fn get_item(&self, _ctx: RequestContext, _args: CatalogGetItemArgs) -> Result<types::ItemView, ApiError> {
        Err(ApiError::not_found("No such item"))
    }
    async fn list_tags(&self, _ctx: RequestContext, _args: CatalogListTagsArgs) -> Result<Page<String>, ApiError> {
        Ok(Page::last(Vec::new()))
    }
    async fn list_parts(&self, _ctx: RequestContext, args: CatalogListPartsArgs) -> Result<Page<types::PartView>, ApiError> {
        // The operation's own refusal, at the route's parameter of a field.
        if args.filter.name.as_ref().and_then(|by| by.contains.as_deref()) == Some("boom") {
            let details = json!({"location": "query", "parameter": "filter[name]", "reason": "names no part"});
            return Err(ApiError::bad_request("Invalid query parameter filter[name]: names no part").with_details(details));
        }
        let part: types::PartView = serde_json::from_value(json!({
            "id": PART,
            "name": said(&args.filter),
            "kind": "bolt",
            "quantity": 1,
            "weight": 1.5,
            "price": "2.50",
            "discontinued": false,
            "madeAt": "2026-01-01T00:00:00Z",
            "retiredOn": null,
            "supplierId": null,
        }))
        .unwrap();
        // Another page follows, so the pager links it with the filter.
        Ok(Page::more(vec![part], Cursor::encode(&json!({"id": PART}))?))
    }
    async fn place_basket(&self, _ctx: RequestContext, _args: CatalogPlaceBasketArgs) -> Result<types::BasketView, ApiError> {
        Err(ApiError::not_implemented("place_basket"))
    }
}

fn app() -> Router {
    Router::builder().discover().fixture_paged_api(Implementations { catalog: Arc::new(Shop) }).build()
}

async fn get(uri: &str) -> (StatusCode, String) {
    let request = http::Request::builder().uri(uri).body(Body::empty()).unwrap();
    let response = app().handle(request).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    (status, html)
}

// The form of a query's pairs.
fn form(pairs: &[(&str, &str)]) -> CatalogListPartsArgsForm {
    CatalogListPartsArgsForm::from_pairs(pairs.iter().map(|(name, value)| ((*name).to_owned(), (*value).to_owned())))
}

#[tokio::test]
async fn a_filtered_lists_form_has_a_control_per_field() {
    let (status, html) = get("/console/parts").await;
    assert_eq!(status, StatusCode::OK, "{html}");
    for want in [
        r#"<form method="get" class="ss-filter">"#,
        // An enum is a group of its members, sent as an in.
        r#"<fieldset class="ss-choices" data-field="filter[kind]"><legend>Kind</legend>"#,
        r#"<label class="ss-choice"><input type="checkbox" name="filter[kind][in]" value="bolt">Bolt</label>"#,
        r#"<label class="ss-choice"><input type="checkbox" name="filter[kind][in]" value="washer">Washer</label>"#,
        // Text is a text box, sent as a contains.
        r#"<label for="catalog-list-parts-filter-name">Name</label><input id="catalog-list-parts-filter-name" name="filter[name][contains]" type="text">"#,
        // A boolean is a select of yes and no after a blank option, and
        // any other field its type's control, each sent as an equality.
        r#"<select id="catalog-list-parts-filter-discontinued" name="filter[discontinued]"><option value="" selected=""></option><option value="true">Yes</option><option value="false">No</option></select>"#,
        r#"<input id="catalog-list-parts-filter-quantity" name="filter[quantity]" type="number" step="1""#,
        r#"<input id="catalog-list-parts-filter-weight" name="filter[weight]" type="number" step="any">"#,
        r#"<input id="catalog-list-parts-filter-price" name="filter[price]" type="text">"#,
        r#"<label for="catalog-list-parts-filter-made-at">Made at (UTC)</label><input id="catalog-list-parts-filter-made-at" name="filter[madeAt]" type="datetime-local" step="any">"#,
        r#"<input id="catalog-list-parts-filter-retired-on" name="filter[retiredOn]" type="date">"#,
        r#"<label for="catalog-list-parts-filter-supplier-id">Supplier id</label><input id="catalog-list-parts-filter-supplier-id" name="filter[supplierId]" type="text""#,
        // The list is asked with no filter.
        "no filter",
    ] {
        assert!(html.contains(want), "missing {want} in {html}");
    }
    // No control writes a range or an isNull.
    for operator in ["[gt]", "[gte]", "[lt]", "[lte]", "[isNull]"] {
        assert!(!html.contains(operator), "{operator} in {html}");
    }
}

#[tokio::test]
async fn the_form_reads_the_bracket_form_and_hands_the_operation_its_typed_filter() {
    let query = format!(
        "filter[kind][in]=bolt,nut&filter[name][contains]=hex+nut&filter[discontinued]=true&filter[quantity]=10&filter[weight]=1.5\
         &filter[price]=2.50&filter[madeAt]=2026-03-01T00:00&filter[retiredOn]=2026-06-30&filter[supplierId]={SUPPLIER}&filter[id]={PART}"
    );
    let (status, html) = get(&format!("/console/parts?{query}")).await;
    assert_eq!(status, StatusCode::OK, "{html}");
    let asked = [
        format!("id eq={}", wire_id(PART)),
        "name contains=hex nut".to_owned(),
        r#"kind in=["bolt","nut"]"#.to_owned(),
        "quantity eq=10".to_owned(),
        "weight eq=1.5".to_owned(),
        // A decimal is its canonical text.
        r#"price eq="2.5""#.to_owned(),
        "discontinued eq=true".to_owned(),
        // A datetime-local control's value is read as UTC.
        r#"madeAt eq="2026-03-01T00:00:00Z""#.to_owned(),
        r#"retiredOn eq="2026-06-30""#.to_owned(),
        format!("supplierId eq={}", wire_id(SUPPLIER)),
    ]
    .join("; ");
    assert!(html.contains(&asked), "missing {asked} in {html}");
    // The controls show the filters in effect.
    for want in [
        r#"name="filter[kind][in]" value="bolt" checked="">Bolt</label>"#,
        r#"name="filter[kind][in]" value="nut" checked="">Nut</label>"#,
        r#"name="filter[kind][in]" value="washer">Washer</label>"#,
        r#"name="filter[name][contains]" type="text" value="hex nut">"#,
        r#"<option value="true" selected="">Yes</option>"#,
        r#"name="filter[weight]" type="number" step="any" value="1.5">"#,
        r#"name="filter[price]" type="text" value="2.50">"#,
        r#"name="filter[retiredOn]" type="date" value="2026-06-30">"#,
    ] {
        assert!(html.contains(want), "missing {want} in {html}");
    }
    // The pager's link to the next page keeps the filter, each one pair,
    // as a header's link to another order does.
    let pager = html.split(r#"<nav class="ss-pager""#).nth(1).unwrap();
    let kept = "filter%5Bname%5D%5Bcontains%5D=hex%20nut&amp;filter%5Bkind%5D%5Bin%5D=bolt%2Cnut&amp;filter%5Bquantity%5D=10";
    assert!(pager.contains(kept) && pager.contains("cursor="), "missing {kept} in {pager}");
    assert!(html.contains(&format!("{kept}&amp;filter%5Bweight%5D=1.5")) && html.contains(r#"&amp;sort=quantity" data-ss-sort="quantity">"#), "{html}");
}

#[tokio::test]
async fn the_form_reads_an_in_and_an_equality_however_they_were_sent() {
    for (query, asked) in [
        // A browser sends each checked member under the group's name.
        ("filter[kind][in]=bolt&filter[kind][in]=nut", r#"kind in=["bolt","nut"]"#),
        ("filter%5Bkind%5D%5Bin%5D=bolt&filter%5Bkind%5D%5Bin%5D=nut", r#"kind in=["bolt","nut"]"#),
        // An equality is one choice, beside an in's.
        ("filter[kind]=washer", r#"kind in=["washer"]"#),
        ("filter[kind][eq]=washer&filter[kind][in]=bolt", r#"kind in=["bolt","washer"]"#),
        ("filter[kind]=bolt&filter[kind][in]=bolt,+nut", r#"kind in=["bolt","nut"]"#),
        ("filter[quantity][eq]=7", "quantity eq=7"),
        ("filter[discontinued]=false", "discontinued eq=false"),
        // A blank control filters by nothing, and so do a field the form
        // has no control for and an operator none of its controls writes.
        ("filter[name][contains]=&filter[discontinued]=&filter[quantity]=&filter[kind][in]=", "no filter"),
        ("filter[color]=red&filter[quantity][gte]=5&filter[retiredOn][isNull]=true&filter[name]=Hex", "no filter"),
    ] {
        let (status, html) = get(&format!("/console/parts?{query}")).await;
        assert_eq!(status, StatusCode::OK, "{query}: {html}");
        assert!(html.contains(&format!(">{asked}<")), "{query}: missing {asked} in {html}");
    }
    let (_, html) = get("/console/parts?filter[kind]=washer").await;
    assert!(html.contains(r#"name="filter[kind][in]" value="washer" checked="">Washer</label>"#), "{html}");
}

#[tokio::test]
async fn a_refused_filter_lands_on_its_fields_control() {
    // A choice the enum does not hold, which the form's parse refuses.
    let (status, html) = get("/console/parts?filter[kind][in]=bolt,gear").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(
        html.contains(r#"<fieldset class="ss-choices" data-field="filter[kind]"><legend>Kind</legend><p class="field-error">does not match the declared type</p>"#),
        "{html}"
    );
    assert!(html.contains(r#"value="bolt" checked="">Bolt</label>"#), "{html}");

    // A value its control's reader refuses.
    let (status, html) = get("/console/parts?filter[quantity]=many").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains(r#"value="many" aria-invalid="true"><p class="field-error">"#), "{html}");

    // A date that is none, which the route's check refuses.
    let (status, html) = get("/console/parts?filter[retiredOn]=yesterday").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(
        html.contains(r#"name="filter[retiredOn]" type="date" value="yesterday" aria-invalid="true"><p class="field-error">failed to parse Date: yesterday</p>"#),
        "{html}"
    );

    // The operation's own refusal at the route's parameter, its status.
    let (status, html) = get("/console/parts?filter[name][contains]=boom&filter[kind]=nut").await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "{html}");
    assert!(
        html.contains(r#"name="filter[name][contains]" type="text" value="boom" aria-invalid="true"><p class="field-error">names no part</p>"#),
        "{html}"
    );
    assert!(html.contains(r#"value="nut" checked="">Nut</label>"#), "{html}");
    assert!(!html.contains("form-error"), "{html}");
}

#[test]
fn the_form_writes_each_filter_as_one_pair_of_the_bracket_form() {
    let sent = form(&[
        ("filter[kind][in]", "bolt"),
        ("filter[kind][in]", "nut"),
        ("filter[kind]", "washer"),
        ("filter[name][contains]", "hex nut"),
        ("filter[discontinued]", "false"),
        ("filter[quantity][eq]", "10"),
        ("filter[weight][gte]", "2"),
        ("sort", "-quantity"),
        ("limit", "5"),
    ]);
    assert_eq!(sent.filter_kind, ["bolt", "nut", "washer"]);
    assert_eq!(sent.filter_name.as_deref(), Some("hex nut"));
    assert_eq!((sent.filter_quantity.as_deref(), sent.filter_weight.as_deref()), (Some("10"), None));
    let pairs: Vec<(String, String)> = [
        ("filter[name][contains]", "hex nut"),
        ("filter[kind][in]", "bolt,nut,washer"),
        ("filter[quantity]", "10"),
        ("filter[discontinued]", "false"),
        ("sort", "-quantity"),
        ("limit", "5"),
    ]
    .map(|(name, value)| (name.to_owned(), value.to_owned()))
    .into();
    assert_eq!(sent.query_pairs(), pairs);
    // The pairs it writes read back as the form.
    assert_eq!(CatalogListPartsArgsForm::from_pairs(pairs), sent);

    let args = sent.parse().unwrap();
    assert_eq!(said(&args.filter), r#"name contains=hex nut; kind in=["bolt","nut","washer"]; quantity eq=10; discontinued eq=false"#);
    assert_eq!(args.page.sort.map(|sort| sort.to_string()).as_deref(), Some("-quantity"));

    // A new form filters by nothing.
    let args = CatalogListPartsArgsForm::new().parse().unwrap();
    assert_eq!(said(&args.filter), "no filter");
    assert!(CatalogListPartsArgsForm::new().query_pairs().is_empty());
}
`

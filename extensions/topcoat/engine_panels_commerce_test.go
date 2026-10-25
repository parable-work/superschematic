package topcoat_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestTheCommercePanels builds fixture-engine-api's crate with its pages
// on and cargo-tests the commerce behaviors' panels (D70) of
// engine_panels.rs on canned describe documents and records, shaped as the
// engine reads them: each describe document is built from the behaviors'
// own declarations (commerceDescribe), and each record holds the fields
// the guide's shop types hold. Through Router::handle, test pages render
// each panel:
//   - Stock: its levels, low marked, a form per operation the type offers
//     (receive, adjust, count) posting to its route of the engine pages, a
//     hidden one left out, and its movements, or the engine's refusal of
//     the read;
//   - Calendar: the time zone, the week's hours, the closures, and the
//     bookings over a window of dates, or the engine's refusal, or the
//     date the query got wrong;
//   - Holds: the items with their unit prices as money, the total as
//     money, "Mixed currencies" when it is null, and the state;
//   - Payments: every amount as money, the attempts, and refundPayment's
//     form, or the types a refund is signed off through, or, on a refund,
//     the instance it refunds;
//   - and no panel for a behavior that is not commerce's.
//
// The fixture's engine composes none of commerce's behaviors, so a read a
// panel makes is the engine's refusal; the canned answers render through
// the panels' own views. Every page is held to the implicit-submission
// rule.
func TestTheCommercePanels(t *testing.T) {
	cases, err := json.Marshal(commerceCases(t))
	if err != nil {
		t.Fatal(err)
	}
	cargoTestCrateWith(t, engineService, commerceAppTest, crateOptions{
		before:  map[string]map[string]any{engineDatabase: engineDatabaseOutputs()},
		files:   map[string]string{"support/commerce.json": string(cases)},
		appends: map[string]string{"src/engine_panels.rs": commercePages},
	})
}

// commercePages are added to engine_panels.rs for TestTheCommercePanels:
// a page per case of tests/support/commerce.json, outside the console's
// layout, that renders a commerce panel through commerce_panel, which
// reads the engine as the page's caller, or through the panel's view with
// the canned answer of its read; and unit tests of what the panels decide
// on their own.
const commercePages = `

/// A case of tests/support/commerce.json: the type's describe document,
/// the instance's record, and the answer of the read its panel makes.
fn commerce_case(name: &str) -> (serde_json::Value, superschematic_engine::InstanceRecord, serde_json::Value) {
    let cases: serde_json::Value =
        serde_json::from_str(include_str!(concat!(env!("CARGO_MANIFEST_DIR"), "/tests/support/commerce.json")))
            .expect("the canned cases");
    let case = &cases[name];
    let record = &case["record"];
    let text = |key: &str| record[key].as_str().unwrap_or_default().to_owned();
    let number = |key: &str| record[key].as_i64().unwrap_or_default();
    let object = |key: &str| record[key].as_object().cloned().unwrap_or_default();
    let record = superschematic_engine::InstanceRecord {
        namespace: text("namespace"),
        schema: text("schema"),
        id: text("id"),
        schema_namespace: text("schemaNamespace"),
        version: number("version"),
        seq: number("seq"),
        data: object("data"),
        behaviors: object("behaviors"),
        created_at: number("createdAt"),
        created_by: text("createdBy"),
        updated_at: number("updatedAt"),
        updated_by: text("updatedBy"),
        value_refs: None,
    };
    (case["describe"].clone(), record, case["read"].clone())
}

/// A panel as its test page shows it, or a note that there is none.
fn commerce_shown<'a>(cx: &'a topcoat::context::Cx, panel: Option<topcoat::view::BoxView<'a>>) -> impl topcoat::view::View + 'a {
    let __cx = cx;
    topcoat::view::view! {
        <main>
            match panel {
                Some(panel) => (panel),
                None => <p class="no-panel">"No panel"</p>,
            }
        </main>
    }
}

/// commerce_panel of a case's record.
async fn commerce_case_panel<'a>(cx: &'a topcoat::context::Cx, case: &str, behavior: &str) -> Option<topcoat::view::BoxView<'a>> {
    let (describe, record, _) = commerce_case(case);
    commerce_panel(cx, behavior, &describe, &record).await
}

#[topcoat::router::page("/panels/stock")]
pub async fn commerce_stock_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    Ok(commerce_shown(cx, commerce_case_panel(cx, "stock", "Stock").await))
}

#[topcoat::router::page("/panels/stock/movements")]
pub async fn commerce_stock_movements_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    let (describe, record, read) = commerce_case("stock");
    Ok(commerce_shown(cx, Some(commerce::stock_view(cx, &describe, &record, Some(Ok(read)), true))))
}

#[topcoat::router::page("/panels/stock/low")]
pub async fn commerce_stock_low_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    Ok(commerce_shown(cx, commerce_case_panel(cx, "stockLow", "Stock").await))
}

#[topcoat::router::page("/panels/calendar")]
pub async fn commerce_calendar_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    Ok(commerce_shown(cx, commerce_case_panel(cx, "calendar", "Calendar").await))
}

#[topcoat::router::page("/panels/calendar/bookings")]
pub async fn commerce_calendar_bookings_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    let (describe, record, read) = commerce_case("calendar");
    let window = commerce::Window::of(Some("2026-10-26".to_owned()), Some("2026-10-27".to_owned()), 0);
    Ok(commerce_shown(cx, Some(commerce::calendar_view(cx, &describe, &record, window, Some(Ok(read)), false))))
}

#[topcoat::router::page("/panels/holds")]
pub async fn commerce_holds_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    Ok(commerce_shown(cx, commerce_case_panel(cx, "checkout", "Holds").await))
}

#[topcoat::router::page("/panels/holds/mixed")]
pub async fn commerce_holds_mixed_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    Ok(commerce_shown(cx, commerce_case_panel(cx, "mixed", "Holds").await))
}

#[topcoat::router::page("/panels/payments")]
pub async fn commerce_payments_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    Ok(commerce_shown(cx, commerce_case_panel(cx, "checkout", "Payments").await))
}

#[topcoat::router::page("/panels/payments/approved")]
pub async fn commerce_payments_approved_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    let (describe, record, _) = commerce_case("checkout");
    Ok(commerce_shown(cx, Some(commerce::payments_view(cx, &describe, &record, &["Refund".to_owned()]))))
}

#[topcoat::router::page("/panels/refund")]
pub async fn commerce_refund_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    Ok(commerce_shown(cx, commerce_case_panel(cx, "refund", "Payments").await))
}

#[topcoat::router::page("/panels/other")]
pub async fn commerce_other_page(cx: &topcoat::context::Cx) -> topcoat::Result<impl topcoat::view::View> {
    Ok(commerce_shown(cx, commerce_case_panel(cx, "refund", "Approvals").await))
}

#[cfg(test)]
mod commerce_panel_tests {
    use super::commerce::{Window, refunds_through_approvals};

    #[test]
    fn a_type_refunds_through_approvals_when_its_refund_link_targets_the_schema() {
        let refund = serde_json::json!({ "types": { "Refund": { "behaviors": [
            { "name": "Workflow", "config": {} },
            { "name": "Approvals", "config": { "gates": { "approved": { "count": 2, "permission": "refunds.approve" } } } },
            { "name": "Links", "config": { "links": { "order": { "schema": "Checkout", "required": true } } } },
            { "name": "Payments", "config": { "amount": { "field": "amountCents" }, "currency": "GBP", "refundOf": { "link": "order" }, "issueOn": ["approved"] } },
        ] } } });
        assert!(refunds_through_approvals(&refund, "Refund", "Checkout"));
        assert!(!refunds_through_approvals(&refund, "Refund", "Order"));
        let mut unsigned = refund.clone();
        unsigned["types"]["Refund"]["behaviors"].as_array_mut().unwrap().remove(1);
        assert!(!refunds_through_approvals(&unsigned, "Refund", "Checkout"));
        let checkout = serde_json::json!({ "types": { "Checkout": { "behaviors": [{ "name": "Payments", "config": { "amount": { "holds": true } } }] } } });
        assert!(!refunds_through_approvals(&checkout, "Checkout", "Checkout"));
    }

    #[test]
    fn a_window_of_bookings_is_its_dates_in_utc_both_included() {
        let window = Window::of(Some("2026-10-26".to_owned()), Some("2026-10-27".to_owned()), 0);
        assert_eq!(window.range, Ok((1_792_972_800_000, 1_793_145_600_000)));
        let week = Window::of(None, None, 1_792_573_200_000);
        assert_eq!((week.from.as_str(), week.to.as_str()), ("2026-10-21", "2026-10-27"));
        assert_eq!(Window::of(Some("2026-02-30".to_owned()), None, 0).range, Err("From is no date (YYYY-MM-DD): 2026-02-30".to_owned()));
    }

    #[test]
    fn the_panels_name_the_operations_they_show() {
        assert!(super::commerce_shows("Stock", "receiveStock"));
        assert!(super::commerce_shows("Calendar", "listBookings"));
        assert!(super::commerce_shows("Payments", "refundPayment"));
        assert!(!super::commerce_shows("Payments", "capturePayment"));
        assert!(!super::commerce_shows("Holds", "retakeHolds"));
        assert!(!super::commerce_shows("Workflow", "transition"));
    }
}
`

// commerceAppTest is tests/app.rs of TestTheCommercePanels: an app over
// fixture-engine-api whose pages' caller is ada, who holds Note, so the
// fixture's engine refuses each read a commerce panel makes of a type it
// does not have.
const commerceAppTest = `use std::sync::Arc;

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_engine_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_engine_api_topcoat::api::{types, Implementations, NotesImplementation, NotesListNotesArgs};
use schemas_fixture_engine_api_topcoat::{engine, views, PageAuthenticator, RouterBuilderFixtureEngineApiExt};
use topcoat::context::Cx;
use topcoat::router::{to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

struct NoBearer;

#[async_trait]
impl Authenticator for NoBearer {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

struct Ada;

#[async_trait]
impl PageAuthenticator for Ada {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(Some(Principal::new("ada", vec!["Note"])))
    }
}

struct Notes;

#[async_trait]
impl NotesImplementation for Notes {
    async fn note_count(&self, _ctx: RequestContext) -> Result<types::NoteCount, ApiError> {
        Ok(types::NoteCount { count: 0.0 })
    }

    async fn list_notes(&self, _ctx: RequestContext, _args: NotesListNotesArgs) -> Result<Vec<types::NoteTitle>, ApiError> {
        Ok(Vec::new())
    }
}

fn app() -> Router {
    let path = std::env::temp_dir().join(format!("topcoat-commerce-{}-{:?}.db", std::process::id(), std::thread::current().id()));
    let _ = std::fs::remove_file(&path);
    let engine = engine::open(&path).expect("open the engine");
    let implementations = Implementations { notes: Arc::new(Notes), authenticator: Arc::new(NoBearer) };
    Router::builder().discover().fixture_engine_api(implementations, Ada, engine).build()
}

/// The page at uri, which answers 200 and holds to the implicit-submission
/// rule.
async fn page(router: &Router, uri: &str) -> String {
    let request = http::Request::builder().method("GET").uri(uri).body(Body::empty()).unwrap();
    let response = router.handle(request).await;
    let status = response.status();
    let headers = response.headers().clone();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = String::from_utf8(bytes.to_vec()).unwrap();
    assert_eq!(status, StatusCode::OK, "{uri}: {body}");
    enter::check_page(uri, &headers, &body);
    body
}

/// An amount as the panels show it.
fn money(amount: i64, currency: &str) -> String {
    format!(r#"<data value="{amount}">{}</data>"#, views::money_text(amount, currency))
}

fn has(body: &str, wants: &[&str]) {
    for want in wants {
        assert!(body.contains(want), "lacks {want}:\n{body}");
    }
}

fn lacks(body: &str, absents: &[&str]) {
    for absent in absents {
        assert!(!body.contains(absent), "has {absent}:\n{body}");
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn stock_shows_its_levels_its_forms_and_its_movements() {
    let router = app();
    let body = page(&router, "/panels/stock").await;
    has(&body, &[
        r#"<section class="ss-panel" data-behavior="Stock">"#,
        r#"<div data-field="Stock.onHand"><dt>On hand</dt><dd>12</dd></div>"#,
        r#"<div data-field="Stock.held"><dt>Held</dt><dd>2</dd></div>"#,
        r#"<div data-field="Stock.available"><dt>Available</dt><dd>10</dd></div>"#,
        r#"<div data-field="Stock.low"><dt>Low</dt><dd>No</dd></div>"#,
        // Each form posts to its operation's route of the engine pages,
        // with the bounds its parameters' schema gives.
        r#"<form method="post" action="/console/engine/Inventory/tea/operations/receiveStock" class="ss-operation" data-operation="receiveStock">"#,
        r#"<input id="stock-receivestock-quantity" name="quantity" type="number" step="1" required="" min="1" max="9007199254740991">"#,
        r#"<input id="stock-receivestock-reason" name="reason" type="text" required="" minlength="1" maxlength="500">"#,
        r#"<input id="stock-receivestock-reference" name="reference" type="text" minlength="1" maxlength="255">"#,
        r#"action="/console/engine/Inventory/tea/operations/adjustStock""#,
        r#"<input id="stock-adjuststock-delta" name="delta" type="number" step="1" required="" min="-9007199254740991" max="9007199254740991">"#,
        r#"action="/console/engine/Inventory/tea/operations/countStock""#,
        r#"<input id="stock-countstock-onhand" name="onHand" type="number" step="1" required="" min="0""#,
        // The fixture's engine has no Inventory: the read's refusal, as
        // the engine words it.
        "<h3>Movements</h3>",
        r#"<p class="ss-refusal">The movements could not be read: forbidden: ada may not read Inventory in namespace fixture-engine-db</p>"#,
    ]);
    lacks(&body, &["data-low", "Newest movements"]);

    let body = page(&router, "/panels/stock/movements").await;
    has(&body, &[
        r#"<table class="ss-table" data-operation="listStockMovements">"#,
        r#"<td><time datetime="2026-10-26T07:33:20Z">26 Oct 2026, 07:33 UTC</time></td><td>commit</td><td>-2</td><td>12</td><td></td><td></td><td><a href="/console/engine/Checkout/c-1">Checkout c-1</a></td><td>shop-db-engine-runner</td>"#,
        "<td>receive</td><td>+15</td><td>15</td><td>Delivery</td><td>DN-1001</td><td></td><td>sam</td>",
        r#"<a href="/console/engine/Inventory/tea?movements=bW92ZW1lbnRzOjE" rel="next">Older movements</a>"#,
        r#"<a href="/console/engine/Inventory/tea">Newest movements</a>"#,
    ]);

    // A low item is marked; an operation the describe document does not
    // list (a hidden one) has no form, and a hidden list no read.
    let body = page(&router, "/panels/stock/low").await;
    has(&body, &[
        r#"<section class="ss-panel" data-behavior="Stock" data-low="true">"#,
        r#"<dd>3 <strong class="ss-low">Low</strong></dd>"#,
        "<dd>Yes</dd>",
        "/operations/receiveStock",
    ]);
    lacks(&body, &["/operations/countStock", "Movements", "ss-refusal"]);
}

#[tokio::test(flavor = "multi_thread")]
async fn calendar_shows_its_week_its_closures_and_its_bookings() {
    let router = app();
    let body = page(&router, "/panels/calendar?bookingsFrom=2026-10-26&bookingsTo=2026-10-27").await;
    has(&body, &[
        r#"<section class="ss-panel" data-behavior="Calendar">"#,
        r#"<div data-field="Calendar.timeZone"><dt>Time zone</dt><dd>Europe/London</dd></div>"#,
        r#"<div data-field="capacity"><dt>Capacity</dt><dd>4</dd></div>"#,
        r#"<tr><th scope="row">Monday</th><td>09:00–12:30, 13:30–17:30</td></tr>"#,
        r#"<tr><th scope="row">Tuesday</th><td>09:00–17:30</td></tr>"#,
        r#"<tr><th scope="row">Sunday</th><td>Closed</td></tr>"#,
        r#"<td>closure-1</td><td><time datetime="2026-10-22T08:00:00Z">22 Oct 2026, 08:00 UTC</time></td><td><time datetime="2026-10-22T09:00:00Z">22 Oct 2026, 09:00 UTC</time></td><td>Stocktake</td>"#,
        r#"<form method="get" action="/console/engine/CollectionPoint/front" class="ss-filter" data-operation="listBookings">"#,
        r#"<input id="calendar-bookings-from" name="bookingsFrom" type="date" required="" value="2026-10-26">"#,
        r#"<input id="calendar-bookings-to" name="bookingsTo" type="date" required="" value="2026-10-27">"#,
        r#"<p class="ss-refusal">The bookings could not be read: forbidden: ada may not read CollectionPoint in namespace fixture-engine-db</p>"#,
    ]);

    // A date the query got wrong is said at the form, and nothing is read.
    let body = page(&router, "/panels/calendar?bookingsFrom=someday").await;
    has(&body, &[
        r#"value="someday""#,
        r#"<p class="form-error">From is no date (YYYY-MM-DD): someday</p>"#,
    ]);
    lacks(&body, &["ss-refusal"]);

    let body = page(&router, "/panels/calendar/bookings").await;
    has(&body, &[
        r#"<table class="ss-table" data-operation="listBookings">"#,
        r#"<td><time datetime="2026-10-26T09:00:00Z">26 Oct 2026, 09:00 UTC</time></td><td><time datetime="2026-10-26T09:15:00Z">26 Oct 2026, 09:15 UTC</time></td><td>held</td><td><a href="/console/engine/Checkout/c-1">Checkout c-1</a></td><td>pickup[0]</td>"#,
        "<td>committed, retired</td>",
        r#"<a href="/console/engine/CollectionPoint/front?bookingsFrom=2026-10-26&amp;bookingsTo=2026-10-27&amp;bookings=Ym9va2luZ3M6Mg" rel="next">Later bookings</a>"#,
    ]);
}

#[tokio::test(flavor = "multi_thread")]
async fn holds_show_their_items_and_total_as_money() {
    let router = app();
    let body = page(&router, "/panels/holds").await;
    has(&body, &[
        r#"<section class="ss-panel" data-behavior="Holds">"#,
        r#"<div data-field="Holds.state"><dt>State</dt><dd>held</dd></div>"#,
        &format!(r#"<div data-field="Holds.total"><dt>Total</dt><dd>{}</dd></div>"#, money(1200, "GBP")),
        &format!(r#"<td>lines[0]</td><td><a href="/console/engine/Inventory/tea">Inventory tea</a></td><td>2 units</td><td>{}</td><td>held</td>"#, money(450, "GBP")),
        "<td>1 unit</td>",
        r#"<td>pickup[0]</td><td><a href="/console/engine/CollectionPoint/front">CollectionPoint front</a></td><td><time datetime="2026-10-27T10:00:00Z">27 Oct 2026, 10:00 UTC</time> to <time datetime="2026-10-27T10:15:00Z">27 Oct 2026, 10:15 UTC</time></td><td></td><td>held</td>"#,
    ]);

    let body = page(&router, "/panels/holds/mixed").await;
    has(&body, &[
        "<dd>Mixed currencies</dd>",
        &money(450, "GBP"),
        &money(300, "EUR"),
    ]);
}

#[tokio::test(flavor = "multi_thread")]
async fn payments_show_their_amounts_and_how_they_are_refunded() {
    let router = app();
    let body = page(&router, "/panels/payments").await;
    has(&body, &[
        r#"<section class="ss-panel" data-behavior="Payments">"#,
        &format!(r#"<div data-field="Payments.due"><dt>Due</dt><dd>{}</dd></div>"#, money(1200, "GBP")),
        &format!(r#"<div data-field="Payments.captured"><dt>Captured</dt><dd>{}</dd></div>"#, money(1200, "GBP")),
        &format!(r#"<div data-field="Payments.outstanding"><dt>Outstanding</dt><dd>{}</dd></div>"#, money(0, "GBP")),
        r#"<div data-field="Payments.state"><dt>State</dt><dd>paid</dd></div>"#,
        &format!(r#"<td>pay_1</td><td>acmepay</td><td>{}</td><td>captured</td><td>visa ending 4242</td>"#, money(1200, "GBP")),
        // No type of the fixture's engine refunds a Checkout through
        // Approvals, so the panel holds refundPayment's form, which the
        // engine's policy and Payments decide.
        r#"<form method="post" action="/console/engine/Checkout/c-1/operations/refundPayment" class="ss-operation" data-operation="refundPayment">"#,
        r#"<input type="hidden" name="idempotencyKey" value="console-refund-7">"#,
        r#"<label for="payments-refundpayment-amount">Amount, in minor units of GBP</label><input id="payments-refundpayment-amount" name="amount" type="number" step="1" required="" min="1""#,
        r#"name="reason" type="text" required="" minlength="1" maxlength="500">"#,
    ]);
    lacks(&body, &["listPaymentActions", "ss-refusal"]);

    let body = page(&router, "/panels/payments/approved").await;
    has(&body, &[r#"<p class="ss-refunds">A refund of this Checkout is signed off through Approvals: see <a href="/console/engine/Refund">Refund</a>.</p>"#]);
    lacks(&body, &["/operations/refundPayment"]);

    let body = page(&router, "/panels/refund").await;
    has(&body, &[
        &format!(r#"<div data-field="Payments.due"><dt>Due</dt><dd>{}</dd></div>"#, money(500, "GBP")),
        "<p class=\"ss-empty\">No attempts.</p>",
        r#"<p class="ss-refunds">Refunds <a href="/console/engine/Checkout/c-1">Checkout c-1</a>.</p>"#,
    ]);
    lacks(&body, &["/operations/refundPayment"]);
}

#[tokio::test(flavor = "multi_thread")]
async fn another_behavior_has_no_commerce_panel() {
    let router = app();
    let body = page(&router, "/panels/other").await;
    has(&body, &[r#"<p class="no-panel">No panel</p>"#]);
}
`

// commerceNamespace is the namespace of the canned records, the fixture
// engine's.
const commerceNamespace = "fixture-engine-db"

// commerceCases are the canned cases of TestTheCommercePanels, by name:
// each a type's describe document, an instance's record, and what the
// engine answers the read its panel makes. The types are the commerce
// guide's shop: Inventory (Stock), CollectionPoint (Calendar), Checkout
// (Workflow, Links, Holds, Payments) and Refund (Workflow, Approvals,
// Links, Payments with refundOf).
func commerceCases(t *testing.T) map[string]any {
	t.Helper()
	stock := composedBehavior{"Stock", map[string]any{
		"price":      map[string]any{"field": "priceCents", "currency": "GBP"},
		"lowAt":      map[string]any{"field": "reorderAt"},
		"maxPerHold": 99,
		"permission": "stock.adjust",
	}}
	calendar := composedBehavior{"Calendar", map[string]any{
		"timeZone": "Europe/London",
		"hours": map[string]any{
			"mon": [][]string{{"09:00", "12:30"}, {"13:30", "17:30"}},
			"tue": [][]string{{"09:00", "17:30"}},
			"sat": [][]string{{"10:00", "16:00"}},
		},
		"capacity": 4, "slotMs": 900000, "minMs": 900000, "maxMs": 900000,
		"bufferMs": map[string]any{"after": 300000}, "leadMs": 7200000, "horizonMs": 5184000000,
		"permission": "calendar.manage",
	}}
	checkout := []composedBehavior{
		{"Workflow", map[string]any{
			"states":      []string{"cart", "held", "placed", "expired", "cancelled"},
			"transitions": []map[string]string{{"from": "cart", "to": "held"}, {"from": "held", "to": "placed"}, {"from": "held", "to": "expired"}, {"from": "held", "to": "cancelled"}, {"from": "cart", "to": "cancelled"}},
		}},
		{"Links", map[string]any{"links": map[string]any{"counter": map[string]any{"schema": "CollectionPoint"}}}},
		{"Holds", map[string]any{
			"holds": map[string]any{
				"lines":  map[string]any{"each": map[string]any{"field": "lines", "target": "inventoryId"}, "schema": "Inventory", "quantity": map[string]any{"field": "quantity"}},
				"pickup": map[string]any{"link": "counter", "interval": map[string]any{"start": "pickupAt", "durationMs": 900000}},
			},
			"takeOn": []string{"held"}, "commitOn": []string{"placed"}, "releaseOn": []string{"expired", "cancelled"},
		}},
		{"Payments", map[string]any{
			"amount": map[string]any{"holds": true}, "require": map[string]any{"placed": "paid"},
			"voidOn": []string{"expired", "cancelled"}, "afterRelease": "refund",
			"refunds": map[string]any{"permission": "payments.refund"}, "record": map[string]any{"permission": "payments.record"},
		}},
	}
	refund := []composedBehavior{
		{"Workflow", map[string]any{
			"states":      []string{"requested", "approved", "issued", "rejected", "failed"},
			"transitions": []map[string]string{{"from": "requested", "to": "approved"}, {"from": "requested", "to": "rejected"}, {"from": "approved", "to": "issued"}, {"from": "approved", "to": "failed"}},
		}},
		{"Approvals", map[string]any{"gates": map[string]any{"approved": map[string]any{"count": 2, "permission": "refunds.approve"}}}},
		{"Links", map[string]any{"links": map[string]any{"order": map[string]any{"schema": "Checkout", "required": true}}}},
		{"Payments", map[string]any{
			"amount": map[string]any{"field": "amountCents"}, "currency": "GBP",
			"refundOf": map[string]any{"link": "order"}, "issueOn": []string{"approved"},
			"record": map[string]any{"permission": "payments.record"},
		}},
	}
	// The engine document hides Payments' service operations wherever it
	// is composed (D70).
	hidden := []string{"Payments.listPaymentActions", "Payments.recordPaymentEvent", "Payments.settleRefundFor"}

	inventory := commerceDescribe(t, "Inventory", []string{"sku", "priceCents", "reorderAt"}, nil, stock)
	held := []any{
		map[string]any{"hold": "lines", "item": 0, "schema": "Inventory", "id": "tea", "quantity": 2, "state": "held", "unitPrice": 450, "currency": "GBP"},
		map[string]any{"hold": "lines", "item": 1, "schema": "Inventory", "id": "jam", "quantity": 1, "state": "held", "unitPrice": 300, "currency": "GBP"},
		map[string]any{"hold": "pickup", "item": 0, "schema": "CollectionPoint", "id": "front", "startMs": int64(1793095200000), "endMs": int64(1793096100000), "state": "held"},
	}
	mixed := []any{held[0], map[string]any{"hold": "lines", "item": 1, "schema": "Inventory", "id": "confiture", "quantity": 1, "state": "held", "unitPrice": 300, "currency": "EUR"}}
	checkoutBehaviors := func(items []any, total any) map[string]any {
		return map[string]any{
			"Workflow": map[string]any{"status": "held", "enteredAt": int64(1792573200000)},
			"Links":    map[string]any{"targets": map[string]any{"counter": map[string]any{"schema": "CollectionPoint", "id": "front"}}},
			"Holds":    map[string]any{"items": items, "state": "held", "total": total},
			"Payments": map[string]any{
				"due": 1200, "authorized": 0, "captured": 1200, "refunded": 0, "outstanding": 0, "overpaid": 0,
				"currency": "GBP", "state": "paid",
				"attempts": []any{map[string]any{
					"attempt": "pay_1", "provider": "acmepay", "providerRef": "pi_1", "amount": 1200, "currency": "GBP",
					"status": "captured", "captured": 1200, "brand": "visa", "last4": "4242",
					"openedAt": int64(1792573200000), "updatedAt": int64(1792573260000),
				}},
			},
		}
	}
	checkoutData := map[string]any{
		"lines":    []any{map[string]any{"inventoryId": "tea", "quantity": 2}, map[string]any{"inventoryId": "jam", "quantity": 1}},
		"pickupAt": "2026-10-27T10:00:00Z",
	}
	checkoutDescribe := commerceDescribe(t, "Checkout", []string{"lines", "pickupAt"}, hidden, checkout...)
	return map[string]any{
		"stock": map[string]any{
			"describe": inventory,
			"record": commerceRecord("Inventory", "tea", 4,
				map[string]any{"sku": "TEA-1", "priceCents": 450, "reorderAt": 5},
				map[string]any{"Stock": map[string]any{"onHand": 12, "held": 2, "available": 10, "low": false}}),
			"read": map[string]any{
				"items": []any{
					map[string]any{"seq": 3, "kind": "commit", "delta": -2, "onHand": 12, "actor": "shop-db-engine-runner", "at": int64(1793000000000), "holder": map[string]any{"schema": "Checkout", "id": "c-1"}, "hold": "lines", "item": 0},
					map[string]any{"seq": 2, "kind": "adjust", "delta": -1, "onHand": 14, "reason": "Damaged tin", "actor": "sam", "at": int64(1792900000000)},
					map[string]any{"seq": 1, "kind": "receive", "delta": 15, "onHand": 15, "reason": "Delivery", "reference": "DN-1001", "actor": "sam", "at": int64(1792800000000)},
				},
				"next": "bW92ZW1lbnRzOjE",
			},
		},
		"stockLow": map[string]any{
			"describe": commerceDescribe(t, "Inventory", []string{"sku", "priceCents", "reorderAt"}, []string{"Stock.listStockMovements", "Stock.countStock"}, stock),
			"record": commerceRecord("Inventory", "jam", 9,
				map[string]any{"sku": "JAM-1", "priceCents": 300, "reorderAt": 5},
				map[string]any{"Stock": map[string]any{"onHand": 4, "held": 1, "available": 3, "low": true}}),
		},
		"calendar": map[string]any{
			"describe": commerceDescribe(t, "CollectionPoint", []string{"name"}, nil, calendar),
			"record": commerceRecord("CollectionPoint", "front", 2,
				map[string]any{"name": "Front counter"},
				map[string]any{"Calendar": map[string]any{
					"timeZone": "Europe/London",
					"hours":    calendar.config["hours"],
					"closures": []any{map[string]any{"closure": "closure-1", "startMs": int64(1792656000000), "endMs": int64(1792659600000), "reason": "Stocktake"}},
				}}),
			"read": map[string]any{
				"items": []any{
					map[string]any{"holder": map[string]any{"schema": "Checkout", "id": "c-1"}, "hold": "pickup", "item": 0, "startMs": int64(1793005200000), "endMs": int64(1793006100000), "state": "held"},
					map[string]any{"holder": map[string]any{"schema": "Checkout", "id": "c-0"}, "hold": "pickup", "item": 0, "startMs": int64(1793095200000), "endMs": int64(1793096100000), "state": "committed", "retired": true},
				},
				"next": "Ym9va2luZ3M6Mg",
			},
		},
		"checkout": map[string]any{
			"describe": checkoutDescribe,
			"record":   commerceRecord("Checkout", "c-1", 7, checkoutData, checkoutBehaviors(held, map[string]any{"amount": 1200, "currency": "GBP"})),
		},
		"mixed": map[string]any{
			"describe": checkoutDescribe,
			"record":   commerceRecord("Checkout", "c-2", 3, checkoutData, checkoutBehaviors(mixed, nil)),
		},
		"refund": map[string]any{
			"describe": commerceDescribe(t, "Refund", []string{"amountCents", "reason"}, hidden, refund...),
			"record": commerceRecord("Refund", "rf-1", 1,
				map[string]any{"amountCents": 500, "reason": "Damaged"},
				map[string]any{
					"Workflow":  map[string]any{"status": "requested", "enteredAt": int64(1792573200000)},
					"Links":     map[string]any{"targets": map[string]any{"order": map[string]any{"schema": "Checkout", "id": "c-1"}}},
					"Approvals": map[string]any{"gates": map[string]any{}},
					"Payments": map[string]any{
						"due": 500, "authorized": 0, "captured": 0, "refunded": 0, "outstanding": 500, "overpaid": 0,
						"currency": "GBP", "state": "unpaid", "attempts": []any{},
					},
				}),
		},
	}
}

// composedBehavior is a behavior a type composes, with its config.
type composedBehavior struct {
	name   string
	config map[string]any
}

// commerceRecord is an instance's record as a read returns it (D16), in
// the fixture engine's namespace.
func commerceRecord(schema, id string, seq int, data, behaviors map[string]any) map[string]any {
	return map[string]any{
		"namespace": commerceNamespace, "schema": schema, "id": id, "schemaNamespace": commerceNamespace,
		"version": 1, "seq": seq, "data": data, "behaviors": behaviors,
		"createdAt": int64(1792573200000), "createdBy": "ada", "updatedAt": int64(1792573260000), "updatedBy": "ada",
	}
}

// commerceDescribe is the describe document of a type that composes
// behaviors, as the engine's tools().describe writes it: each behavior's
// config, fields, operations and vetoes from its declaration, and each
// operation the document does not hide (hidden, Behavior.operation) with
// its parameters wrapped as the engine's tools wrap them: an instance
// operation's beside the id and expectedSeq, a schema operation's alone.
func commerceDescribe(t *testing.T, schema string, own []string, hidden []string, behaviors ...composedBehavior) map[string]any {
	t.Helper()
	var fields, described, operations []any
	for _, name := range own {
		fields = append(fields, map[string]any{"name": name})
	}
	for _, behavior := range behaviors {
		declaration := behaviorDeclaration(t, behavior.name)
		var declaredFields, names, vetoes []any
		for _, field := range declaration.Fields {
			declaredFields = append(declaredFields, map[string]any{"name": field.Name, "description": field.Description})
		}
		for _, veto := range declaration.Vetoes {
			vetoes = append(vetoes, map[string]any{"code": veto.Code, "description": veto.Description})
		}
		for _, operation := range declaration.Operations {
			names = append(names, operation.Name)
			if slices.Contains(hidden, behavior.name+"."+operation.Name) {
				continue
			}
			scope := operation.Scope
			if scope == "" {
				scope = "instance"
			}
			operations = append(operations, map[string]any{
				"name": operation.Name, "behavior": behavior.name, "scope": scope,
				"title":       schema + ": " + operation.Name,
				"description": operation.Description,
				"writes":      operation.Writes,
				"params":      operationArguments(operation.ParamsSchema, scope),
				"result":      operation.ResultSchema,
				"tool":        strings.ToLower(schema[:1]) + schema[1:] + "." + operation.Name,
			})
		}
		described = append(described, map[string]any{
			"name": behavior.name, "description": declaration.Description, "config": behavior.config,
			"fields": declaredFields, "operations": names, "vetoes": vetoes,
		})
	}
	return map[string]any{
		"namespace": commerceNamespace, "name": schema, "schemaNamespace": commerceNamespace,
		"version": 1, "instanceType": schema, "fields": fields,
		"behaviors": described, "operations": operations,
	}
}

// operationArguments are an operation's arguments as the engine's tools
// render them around its paramsSchema.
func operationArguments(params map[string]any, scope string) map[string]any {
	listed, _ := params["required"].([]any)
	required := len(listed) > 0 || params["anyOf"] != nil || params["oneOf"] != nil
	if scope == "schema" {
		out := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"params": params}}
		if required {
			out["required"] = []string{"params"}
		}
		return out
	}
	out := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id":     map[string]any{"type": "string", "description": "The instance id", "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"},
			"params": params,
			"expectedSeq": map[string]any{
				"type": "integer", "minimum": 0,
				"description": "The sequence the caller last read (an instance's seq): the call is refused unless the instance is still at it",
			},
		},
		"required": []string{"id"},
	}
	if required {
		out["required"] = []string{"id", "params"}
	}
	return out
}

// behaviorFile is what a behavior's declaration file holds that a describe
// document copies.
type behaviorFile struct {
	Description string `json:"description"`
	Fields      []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"fields"`
	Operations []struct {
		Name         string         `json:"name"`
		Description  string         `json:"description"`
		Scope        string         `json:"scope"`
		Writes       bool           `json:"writes"`
		ParamsSchema map[string]any `json:"paramsSchema"`
		ResultSchema map[string]any `json:"resultSchema"`
	} `json:"operations"`
	Vetoes []struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	} `json:"vetoes"`
}

// behaviorDeclaration reads a behavior's declaration: commerce's, or the
// core's.
func behaviorDeclaration(t *testing.T, name string) behaviorFile {
	t.Helper()
	var out behaviorFile
	for _, dir := range []string{"../../runtime/engine-commerce/rust/declarations", "../../runtime/engine/rust/behaviors/declarations"} {
		data, err := os.ReadFile(filepath.Join(dir, name+".behavior.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("the declaration of %s: %v", name, err)
		}
		return out
	}
	t.Fatalf("no declaration of behavior %s", name)
	return out
}

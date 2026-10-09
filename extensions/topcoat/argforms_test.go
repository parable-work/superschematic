package topcoat_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
	"github.com/parable-work/superschematic/registry"
)

// argsService is the extension's own fixture whose operations take path,
// query and body arguments a form holds, one an input beside a path
// argument, and one a map a form does not hold.
const argsService = "fixture-args-api"

// TestArgumentFormsServeATopcoatApp does what TestFormsServeATopcoatApp
// does for fixture-args-api with argsAppTest: an argument form renders its
// path argument hidden and its rules as attributes; a valid post calls the
// operation with its arguments; an over-long reason, a forged path
// argument and the operation's refusal re-render the form with 422 and
// each error at its control or the form's; a GET filter reads repeated
// enum values, a comma-separated list, a limit, a page and a flag from the
// query and renders them as sent, and a limit out of range at its control;
// a form that embeds its input's form parses both.
func TestArgumentFormsServeATopcoatApp(t *testing.T) {
	cargoTestCrate(t, argsService, argsAppTest)
}

// TestWhatHasNoArgumentForm builds fixture-args-api: every operation with
// an argument beside its input has an argument form, save tagOrder, whose
// map the build log names; the filter is a GET form; writeReview's embeds
// its input's form. With outputs.topcoat.forms false the crate has
// neither kind of form.
func TestWhatHasNoArgumentForm(t *testing.T) {
	root := testpaths.TempDir(t)
	var log bytes.Buffer
	if _, err := buildWithNaming(t, registry.DefaultNaming(), argsService, root, rustOutputs(), &log); err != nil {
		t.Fatalf("build: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, argsService), "src", "arg_forms.rs"))
	if err != nil {
		t.Fatal(err)
	}
	forms := string(data)
	for _, want := range []string{
		"pub struct OrderCancelOrderArgsForm {",
		"pub struct OrderGetOrderArgsForm {",
		"pub struct OrderListOrdersArgsForm {\n    pub statuses: Vec<String>,",
		"pub struct ProductReviewsWriteReviewArgsForm {\n    pub product_id: Option<String>,\n    pub input: crate::forms::WriteReviewInputForm,\n}",
		"impl OrderListOrdersArgsForm {\n    /// How a page sends the form: `<form method=(Self::METHOD)>`.\n    pub const METHOD: &'static str = \"get\";",
		"push(&mut form.statuses, value, true);",
		"let arg_archived = single(&mut errors, \"archived\", self.archived.as_deref(), flag);",
		"pub fn new(id: impl ToString) -> Self {",
		"pub async fn order_cancel_order_args_fields(",
		"write_review_input_fields(form: form.input.clone(), errors: input_errors)",
	} {
		if !strings.Contains(forms, want) {
			t.Errorf("arg_forms.rs lacks %q", want)
		}
	}
	if strings.Contains(forms, "OrderTagOrderArgsForm") {
		t.Error("arg_forms.rs has a form for tagOrder, whose map a form does not hold")
	}
	if want := "no argument form for order.tagOrder: argument labels is a map"; !strings.Contains(log.String(), want) {
		t.Errorf("the build log lacks %q:\n%s", want, log.String())
	}

	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "forms": false}
	root = testpaths.TempDir(t)
	if _, err := buildService(t, argsService, root, outputs); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, file := range []string{"arg_forms.rs", "forms.rs"} {
		if _, err := os.Stat(filepath.Join(topcoat.Dir(root, argsService), "src", file)); !os.IsNotExist(err) {
			t.Errorf("%s written with forms false: %v", file, err)
		}
	}
}

// argsAppTest is tests/app.rs of fixture-args-api's Topcoat crate.
const argsAppTest = `use std::sync::Arc;

use async_trait::async_trait;
use schemas_fixture_args_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_args_api_topcoat::api::{
    types, Implementations, OrderCancelOrderArgs, OrderGetOrderArgs, OrderImplementation, OrderListOrdersArgs,
    OrderTagOrderArgs, ProductReviewsImplementation, ProductReviewsWriteReviewArgs,
};
use schemas_fixture_args_api_topcoat::arg_forms::{
    order_cancel_order_args_fields, order_list_orders_args_fields, product_reviews_write_review_args_fields,
    OrderCancelOrderArgsForm, OrderListOrdersArgsForm, ProductReviewsWriteReviewArgsForm,
};
use schemas_fixture_args_api_topcoat::forms::FormErrors;
use schemas_fixture_args_api_topcoat::RouterBuilderFixtureArgsApiExt;
use serde_json::json;
use topcoat::context::Cx;
use topcoat::router::content::Form;
use topcoat::router::{header, page, to_bytes, Body, Router, StatusCode};
use topcoat::view::{view, View};

const ORDER: &str = "8d1f6c9e-0000-4000-8000-000000000001";
const PRODUCT: &str = "8d1f6c9e-0000-4000-8000-000000000002";

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(json!(text)).unwrap()
}

// Which fixture id an id is, for the pages to show.
fn which(id: &types::IdentityUUID) -> &'static str {
    if *id == uuid(ORDER) {
        "order"
    } else if *id == uuid(PRODUCT) {
        "product"
    } else {
        "unknown"
    }
}

fn order(status: types::OrderStatus, note: String) -> types::OrderView {
    types::OrderView { id: uuid(ORDER), status, note: Some(note) }
}

// Each operation answers with what it received, which the pages show.
struct Shop;

#[async_trait]
impl OrderImplementation for Shop {
    async fn list_orders(&self, _ctx: RequestContext, args: OrderListOrdersArgs) -> Result<Vec<types::OrderView>, ApiError> {
        let note = format!("tags={:?} limit={:?} page={:?} archived={:?}", args.tags, args.limit, args.page, args.archived);
        Ok(args.statuses.unwrap_or_default().into_iter().map(|status| order(status, note.clone())).collect())
    }
    async fn get_order(&self, _ctx: RequestContext, args: OrderGetOrderArgs) -> Result<types::OrderView, ApiError> {
        Ok(order(types::OrderStatus::Placed, which(&args.id).to_owned()))
    }
    async fn cancel_order(&self, _ctx: RequestContext, args: OrderCancelOrderArgs) -> Result<types::OrderView, ApiError> {
        if args.reason.as_deref() == Some("too late") {
            return Err(ApiError::conflict("The order has shipped"));
        }
        let note = format!("cancelled {} {} {:?}", which(&args.id), json!(args.code), args.reason);
        Ok(order(types::OrderStatus::Cancelled, note))
    }
    async fn tag_order(&self, _ctx: RequestContext, _args: OrderTagOrderArgs) -> Result<types::OrderView, ApiError> {
        Err(ApiError::not_implemented("tag_order"))
    }
}

#[async_trait]
impl ProductReviewsImplementation for Shop {
    async fn write_review(&self, _ctx: RequestContext, args: ProductReviewsWriteReviewArgs) -> Result<types::ReviewView, ApiError> {
        Ok(types::ReviewView { id: args.product_id, rating: args.input.rating, title: args.input.title })
    }
}

#[page("/cancel")]
async fn cancel_page() -> topcoat::Result<impl View> {
    Ok(view! {
        <form method=(OrderCancelOrderArgsForm::METHOD)>order_cancel_order_args_fields(form: OrderCancelOrderArgsForm::new(ORDER))</form>
    })
}

#[page(POST "/cancel")]
async fn cancel(cx: &Cx, Form(form): Form<OrderCancelOrderArgsForm>) -> topcoat::Result<impl View> {
    let (status, note, errors) = match form.submit(cx).await {
        Ok(order) => (StatusCode::OK, order.note, FormErrors::default()),
        Err(errors) => (StatusCode::UNPROCESSABLE_ENTITY, None, errors),
    };
    Ok(view! {
        (status)
        if let Some(note) = note {
            <p class="done">(note)</p>
        }
        <form method="post">order_cancel_order_args_fields(form: form, errors: errors)</form>
    })
}

#[page("/orders")]
async fn orders(cx: &Cx, Form(filter): Form<OrderListOrdersArgsForm>) -> topcoat::Result<impl View> {
    let (status, orders, errors) = match filter.submit(cx).await {
        Ok(orders) => (StatusCode::OK, orders, FormErrors::default()),
        Err(errors) => (StatusCode::UNPROCESSABLE_ENTITY, Vec::new(), errors),
    };
    Ok(view! {
        (status)
        <form method=(OrderListOrdersArgsForm::METHOD)>order_list_orders_args_fields(form: filter, errors: errors)</form>
        for order in orders {
            <p class="order">(format!("{:?} {}", order.status, order.note.unwrap_or_default()))</p>
        }
    })
}

#[page("/filter")]
async fn filter_page(cx: &Cx) -> topcoat::Result<impl View> {
    let filter = OrderListOrdersArgsForm::from_query(cx)?;
    Ok(view! { <form method="get">order_list_orders_args_fields(form: filter)</form> })
}

#[page(POST "/review")]
async fn review(cx: &Cx, Form(form): Form<ProductReviewsWriteReviewArgsForm>) -> topcoat::Result<impl View> {
    let (status, title, errors) = match form.submit(cx).await {
        Ok(review) => (StatusCode::OK, Some(format!("{} {}", which(&review.id), review.title)), FormErrors::default()),
        Err(errors) => (StatusCode::UNPROCESSABLE_ENTITY, None, errors),
    };
    Ok(view! {
        (status)
        if let Some(title) = title {
            <p class="done">(title)</p>
        }
        <form method="post">product_reviews_write_review_args_fields(form: form, errors: errors)</form>
    })
}

#[page("/review")]
async fn review_page() -> topcoat::Result<impl View> {
    Ok(view! {
        <form method="post">product_reviews_write_review_args_fields(form: ProductReviewsWriteReviewArgsForm::new(PRODUCT))</form>
    })
}

fn app() -> Router {
    Router::builder()
        .page(cancel_page)
        .page(cancel)
        .page(orders)
        .page(filter_page)
        .page(review)
        .page(review_page)
        .fixture_args_api(Implementations { order: Arc::new(Shop), product_reviews: Arc::new(Shop) })
        .build()
}

async fn send(request: http::Request<Body>) -> (StatusCode, String) {
    let response = app().handle(request).await;
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, String::from_utf8(bytes.to_vec()).unwrap())
}

async fn get(uri: &str) -> (StatusCode, String) {
    send(http::Request::builder().uri(uri).body(Body::empty()).unwrap()).await
}

async fn post(uri: &str, body: &str) -> (StatusCode, String) {
    send(
        http::Request::builder()
            .method("POST")
            .uri(uri)
            .header(header::CONTENT_TYPE, "application/x-www-form-urlencoded")
            .body(Body::from(body.to_owned()))
            .unwrap(),
    )
    .await
}

#[tokio::test]
async fn an_argument_form_renders_its_path_hidden_and_its_rules_as_attributes() {
    let (status, html) = get("/cancel").await;
    assert_eq!(status, StatusCode::OK);
    for want in [
        r#"<form method="post">"#,
        &format!(r#"<input type="hidden" name="id" value="{ORDER}">"#),
        r#"<select id="order-cancel-order-code" name="code" required="">"#,
        r#"<option value="too_slow">Too slow</option>"#,
        r#"<label for="order-cancel-order-reason">Reason</label>"#,
        r#"<p class="field-hint" id="order-cancel-order-reason-hint">Why the order is cancelled, as the shopper wrote it.</p>"#,
        r#"name="reason" type="text" maxlength="500" aria-describedby="order-cancel-order-reason-hint">"#,
    ] {
        assert!(html.contains(want), "missing {want} in {html}");
    }
}

#[tokio::test]
async fn a_valid_post_calls_the_operation_with_its_arguments() {
    let (status, html) = post("/cancel", &format!("id={ORDER}&code=too_slow&reason=Found+it+cheaper")).await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert!(html.contains(r#"<p class="done">cancelled order "too_slow" Some("Found it cheaper")</p>"#), "{html}");

    // A blank optional argument is absent.
    let (status, html) = post("/cancel", &format!("id={ORDER}&code=changed_mind&reason=")).await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert!(html.contains(r#"cancelled order "changed_mind" None"#), "{html}");
}

#[tokio::test]
async fn a_refused_post_renders_again_with_each_error_at_its_control() {
    let long = "x".repeat(501);
    let (status, html) = post("/cancel", &format!("id={ORDER}&code=too_slow&reason={long}")).await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains(&format!(r#"value="{long}" aria-invalid="true">"#)), "{html}");
    assert!(html.contains(r#"<p class="field-error">must be at most 500 characters</p>"#), "{html}");
    assert!(html.contains(r#"<option value="too_slow" selected="">"#), "{html}");

    // A path argument that does not parse is the form's error, named.
    let (status, html) = post("/cancel", "id=not-a-uuid&code=too_slow").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains(r#"<p class="form-error">id: does not match the declared type</p>"#), "{html}");

    // A value the select does not offer, and the operation's own refusal.
    let (status, html) = post("/cancel", &format!("id={ORDER}&code=whenever")).await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains(r#"<p class="field-error">does not match the declared type</p>"#), "{html}");
    let (status, html) = post("/cancel", &format!("id={ORDER}&code=too_slow&reason=too+late")).await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains(r#"<p class="form-error">The order has shipped</p>"#), "{html}");
}

#[tokio::test]
async fn a_filter_reads_the_query_and_renders_it_as_sent() {
    let (status, html) =
        get("/orders?statuses=placed&statuses=shipped&tags=fragile,+heavy&limit=5&page=2&archived=on").await;
    assert_eq!(status, StatusCode::OK, "{html}");
    for want in [
        r#"<form method="get">"#,
        r#"value="placed" checked="""#,
        r#"value="shipped" checked="""#,
        r#"<input name="tags" type="text" maxlength="20" aria-label="Tags" value="fragile">"#,
        r#"<input name="tags" type="text" maxlength="20" aria-label="Tags" value="heavy">"#,
        r#"type="number" step="any" min="1" max="100" value="5">"#,
        r#"type="number" step="1" min="1" max="9007199254740991" value="2">"#,
        r#"name="archived" type="checkbox" checked="""#,
        r#"<p class="order">Placed tags=Some(["fragile", "heavy"]) limit=Some(5.0) page=Some(2) archived=Some(true)</p>"#,
        r#"<p class="order">Shipped "#,
    ] {
        assert!(html.contains(want), "missing {want} in {html}");
    }
    assert!(!html.contains(r#"value="on_hold" checked"#), "{html}");

    // An empty filter: no list, no limit, and an unchecked flag is absent.
    let (status, html) = get("/orders?statuses=placed&limit=").await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert!(html.contains(r#"<p class="order">Placed tags=None limit=None page=None archived=None</p>"#), "{html}");

    // from_query reads the same filter outside an extractor.
    let (_, html) = get("/filter?statuses=on_hold&limit=7").await;
    assert!(html.contains(r#"value="on_hold" checked="""#), "{html}");
    assert!(html.contains(r#"value="7""#), "{html}");
}

#[tokio::test]
async fn a_filter_out_of_range_renders_its_error_at_its_control() {
    let (status, html) = get("/orders?statuses=placed&limit=500").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains(r#"value="500" aria-invalid="true">"#), "{html}");
    assert!(html.contains(r#"<p class="field-error">must be at most 100</p>"#), "{html}");
    assert!(html.contains(r#"value="placed" checked="""#), "{html}");

    let (status, html) = get("/orders?page=two").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains(r#"<p class="field-error">must be a whole number</p>"#), "{html}");
}

#[tokio::test]
async fn a_form_with_an_input_covers_both() {
    let (_, html) = get("/review").await;
    assert!(html.contains(&format!(r#"<input type="hidden" name="productId" value="{PRODUCT}">"#)), "{html}");
    assert!(html.contains(r#"name="rating" type="number""#), "{html}");

    let (status, html) = post("/review", &format!("productId={PRODUCT}&rating=4&title=Fish+%26+chips+100%25")).await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert!(html.contains(r#"<p class="done">product Fish &amp; chips 100%</p>"#), "{html}");

    let (status, html) = post("/review", &format!("productId={PRODUCT}&rating=9&title=Nice")).await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains(r#"<p class="field-error">must be at most 5</p>"#), "{html}");
    assert!(html.contains(r#"value="Nice""#), "{html}");
    assert!(!html.contains("form-error"), "{html}");
}

#[test]
fn a_form_parses_into_its_arguments() {
    let form = OrderListOrdersArgsForm::from_pairs([
        ("statuses".to_owned(), "placed,on_hold".to_owned()),
        ("limit".to_owned(), "10".to_owned()),
        ("limit".to_owned(), "20".to_owned()),
    ]);
    assert_eq!(form.statuses, vec!["placed", "on_hold"]);
    assert_eq!(form.limit.as_deref(), Some("10"));
    let args = form.parse().unwrap();
    assert_eq!(args.statuses, Some(vec![types::OrderStatus::Placed, types::OrderStatus::Onhold]));
    assert_eq!((args.limit, args.archived), (Some(10.0), None));

    let errors = OrderCancelOrderArgsForm::new(ORDER).parse().unwrap_err();
    assert_eq!(errors.of("code"), vec!["is required"]);
}
`

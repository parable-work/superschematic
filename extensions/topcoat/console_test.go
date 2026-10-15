package topcoat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
	"github.com/parable-work/superschematic/registry"
)

// consoleService is the extension's own fixture for a console that pages
// its lists (D60), picks and names what an id refers to (D61) and searches
// (D62).
const consoleService = "fixture-console-api"

// TestTheConsolePagesPicksAndSearches builds fixture-console-api's crates
// with the pages on, their lists live, and runs cargo clippy, then cargo
// test, with consoleAppTest. Through Router::handle: a paged list shows a
// page of its rows, a "Next page" link carrying the next cursor and the
// filter and the limit, and on a later page a "First page" link; the
// filter holds neither; a limit or cursor the route refuses, and a cursor
// the implementation cannot read, is the route's 400 as the form's own
// message, and a paged list without a filter renders no form and its
// refusal alone. The search box is the filter's first control, a search
// form's, labeled Search, and filters by each word in any case, its pages
// carrying it. A reference is a select of the options operation's items
// on the new page, the app's own page and an action's form, from one
// listing a render, and a value is named by its item's label through the
// lookup, one call a render, in chunks of its listMax when more, or from
// the listing for a reference by sku; a caller who may not list the items
// gets the plain control and the value, the app's choices win and spare
// the call, and more items than a page keeps the control. Products link
// their rows through the reference of getProduct's productId, name their
// suppliers, a target's own reference, beside the reviews and lines that
// name products, and a product's page shows the first page of its reviews
// and a link to the next. The live list's shard pages in place, a
// procedure answers a page record, and the site's pages, a paged list at
// the path page, link their next and first pages.
func TestTheConsolePagesPicksAndSearches(t *testing.T) {
	cargoTestCrate(t, consoleService, consoleAppTest)
}

// TestTheCoreFixturesPageAndRefer builds the core's fixture-paged-api,
// whose tags are a page of strings and whose items a page with a filter,
// and fixture-refs-api, whose basket refers to items by id, through a
// lookup that does not page, and by sku, with the pages on, and runs cargo
// clippy and cargo test on each crate.
func TestTheCoreFixturesPageAndRefer(t *testing.T) {
	for _, service := range []string{"fixture-paged-api", "fixture-refs-api"} {
		t.Run(service, func(t *testing.T) {
			cargoTestCrateWith(t, service, "#[test]\nfn builds() {}\n", crateOptions{outputs: pagesOutputs()})
		})
	}
}

// TestReferencesBuildWithFormsOrViewsAlone builds fixture-console-api's
// crate without its display components, so its references give the forms
// options alone, and without its forms, so they give the components labels
// alone, through lookups, and runs cargo clippy and cargo test on each.
func TestReferencesBuildWithFormsOrViewsAlone(t *testing.T) {
	for _, off := range []string{"views", "forms"} {
		t.Run(off, func(t *testing.T) {
			outputs := rustOutputs()
			outputs["topcoat"] = map[string]any{"enabled": true, off: false}
			cargoTestCrateWith(t, consoleService, "#[test]\nfn builds() {}\n", crateOptions{outputs: outputs})
		})
	}
}

// TestAPagedOperationIsItsPageEverywhere reads the crate of
// fixture-console-api: a paged operation's call names the runtime, its
// page is a page record, its procedure takes cursor and limit, its
// argument form holds the page, even for a list without a filter, reads
// it by the route's rules and links its pages by its query pairs, the
// search argument first; and the core's fixture-paged-api, whose tags are
// a page of strings.
func TestAPagedOperationIsItsPageEverywhere(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	for file, wants := range map[string][]string{
		"operations.rs": {
			"use crate::api::runtime;",
			"pub async fn order_list_orders(cx: &Cx, args: crate::api::OrderListOrdersArgs) -> Result<runtime::page::Page<types::Order>, ApiError> {",
		},
		"records.rs": {
			"pub struct OrderPageRecord {\n    pub items: Vec<OrderRecord>,\n    pub next: Option<String>,\n}",
			"impl From<&runtime::page::Page<types::Order>> for OrderPageRecord {",
		},
		"procedures.rs": {
			"    pub cursor: Option<String>,\n    pub limit: Option<i64>,\n}",
			"page: crate::api::OrderListOrdersArgs::PAGE.request(self.cursor.as_deref(), self.limit).map_err(ProblemRecord::from)?,",
			"-> topcoat::Result<Result<OrderPageRecord, ProblemRecord>> {",
			"pub struct SupplierListSuppliersArgsRecord {\n    pub cursor: Option<String>,\n    pub limit: Option<i64>,\n}",
		},
		"forms.rs": {
			"pub struct OrderListOrdersArgsForm {\n    pub q: Option<String>,\n    pub statuses: Vec<String>,\n    pub product_id: Option<String>,\n",
			"pub struct SupplierListSuppliersArgsForm {\n    /// The page asked for (D60)",
			"crate::api::OrderListOrdersArgs::PAGE.query(&QueryValues::new(pairs))",
			"pub fn query_pairs(&self) -> Vec<(String, String)> {",
			"self.page().map_err(|err| Refusal::from_api(&err))?;",
			`<label for="order-list-orders-q">"Search"</label>`,
			`type="search" maxlength="50"`,
		},
		"pages.rs": {
			`<form method="get" class="ss-filter" role="search" data-ss-live="orders"`,
			"pager(base: href.clone(), first: None, next: next_page)",
			"Ok(items) => suppliers_rows(items: items),\n            Err(refusal) => refused(refusal: page_refusal(refusal, &[]), next: next),",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(files[file], want) {
				t.Errorf("%s lacks %q", file, want)
			}
		}
	}
	if strings.Contains(files["pages.rs"], "supplier_list_suppliers_args_fields") {
		t.Error("pages.rs renders the suppliers' form, which shows no control")
	}

	paged, _ := readCrate(t, registry.DefaultNaming(), "fixture-paged-api", pagesOutputs())
	for _, want := range []string{
		"pub struct StringPageRecord {\n    pub items: Vec<String>,\n    pub next: Option<String>,\n}",
		"impl From<&runtime::page::Page<String>> for StringPageRecord {",
	} {
		if !strings.Contains(paged["records.rs"], want) {
			t.Errorf("fixture-paged-api's records.rs lacks %q", want)
		}
	}
	if !strings.Contains(paged["forms.rs"], "pub struct CatalogListTagsArgsForm {") {
		t.Error("fixture-paged-api's tags, a page with no other argument, have no argument form")
	}
}

// TestReferencesResolveByTheCoreRule reads the references of
// fixture-console-api's crate: a source per target and value, its options
// from the one operation that lists the target, its labels through the
// lookup in chunks of its listMax, or from the listing for a reference by
// sku, which no lookup refers by; a choices function per form and a labels
// function per record that holds a reference, Product's among them, since
// a product names its supplier, beside the private functions of Product as
// a target; the pages calling them; and products' rows linked through
// getProduct's reference.
func TestReferencesResolveByTheCoreRule(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	references := files["references.rs"]
	for _, want := range []string{
		"async fn product_list_products_options(cx: &Cx) -> Option<Listed> {",
		"page: PageRequest::first(crate::api::ProductListProductsArgs::PAGE.max_limit()),",
		"async fn product_list_products_by_ids(cx: &Cx, values: &[String]) -> Option<Vec<Value>> {",
		`(!listed.more).then(|| choices_of(&listed.items, "id", Some("name")))`,
		`(!listed.more).then(|| choices_of(&listed.items, "sku", Some("name")))`,
		"for chunk in values.chunks(5).take(MAX_LOOKUP_CALLS) {",
		"async fn product_by_sku_target_labels(cx: &Cx, values: &BTreeSet<String>) -> Vec<Choice> {",
		"async fn product_target_labels(cx: &Cx, values: &BTreeSet<String>) -> Vec<Choice> {",
		"async fn product_target_choices(cx: &Cx) -> Option<Vec<Choice>> {",
		"pub async fn product_labels(cx: &Cx, records: &[ProductRecord], choices: Choices) -> Choices {",
		"let labels = supplier_target_labels(cx, &values_at(&json, &sites)).await;",
		"pub async fn order_input_form_choices(cx: &Cx, choices: Choices) -> Choices {",
		"pub async fn order_list_orders_args_form_choices(cx: &Cx, choices: Choices) -> Choices {",
		"pub async fn order_add_line_args_form_choices(cx: &Cx, choices: Choices) -> Choices {",
		"pub async fn order_labels(cx: &Cx, records: &[OrderRecord], choices: Choices) -> Choices {",
		"pub async fn review_labels(cx: &Cx, records: &[ReviewRecord], choices: Choices) -> Choices {",
		`let sites = unnamed(&choices, &[("lines.productId", &["lines", "productId"])]);`,
	} {
		if !strings.Contains(references, want) {
			t.Errorf("references.rs lacks %q", want)
		}
	}
	for _, want := range []string{
		"use crate::references;",
		`let choices = references::order_labels(cx, &rows, choices_of(cx, "orders")).await;`,
		`let choices = references::order_input_form_choices(cx, choices_of(cx, "orders")).await;`,
		"let add_line_form_choices = match &add_line_form {",
		"let labels = match &found {",
		"products_item_href(&row.id).unwrap_or_default()",
		`let choices = references::product_labels(cx, &rows, choices_of(cx, "products")).await;`,
	} {
		if !strings.Contains(files["pages.rs"], want) {
			t.Errorf("pages.rs lacks %q", want)
		}
	}
	if !strings.Contains(files["choices.rs"], "pub fn or(self, base: Choices) -> Self {") {
		t.Error("choices.rs has no Choices::or")
	}
	if !strings.Contains(files["lib.rs"], "pub mod references;") {
		t.Error("lib.rs declares no references")
	}
}

// TestReferenceFunctionsAreNamedApart builds fixture-console-api with a
// type named ProductTarget that refers to a product, whose record's labels
// function, product_target_labels, Product's private one as a target would
// be: the target's functions take a number instead.
func TestReferenceFunctionsAreNamedApart(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		schema = strings.Replace(schema, "types:\n", "types:\n  ProductTarget:\n    name: ProductTarget\n    role: APIView\n    fields:\n      - name: id\n        typeRef: { name: string }\n        required: true\n      - name: productId\n        typeRef: { name: Identity.UUID }\n        required: true\n        references: { type: Product }\n", 1)
		return strings.Replace(schema, "operationSets:\n", "operationSets:\n  - name: TargetQueries\n    operations:\n      - name: listTargets\n        typeRef: { name: ProductTarget, isArray: true }\n        required: true\n        httpMethod: GET\n        restPath: targets\n        permissions: [products.read]\n", 1)
	})
	root := testpaths.TempDir(t)
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, livePagesOutputs(), nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, consoleService), "src", "references.rs"))
	if err != nil {
		t.Fatal(err)
	}
	references := string(data)
	for _, want := range []string{
		"pub async fn product_target_labels(cx: &Cx, records: &[ProductTargetRecord], choices: Choices) -> Choices {",
		"async fn product_2_target_labels(cx: &Cx, values: &BTreeSet<String>) -> Vec<Choice> {",
		"async fn product_2_target_choices(cx: &Cx) -> Option<Vec<Choice>> {",
		"let labels = product_2_target_labels(cx, &values_at(&json, &sites)).await;",
	} {
		if !strings.Contains(references, want) {
			t.Errorf("references.rs lacks %q", want)
		}
	}
	if n := strings.Count(references, "fn product_target_labels("); n != 1 {
		t.Errorf("references.rs has %d functions product_target_labels, want 1", n)
	}
}

// TestACollectionAtPagerIsNamedApart reads fixture-console-api's pages,
// whose site pages at the path page have their page_href beside the
// pager's pager_href, then builds it with them at the path pager, whose
// <ident>_href would be pager_href: the collection's functions take a
// number instead.
func TestACollectionAtPagerIsNamedApart(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	for _, want := range []string{"fn page_href() -> Option<String> {", "fn pager_href(base: &str, query: &str) -> String {"} {
		if n := strings.Count(files["pages.rs"], want); n != 1 {
			t.Errorf("pages.rs has %q %d times, want once", want, n)
		}
	}
	dir := consoleCopy(t, func(schema string) string {
		return strings.Replace(schema, "        restPath: page\n", "        restPath: pager\n", 1)
	})
	root := testpaths.TempDir(t)
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, livePagesOutputs(), nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, consoleService), "src", "pages.rs"))
	if err != nil {
		t.Fatal(err)
	}
	pages := string(data)
	for _, want := range []string{
		"fn pager_2_href() -> Option<String> {",
		`#[page("/console/pager")]`,
		`#[shard("/console/_live/pager_2")]`,
	} {
		if !strings.Contains(pages, want) {
			t.Errorf("pages.rs lacks %q", want)
		}
	}
	if n := strings.Count(pages, "fn pager_href("); n != 1 {
		t.Errorf("pages.rs has %d functions pager_href, want 1", n)
	}
}

// TestAReferenceWithoutACallIsIgnored builds fixture-console-api with
// listProducts and listSuppliers behind a provider's signature, so neither
// has an in-process call: every reference to Product and to Supplier,
// whose options they are, is left out with its reason in the build log,
// its controls stay as they were, and the crate has no references module.
func TestAReferenceWithoutACallIsIgnored(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		for _, path := range []string{"products", "suppliers"} {
			schema = strings.Replace(schema, "        restPath: "+path+"\n        permissions: [products.read]\n", "        restPath: "+path+"\n        permissions: [products.read]\n        hmacVerifiedProvider: stripe\n", 1)
		}
		return schema
	})
	root := testpaths.TempDir(t)
	var log strings.Builder
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, livePagesOutputs(), &log); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{
		"topcoat: OrderLine.productId refers to Product, whose options operation ProductQueries.listProducts has no in-process call: its route checks a provider's signature first (@hmacVerified)",
		"topcoat: OrderInput.giftSku refers to Product, whose options operation ProductQueries.listProducts has no in-process call",
		"topcoat: order.listOrders(productId) refers to Product, whose options operation ProductQueries.listProducts has no in-process call",
		"topcoat: Product.supplierId refers to Supplier, whose options operation SupplierQueries.listSuppliers has no in-process call",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the build log lacks %q:\n%s", want, log.String())
		}
	}
	src := filepath.Join(topcoat.Dir(root, consoleService), "src")
	if _, err := os.Stat(filepath.Join(src, "references.rs")); !os.IsNotExist(err) {
		t.Errorf("references.rs written with no reference to resolve: %v", err)
	}
	pages, err := os.ReadFile(filepath.Join(src, "pages.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pages), "references::") {
		t.Error("pages.rs calls references the crate does not have")
	}
}

// TestAPageRecordNamedLikeATypeIsRefused builds fixture-console-api with a
// type named OrderPage, whose record would be the orders' page record.
func TestAPageRecordNamedLikeATypeIsRefused(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		return strings.Replace(schema, "types:\n", "types:\n  OrderPage:\n    name: OrderPage\n    role: APIView\n    fields:\n      - name: id\n        typeRef: { name: string }\n        required: true\n", 1)
	})
	_, err := buildDir(t, registry.DefaultNaming(), dir, testpaths.TempDir(t), rustOutputs(), nil)
	if err == nil || !strings.Contains(err.Error(), "the page order.listOrders returns is the record OrderPageRecord, and so is the record of the type OrderPage; rename the type") {
		t.Fatalf("build: %v, want the page record's name refused", err)
	}
}

// consoleCopy is a copy of fixture-console-api whose schema edit changes.
func consoleCopy(t *testing.T, edit func(schema string) string) string {
	t.Helper()
	dir := filepath.Join(testpaths.TempDir(t), consoleService)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"schema.config.yaml", filepath.Join("src", "shop.schema.yaml")} {
		data, err := os.ReadFile(filepath.Join(serviceDir(consoleService), file))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.HasSuffix(file, "shop.schema.yaml") {
			edited := edit(text)
			if edited == text {
				t.Fatal("the edit changed nothing")
			}
			text = edited
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// consoleAppTest is tests/app.rs of fixture-console-api's Topcoat crate.
const consoleAppTest = `use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_console_api_topcoat::api::runtime::page::{Cursor, Page};
use schemas_fixture_console_api_topcoat::api::runtime::search;
use schemas_fixture_console_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_console_api_topcoat::api::{
    types, Implementations, OrderAddLineArgs, OrderGetOrderArgs, OrderImplementation, OrderListOrdersArgs, OrderPlaceOrderArgs,
    ProductGetProductArgs, ProductImplementation, ProductListProductsArgs, ReviewImplementation, ReviewListReviewsArgs,
    SiteImplementation, SiteListPagesArgs, SupplierImplementation, SupplierListSuppliersArgs,
};
use schemas_fixture_console_api_topcoat::choices::Choices;
use schemas_fixture_console_api_topcoat::forms::{order_input_fields, FormErrors, OrderInputForm};
use schemas_fixture_console_api_topcoat::pages::Settings;
use schemas_fixture_console_api_topcoat::procedures::{self, OrderListOrdersArgsRecord};
use schemas_fixture_console_api_topcoat::records::OrderPageRecord;
use schemas_fixture_console_api_topcoat::references;
use schemas_fixture_console_api_topcoat::{PageAuthenticator, RouterBuilderFixtureConsoleApiExt};
use topcoat::context::Cx;
use topcoat::router::request::parts;
use topcoat::router::{header, page, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};
use topcoat::view::{view, View};

const ANVIL: &str = "8d1f6c9e-0000-4000-8000-000000000001";
const BUCKET: &str = "8d1f6c9e-0000-4000-8000-000000000002";
const CRATE: &str = "8d1f6c9e-0000-4000-8000-000000000003";
const DRUM: &str = "8d1f6c9e-0000-4000-8000-000000000004";
const EASEL: &str = "8d1f6c9e-0000-4000-8000-000000000005";
const FUNNEL: &str = "8d1f6c9e-0000-4000-8000-000000000006";
const FIRST: &str = "8d1f6c9e-0000-4000-8000-000000000101";

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(serde_json::json!(text)).unwrap()
}

// An id as the API's JSON writes it, which a select's option and a row's
// link hold.
fn wire(id: &types::IdentityUUID) -> String {
    serde_json::to_value(id).unwrap().as_str().unwrap().to_owned()
}

// A product, which Acme supplies, or Bolt from C on.
fn product(id: &str, name: &str) -> types::Product {
    let supplier = if name < "C" { "acme" } else { "bolt" };
    types::Product { id: uuid(id), name: name.to_owned(), sku: name.to_lowercase(), supplier_id: supplier.to_owned(), in_stock: true }
}

fn line(product: &str, quantity: f64) -> types::OrderLine {
    types::OrderLine { product_id: uuid(product), quantity }
}

fn order(n: u32, status: types::OrderStatus, note: Option<&str>, gift: Option<&str>, lines: Vec<types::OrderLine>) -> types::Order {
    types::Order {
        id: uuid(&format!("8d1f6c9e-0000-4000-8000-000000000{}", 100 + n)),
        number: format!("A-{n}"),
        status,
        note: note.map(str::to_owned),
        gift_sku: gift.map(str::to_owned),
        lines,
    }
}

// The shop in memory, and each call of listProducts: the listings, which
// send no ids, and the lookups, by the ids they send.
struct Store {
    products: Mutex<Vec<types::Product>>,
    orders: Mutex<Vec<types::Order>>,
    reviews: Vec<types::Review>,
    listings: AtomicUsize,
    lookups: Mutex<Vec<Vec<String>>>,
}

impl Store {
    fn new() -> Arc<Self> {
        use types::OrderStatus::{Placed, Shipped};
        Arc::new(Store {
            products: Mutex::new(vec![product(ANVIL, "Anvil"), product(BUCKET, "Bucket"), product(CRATE, "Crate"), product(DRUM, "Drum")]),
            orders: Mutex::new(vec![
                order(1, Placed, Some("rush"), Some("bucket"), vec![line(ANVIL, 1.0), line(CRATE, 2.0)]),
                order(2, Shipped, None, None, vec![line(BUCKET, 1.0)]),
                order(3, Placed, Some("rush, gift wrap"), None, vec![line(DRUM, 3.0)]),
                order(4, Placed, None, Some("anvil"), vec![line(ANVIL, 1.0)]),
                order(5, Shipped, None, None, vec![line(CRATE, 1.0)]),
            ]),
            reviews: (1..=3)
                .map(|n| types::Review {
                    id: format!("r{n}"),
                    product_id: uuid(ANVIL),
                    rating: f64::from(n + 2),
                    title: ["Sturdy", "Heavy", "Sturdy and heavy"][n as usize - 1].to_owned(),
                })
                .collect(),
            listings: AtomicUsize::new(0),
            lookups: Mutex::new(Vec::new()),
        })
    }

    // The calls of listProducts since the last count: the listings, and
    // each lookup's ids.
    fn calls(&self) -> (usize, Vec<Vec<String>>) {
        (self.listings.swap(0, Ordering::SeqCst), std::mem::take(&mut *self.lookups.lock().unwrap()))
    }
}

// The rows after the cursor, in the order of their keys, up to a page and
// one more: what an implementation reads, which Page::from_rows keeps.
fn page_of<T, K: serde::Serialize + serde::de::DeserializeOwned + Ord>(
    mut rows: Vec<T>,
    page: &schemas_fixture_console_api_topcoat::api::runtime::page::PageRequest,
    key: impl Fn(&T) -> K,
) -> Result<Page<T>, ApiError> {
    let after: Option<K> = page.cursor.as_ref().map(Cursor::decode).transpose()?;
    rows.sort_by_key(|row| key(row));
    rows.retain(|row| after.as_ref().is_none_or(|after| key(row) > *after));
    Page::from_rows(rows, page.limit, key)
}

struct Shop(Arc<Store>);

#[async_trait]
impl ProductImplementation for Shop {
    async fn list_products(&self, _ctx: RequestContext, args: ProductListProductsArgs) -> Result<Page<types::Product>, ApiError> {
        match &args.ids {
            None => {
                self.0.listings.fetch_add(1, Ordering::SeqCst);
            }
            Some(ids) => self.0.lookups.lock().unwrap().push(ids.iter().map(wire).collect()),
        }
        let terms = search::terms(args.q.as_deref().unwrap_or_default());
        let rows: Vec<types::Product> = self
            .0
            .products
            .lock()
            .unwrap()
            .iter()
            .filter(|product| args.ids.as_ref().is_none_or(|ids| ids.contains(&product.id)))
            .filter(|product| search::matches(&product.name, &terms))
            .cloned()
            .collect();
        page_of(rows, &args.page, |product| (product.name.clone(), wire(&product.id)))
    }
    async fn get_product(&self, _ctx: RequestContext, args: ProductGetProductArgs) -> Result<types::Product, ApiError> {
        let products = self.0.products.lock().unwrap();
        products.iter().find(|product| product.id == args.product_id).cloned().ok_or_else(|| ApiError::not_found("No such product"))
    }
}

#[async_trait]
impl ReviewImplementation for Shop {
    async fn list_reviews(&self, _ctx: RequestContext, args: ReviewListReviewsArgs) -> Result<Page<types::Review>, ApiError> {
        let terms = search::terms(args.q.as_deref().unwrap_or_default());
        let rows: Vec<types::Review> = self
            .0
            .reviews
            .iter()
            .filter(|review| review.product_id == args.product_id && search::matches(&review.title, &terms))
            .filter(|review| args.min_rating.is_none_or(|least| review.rating >= least))
            .cloned()
            .collect();
        page_of(rows, &args.page, |review| review.id.clone())
    }
}

#[async_trait]
impl SupplierImplementation for Shop {
    async fn list_suppliers(&self, _ctx: RequestContext, args: SupplierListSuppliersArgs) -> Result<Page<types::Supplier>, ApiError> {
        let rows = ["Acme", "Bolt", "Cog"].map(|name| types::Supplier { id: name.to_lowercase(), name: name.to_owned() });
        page_of(rows.to_vec(), &args.page, |supplier| supplier.name.clone())
    }
}

#[async_trait]
impl SiteImplementation for Shop {
    async fn list_pages(&self, _ctx: RequestContext, args: SiteListPagesArgs) -> Result<Page<types::SitePage>, ApiError> {
        let rows = [("about", "About us"), ("terms", "Terms")].map(|(slug, title)| types::SitePage { slug: slug.to_owned(), title: title.to_owned() });
        page_of(rows.to_vec(), &args.page, |page| page.slug.clone())
    }
}

#[async_trait]
impl OrderImplementation for Shop {
    async fn list_orders(&self, _ctx: RequestContext, args: OrderListOrdersArgs) -> Result<Page<types::Order>, ApiError> {
        let terms = search::terms(args.q.as_deref().unwrap_or_default());
        let statuses = args.statuses.unwrap_or_default();
        let rows: Vec<types::Order> = self
            .0
            .orders
            .lock()
            .unwrap()
            .iter()
            .filter(|order| statuses.is_empty() || statuses.contains(&order.status))
            .filter(|order| args.product_id.as_ref().is_none_or(|id| order.lines.iter().any(|line| line.product_id == *id)))
            .filter(|order| search::matches(&format!("{} {}", order.number, order.note.as_deref().unwrap_or_default()), &terms))
            .cloned()
            .collect();
        page_of(rows, &args.page, |order| order.number.clone())
    }
    async fn place_order(&self, _ctx: RequestContext, args: OrderPlaceOrderArgs) -> Result<types::Order, ApiError> {
        let mut orders = self.0.orders.lock().unwrap();
        let n = u32::try_from(orders.len()).unwrap() + 1;
        let lines = args.input.lines.into_iter().map(|line| types::OrderLine { product_id: line.product_id, quantity: line.quantity }).collect();
        let placed = types::Order { gift_sku: args.input.gift_sku, note: args.input.note, ..order(n, types::OrderStatus::Placed, None, None, lines) };
        orders.push(placed.clone());
        Ok(placed)
    }
    async fn get_order(&self, _ctx: RequestContext, args: OrderGetOrderArgs) -> Result<types::Order, ApiError> {
        let orders = self.0.orders.lock().unwrap();
        orders.iter().find(|order| order.id == args.id).cloned().ok_or_else(|| ApiError::not_found("No such order"))
    }
    async fn add_line(&self, _ctx: RequestContext, args: OrderAddLineArgs) -> Result<types::Order, ApiError> {
        let mut orders = self.0.orders.lock().unwrap();
        let order = orders.iter_mut().find(|order| order.id == args.id).ok_or_else(|| ApiError::not_found("No such order"))?;
        order.lines.push(types::OrderLine { product_id: args.product_id, quantity: args.quantity });
        Ok(order.clone())
    }
}

// The JSON API's callers: none, since these tests drive the pages.
struct NoRequests;

#[async_trait]
impl Authenticator for NoRequests {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

// A page's caller, by the x-caller header: a clerk, who reads and places
// orders and reads products; an order taker, who may not list products;
// or none.
struct Callers;

#[async_trait]
impl PageAuthenticator for Callers {
    async fn principal(&self, cx: &Cx) -> Result<Option<Principal>, ApiError> {
        let caller = parts(cx).headers.get("x-caller").and_then(|value| value.to_str().ok()).unwrap_or_default();
        let permissions: &[&str] = match caller {
            "clerk" => &["orders", "products.read"],
            "taker" => &["orders"],
            _ => return Ok(None),
        };
        Ok(Some(Principal::new(caller, permissions.iter().copied())))
    }
}

fn implementations(store: &Arc<Store>) -> Implementations {
    Implementations {
        order: Arc::new(Shop(Arc::clone(store))),
        product: Arc::new(Shop(Arc::clone(store))),
        review: Arc::new(Shop(Arc::clone(store))),
        site: Arc::new(Shop(Arc::clone(store))),
        supplier: Arc::new(Shop(Arc::clone(store))),
        authenticator: Arc::new(NoRequests),
    }
}

fn app_with(store: &Arc<Store>, settings: Settings) -> Router {
    Router::builder().discover().app_context(settings).fixture_console_api(implementations(store), Callers).build()
}

fn app(store: &Arc<Store>) -> Router {
    app_with(store, Settings::new().title("Shop console").sign_in("/sign-in"))
}

// The app's own page that places an order: the generated form, its
// product a select from the references, as the console's new page has it.
#[page("/app/new-order")]
async fn app_new_order(cx: &Cx) -> topcoat::Result<impl View> {
    let choices = references::order_input_form_choices(cx, Choices::new()).await;
    Ok(view! {
        <form method="post">
            order_input_fields(form: OrderInputForm::new(), errors: FormErrors::default(), choices: choices)
        </form>
    })
}

// The orders' procedure, from a page: its page record's numbers and next.
#[page("/app/orders-procedure")]
async fn app_orders_procedure(cx: &Cx) -> topcoat::Result<impl View> {
    let query = parts(cx).uri.query().unwrap_or_default().to_owned();
    let cursor = query.strip_prefix("cursor=").map(str::to_owned);
    let limit = query.strip_prefix("limit=").map_or(Some(3), |limit| limit.parse().ok());
    let args = OrderListOrdersArgsRecord { statuses: None, product_id: None, q: None, cursor, limit };
    let said = match procedures::call_order_list_orders(cx, args).await {
        Ok(OrderPageRecord { items, next }) => {
            let numbers: Vec<String> = items.into_iter().map(|order| order.number).collect();
            format!("{} next={}", numbers.join(","), next.unwrap_or_default())
        }
        Err(problem) => format!("{} {}", problem.status, problem.detail),
    };
    Ok(view! { <p id="said">(said)</p> })
}

struct Answer {
    status: StatusCode,
    html: String,
}

async fn send(router: &Router, method: &str, uri: &str, caller: &str, content_type: &str, body: String) -> Answer {
    let mut request = http::Request::builder().method(method).uri(uri);
    if !caller.is_empty() {
        request = request.header("x-caller", caller);
    }
    if !content_type.is_empty() {
        request = request.header(header::CONTENT_TYPE, content_type);
    }
    let response = router.handle(request.body(Body::from(body)).unwrap()).await;
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    Answer { status, html: String::from_utf8(bytes.to_vec()).unwrap() }
}

async fn get(router: &Router, uri: &str, caller: &str) -> Answer {
    send(router, "GET", uri, caller, "", String::new()).await
}

async fn post(router: &Router, uri: &str, caller: &str, body: &str) -> Answer {
    send(router, "POST", uri, caller, "application/x-www-form-urlencoded", body.to_owned()).await
}

// A shard's request, as Topcoat's runtime sends one.
async fn shard(router: &Router, uri: &str, caller: &str, body: serde_json::Value) -> Answer {
    send(router, "POST", uri, caller, "application/json", body.to_string()).await
}

// The text of html between the first start and the end after it.
fn between<'a>(html: &'a str, start: &str, end: &str) -> &'a str {
    let rest = &html[html.find(start).unwrap_or_else(|| panic!("no {start} in {html}")) + start.len()..];
    &rest[..rest.find(end).unwrap()]
}

// The href of the pager's link of rel, unescaped, or none.
fn link(html: &str, rel: &str) -> Option<String> {
    let start = format!(r#"<a rel="{rel}" href=""#);
    html.contains(&start).then(|| between(html, &start, "\"").replace("&amp;", "&"))
}

// The numbers of the orders a page's table shows, in order.
fn numbers(html: &str) -> Vec<String> {
    html.split(r#"<th scope="row" data-field="number">"#)
        .skip(1)
        .map(|cell| {
            let cell = cell.split("</th>").next().unwrap();
            let text = cell.rsplit_once("\">").map_or(cell, |(_, text)| text);
            text.trim_end_matches("</a>").to_owned()
        })
        .collect()
}

#[tokio::test]
async fn a_paged_list_links_its_next_and_its_first_page_with_its_filter() {
    let app = app(&Store::new());
    let first = get(&app, "/console/orders", "clerk").await;
    assert_eq!(first.status, StatusCode::OK, "{}", first.html);
    assert_eq!(numbers(&first.html), ["A-1", "A-2"], "{}", first.html);
    assert!(first.html.contains(r#"<nav class="ss-pager" aria-label="Pages">"#), "{}", first.html);
    assert!(link(&first.html, "first").is_none(), "{}", first.html);
    // The filter holds neither the cursor nor the limit, so a submitted
    // filter starts at the first page.
    assert!(!first.html.contains(r#"name="cursor""#) && !first.html.contains(r#"name="limit""#), "{}", first.html);

    let next = link(&first.html, "next").unwrap();
    assert!(next.starts_with("/console/orders?cursor="), "{next}");
    let second = get(&app, &next, "clerk").await;
    assert_eq!(second.status, StatusCode::OK, "{}", second.html);
    assert_eq!(numbers(&second.html), ["A-3", "A-4"], "{}", second.html);
    assert_eq!(link(&second.html, "first").as_deref(), Some("/console/orders"), "{}", second.html);
    let third = get(&app, &link(&second.html, "next").unwrap(), "clerk").await;
    assert_eq!(numbers(&third.html), ["A-5"], "{}", third.html);
    assert!(link(&third.html, "next").is_none(), "{}", third.html);
    assert_eq!(link(&third.html, "first").as_deref(), Some("/console/orders"), "{}", third.html);

    // The links keep the filter and the limit.
    let placed = get(&app, "/console/orders?statuses=placed&limit=1", "clerk").await;
    assert_eq!(numbers(&placed.html), ["A-1"], "{}", placed.html);
    let next = link(&placed.html, "next").unwrap();
    assert!(next.starts_with("/console/orders?statuses=placed&limit=1&cursor="), "{next}");
    let later = get(&app, &next, "clerk").await;
    assert_eq!(numbers(&later.html), ["A-3"], "{}", later.html);
    assert_eq!(link(&later.html, "first").as_deref(), Some("/console/orders?statuses=placed&limit=1"), "{}", later.html);

    // A limit or a cursor the route refuses is its 400, the form's own
    // message, with no rows; a cursor the implementation cannot read too.
    for (query, message) in [
        ("limit=6", "limit: must be at most 5"),
        ("limit=0", "limit: must be at least 1"),
        ("limit=two", "limit: expected an integer"),
        ("cursor=%21%21", "cursor: does not match the required pattern"),
        ("cursor=e30", "cursor: not a cursor this operation returned"),
    ] {
        let refused = get(&app, &format!("/console/orders?{query}"), "clerk").await;
        assert_eq!(refused.status, StatusCode::BAD_REQUEST, "{query}: {}", refused.html);
        assert!(refused.html.contains(&format!(r#"<p class="form-error">{message}</p>"#)), "{query}: {}", refused.html);
        assert!(!refused.html.contains("<table"), "{query}: {}", refused.html);
    }
    // A refused cursor still offers the first page.
    let forged = get(&app, "/console/orders?statuses=shipped&cursor=e30", "clerk").await;
    assert_eq!(link(&forged.html, "first").as_deref(), Some("/console/orders?statuses=shipped"), "{}", forged.html);
}

#[tokio::test]
async fn a_search_box_comes_first_filters_and_pages() {
    let app = app(&Store::new());
    let list = get(&app, "/console/orders?q=RUSH", "clerk").await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    assert!(list.html.contains(r#"<form method="get" class="ss-filter" role="search" data-ss-live="orders""#), "{}", list.html);
    let search = r#"<label for="order-list-orders-q">Search</label><input id="order-list-orders-q" name="q" type="search" maxlength="50" value="RUSH""#;
    assert!(list.html.contains(search), "{}", list.html);
    let filter = between(&list.html, r#"<form method="get""#, "</form>");
    assert!(filter.find(r#"name="q""#).unwrap() < filter.find(r#"name="statuses""#).unwrap(), "{filter}");
    assert_eq!(numbers(&list.html), ["A-1", "A-3"], "{}", list.html);

    // Its words each match, in any case, and the pages carry it.
    let wrap = get(&app, "/console/orders?q=wrap%20rush&limit=1", "clerk").await;
    assert_eq!(numbers(&wrap.html), ["A-3"], "{}", wrap.html);
    let rush = get(&app, "/console/orders?q=rush&limit=1", "clerk").await;
    assert_eq!(numbers(&rush.html), ["A-1"], "{}", rush.html);
    let next = link(&rush.html, "next").unwrap();
    assert!(next.starts_with("/console/orders?q=rush&limit=1&cursor="), "{next}");
    assert_eq!(numbers(&get(&app, &next, "clerk").await.html), ["A-3"]);

    // A search past its rules is the form's 422 at the box.
    let long = get(&app, &format!("/console/orders?q={}", "x".repeat(51)), "clerk").await;
    assert_eq!(long.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", long.html);
}

#[tokio::test]
async fn a_reference_is_a_select_of_its_items_and_a_value_is_named_by_its_label() {
    let store = Store::new();
    let app = app(&store);
    // The new order's line names its product from a select of the
    // products, each by its name, and its gift by sku, from one listing.
    let new = get(&app, "/console/orders/new", "clerk").await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.html);
    let anvil = wire(&uuid(ANVIL));
    for want in [
        r#"<select id="order-input-lines-0-product-id" name="lines[0].productId" required"#.to_owned(),
        format!(r#"<option value="{anvil}">Anvil</option>"#),
        r#"<select id="order-input-gift-sku" name="giftSku""#.to_owned(),
        r#"<option value="drum">Drum</option>"#.to_owned(),
    ] {
        assert!(new.html.contains(&want), "the new page lacks {want}: {}", new.html);
    }
    assert_eq!(store.calls(), (1, vec![]), "one listing for the new page's two references");
    // The app's own page takes the same choices.
    let own = get(&app, "/app/new-order", "clerk").await;
    assert!(own.html.contains(&format!(r#"<option value="{anvil}">Anvil</option>"#)), "{}", own.html);
    store.calls();

    // An order's page names its lines' products through the lookup, its
    // gift from the listing, and its action's product is a select.
    let first = uuid("8d1f6c9e-0000-4000-8000-000000000101");
    let item = get(&app, &format!("/console/orders/{}", wire(&first)), "clerk").await;
    assert_eq!(item.status, StatusCode::OK, "{}", item.html);
    assert!(item.html.contains(r#"<td data-field="productId">Anvil</td>"#), "{}", item.html);
    assert!(item.html.contains(r#"<td data-field="productId">Crate</td>"#), "{}", item.html);
    assert!(item.html.contains(r#"<dd>Bucket</dd>"#), "the gift by its name: {}", item.html);
    assert!(item.html.contains(r#"<select id="order-add-line-product-id" name="productId" required"#), "{}", item.html);
    let (listings, lookups) = store.calls();
    assert_eq!(listings, 1, "one listing for the gift and the action's select");
    assert_eq!(lookups, vec![vec![anvil.clone(), wire(&uuid(CRATE))]], "one lookup of the lines' products");

    // The list names each row's gift; its filter's product is a select.
    let list = get(&app, "/console/orders", "clerk").await;
    assert!(list.html.contains(r#"<td data-field="giftSku">Bucket</td>"#), "{}", list.html);
    assert!(list.html.contains(r#"<select id="order-list-orders-product-id" name="productId""#), "{}", list.html);
    let (listings, lookups) = store.calls();
    assert_eq!(listings, 1, "one listing for the rows' gifts and the filter");
    assert_eq!(lookups.len(), 1, "one lookup for the rows' lines: {lookups:?}");

    // A post through the select places the order with the product's id.
    let placed = post(&app, "/console/orders/new", "clerk", &format!("lines[0].productId={anvil}&lines[0].quantity=2&giftSku=drum")).await;
    assert_eq!(placed.status, StatusCode::SEE_OTHER, "{}", placed.html);
    let order = store.orders.lock().unwrap().last().cloned().unwrap();
    assert_eq!((order.lines[0].product_id, order.gift_sku.as_deref()), (uuid(ANVIL), Some("drum")));
}

#[tokio::test]
async fn a_caller_who_may_not_list_the_items_gets_the_plain_control_and_the_value() {
    let store = Store::new();
    let app = app(&store);
    let new = get(&app, "/console/orders/new", "taker").await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.html);
    assert!(new.html.contains(r#"<input id="order-input-lines-0-product-id" name="lines[0].productId" type="text" required"#), "{}", new.html);
    assert!(!new.html.contains("<option value=\"drum\""), "{}", new.html);
    let item = get(&app, &format!("/console/orders/{}", wire(&uuid(FIRST))), "taker").await;
    assert_eq!(item.status, StatusCode::OK, "{}", item.html);
    assert!(item.html.contains(&format!(r#"<td data-field="productId">{}</td>"#, wire(&uuid(ANVIL)))), "{}", item.html);
    assert!(item.html.contains(r#"<dd>bucket</dd>"#), "{}", item.html);
    // The calls were refused before the implementation.
    assert_eq!(store.calls(), (0, vec![]));
}

#[tokio::test]
async fn the_apps_choices_win_and_spare_the_call() {
    let store = Store::new();
    // The app names the lines' products itself, by their ids' text.
    let anvil = wire(&uuid(ANVIL));
    let own = Choices::new().with("lines.productId", [(anvil.clone(), "The anvil")]);
    let app = app_with(&store, Settings::new().choices(own));
    let new = get(&app, "/console/orders/new", "clerk").await;
    assert!(new.html.contains(&format!(r#"<option value="{anvil}">The anvil</option>"#)), "{}", new.html);
    assert!(new.html.contains(r#"<option value="drum">Drum</option>"#), "{}", new.html);
    assert_eq!(store.calls().0, 1, "the gift's listing alone");
    let item = get(&app, &format!("/console/orders/{}", wire(&uuid(FIRST))), "clerk").await;
    assert!(item.html.contains(r#"<td data-field="productId">The anvil</td>"#), "{}", item.html);
    assert_eq!(store.calls().1, Vec::<Vec<String>>::new(), "no lookup for a path the app names");
}

#[tokio::test]
async fn more_items_than_a_page_keep_the_control_and_are_named_in_chunks() {
    let store = Store::new();
    store.products.lock().unwrap().extend([product(EASEL, "Easel"), product(FUNNEL, "Funnel")]);
    let all = [ANVIL, BUCKET, CRATE, DRUM, EASEL, FUNNEL];
    store.orders.lock().unwrap().push(order(9, types::OrderStatus::Placed, None, None, all.iter().map(|id| line(id, 1.0)).collect()));
    let app = app(&store);
    let new = get(&app, "/console/orders/new", "clerk").await;
    assert!(new.html.contains(r#"<input id="order-input-lines-0-product-id" name="lines[0].productId" type="text" required"#), "{}", new.html);
    store.calls();
    let item = get(&app, &format!("/console/orders/{}", wire(&uuid("8d1f6c9e-0000-4000-8000-000000000109"))), "clerk").await;
    for name in ["Anvil", "Easel", "Funnel"] {
        assert!(item.html.contains(&format!(r#"<td data-field="productId">{name}</td>"#)), "{name}: {}", item.html);
    }
    let lookups: Vec<usize> = store.calls().1.iter().map(Vec::len).collect();
    assert_eq!(lookups, [5, 1], "six products in chunks of the lookup's listMax");
}

#[tokio::test]
async fn products_link_through_their_reference_and_page_their_reviews() {
    let app = app(&Store::new());
    let list = get(&app, "/console/products", "clerk").await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    let anvil = wire(&uuid(ANVIL));
    assert!(list.html.contains(&format!(r#"<a href="/console/products/{anvil}">Anvil</a>"#)), "{}", list.html);
    assert!(link(&list.html, "next").is_some(), "{}", list.html);
    // A product names its supplier, from the suppliers' first page, while
    // the reviews and the order lines that refer to products name them.
    assert!(list.html.contains(r#"<td data-field="supplierId">Acme</td>"#), "{}", list.html);
    let later = get(&app, "/console/products?q=drum", "clerk").await;
    assert!(later.html.contains(r#"<td data-field="supplierId">Bolt</td>"#), "{}", later.html);
    let searched = get(&app, "/console/products?q=dr", "clerk").await;
    assert!(searched.html.contains(">Drum</a>") && !searched.html.contains(">Anvil</a>"), "{}", searched.html);

    // A product's page shows the first page of its reviews, each naming the
    // product, and a link to the next page of its list.
    let item = get(&app, &format!("/console/products/{anvil}"), "clerk").await;
    assert_eq!(item.status, StatusCode::OK, "{}", item.html);
    assert!(item.html.contains("<dd>Acme</dd>"), "{}", item.html);
    let section = between(&item.html, r#"<section class="ss-section" data-resource="reviews">"#, "</section>");
    assert!(section.contains("Sturdy") && section.contains("Heavy") && !section.contains("Sturdy and heavy"), "{section}");
    assert!(section.contains(r#"<td data-field="productId">Anvil</td>"#), "{section}");
    let next = link(section, "next").unwrap();
    assert!(next.starts_with(&format!("/console/products/{anvil}/reviews?cursor=")), "{next}");
    let reviews = get(&app, &next, "clerk").await;
    assert_eq!(reviews.status, StatusCode::OK, "{}", reviews.html);
    assert!(reviews.html.contains("Sturdy and heavy"), "{}", reviews.html);
    assert_eq!(link(&reviews.html, "first"), Some(format!("/console/products/{anvil}/reviews")), "{}", reviews.html);
    let searched = get(&app, &format!("/console/products/{anvil}/reviews?q=sturdy&minRating=4"), "clerk").await;
    assert!(searched.html.contains("Sturdy and heavy") && !searched.html.contains(">Heavy<"), "{}", searched.html);
}

#[tokio::test]
async fn a_live_list_pages_in_place() {
    let app = app(&Store::new());
    let list = get(&app, "/console/orders", "clerk").await;
    let next = link(&list.html, "next").unwrap();
    assert!(list.html.contains(r#"<a rel="next" href=""#) && list.html.contains("data-topcoat-on:click="), "{}", list.html);
    assert!(list.html.contains("history.replaceState"), "{}", list.html);
    assert!(!list.html.contains(")return"), "the address bar's statement ends: {}", list.html);
    let query = next.strip_prefix("/console/orders?").unwrap().to_owned();
    // The shard renders the page its query asks for, without the layout.
    let live = shard(&app, "/console/_live/orders", "clerk", serde_json::json!({"args": [query, "/console/orders"]})).await;
    assert_eq!(live.status, StatusCode::OK, "{}", live.html);
    assert_eq!(numbers(&live.html), ["A-3", "A-4"], "{}", live.html);
    assert!(!live.html.contains("ss-nav"), "{}", live.html);
    assert_eq!(link(&live.html, "first").as_deref(), Some("/console/orders"), "{}", live.html);
    // As the runtime renders it again, under the shard's identity, with the
    // query a link set in its signal.
    let identity = between(&list.html, r#"::topcoat::shard::start("/console/_live/orders", ""#, "\"");
    let signal = between(&list.html, "::topcoat::signal({&quot;t&quot;:&quot;signal&quot;,&quot;id&quot;:&quot;", "&quot;");
    let body = serde_json::json!({"args": ["", "/console/orders"], "signals": {signal: query}});
    let mut request = http::Request::builder().method("POST").uri("/console/_live/orders").header("x-caller", "clerk");
    request = request.header(header::CONTENT_TYPE, "application/json").header("x-topcoat-identity", identity);
    let response = app.handle(request.body(Body::from(body.to_string())).unwrap()).await;
    assert_eq!(response.status(), StatusCode::OK);
    let html = String::from_utf8(to_bytes(response.into_body(), usize::MAX).await.unwrap().to_vec()).unwrap();
    assert_eq!(numbers(&html), ["A-3", "A-4"], "{html}");
    // A refused cursor is the shard's 400 too.
    let refused = shard(&app, "/console/_live/orders", "clerk", serde_json::json!({"args": ["cursor=e30", "/console/orders"]})).await;
    assert_eq!(refused.status, StatusCode::BAD_REQUEST, "{}", refused.html);
}

#[tokio::test]
async fn a_procedure_answers_a_page_record() {
    let app = app(&Store::new());
    let first = get(&app, "/app/orders-procedure", "clerk").await;
    let said = between(&first.html, r#"<p id="said">"#, "</p>").to_owned();
    let (numbers, next) = said.split_once(" next=").unwrap();
    assert_eq!(numbers, "A-1,A-2,A-3");
    let second = get(&app, &format!("/app/orders-procedure?cursor={next}"), "clerk").await;
    assert_eq!(between(&second.html, r#"<p id="said">"#, "</p>"), "A-4,A-5 next=");
    let forged = get(&app, "/app/orders-procedure?cursor=e30", "clerk").await;
    assert_eq!(between(&forged.html, r#"<p id="said">"#, "</p>"), "400 cursor is not one this operation returned");
    let past = get(&app, "/app/orders-procedure?limit=9", "clerk").await;
    assert_eq!(between(&past.html, r#"<p id="said">"#, "</p>"), "400 Invalid query parameter limit: must be at most 5");
}

#[tokio::test]
async fn a_paged_list_without_a_filter_renders_no_form_and_its_refusal_alone() {
    let app = app(&Store::new());
    let first = get(&app, "/console/suppliers", "clerk").await;
    assert_eq!(first.status, StatusCode::OK, "{}", first.html);
    assert!(!first.html.contains("<form"), "{}", first.html);
    assert!(first.html.contains(">Acme</th>") && !first.html.contains(">Bolt</th>"), "{}", first.html);
    let next = link(&first.html, "next").unwrap();
    assert!(next.starts_with("/console/suppliers?cursor="), "{next}");
    let two = get(&app, "/console/suppliers?limit=2", "clerk").await;
    assert!(two.html.contains(">Acme</th>") && two.html.contains(">Bolt</th>"), "{}", two.html);
    let last = get(&app, &link(&two.html, "next").unwrap(), "clerk").await;
    assert!(last.html.contains(">Cog</th>") && link(&last.html, "next").is_none(), "{}", last.html);
    assert_eq!(link(&last.html, "first").as_deref(), Some("/console/suppliers?limit=2"), "{}", last.html);
    let refused = get(&app, "/console/suppliers?limit=3", "clerk").await;
    assert_eq!(refused.status, StatusCode::BAD_REQUEST, "{}", refused.html);
    assert!(refused.html.contains(r#"<section class="ss-refusal" data-status="400">"#), "{}", refused.html);
    assert!(refused.html.contains("<p>limit: must be at most 2</p>"), "{}", refused.html);
}

#[tokio::test]
async fn a_list_at_page_pages_beside_the_pagers_helpers() {
    let app = app(&Store::new());
    let first = get(&app, "/console/page", "clerk").await;
    assert_eq!(first.status, StatusCode::OK, "{}", first.html);
    assert!(first.html.contains("About us") && !first.html.contains("Terms"), "{}", first.html);
    let next = link(&first.html, "next").unwrap();
    assert!(next.starts_with("/console/page?cursor="), "{next}");
    let second = get(&app, &next, "clerk").await;
    assert!(second.html.contains("Terms") && !second.html.contains("About us"), "{}", second.html);
    assert_eq!(link(&second.html, "first").as_deref(), Some("/console/page"), "{}", second.html);
}
`

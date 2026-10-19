package topcoat_test

import (
	"fmt"
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
// filter and the limit, and on a later page a "First page" link and a
// "Previous page" link, which the history in the query's back gives (D44,
// amended): walked forward and back, each Previous lands on the page left,
// a history past its bound drops its oldest cursor with the mark first,
// and one that breaks its rule, or comes without a cursor, links no page
// before and is never refused. After the links a size form offers the
// route's sizes, the limit sent selected, holding the filter, and starts
// at the first page; the filter holds the limit sent, and neither form a
// cursor or a history. A limit or cursor the route refuses, and a cursor
// the implementation cannot read, is the route's 400 as the form's own
// message, and a paged list without a filter renders no filter form and
// its refusal alone. The search box is the filter's first control, a search
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
// and a link to the next. The live list's shard pages in place, back by
// the history in its signal's query too, and its size form sets the
// signal; a procedure answers a page record, and the site's pages, a paged
// list at the path page, link their next and first pages. A supplier's
// products, a list under a supplier that no operation reads, name the
// supplier in the trail through what their route's supplierId refers to,
// or by its id past listSuppliers' first page or to a caller who may not
// list suppliers.
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
			"pager(base: href.clone(), first: None, prev: None, next: next_page)",
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

// TestThePagerGoesBackAndSetsItsSize reads fixture-console-api's pages: a
// paged list keeps the cursors of the pages before it in the query's back,
// at most MAX_BACK characters, links the page before with rel="prev", and
// renders a size form of the route's sizes, which a live list submits in
// place.
func TestThePagerGoesBackAndSetsItsSize(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	pages := files["pages.rs"]
	for _, want := range []string{
		"const MAX_BACK: usize = 4096;",
		`<a rel="prev" href=(href.clone())`,
		`<form method="get" class="ss-page-size" data-ss-live-size="orders"`,
		`document.querySelector('form[data-ss-live-size=\"orders\"]')`,
		"const ORDERS_PAGE_SIZES: &[i64] = &[2, 5];",
		"const SUPPLIERS_PAGE_SIZES: &[i64] = &[1, 2];",
		"let back = Back::read(&sent, cursor.is_some());",
		`<input type="hidden" name="limit" value=(limit)>`,
	} {
		if !strings.Contains(pages, want) {
			t.Errorf("pages.rs lacks %q", want)
		}
	}
	// A section's pager has no history and no size form.
	if !strings.Contains(pages, "pager(base: href.clone(), first: None, prev: None, next: next_page)") {
		t.Error("the reviews' section pages with more than its next page")
	}
	// Without the shards, the pager component renders them.
	static, _ := readCrate(t, registry.DefaultNaming(), consoleService, pagesOutputs())
	for _, want := range []string{
		`<a rel="prev" href=(pager_href(&base, &earlier))>"Previous page"</a>`,
		`<form method="get" class="ss-page-size">`,
		"pager(base: base, first: first_page, prev: prev_page, next: next_page, sizes: sizes, hidden: hidden)",
		"let back = Back::read(uri(cx).query().unwrap_or_default(), cursor.is_some());",
	} {
		if !strings.Contains(static["pages.rs"], want) {
			t.Errorf("pages.rs without live lists lacks %q", want)
		}
	}
	if strings.Contains(static["pages.rs"], "data-ss-live-size") {
		t.Error("pages.rs without live lists submits a size form in place")
	}
}

// TestAnArgumentNamedBackHasNoPreviousPage builds fixture-console-api with
// a back argument on listOrders: the orders' list links no page before its
// own and keeps no history, the build log says why, and the other lists go
// back as before.
func TestAnArgumentNamedBackHasNoPreviousPage(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		return strings.Replace(schema, "          - name: statuses\n", "          - name: back\n            typeRef: { name: string }\n            isQuery: true\n          - name: statuses\n", 1)
	})
	root := testpaths.TempDir(t)
	var log strings.Builder
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, livePagesOutputs(), &log); err != nil {
		t.Fatalf("build: %v", err)
	}
	want := "  - topcoat: no previous page for orders: order.listOrders declares an argument named back, the query parameter the console keeps the earlier pages' cursors in\n"
	if !strings.Contains(log.String(), want) {
		t.Errorf("the build log lacks %q:\n%s", want, log.String())
	}
	data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, consoleService), "src", "pages.rs"))
	if err != nil {
		t.Fatal(err)
	}
	pages := string(data)
	orders := pages[strings.Index(pages, "async fn orders_list("):]
	orders = orders[:strings.Index(orders, "\n}\n")]
	for _, banned := range []string{`rel="prev"`, "Back::read", "back.push"} {
		if strings.Contains(orders, banned) {
			t.Errorf("the orders' list holds %q:\n%s", banned, orders)
		}
	}
	if !strings.Contains(orders, "pager_query(&pairs, Some(next.as_str()), None)") || !strings.Contains(orders, `class="ss-page-size"`) {
		t.Errorf("the orders' list lost its Next page or its size form:\n%s", orders)
	}
	products := pages[strings.Index(pages, "async fn products_list("):]
	if !strings.Contains(products[:strings.Index(products, "\n}\n")], `rel="prev"`) {
		t.Error("the products' list has no page before")
	}
}

// TestATrailStepIsNamedByItsReference reads fixture-console-api's crate: a
// supplier's step in the trail, which no detail titles, is named through
// the reference of listSupplierProducts' supplierId, and a product's,
// whose detail titles it, by the reference getProduct and listReviews
// agree on when its title does not.
func TestATrailStepIsNamedByItsReference(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	for file, wants := range map[string][]string{
		"pages.rs": {
			"        key: \"suppliers\",\n        titled: false,\n        reference: Some(\"suppliers\"),",
			"        key: \"products\",\n        titled: true,\n        reference: Some(\"products\"),",
			`"suppliers" => references::supplier_target_label(cx, value).await,`,
			`"products" => references::product_target_label(cx, value).await,`,
			"reference: node.reference.map(|key| (key, decode(item))),",
			"} else if let Some((key, value)) = &crumb.reference",
		},
		"references.rs": {
			"pub(crate) async fn supplier_target_label(cx: &Cx, value: &str) -> Option<String> {",
			"pub(crate) async fn product_target_label(cx: &Cx, value: &str) -> Option<String> {",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(files[file], want) {
				t.Errorf("%s lacks %q", file, want)
			}
		}
	}
}

// TestAStepWhoseReferencesDisagreeIsNamedByItsID builds fixture-console-api
// with a second list under a supplier whose supplierId refers to a product
// by its sku: the supplier's step is named by nothing but its id, and the
// build log names each reference.
func TestAStepWhoseReferencesDisagreeIsNamedByItsID(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		return strings.Replace(schema, "  - name: SiteQueries\n", `      - name: listSupplierOrders
        typeRef: { name: Order, isArray: true }
        required: true
        httpMethod: GET
        restPath: "suppliers/{supplierId}/orders"
        permissions: [orders.read]
        arguments:
          - name: supplierId
            typeRef: { name: string }
            required: true
            references: { type: Product, value: sku }
  - name: SiteQueries
`, 1)
	})
	root := testpaths.TempDir(t)
	var log strings.Builder
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, livePagesOutputs(), &log); err != nil {
		t.Fatalf("build: %v", err)
	}
	want := "  - topcoat: the trail names suppliers/{supplierId} by its id: supplier.listSupplierOrders(supplierId) refers to Product by sku, and supplier.listSupplierProducts(supplierId) to Supplier by id\n"
	if !strings.Contains(log.String(), want) {
		t.Errorf("the build log lacks %q:\n%s", want, log.String())
	}
	data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, consoleService), "src", "pages.rs"))
	if err != nil {
		t.Fatal(err)
	}
	pages := string(data)
	if !strings.Contains(pages, "        key: \"suppliers\",\n        titled: false,\n        reference: None,") || strings.Contains(pages, `"suppliers" => references::`) {
		t.Error("the supplier's step is named by a reference its operations disagree on")
	}
	if !strings.Contains(pages, `"products" => references::product_target_label(cx, value).await,`) {
		t.Error("the product's step lost its reference")
	}
}

// TestATrailStepMeetsItsOptionsControls builds fixture-console-api with a
// rate limit on listSuppliers and a list of pages under a supplier, whose
// rows refer to nothing: the list's page meets the limit, since its trail
// names the supplier through listSuppliers.
func TestATrailStepMeetsItsOptionsControls(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		schema = strings.Replace(schema, "        typeRef: { name: Product, isArray: true }\n        required: true\n        httpMethod: GET\n        restPath: \"suppliers/{supplierId}/products\"\n",
			"        typeRef: { name: SitePage, isArray: true }\n        required: true\n        httpMethod: GET\n        restPath: \"suppliers/{supplierId}/pages\"\n", 1)
		return strings.Replace(schema, "        restPath: suppliers\n        permissions: [products.read]\n", "        restPath: suppliers\n        permissions: [products.read]\n        middleware: { rateLimit: 30 }\n", 1)
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
	controls := pages[strings.Index(pages, "        controls: vec![\n"):]
	controls = controls[:strings.Index(controls, "        ],\n    })")]
	limiter := strings.Count(controls[:strings.Index(controls, "Controls::default().rate_limit(30)")], "Controls::default()")
	for _, want := range []string{
		fmt.Sprintf(`(Method::GET, "/console/suppliers/{supplier_id}/pages", None, &[%d]),`, limiter),
		fmt.Sprintf(`(Method::GET, "/console/suppliers", Some(%d), &[]),`, limiter),
	} {
		if !strings.Contains(pages, want) {
			t.Errorf("pages.rs lacks %q:\n%s", want, controls)
		}
	}
}

// suppliersService is the extension's fixture of an API that declares
// Supplier, which a copy of fixture-console-api imports (D66).
const suppliersService = "fixture-console-suppliers-api"

// suppliersCopy is a copy of fixture-console-api whose Supplier is
// fixture-console-suppliers-api's, a dependency of kind API, after edit.
func suppliersCopy(t *testing.T, edit func(schema string) string) string {
	t.Helper()
	dir := consoleCopy(t, func(schema string) string {
		start := strings.Index(schema, "  # Who supplies the shop's products.\n")
		end := strings.Index(schema, "  # A page of the shop's site")
		schema = schema[:start] + schema[end:]
		schema = strings.Replace(schema, "kind: API\n", "kind: API\nimports:\n  - package: \"@schemas/"+suppliersService+"\"\n    types: [Supplier]\n", 1)
		return edit(schema)
	})
	config := filepath.Join(dir, "schema.config.yaml")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, string(data)+"dependencies:\n  - name: "+suppliersService+"\n    kind: API\n")
	return dir
}

// TestAReferenceToAnAPIDependencysType builds fixture-console-api with its
// Supplier imported from fixture-console-suppliers-api, an API dependency
// (D66), and a new product's form whose supplier refers to it: the
// references offer and name suppliers through listSuppliers, the API's
// own listing of the dependency's type, as they do a local type's, and
// cargo clippy and cargo test run on the crate with supplierAppTest. With
// listSuppliers behind a provider's signature, the build log names the
// target by its service.
func TestAReferenceToAnAPIDependencysType(t *testing.T) {
	withForm := func(schema string) string {
		schema = strings.Replace(schema, "  OrderLineInput:\n", `  # A new product, whose supplier is one of the suppliers.
  ProductInput:
    name: ProductInput
    role: APIInput
    fields:
      - name: name
        typeRef: { name: string }
        required: true
      - name: sku
        typeRef: { name: string }
        required: true
      - name: supplierId
        title: Supplier
        typeRef: { name: string }
        required: true
        references: { type: Supplier }
  OrderLineInput:
`, 1)
		return strings.Replace(schema, "  - name: SupplierQueries\n", `  - name: ProductMutations
    operations:
      # Adds a product.
      - name: createProduct
        typeRef: { name: Product }
        required: true
        httpMethod: POST
        restPath: products
        permissions: [products.read]
        arguments:
          - name: input
            typeRef: { name: ProductInput }
            required: true
  - name: SupplierQueries
`, 1)
	}
	dir := suppliersCopy(t, withForm)
	root := testpaths.TempDir(t)
	var log strings.Builder
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, livePagesOutputs(), &log); err != nil {
		t.Fatalf("build: %v\n%s", err, log.String())
	}
	data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, consoleService), "src", "references.rs"))
	if err != nil {
		t.Fatal(err)
	}
	references := string(data)
	for _, want := range []string{
		"async fn supplier_target_choices(cx: &Cx) -> Option<Vec<Choice>> {",
		"async fn supplier_target_labels(cx: &Cx, values: &BTreeSet<String>) -> Vec<Choice> {",
		"pub async fn product_labels(cx: &Cx, records: &[ProductRecord], choices: Choices) -> Choices {",
		`let sites = unnamed(&choices, &[("supplierId", &["supplierId"])]);`,
		"let labels = supplier_target_labels(cx, &values_at(&json, &sites)).await;",
		"pub async fn product_input_form_choices(cx: &Cx, choices: Choices) -> Choices {",
	} {
		if !strings.Contains(references, want) {
			t.Errorf("references.rs lacks %q", want)
		}
	}
	cargoTestCrateWith(t, consoleService, supplierAppTest, crateOptions{outputs: livePagesOutputs(), dir: dir, dependencies: []string{suppliersService}})

	signed := suppliersCopy(t, func(schema string) string {
		return strings.Replace(withForm(schema), "        restPath: suppliers\n        permissions: [products.read]\n", "        restPath: suppliers\n        permissions: [products.read]\n        hmacVerifiedProvider: stripe\n", 1)
	})
	log.Reset()
	if _, err := buildDir(t, registry.DefaultNaming(), signed, testpaths.TempDir(t), livePagesOutputs(), &log); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{
		"topcoat: Product.supplierId refers to fixture-console-suppliers-api.Supplier, whose options operation SupplierQueries.listSuppliers has no in-process call: its route checks a provider's signature first (@hmacVerified)",
		"topcoat: ProductInput.supplierId refers to fixture-console-suppliers-api.Supplier, whose options operation SupplierQueries.listSuppliers has no in-process call",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the build log lacks %q:\n%s", want, log.String())
		}
	}
}

// TestADerivedReferenceLabelsRows builds fixture-console-api with its order
// lines and products sourced from a database's lines and items, @source,
// a line's product a Relation to Item, and with OrderLine.productId saying no
// RefersTo: the key derives its reference to Product, the one type sourced
// from the related table (D61, amended), so the crate's references are
// those of the written reference, its order lines' products labeled
// through the same lookup.
func TestADerivedReferenceLabelsRows(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		schema = strings.Replace(schema, "kind: API\n", "kind: API\nimports:\n  - package: \"@schemas/fixture-console-db\"\n    types: [Item, Line]\n", 1)
		schema = strings.Replace(schema, "  Product:\n    name: Product\n    role: APIView\n", "  Product:\n    name: Product\n    role: APIView\n    source: { target: fixture-console-db.Item }\n", 1)
		return strings.Replace(schema, "  OrderLine:\n    name: OrderLine\n    role: APIView\n    display:\n      noun: Line\n    fields:\n      - name: productId\n        title: Product\n        typeRef: { name: Identity.UUID }\n        required: true\n        references: { type: Product }\n",
			"  OrderLine:\n    name: OrderLine\n    role: APIView\n    source: { target: fixture-console-db.Line }\n    display:\n      noun: Line\n    fields:\n      - name: productId\n        title: Product\n        typeRef: { name: Identity.UUID }\n        required: true\n", 1)
	})
	config := filepath.Join(dir, "schema.config.yaml")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, string(data)+"dependencies:\n  - name: fixture-console-db\n    kind: DB\n")
	db := filepath.Join(filepath.Dir(dir), "fixture-console-db")
	writeFile(t, filepath.Join(db, "schema.config.yaml"), "name: fixture-console-db\nkind: DB\noutputs: {}\n")
	writeFile(t, filepath.Join(db, "src", "shop.schema.yaml"), `name: fixture-console-db
kind: DB
scalars:
  Identity.UUID:
    name: Identity.UUID
    languagePrimitive: string
types:
  Item:
    name: Item
    role: DBTable
    fields:
      - name: id
        typeRef: { name: Identity.UUID }
        required: true
        key: true
      - name: name
        typeRef: { name: string }
        required: true
  Line:
    name: Line
    role: DBTable
    fields:
      - name: id
        typeRef: { name: Identity.UUID }
        required: true
        key: true
      - name: product
        typeRef: { name: Item }
        required: true
        relation: { type: Item }
      - name: quantity
        typeRef: { name: number }
        required: true
`)
	root := testpaths.TempDir(t)
	var log strings.Builder
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, livePagesOutputs(), &log); err != nil {
		t.Fatalf("build: %v\n%s", err, log.String())
	}
	got, err := os.ReadFile(filepath.Join(topcoat.Dir(root, consoleService), "src", "references.rs"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "golden", consoleService, "src", "references.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("references.rs with the derived reference differs from the written one's:\n%s", got)
	}
	if !strings.Contains(string(got), `let sites = unnamed(&choices, &[("lines.productId", &["lines", "productId"])]);`) {
		t.Error("the order's lines do not name their products")
	}
}

// writeFile writes text to path, making its directory.
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
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
    SiteImplementation, SiteListPagesArgs, SupplierImplementation, SupplierListSupplierProductsArgs, SupplierListSuppliersArgs,
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
    async fn list_supplier_products(&self, _ctx: RequestContext, args: SupplierListSupplierProductsArgs) -> Result<Vec<types::Product>, ApiError> {
        let products = self.0.products.lock().unwrap();
        Ok(products.iter().filter(|product| product.supplier_id == args.supplier_id).cloned().collect())
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
    // The filter holds neither a cursor nor a limit the query did not send,
    // so a submitted filter starts at the first page.
    let filter = between(&first.html, r#"<form method="get" class="ss-filter""#, "</form>");
    assert!(!filter.contains(r#"name="cursor""#) && !filter.contains(r#"name="limit""#), "{filter}");

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
    // Its one form is the pager's size form.
    assert!(!first.html.contains("ss-filter"), "{}", first.html);
    assert_eq!(first.html.matches("<form").count(), 1, "{}", first.html);
    assert!(first.html.contains(r#"<form method="get" class="ss-page-size""#), "{}", first.html);
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

// The value of a query parameter of href, as it is written, or none.
fn param<'a>(href: &'a str, name: &str) -> Option<&'a str> {
    href.split_once('?')?.1.split('&').find_map(|pair| pair.strip_prefix(name)?.strip_prefix('='))
}

// The text of a page's size form, which its pager holds after its links.
fn size_form(html: &str) -> &str {
    between(html, r#"<form method="get" class="ss-page-size""#, "</form>")
}

// Seven orders, so the orders' pages of two are four.
fn seven_orders() -> Arc<Store> {
    let store = Store::new();
    store.orders.lock().unwrap().extend([
        order(6, types::OrderStatus::Placed, None, None, vec![line(ANVIL, 1.0)]),
        order(7, types::OrderStatus::Shipped, None, None, vec![line(BUCKET, 1.0)]),
    ]);
    store
}

#[tokio::test]
async fn the_pager_goes_back_a_page_by_its_history() {
    let app = app(&seven_orders());
    // Next three times: each page's href and rows. Each Next carries the
    // history of the pages before it, which grows by the cursor of the page
    // it leaves, the first page's none.
    let mut pages = vec![("/console/orders".to_owned(), get(&app, "/console/orders", "clerk").await)];
    for _ in 0..3 {
        let next = link(&pages.last().unwrap().1.html, "next").unwrap();
        let page = get(&app, &next, "clerk").await;
        assert_eq!(page.status, StatusCode::OK, "{}", page.html);
        pages.push((next, page));
    }
    let rows: Vec<Vec<String>> = pages.iter().map(|(_, page)| numbers(&page.html)).collect();
    assert_eq!(rows, [vec!["A-1", "A-2"], vec!["A-3", "A-4"], vec!["A-5", "A-6"], vec!["A-7"]]);
    let cursors: Vec<&str> = pages[1..].iter().map(|(href, _)| param(href, "cursor").unwrap()).collect();
    assert_eq!(param(&pages[1].0, "back"), None, "{}", pages[1].0);
    assert_eq!(param(&pages[2].0, "back"), Some(cursors[0]), "{}", pages[2].0);
    assert_eq!(param(&pages[3].0, "back"), Some(format!("{}.{}", cursors[0], cursors[1]).as_str()), "{}", pages[3].0);

    // The first page has no Previous; each later page's is the page it was
    // reached from, and following it lands on the rows that page showed.
    assert!(link(&pages[0].1.html, "prev").is_none(), "{}", pages[0].1.html);
    for at in 1..pages.len() {
        let prev = link(&pages[at].1.html, "prev").unwrap();
        assert_eq!(prev, pages[at - 1].0, "page {}: {}", at + 1, pages[at].1.html);
        assert_eq!(numbers(&get(&app, &prev, "clerk").await.html), rows[at - 1], "page {}", at + 1);
    }
    // The order is First, Previous, Next, then the size form, and on the
    // second page Previous is First, both rendered.
    let second = &pages[1].1.html;
    assert_eq!(link(second, "prev"), link(second, "first"), "{second}");
    let pager = between(second, r#"<nav class="ss-pager" aria-label="Pages">"#, "</nav>");
    let places: Vec<usize> = [r#"rel="first""#, r#"rel="prev""#, r#"rel="next""#, "ss-page-size"].iter().map(|at| pager.find(at).unwrap()).collect();
    assert!(places.is_sorted(), "{pager}");
}

#[tokio::test]
async fn a_history_past_its_bound_or_its_rule_has_no_previous_page() {
    let app = app(&seven_orders());
    // A cursor of 1024 characters, the most a route reads, after A-1, and
    // entries of 1024 characters, which are cursors by the rule though no
    // page returned them.
    let long = Cursor::encode(&format!("A-1{}", "x".repeat(763))).unwrap();
    assert_eq!(long.as_str().len(), 1024);
    let long = long.as_str().to_owned();
    let entry = "A".repeat(1024);

    // Three entries are 3074 characters: the page goes back to the last,
    // with the rest; its Next carries them with its cursor, past the bound,
    // so the oldest is dropped, and the mark comes first.
    let three = [entry.as_str(); 3].join(".");
    let page = get(&app, &format!("/console/orders?cursor={long}&back={three}"), "clerk").await;
    assert_eq!(page.status, StatusCode::OK, "{}", page.html);
    assert_eq!(numbers(&page.html), ["A-2", "A-3"], "{}", page.html);
    let prev = link(&page.html, "prev").unwrap();
    assert_eq!(param(&prev, "cursor"), Some(entry.as_str()), "{prev}");
    assert_eq!(param(&prev, "back"), Some([entry.as_str(); 2].join(".").as_str()), "{prev}");
    let next = link(&page.html, "next").unwrap();
    let pushed = param(&next, "back").unwrap();
    assert_eq!(pushed, format!("~.{entry}.{entry}.{long}"), "{next}");
    assert!(pushed.len() - 2 <= 4096, "{}", pushed.len());
    // That page's history reads as sent: its Previous goes back to the long
    // cursor, whose own history keeps the mark; one entry after the mark
    // goes back to it, and the mark alone knows no page before.
    let later = get(&app, &next, "clerk").await;
    let back = link(&later.html, "prev").unwrap();
    assert_eq!(param(&back, "cursor"), Some(long.as_str()), "{back}");
    assert_eq!(param(&back, "back"), Some(format!("~.{entry}.{entry}").as_str()), "{back}");
    let marked = get(&app, &format!("/console/orders?cursor={long}&back=~.{entry}"), "clerk").await;
    assert_eq!(link(&marked.html, "prev").as_deref().and_then(|href| param(href, "back")), Some("~"), "{}", marked.html);
    let mark = get(&app, &format!("/console/orders?cursor={long}&back=~"), "clerk").await;
    assert!(link(&mark.html, "prev").is_none(), "{}", mark.html);

    // Four entries are 4099 characters, past the bound, as is a malformed
    // history: no Previous, the first page still offered, a 200, never the
    // route's 400, and a Next whose history begins again at the mark.
    let four = [entry.as_str(); 4].join(".");
    let long_entry = "A".repeat(1025);
    for back in [four.as_str(), "a..b", "~~", "%24", "a.~", ".", long_entry.as_str()] {
        let page = get(&app, &format!("/console/orders?cursor={long}&back={back}"), "clerk").await;
        assert_eq!(page.status, StatusCode::OK, "back={back}: {}", page.html);
        assert!(link(&page.html, "prev").is_none(), "back={back}: {}", page.html);
        assert_eq!(link(&page.html, "first").as_deref(), Some("/console/orders"), "back={back}");
        assert_eq!(link(&page.html, "next").as_deref().and_then(|href| param(href, "back")), Some(format!("~.{long}").as_str()), "back={back}");
    }
    // Without a cursor the page is the first, whatever back says.
    let first = get(&app, &format!("/console/orders?back={three}"), "clerk").await;
    assert_eq!(first.status, StatusCode::OK, "{}", first.html);
    assert_eq!(numbers(&first.html), ["A-1", "A-2"], "{}", first.html);
    assert!(link(&first.html, "prev").is_none() && link(&first.html, "first").is_none(), "{}", first.html);
    assert_eq!(link(&first.html, "next").as_deref().and_then(|href| param(href, "back")), None, "{}", first.html);
}

#[tokio::test]
async fn the_size_form_sets_the_page_size_and_keeps_the_filter() {
    let app = app(&seven_orders());
    // The route's sizes, 2 and 5, the default selected, then the limit sent.
    let first = get(&app, "/console/orders", "clerk").await;
    assert!(size_form(&first.html).contains(r#"<label>Per page <select name="limit"><option value="2" selected="">2</option><option value="5">5</option></select></label>"#), "{}", first.html);
    let five = get(&app, "/console/orders?limit=5", "clerk").await;
    assert_eq!(numbers(&five.html), ["A-1", "A-2", "A-3", "A-4", "A-5"], "{}", five.html);
    assert!(size_form(&five.html).contains(r#"<option value="2">2</option><option value="5" selected="">5</option>"#), "{}", five.html);
    // The filter keeps the limit sent, and neither a cursor nor a history.
    let filter = between(&five.html, r#"<form method="get" class="ss-filter""#, "</form>");
    assert!(filter.contains(r#"<input type="hidden" name="limit" value="5">"#), "{filter}");

    // A later page of a search: the size form holds the filter and the
    // search, with the limit sent among the sizes, and never its cursor,
    // its history or a second limit.
    let searched = get(&app, "/console/orders?q=rush&statuses=placed&limit=1", "clerk").await;
    let later = get(&app, &link(&searched.html, "next").unwrap(), "clerk").await;
    assert_eq!(numbers(&later.html), ["A-3"], "{}", later.html);
    let form = size_form(&later.html);
    for want in [
        r#"<input type="hidden" name="q" value="rush">"#,
        r#"<input type="hidden" name="statuses" value="placed">"#,
        r#"<option value="1" selected="">1</option><option value="2">2</option><option value="5">5</option>"#,
        r#"<button type="submit">Show</button>"#,
    ] {
        assert!(form.contains(want), "the size form lacks {want}: {form}");
    }
    assert!(!form.contains(r#"name="cursor""#) && !form.contains(r#"name="back""#), "{form}");
    assert_eq!(form.matches(r#"name="limit""#).count(), 1, "{form}");
    let filter = between(&later.html, r#"<form method="get" class="ss-filter""#, "</form>");
    assert!(!filter.contains(r#"name="cursor""#) && !filter.contains(r#"name="back""#), "{filter}");

    // Its GET, as a browser sends it, is the first page at the new size.
    let resized = get(&app, "/console/orders?q=rush&statuses=placed&limit=5", "clerk").await;
    assert_eq!(numbers(&resized.html), ["A-1", "A-3"], "{}", resized.html);
    assert!(link(&resized.html, "first").is_none() && link(&resized.html, "prev").is_none(), "{}", resized.html);

    // A refused limit is the route's 400 with the first page alone: no size
    // form, nor a link to another page.
    let refused = get(&app, "/console/orders?limit=6", "clerk").await;
    assert_eq!(refused.status, StatusCode::BAD_REQUEST, "{}", refused.html);
    assert!(!refused.html.contains("ss-page-size"), "{}", refused.html);

    // The suppliers' route pages one at a time, up to two; the products'
    // section shows no size form.
    let suppliers = get(&app, "/console/suppliers", "clerk").await;
    assert!(size_form(&suppliers.html).contains(r#"<option value="1" selected="">1</option><option value="2">2</option>"#), "{}", suppliers.html);
    let item = get(&app, &format!("/console/products/{}", wire(&uuid(ANVIL))), "clerk").await;
    let section = between(&item.html, r#"<section class="ss-section" data-resource="reviews">"#, "</section>");
    assert!(!section.contains("ss-page-size") && !section.contains(r#"rel="prev""#), "{section}");
}

#[tokio::test]
async fn a_live_list_goes_back_and_sets_its_size_in_place() {
    let app = app(&seven_orders());
    let first = get(&app, "/console/orders", "clerk").await;
    let second = get(&app, &link(&first.html, "next").unwrap(), "clerk").await;
    let third = link(&second.html, "next").unwrap();
    let query = third.strip_prefix("/console/orders?").unwrap().to_owned();
    let c2 = param(&third, "back").unwrap().to_owned();
    // The shard reads its query's history as the page reads it: its
    // Previous is the page before, and sets the shard's query in place.
    let live = shard(&app, "/console/_live/orders", "clerk", serde_json::json!({"args": [query, "/console/orders"]})).await;
    assert_eq!(live.status, StatusCode::OK, "{}", live.html);
    assert_eq!(numbers(&live.html), ["A-5", "A-6"], "{}", live.html);
    assert_eq!(link(&live.html, "prev"), Some(format!("/console/orders?cursor={c2}")), "{}", live.html);
    let prev = between(&live.html, r#"<a rel="prev""#, "</a>");
    assert!(prev.contains("data-topcoat-on:click="), "{prev}");
    // The size form's submit runs the filter's script over its own form,
    // through its own hidden input, which sets the same signal.
    let form = size_form(&live.html);
    assert!(form.contains(r#"data-ss-live-size="orders""#) && form.contains("data-topcoat-on:submit="), "{form}");
    assert!(form.contains("data-ss-query"), "{form}");
    // A signal from the size form's query renders the first page at its
    // size, under the shard's identity.
    let identity = between(&first.html, r#"::topcoat::shard::start("/console/_live/orders", ""#, "\"");
    let signal = between(&first.html, "::topcoat::signal({&quot;t&quot;:&quot;signal&quot;,&quot;id&quot;:&quot;", "&quot;");
    let body = serde_json::json!({"args": ["", "/console/orders"], "signals": {signal: "limit=5"}});
    let mut request = http::Request::builder().method("POST").uri("/console/_live/orders").header("x-caller", "clerk");
    request = request.header(header::CONTENT_TYPE, "application/json").header("x-topcoat-identity", identity);
    let response = app.handle(request.body(Body::from(body.to_string())).unwrap()).await;
    assert_eq!(response.status(), StatusCode::OK);
    let html = String::from_utf8(to_bytes(response.into_body(), usize::MAX).await.unwrap().to_vec()).unwrap();
    assert_eq!(numbers(&html), ["A-1", "A-2", "A-3", "A-4", "A-5"], "{html}");
    assert!(link(&html, "first").is_none() && link(&html, "prev").is_none(), "{html}");
    assert!(size_form(&html).contains(r#"<option value="5" selected="">5</option>"#), "{html}");
}

// The trail of a page, each step's label, as the layout renders it.
fn trail(html: &str) -> Vec<String> {
    between(html, r#"<nav class="ss-trail" aria-label="Breadcrumb"><ol>"#, "</ol>")
        .split("<li>")
        .skip(1)
        .map(|step| {
            let step = step.trim_end_matches("</li>");
            let text = step.split_once('>').map_or(step, |(_, rest)| rest);
            text.split('<').next().unwrap_or_default().to_owned()
        })
        .collect()
}

#[tokio::test]
async fn a_trail_names_a_supplier_by_what_its_route_refers_to() {
    let app = app(&Store::new());
    // No operation reads a supplier, so its step has no title: the supplier
    // products' supplierId refers to Supplier, and listSuppliers names it.
    let acme = get(&app, "/console/suppliers/acme/products", "clerk").await;
    assert_eq!(acme.status, StatusCode::OK, "{}", acme.html);
    assert_eq!(trail(&acme.html), ["Shop console", "Suppliers", "Acme", "Products"], "{}", acme.html);
    assert!(acme.html.contains(">Anvil</th>") && !acme.html.contains(">Crate</th>"), "{}", acme.html);
    let bolt = get(&app, "/console/suppliers/bolt/products", "clerk").await;
    assert_eq!(trail(&bolt.html), ["Shop console", "Suppliers", "Bolt", "Products"], "{}", bolt.html);
    // Cog is past the first page of listSuppliers, which has no lookup, so
    // its step is its noun and id; so is a supplier no item has.
    let cog = get(&app, "/console/suppliers/cog/products", "clerk").await;
    assert_eq!(cog.status, StatusCode::OK, "{}", cog.html);
    assert_eq!(trail(&cog.html), ["Shop console", "Suppliers", "Supplier cog", "Products"], "{}", cog.html);
    let none = get(&app, "/console/suppliers/nobody/products", "clerk").await;
    assert_eq!(trail(&none.html), ["Shop console", "Suppliers", "Supplier nobody", "Products"], "{}", none.html);
    // An order taker may list a supplier's products but not the suppliers:
    // the step is the supplier's id.
    let taker = get(&app, "/console/suppliers/acme/products", "taker").await;
    assert_eq!(taker.status, StatusCode::OK, "{}", taker.html);
    assert_eq!(trail(&taker.html), ["Shop console", "Suppliers", "Supplier acme", "Products"], "{}", taker.html);
    // A product's step keeps its title, which its detail gives.
    let anvil = wire(&uuid(ANVIL));
    let reviews = get(&app, &format!("/console/products/{anvil}/reviews"), "clerk").await;
    assert_eq!(trail(&reviews.html), ["Shop console", "Products", "Anvil", "Reviews"], "{}", reviews.html);
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

// supplierAppTest is tests/app.rs of the Topcoat crate of the copy of
// fixture-console-api whose Supplier is fixture-console-suppliers-api's.
const supplierAppTest = `use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_console_api_topcoat::api::runtime::page::Page;
use schemas_fixture_console_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_console_api_topcoat::api::{
    types, Implementations, OrderAddLineArgs, OrderGetOrderArgs, OrderImplementation, OrderListOrdersArgs, OrderPlaceOrderArgs,
    ProductCreateProductArgs, ProductGetProductArgs, ProductImplementation, ProductListProductsArgs, ReviewImplementation,
    ReviewListReviewsArgs, SiteImplementation, SiteListPagesArgs, SupplierImplementation, SupplierListSupplierProductsArgs,
    SupplierListSuppliersArgs,
};
use schemas_fixture_console_api_topcoat::pages::Settings;
use schemas_fixture_console_api_topcoat::{PageAuthenticator, RouterBuilderFixtureConsoleApiExt};
use topcoat::context::Cx;
use topcoat::router::{header, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

const ANVIL: &str = "8d1f6c9e-0000-4000-8000-000000000001";
const BUCKET: &str = "8d1f6c9e-0000-4000-8000-000000000002";
const CRATE: &str = "8d1f6c9e-0000-4000-8000-000000000003";

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(serde_json::json!(text)).unwrap()
}

fn product(id: &str, name: &str, supplier: &str) -> types::Product {
    types::Product { id: uuid(id), name: name.to_owned(), sku: name.to_lowercase(), supplier_id: supplier.to_owned(), in_stock: true }
}

// The products, two suppliers, which the dependency's type declares, and
// nothing else.
struct Shop(Mutex<Vec<types::Product>>);

#[async_trait]
impl ProductImplementation for Shop {
    async fn list_products(&self, _ctx: RequestContext, args: ProductListProductsArgs) -> Result<Page<types::Product>, ApiError> {
        let products = self.0.lock().unwrap();
        Ok(Page::last(products.iter().filter(|product| args.ids.as_ref().is_none_or(|ids| ids.contains(&product.id))).cloned().collect()))
    }
    async fn get_product(&self, _ctx: RequestContext, args: ProductGetProductArgs) -> Result<types::Product, ApiError> {
        let products = self.0.lock().unwrap();
        products.iter().find(|product| product.id == args.product_id).cloned().ok_or_else(|| ApiError::not_found("No such product"))
    }
    async fn create_product(&self, _ctx: RequestContext, args: ProductCreateProductArgs) -> Result<types::Product, ApiError> {
        let created = types::Product { name: args.input.name, sku: args.input.sku, supplier_id: args.input.supplier_id, ..product(CRATE, "", "") };
        self.0.lock().unwrap().push(created.clone());
        Ok(created)
    }
}

#[async_trait]
impl SupplierImplementation for Shop {
    async fn list_suppliers(&self, _ctx: RequestContext, _args: SupplierListSuppliersArgs) -> Result<Page<types::Supplier>, ApiError> {
        Ok(Page::last(["Acme", "Bolt"].map(|name| types::Supplier { id: name.to_lowercase(), name: name.to_owned() }).to_vec()))
    }
    async fn list_supplier_products(&self, _ctx: RequestContext, args: SupplierListSupplierProductsArgs) -> Result<Vec<types::Product>, ApiError> {
        let products = self.0.lock().unwrap();
        Ok(products.iter().filter(|product| product.supplier_id == args.supplier_id).cloned().collect())
    }
}

#[async_trait]
impl ReviewImplementation for Shop {
    async fn list_reviews(&self, _ctx: RequestContext, _args: ReviewListReviewsArgs) -> Result<Page<types::Review>, ApiError> {
        Ok(Page::last(Vec::new()))
    }
}

#[async_trait]
impl SiteImplementation for Shop {
    async fn list_pages(&self, _ctx: RequestContext, _args: SiteListPagesArgs) -> Result<Page<types::SitePage>, ApiError> {
        Ok(Page::last(Vec::new()))
    }
}

#[async_trait]
impl OrderImplementation for Shop {
    async fn list_orders(&self, _ctx: RequestContext, _args: OrderListOrdersArgs) -> Result<Page<types::Order>, ApiError> {
        Ok(Page::last(Vec::new()))
    }
    async fn place_order(&self, _ctx: RequestContext, _args: OrderPlaceOrderArgs) -> Result<types::Order, ApiError> {
        Err(ApiError::not_found("No orders here"))
    }
    async fn get_order(&self, _ctx: RequestContext, _args: OrderGetOrderArgs) -> Result<types::Order, ApiError> {
        Err(ApiError::not_found("No orders here"))
    }
    async fn add_line(&self, _ctx: RequestContext, _args: OrderAddLineArgs) -> Result<types::Order, ApiError> {
        Err(ApiError::not_found("No orders here"))
    }
}

struct NoRequests;

#[async_trait]
impl Authenticator for NoRequests {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

// Every page's caller is a clerk, who reads products and suppliers.
struct Clerk;

#[async_trait]
impl PageAuthenticator for Clerk {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(Some(Principal::new("clerk", ["orders", "products.read"])))
    }
}

fn app(shop: &Arc<Shop>) -> Router {
    let implementations = Implementations {
        order: Arc::clone(shop) as _,
        product: Arc::clone(shop) as _,
        review: Arc::clone(shop) as _,
        site: Arc::clone(shop) as _,
        supplier: Arc::clone(shop) as _,
        authenticator: Arc::new(NoRequests),
    };
    Router::builder().discover().app_context(Settings::new().title("Shop console")).fixture_console_api(implementations, Clerk).build()
}

async fn send(router: &Router, method: &str, uri: &str, body: &str) -> (StatusCode, String) {
    let mut request = http::Request::builder().method(method).uri(uri);
    if method == "POST" {
        request = request.header(header::CONTENT_TYPE, "application/x-www-form-urlencoded");
    }
    let response = router.handle(request.body(Body::from(body.to_owned())).unwrap()).await;
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, String::from_utf8(bytes.to_vec()).unwrap())
}

#[tokio::test]
async fn a_reference_to_a_dependencys_type_offers_and_names_its_items() {
    let shop = Arc::new(Shop(Mutex::new(vec![product(ANVIL, "Anvil", "acme"), product(BUCKET, "Bucket", "bolt")])));
    let app = app(&shop);
    // The rows and a product's page name its supplier, the dependency's
    // type, through listSuppliers.
    let (status, list) = send(&app, "GET", "/console/products", "").await;
    assert_eq!(status, StatusCode::OK, "{list}");
    assert!(list.contains(r#"<td data-field="supplierId">Acme</td>"#) && list.contains(r#"<td data-field="supplierId">Bolt</td>"#), "{list}");
    let anvil = serde_json::to_value(uuid(ANVIL)).unwrap().as_str().unwrap().to_owned();
    let (_, item) = send(&app, "GET", &format!("/console/products/{anvil}"), "").await;
    assert!(item.contains("<dd>Acme</dd>"), "{item}");
    // The new product's supplier is a select of the suppliers, by name.
    let (status, new) = send(&app, "GET", "/console/products/new", "").await;
    assert_eq!(status, StatusCode::OK, "{new}");
    assert!(new.contains(r#"<select id="product-input-supplier-id" name="supplierId" required"#), "{new}");
    assert!(new.contains(r#"<option value="acme">Acme</option><option value="bolt">Bolt</option>"#), "{new}");
    let (status, placed) = send(&app, "POST", "/console/products/new", "name=Crate&sku=crate&supplierId=bolt").await;
    assert_eq!(status, StatusCode::SEE_OTHER, "{placed}");
    assert_eq!(shop.0.lock().unwrap().last().map(|product| product.supplier_id.clone()).as_deref(), Some("bolt"));
    // The trail names a supplier through the reference of its products'
    // route.
    let (_, products) = send(&app, "GET", "/console/suppliers/bolt/products", "").await;
    assert!(products.contains("<li><span>Bolt</span></li>"), "{products}");
    assert!(products.contains(">Bucket</th>") && products.contains(">Crate</th>"), "{products}");
}
`

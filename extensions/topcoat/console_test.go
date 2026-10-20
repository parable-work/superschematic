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
// cursor or a history. A limit the route refuses is kept by no link and no
// form, so the 400's filter and First link start again at its default. A limit or cursor the route refuses, and a cursor
// the implementation cannot read, is the route's 400 as the form's own
// message, and a paged list without a filter renders no filter form and
// its refusal alone. The search box is the filter's first control, a search
// form's, labeled Search, and filters by each word in any case, its pages
// carrying it. A reference to Product, whose listProducts searches, is a
// picker (D61, amended) on the new page, the app's own page, an action's
// form and the orders' filter: a search box, a Find after the hidden
// submit and a select of the first page of the matches, from one listing
// a distinct text a request, the value held among them named through the
// lookup. A filter's Find searches by GET, a post's renders the form again
// as sent, 200, every picker's text searched, as a row button's post and
// a refused one do, a removed line's text going with it, Enter still
// submits, a text past the search's maxLength shows the route's message,
// and the shard renders what a typed text finds with the value kept, and
// again for each press of its Find. A unit test of references.rs,
// referencesUnitTest, holds a request to MAX_LOOKUP_CALLS distinct texts
// through listProducts, a pressed Find's past them. A record's
// value is named by its item's label through the lookup, one call a
// render, in chunks of its listMax when more, or from the listing for a
// reference by sku; a caller who may not list the items gets the plain
// control and the value, and the app's choices win and spare the call.
// Products link
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
// list suppliers. A sorted list's headers (D44, amended) link each sort
// field's column, its own order reversed and any other's ascending, mark
// the column of the order with aria-sort, start again at the first page
// without a history, and set a live list's shard's query; the filter, the
// size form, the pager and a procedure keep the sort, a refused sort and
// a cursor of another sort are the route's 400, and a section's table has
// no header links.
func TestTheConsolePagesPicksAndSearches(t *testing.T) {
	cargoTestCrateWith(t, consoleService, consoleAppTest, crateOptions{appends: map[string]string{"src/references.rs": referencesUnitTest}})
}

// referencesUnitTest is appended to fixture-console-api's
// src/references.rs, under cargo test alone: a request searches at most
// MAX_LOOKUP_CALLS distinct texts through listProducts, every call of its
// memo counting together, a text searched already and a pressed Find's
// text past them, and another request counts again (D61, amended). The
// pages' tests cannot post that many texts, since an order holds at most
// three lines and a gift.
const referencesUnitTest = `
#[cfg(test)]
mod searched_tests {
    use super::*;

    #[test]
    fn a_request_searches_at_most_max_lookup_calls_texts_through_one_listing() {
        let cx = Cx::default();
        let searched = product_list_products_searched(&cx);
        for i in 0..MAX_LOOKUP_CALLS {
            assert!(searched.admits(&format!("text {i}"), false), "text {i}");
        }
        assert!(!searched.admits("one too many", false), "a text past the cap");
        assert!(searched.admits("text 0", false), "a text the request searched");
        assert!(searched.admits("pressed", true), "a pressed Find's text");
        assert!(searched.admits("pressed", false), "a text a pressed Find searched");
        // Each picker's call is the request's one memo: the product's
        // target and the product by sku's count together.
        assert!(!product_list_products_searched(&cx).admits("one too many", false));
        // Another request counts again.
        assert!(product_list_products_searched(&Cx::default()).admits("one too many", false));
    }
}
`

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
// fixture-console-api: a paged operation's call names the runtime, a
// sorted one's unbinds its cursor and binds its next (D60, amended), its
// page is a page record, its procedure takes cursor and limit, and a
// sorted one's its sort, its argument form holds the page, the sort among
// it, even for a list without a filter, reads it by the route's rules and
// links its pages by its query pairs, the search argument first; and the
// core's fixture-paged-api, whose tags are a page of strings and whose
// items sort.
func TestAPagedOperationIsItsPageEverywhere(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	for file, wants := range map[string][]string{
		"operations.rs": {
			"use crate::api::runtime;",
			"pub async fn order_list_orders(cx: &Cx, mut args: crate::api::OrderListOrdersArgs) -> Result<runtime::page::Page<types::Order>, ApiError> {",
			"    args.check()?;\n    // The cursor names the sort it was returned under (D60, amended).\n    args.page = crate::api::OrderListOrdersArgs::PAGE.unbind(args.page)?;\n    let request = args.page.clone();\n",
			"        .await?\n        .bind(&request)\n}",
			"pub async fn supplier_list_suppliers(cx: &Cx, args: crate::api::SupplierListSuppliersArgs) -> Result<runtime::page::Page<types::Supplier>, ApiError> {",
		},
		"records.rs": {
			"pub struct OrderPageRecord {\n    pub items: Vec<OrderRecord>,\n    pub next: Option<String>,\n}",
			"impl From<&runtime::page::Page<types::Order>> for OrderPageRecord {",
		},
		"procedures.rs": {
			"    pub cursor: Option<String>,\n    pub limit: Option<i64>,\n    pub sort: Option<String>,\n}",
			".sorted_request(self.cursor.as_deref(), self.limit, self.sort.as_deref())",
			"page: crate::api::SupplierListSuppliersArgs::PAGE.request(self.cursor.as_deref(), self.limit).map_err(ProblemRecord::from)?,",
			"-> topcoat::Result<Result<OrderPageRecord, ProblemRecord>> {",
			"pub struct SupplierListSuppliersArgsRecord {\n    pub cursor: Option<String>,\n    pub limit: Option<i64>,\n}",
		},
		"forms.rs": {
			"pub struct OrderListOrdersArgsForm {\n    pub q: Option<String>,\n    pub statuses: Vec<String>,\n    pub product_id: Option<String>,\n",
			"pub struct SupplierListSuppliersArgsForm {\n    /// The page asked for (D60)",
			"crate::api::OrderListOrdersArgs::PAGE.query(&QueryValues::new(pairs))",
			`let pairs = [("cursor", &self.cursor), ("limit", &self.limit), ("sort", &self.sort)]`,
			"            sort: posted.get(\"sort\").first(),\n",
			"        if let Some(sort) = &self.sort {\n            pairs.push((\"sort\".to_owned(), sort.clone()));\n        }\n        if let Some(limit) = &self.limit {",
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
	if !strings.Contains(paged["operations.rs"], "args.page = crate::api::CatalogListItemsArgs::PAGE.unbind(args.page)?;") {
		t.Error("fixture-paged-api's sorted items' call does not unbind its cursor")
	}
}

// TestTheHeadersSortAList reads fixture-console-api's crate: a sorted
// list's table takes Sorted, whose header links set aria-sort on the
// column the rows are in order of and data-ss-sort on each link, and a
// live list's links set its shard's query; the list computes them from
// its query pairs, the filter keeps the sort as a hidden input, and a
// section's rows take none (D44, amended).
func TestTheHeadersSortAList(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	for file, wants := range map[string][]string{
		"views.rs": {
			"pub struct Sorted {",
			"pub live: Option<Signal<String>>,",
			`<th scope="col" data-field="number" aria-sort=(sorted.aria("number"))>`,
			`Some(live) => <a href=(href.clone()) data-ss-sort="number" @click=$(|e: Event| {`,
			`None => <a href=(href) data-ss-sort="number">"Number"</a>,`,
			`<th scope="col" data-field="status">"Status"</th>`,
			"    #[default] sorted: Sorted,\n) -> topcoat::Result<impl View> {",
		},
		"pages.rs": {
			"fn sort_query(pairs: &[(String, String)], param: &str, value: &str) -> String {",
			`let sorted = sorting(&pairs, &crate::api::ProductListProductsArgs::PAGE, filter.sort.as_deref(), &["name", "sku"], &base, Some(query.clone()));`,
			`<input type="hidden" name="sort" value=(sort)>`,
			"Ok(items) => products_rows(items: items, sorted: sorted),",
			"items: Vec<types::Product>, #[default] sorted: Sorted) -> topcoat::Result<impl View> {",
			"Some(Ok(items)) => products_reviews_rows(items: items),",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(files[file], want) {
				t.Errorf("%s lacks %q", file, want)
			}
		}
	}
	if strings.Contains(files["views.rs"], `data-field="supplier"`) || strings.Contains(files["views.rs"], `data-ss-sort="supplierId"`) {
		t.Error("views.rs sorts by a column no list sorts by")
	}
	// Without live lists, a header's link loads the page.
	static, _ := readCrate(t, registry.DefaultNaming(), consoleService, pagesOutputs())
	if !strings.Contains(static["views.rs"], `Some((href, _)) => <a href=(href) data-ss-sort="number">"Number"</a>,`) || strings.Contains(static["views.rs"], "Signal") {
		t.Error("views.rs without live lists sets a signal from its headers")
	}
}

// TestASortFieldWithoutAColumnIsLogged builds fixture-console-api with
// listOrders sorted by id too, which its table shows no column of: the
// build log names it, and its headers sort by number alone (D44,
// amended).
func TestASortFieldWithoutAColumnIsLogged(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		return strings.Replace(schema, "            sort: [number]\n", "            sort: [number, id]\n", 1)
	})
	root := testpaths.TempDir(t)
	var log strings.Builder
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, livePagesOutputs(), &log); err != nil {
		t.Fatalf("build: %v\n%s", err, log.String())
	}
	if want := "  - topcoat: orders sorts by id, which its table shows no column of\n"; !strings.Contains(log.String(), want) {
		t.Errorf("the build log lacks %q:\n%s", want, log.String())
	}
	data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, consoleService), "src", "pages.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `filter.sort.as_deref(), &["number"], &base`) {
		t.Error("the orders' headers sort by a field their table does not show")
	}
}

// TestReferencesResolveByTheCoreRule reads the references of
// fixture-console-api's crate: a source per target and value, its options
// from the one operation that lists the target, its search from that
// operation's search argument, its labels through the lookup in chunks of
// its listMax, or from the listing for a reference by sku, which no lookup
// refers by; a choices function per form, which takes the form, and a
// labels function per record that holds a reference, Product's among
// them, since a product names its supplier, beside the private functions
// of Product as a target; the pages calling them; and products' rows
// linked through getProduct's reference.
func TestReferencesResolveByTheCoreRule(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	references := files["references.rs"]
	for _, want := range []string{
		"async fn product_list_products_options(cx: &Cx) -> Option<Listed> {",
		"page: PageRequest::first(crate::api::ProductListProductsArgs::PAGE.max_limit()),",
		"async fn product_list_products_by_ids(cx: &Cx, values: &[String]) -> Option<Vec<Value>> {",
		`(!listed.more).then(|| choices_of(&listed.items, "id", Some("name")))`,
		"async fn product_list_products_search(cx: &Cx, text: &str) -> Option<Result<Listed, String>> {",
		`Some(Ok(listed)) => Some((choices_of(&listed.items, "sku", Some("name")), listed.more, capped.then(|| TOO_MANY_SEARCHES.to_owned()))),`,
		"for chunk in values.chunks(5).take(MAX_LOOKUP_CALLS) {",
		"async fn product_by_sku_target_labels(cx: &Cx, values: &BTreeSet<String>) -> Vec<Choice> {",
		"async fn product_target_labels(cx: &Cx, values: &BTreeSet<String>) -> Vec<Choice> {",
		"async fn product_target_choices(cx: &Cx) -> Option<Vec<Choice>> {",
		"pub async fn product_labels(cx: &Cx, records: &[ProductRecord], choices: Choices) -> Choices {",
		"let labels = supplier_target_labels(cx, &values_at(&json, &sites)).await;",
		"pub async fn order_input_form_choices(cx: &Cx, form: &OrderInputForm, choices: Choices) -> Choices {",
		"pub async fn order_list_orders_args_form_choices(cx: &Cx, form: &OrderListOrdersArgsForm, choices: Choices) -> Choices {",
		"pub async fn order_add_line_args_form_choices(cx: &Cx, form: &OrderAddLineArgsForm, choices: Choices) -> Choices {",
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
		`let choices = references::order_input_form_choices(cx, &form, choices_of(cx, "orders")).await;`,
		`let choices = references::order_list_orders_args_form_choices(cx, &filter, choices_of(cx, "orders")).await;`,
		"Some((form, _)) => references::order_add_line_args_form_choices(cx, form, choices.clone()).await,",
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

// TestPickersAreWrittenForSearchingOptions reads fixture-console-api's
// crate, whose listProducts searches (D61, amended): each reference to
// Product is a picker, its source's picks and its shard at
// /_superschematic/<service>/_pick/<target>, whose Find counts its presses
// in a signal the shard reads tracked, so each press renders it again; the
// picks of both of Product's sources count their texts in one memo of
// listProducts' for the request; its form holds the pickers' texts,
// numbered again as a removed row's are, and a post's Find acts on it
// after the hidden submit; a filter's Find sends its query; and the ids
// argument, a query list whose controls share a name, stays a select of
// the options.
func TestPickersAreWrittenForSearchingOptions(t *testing.T) {
	files, _ := readCrate(t, registry.DefaultNaming(), consoleService, livePagesOutputs())
	for file, wants := range map[string][]string{
		"references.rs": {
			`#[shard("/_superschematic/fixture-console-api/_pick/product")]`,
			"pub async fn product_pick(cx: &Cx, control: PickControl, pick: Pick) -> topcoat::Result<impl View> {",
			`#[shard("/_superschematic/fixture-console-api/_pick/product-by-sku")]`,
			"async fn product_target_picks(cx: &Cx, wanted: &[(String, String, bool)]) -> Vec<Option<Pick>> {",
			"let labels = product_target_labels(cx, &missing).await;",
			`aria-label="Search Products" aria-controls=(control.id.clone()) maxlength="50" @input=$(|e: Event| {`,
			"            for (i0, row0) in form.lines.iter().enumerate() {\n                let name = format!(\"lines[{i0}].productId\");",
			`controls.push(("giftSku".to_owned(), form.gift_sku.clone()));`,
			"form.finds.text(name).to_owned(), form.finds.pressed(name))",
			"form.finds.text(name).to_owned(), false)",
			`found = found.with("ids", pairs(&options));`,
			"#[memoize]\nfn product_list_products_searched(cx: &Cx) -> Searched {",
			"let capped = !text.is_empty() && !searched.admits(text, *pressed);",
			"    let presses = signal(cx, || 0_u32);\n    let typed = text.get();\n    let pressed = presses.get() > 0;\n    let held = value.get_untracked();\n    let pick = if pressed || searched.get_untracked() {",
			"formnovalidate=(true) @click=$(|e: Event| {\n                            e.prevent_default();\n                            presses.increment()\n                        })>\"Find\"</button>",
		},
		"choices.rs": {
			"pub struct Pick {",
			"pub fn with_pick(mut self, name: impl Into<String>, pick: Pick) -> Self {",
			"picks: Arc<Vec<(String, Pick)>>,",
		},
		"forms.rs": {
			"pub struct Finds {",
			"use crate::references::{product_by_sku_pick, product_pick};",
			"    pub finds: Finds,\n",
			`finds: Finds::from_posted(posted.get("_find")),`,
			"        if let Some(name) = action.strip_prefix(\"find:\") {\n            self.finds.pressed = Some(name.to_owned());",
			"    fn removed(&mut self, path: &[Segment]) {",
			"        self.finds.removed(&path);\n        Some(path)",
			`Some(pick) => product_pick(control: pick_control(&name, &id, true, errors.invalid(&name), row0.product_id.as_deref(), true), pick: pick.clone()),`,
			`Some(pick) => product_pick(control: pick_control("productId", "order-list-orders-product-id", false, errors.invalid("productId"), form.product_id.as_deref(), false), pick: pick.clone()),`,
		},
		"pages.rs": {
			"let sent = if form.apply_action() {\n        rows_again()",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(files[file], want) {
				t.Errorf("%s lacks %q", file, want)
			}
		}
	}
	// Both of Product's sources count their texts in listProducts' memo,
	// and Find sets no searched signal, which the shard reads untracked.
	references := files["references.rs"]
	if n := strings.Count(references, "    let searched = product_list_products_searched(cx);\n"); n != 2 {
		t.Errorf("references.rs counts listProducts' texts in %d picks, want 2 (product's and product-by-sku's)", n)
	}
	if strings.Contains(references, "searched.set(true)\n                        })>\"Find\"") {
		t.Error("a Find sets the searched signal, which renders nothing again")
	}
	// The products' filter's ids keep their select, and need no hidden
	// submit, nor its form a finds.
	forms := files["forms.rs"]
	products := forms[strings.Index(forms, "pub struct ProductListProductsArgsForm {"):]
	products = products[:strings.Index(products, "\n}\n")]
	if strings.Contains(products, "finds") {
		t.Errorf("the products' filter holds finds:\n%s", products)
	}
}

// TestAPickerNameIsReserved builds fixture-console-api with an order input
// field named finds, which a form with a picker keeps for its pickers'
// texts (D61, amended): the build is refused.
func TestAPickerNameIsReserved(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		return strings.Replace(schema, "      - name: note\n        typeRef: { name: string }\n        validateMaxLength: 200\n", "      - name: note\n        typeRef: { name: string }\n        validateMaxLength: 200\n      - name: finds\n        typeRef: { name: string }\n", 1)
	})
	_, err := buildDir(t, registry.DefaultNaming(), dir, testpaths.TempDir(t), rustOutputs(), nil)
	if err == nil || !strings.Contains(err.Error(), "topcoat: form OrderInputForm: field finds is the form's finds, its pickers' texts (D61, amended)") {
		t.Fatalf("build: %v, want the field named finds refused", err)
	}
}

// TestAPickerMeetsItsOptionsControls builds fixture-console-api with a rate
// limit and a timeout on listProducts: each picker's shard meets them
// through a layer on its path, its limiter the operation's in the app's
// registry, which the setup adds after the pages' (D61, amended).
func TestAPickerMeetsItsOptionsControls(t *testing.T) {
	dir := consoleCopy(t, func(schema string) string {
		return strings.Replace(schema, "        restPath: products\n        permissions: [products.read]\n", "        restPath: products\n        permissions: [products.read]\n        middleware: { rateLimit: 30, timeout: 5 }\n", 1)
	})
	root := testpaths.TempDir(t)
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, livePagesOutputs(), nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	src := filepath.Join(topcoat.Dir(root, consoleService), "src")
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	references, lib := read("references.rs"), read("lib.rs")
	for _, want := range []string{
		"pub(crate) fn route_controls(router: RouterBuilder, limits: &RateLimits) -> RouterBuilder {",
		`path: Path::new("/_superschematic/fixture-console-api/_pick/product"),`,
		"rate_limit: limits.of(&crate::api::operations::PRODUCT_LIST_PRODUCTS),",
		"timeout: Some(Duration::from_secs(5)),",
	} {
		if !strings.Contains(references, want) {
			t.Errorf("references.rs lacks %q", want)
		}
	}
	if !strings.Contains(lib, "references::route_controls(pages::route_controls(procedures::route_controls(router, &rate_limits), &rate_limits), &rate_limits)") {
		t.Errorf("lib.rs adds no picker's controls:\n%s", lib)
	}
	cargoTestCrateWith(t, consoleService, "#[test]\nfn builds() {}\n", crateOptions{outputs: livePagesOutputs(), dir: dir})
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
		// A link and the filter keep only a limit the route admits.
		"let pairs = admitted_pairs(filter.query_pairs(), &crate::api::OrderListOrdersArgs::PAGE);",
		"let limit = admitted_limit(filter.limit.as_deref(), &crate::api::OrderListOrdersArgs::PAGE).map(str::to_owned);",
		"let sent = admitted_limit(sent, page).and_then(|limit| limit.parse::<i64>().ok());",
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
	limiter := strings.Count(controls[:strings.Index(controls, "Controls::default().rate_limiter(limits.of(&crate::api::operations::SUPPLIER_LIST_SUPPLIERS))")], "Controls::default()")
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
	withForm := productForm
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
		"pub async fn product_input_form_choices(cx: &Cx, form: &ProductInputForm, choices: Choices) -> Choices {",
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

// productForm adds to fixture-console-api's schema a new product's form,
// whose supplier refers to Supplier, and the operation that takes it.
func productForm(schema string) string {
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

// hostedCopy is a copy of fixture-console-api whose Supplier is
// fixture-console-suppliers-api's, with a new product's form, and which
// lists no supplier itself: Supplier's options are the dependency's own
// listSuppliers (D61, amended).
func hostedCopy(t *testing.T) string {
	t.Helper()
	return suppliersCopy(t, func(schema string) string {
		start := strings.Index(schema, "      # The suppliers, by name, one to a page")
		end := strings.Index(schema, "      # A supplier's products")
		return productForm(schema[:start] + schema[end:])
	})
}

// bothServices is the [extension.topcoat] table of one app that hosts
// fixture-console-api and fixture-console-suppliers-api.
const bothServices = "services = [\"fixture-console-api\", \"fixture-console-suppliers-api\"]\n"

// suppliersOutputs are fixture-console-suppliers-api's outputs when one app
// hosts it beside fixture-console-api: its Rust API and its Topcoat crate,
// with pages under /suppliers.
func suppliersOutputs() map[string]any {
	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "pages": map[string]any{"enabled": true, "prefix": "/suppliers"}}
	return outputs
}

// TestAReferenceToAnotherAPIsOptions builds a copy of fixture-console-api
// whose Supplier, and its options, are fixture-console-suppliers-api's
// (D61, amended). With the table listing both, the references call the
// dependency's crate in-process, through its operations, its listing, its
// lookup-free labels and its picker's search; the crate depends on it; and
// a page and the picker's shard that call listSuppliers meet its rate
// limit, its limiter taken from the dependency's setup. With one listed,
// with the dependency's config generating no API server, with its server
// in Go, with its config unreadable, and with its crate not built first,
// the reference is left out, and the build log gives the reason; the
// build does not fail.
func TestAReferenceToAnotherAPIsOptions(t *testing.T) {
	dir := hostedCopy(t)
	names, err := registry.ParseNaming([]byte("[extension.topcoat]\n"+bothServices), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	root := testpaths.TempDir(t)
	dep := dependencyDir(dir, suppliersService)
	if _, err := buildIn(t, buildSpec{names: names, dir: dep, root: root, outputs: suppliersOutputs()}); err != nil {
		t.Fatalf("build %s: %v", suppliersService, err)
	}
	var log strings.Builder
	depOutputs := map[string]map[string]any{suppliersService: suppliersOutputs()}
	if _, err := buildIn(t, buildSpec{names: names, dir: dir, root: root, outputs: livePagesOutputs(), log: &log, depOutputs: depOutputs}); err != nil {
		t.Fatalf("build: %v\n%s", err, log.String())
	}
	crate := topcoat.Dir(root, consoleService)
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(crate, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	const hosted = "schemas_fixture_console_suppliers_api_topcoat"
	for file, wants := range map[string][]string{
		"Cargo.toml": {`schemas-fixture-console-suppliers-api-topcoat = { path = "../fixture-console-suppliers-api" }`},
		"src/references.rs": {
			"async fn fixture_console_suppliers_api_supplier_list_suppliers_options(cx: &Cx) -> Option<Listed> {",
			"    let args = " + hosted + "::api::SupplierListSuppliersArgs {",
			"page: " + hosted + "::api::runtime::page::PageRequest::first(" + hosted + "::api::SupplierListSuppliersArgs::PAGE.max_limit()),",
			"let page = " + hosted + "::operations::supplier_list_suppliers(cx, args).await.ok()?;",
			"async fn fixture_console_suppliers_api_supplier_list_suppliers_search(cx: &Cx, text: &str) -> Option<Result<Listed, String>> {",
			"async fn supplier_target_picks(cx: &Cx, wanted: &[(String, String, bool)]) -> Vec<Option<Pick>> {",
			"rate_from: Some(|cx: &Cx| try_app_context::<" + hosted + "::Setup>(cx)?.rate_limits.of(&" + hosted + "::api::operations::SUPPLIER_LIST_SUPPLIERS)),",
		},
		"src/pages.rs": {
			".rate_limiter_from(|cx: &Cx| try_app_context::<" + hosted + "::Setup>(cx)?.rate_limits.of(&" + hosted + "::api::operations::SUPPLIER_LIST_SUPPLIERS))",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(read(file), want) {
				t.Errorf("%s lacks %q", file, want)
			}
		}
	}
	if strings.Contains(log.String(), "fixture-console-suppliers-api.Supplier, whose options") {
		t.Errorf("the build log leaves a hosted reference out:\n%s", log.String())
	}

	reason := func(t *testing.T, table string, depOutputs map[string]map[string]any, buildDep bool) string {
		t.Helper()
		names, err := registry.ParseNaming([]byte("[extension.topcoat]\n"+table), "superschematic.toml")
		if err != nil {
			t.Fatalf("ParseNaming: %v", err)
		}
		root := testpaths.TempDir(t)
		if buildDep {
			if _, err := buildIn(t, buildSpec{names: names, dir: dep, root: root, outputs: suppliersOutputs()}); err != nil {
				t.Fatalf("build %s: %v", suppliersService, err)
			}
		}
		var log strings.Builder
		if _, err := buildIn(t, buildSpec{names: names, dir: dir, root: root, outputs: livePagesOutputs(), log: &log, depOutputs: depOutputs}); err != nil {
			t.Fatalf("build: %v\n%s", err, log.String())
		}
		return log.String()
	}
	goServer := rustOutputs()
	goServer["api"] = map[string]any{"enabled": true, "language": "GO"}
	goServer["types"] = map[string]any{"go": map[string]any{"enabled": true}}
	noServer := map[string]any{"types": map[string]any{"rust": map[string]any{"enabled": true}}}
	for _, tc := range []struct {
		name, table string
		depOutputs  map[string]map[string]any
		buildDep    bool
		want        string
	}{
		{"one listed", "services = [\"fixture-console-api\"]\n", nil, true,
			"[extension.topcoat] services does not list fixture-console-suppliers-api beside fixture-console-api, so no app hosts both\n"},
		{"no API server", bothServices, map[string]map[string]any{suppliersService: noServer}, true,
			"fixture-console-suppliers-api's config generates no API server, so it has no crate\n"},
		{"a Go server", bothServices, map[string]map[string]any{suppliersService: goServer}, true,
			"fixture-console-suppliers-api's API server is not Rust, so it has no crate\n"},
		{"an unreadable config", bothServices, map[string]map[string]any{suppliersService: {"nope": true}}, true,
			"fixture-console-suppliers-api's Rust API could not be read: schema config for fixture-console-suppliers-api: outputs block has unknown key \"nope\""},
		{"no crate built", bothServices, depOutputs, false,
			"fixture-console-suppliers-api's Topcoat crate is not in the output root, which a build of it first writes (build --with-deps)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := reason(t, tc.table, tc.depOutputs, tc.buildDep)
			want := "  - topcoat: Product.supplierId refers to fixture-console-suppliers-api.Supplier, whose options operation fixture-console-suppliers-api's SupplierQueries.listSuppliers has no in-process call: " + tc.want
			if !strings.Contains(log, want) {
				t.Errorf("the build log lacks %q:\n%s", want, log)
			}
		})
	}
}

// TestAnUncalledDependencyIsNotRead builds the copy of fixture-console-api
// whose Supplier is fixture-console-suppliers-api's but whose options are
// its own listSuppliers, with the table listing both and the dependency's
// config unreadable: no reference calls the dependency's crate, so the
// extension never reads its API, and the build neither fails nor logs it
// (D61, amended). The copy whose options are the dependency's then gives
// its reason and builds.
func TestAnUncalledDependencyIsNotRead(t *testing.T) {
	names, err := registry.ParseNaming([]byte("[extension.topcoat]\n"+bothServices), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	broken := map[string]map[string]any{suppliersService: {"nope": true}}
	var log strings.Builder
	dir := suppliersCopy(t, productForm)
	if _, err := buildIn(t, buildSpec{names: names, dir: dir, root: testpaths.TempDir(t), outputs: livePagesOutputs(), log: &log, depOutputs: broken}); err != nil {
		t.Fatalf("build: %v\n%s", err, log.String())
	}
	if strings.Contains(log.String(), "could not be read") || strings.Contains(log.String(), "fixture-console-suppliers-api's SupplierQueries") {
		t.Errorf("the build log reads a dependency no reference calls:\n%s", log.String())
	}
	log.Reset()
	if _, err := buildIn(t, buildSpec{names: names, dir: hostedCopy(t), root: testpaths.TempDir(t), outputs: livePagesOutputs(), log: &log, depOutputs: broken}); err != nil {
		t.Fatalf("build of the hosted copy: %v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "fixture-console-suppliers-api's Rust API could not be read") {
		t.Errorf("the build log lacks the hosted reference's reason:\n%s", log.String())
	}
}

// TestTwoServicesInOneApp builds the crates of fixture-console-suppliers-api
// and of a copy of fixture-console-api whose Supplier's options are its,
// with the table listing both, and runs cargo clippy and cargo test with
// hostedAppTest (D61, amended): one app sets up the console with its JSON
// API and the suppliers without one, `_without_api`, and serves both
// crates' pages; a product names its supplier and a new product picks one
// through the suppliers' crate; a page meets listSuppliers' rate limit; an
// app that does not set the suppliers up names a supplier by its id, and
// its call is the 500; and an app that mounts both JSON APIs is refused as
// it builds, by Topcoat's "duplicate any-method route", the reason for
// `_without_api`.
func TestTwoServicesInOneApp(t *testing.T) {
	cargoTestCrateWith(t, consoleService, hostedAppTest, crateOptions{
		outputs:      livePagesOutputs(),
		dir:          hostedCopy(t),
		dependencies: []string{suppliersService},
		depOutputs:   map[string]map[string]any{suppliersService: suppliersOutputs()},
		table:        bothServices,
	})
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
use schemas_fixture_console_api_topcoat::api::runtime::page::{Cursor, Page, PageRequest, Sort};
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

#[path = "support/enter.rs"]
mod enter;

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
    page: &PageRequest,
    key: impl Fn(&T) -> K,
) -> Result<Page<T>, ApiError> {
    let after: Option<K> = page.cursor.as_ref().map(Cursor::decode).transpose()?;
    rows.sort_by_key(|row| key(row));
    rows.retain(|row| after.as_ref().is_none_or(|after| key(row) > *after));
    Page::from_rows(rows, page.limit, key)
}

// The rows of a sorted list (D60, amended) after the cursor, in the page's
// order: the sort's field's text, then the row's id, either way, as key
// gives them. The call fills the default sort in and hands the
// implementation its own cursor, which a bound one would not decode as.
fn sorted_page<T>(mut rows: Vec<T>, page: &PageRequest, key: impl Fn(&T, &str) -> (String, String)) -> Result<Page<T>, ApiError> {
    let sort = page.sort.clone().expect("the call fills the default sort in");
    let field = sort.field().to_owned();
    let after: Option<(String, String)> = page.cursor.as_ref().map(Cursor::decode).transpose()?;
    rows.sort_by_key(|row| key(row, &field));
    if sort.desc() {
        rows.reverse();
    }
    rows.retain(|row| after.as_ref().is_none_or(|after| if sort.desc() { key(row, &field) < *after } else { key(row, &field) > *after }));
    Page::from_rows(rows, page.limit, |row| key(row, &field))
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
        sorted_page(rows, &args.page, |product, field| (if field == "sku" { product.sku.clone() } else { product.name.clone() }, wire(&product.id)))
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
        sorted_page(rows, &args.page, |review, field| (if field == "rating" { format!("{:08.2}", review.rating) } else { review.title.clone() }, review.id.clone()))
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
        // The orders sort by their numbers, which no two share, either way:
        // the key is the number alone.
        let desc = args.page.sort.as_ref().is_some_and(|sort| sort.desc());
        let mut page = PageRequest { sort: None, ..args.page };
        if desc {
            let after: Option<String> = page.cursor.take().map(|cursor| cursor.decode()).transpose()?;
            let mut rows: Vec<types::Order> = rows.into_iter().filter(|order| after.as_ref().is_none_or(|after| order.number < *after)).collect();
            rows.sort_by(|a, b| b.number.cmp(&a.number));
            return Page::from_rows(rows, page.limit, |order| order.number.clone());
        }
        page_of(rows, &page, |order| order.number.clone())
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
// product a picker from the references, as the console's new page has it.
#[page("/app/new-order")]
async fn app_new_order(cx: &Cx) -> topcoat::Result<impl View> {
    let form = OrderInputForm::new();
    let choices = references::order_input_form_choices(cx, &form, Choices::new()).await;
    Ok(view! {
        <form method="post">
            order_input_fields(form: form, errors: FormErrors::default(), choices: choices)
            <button type="submit">"Place the order"</button>
        </form>
    })
}

// The orders' procedure, from a page: its page record's numbers and next.
#[page("/app/orders-procedure")]
async fn app_orders_procedure(cx: &Cx) -> topcoat::Result<impl View> {
    let query = parts(cx).uri.query().unwrap_or_default().to_owned();
    let cursor = query.strip_prefix("cursor=").map(str::to_owned);
    let limit = query.strip_prefix("limit=").map_or(Some(3), |limit| limit.parse().ok());
    let sort = query.strip_prefix("sort=").map(str::to_owned);
    let args = OrderListOrdersArgsRecord { statuses: None, product_id: None, q: None, cursor, limit, sort };
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
    let (status, headers) = (response.status(), response.headers().clone());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    Answer { status, html }
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

// The text of the picker of the control named name: its shard's markup,
// from the picker's start to its end.
fn picker<'a>(html: &'a str, name: &str) -> &'a str {
    between(html, &format!(r#"<div class="ss-pick" data-ss-pick="{name}">"#), "</div>")
}

// The options of a picker's select, each its value, its label and whether
// it is selected.
fn picked(picker: &str) -> Vec<(String, String, bool)> {
    between(picker, "<select", "</select>")
        .split("<option value=\"")
        .skip(1)
        .map(|option| {
            let (value, rest) = option.split_once('"').unwrap();
            let label = rest.split_once('>').unwrap().1.trim_end_matches("</option>").to_owned();
            (value.to_owned(), label, rest.starts_with(r#" selected="""#))
        })
        .filter(|(value, _, _)| !value.is_empty())
        .collect()
}

// A picker's options as their labels.
fn labels(picker: &str) -> Vec<String> {
    picked(picker).into_iter().map(|(_, label, _)| label).collect()
}

// The option a picker's select holds selected, by its value.
fn chosen(picker: &str) -> Option<String> {
    picked(picker).into_iter().find(|(_, _, selected)| *selected).map(|(value, _, _)| value)
}

#[tokio::test]
async fn a_searched_reference_is_a_picker_and_a_value_is_named_by_its_label() {
    let store = Store::new();
    let app = app(&store);
    // The new order's line and its gift are pickers: listProducts searches,
    // so each is a search box, a Find and a select of the first page of
    // the products, with a note that more match, from one listing.
    let new = get(&app, "/console/orders/new", "clerk").await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.html);
    let line = picker(&new.html, "lines[0].productId");
    for want in [
        r#"<input type="search" name="_find.lines[0].productId" value="" aria-label="Search Products" aria-controls="order-input-lines-0-product-id" maxlength="50""#,
        r#"<button type="submit" name="_action" value="find:lines[0].productId" formnovalidate="""#,
        r#"<select id="order-input-lines-0-product-id" name="lines[0].productId" required="""#,
        r#"<p class="ss-pick-more">More products match: search for more of the text.</p>"#,
    ] {
        assert!(line.contains(want), "the line's picker lacks {want}: {line}");
    }
    assert_eq!(labels(line), ["Anvil", "Bucket"], "{line}");
    let anvil = wire(&uuid(ANVIL));
    assert_eq!(picked(line)[0].0, anvil, "{line}");
    let gift = picker(&new.html, "giftSku");
    assert!(gift.contains(r#"name="_find.giftSku""#) && gift.contains(r#"value="find:giftSku""#), "{gift}");
    assert_eq!(picked(gift), [("anvil".to_owned(), "Anvil".to_owned(), false), ("bucket".to_owned(), "Bucket".to_owned(), false)], "{gift}");
    // The label stays the control's own, and Enter presses the hidden
    // submit ahead of every Find.
    assert!(new.html.contains(r#"<label for="order-input-lines-0-product-id">Product</label>"#), "{}", new.html);
    let form = between(&new.html, r#"<form method="post" class="ss-form">"#, "</form>");
    assert!(form.starts_with(r#"<button type="submit" class="ss-submit" hidden=""></button>"#), "{form}");
    assert_eq!(store.calls(), (1, vec![]), "one listing for the new page's two pickers");
    // The app's own page takes the same choices.
    let own = get(&app, "/app/new-order", "clerk").await;
    assert_eq!(labels(picker(&own.html, "lines[0].productId")), ["Anvil", "Bucket"], "{}", own.html);
    store.calls();

    // An order's page names its lines' products through the lookup and its
    // gift from the listing, and its action's product is a picker.
    let item = get(&app, &format!("/console/orders/{}", wire(&uuid(FIRST))), "clerk").await;
    assert_eq!(item.status, StatusCode::OK, "{}", item.html);
    assert!(item.html.contains(r#"<td data-field="productId">Anvil</td>"#), "{}", item.html);
    assert!(item.html.contains(r#"<td data-field="productId">Crate</td>"#), "{}", item.html);
    assert!(item.html.contains(r#"<dd>Bucket</dd>"#), "the gift by its name: {}", item.html);
    let action = picker(&item.html, "productId");
    assert!(action.contains(r#"<select id="order-add-line-product-id" name="productId" required="""#), "{action}");
    let (listings, lookups) = store.calls();
    assert_eq!(listings, 2, "the gift's listing and the action's first page");
    assert_eq!(lookups, vec![vec![anvil.clone(), wire(&uuid(CRATE))]], "one lookup of the lines' products");

    // The list names each row's gift; its filter's product is a picker.
    let list = get(&app, "/console/orders", "clerk").await;
    assert!(list.html.contains(r#"<td data-field="giftSku">Bucket</td>"#), "{}", list.html);
    let filter = picker(&list.html, "productId");
    assert!(filter.contains(r#"<select id="order-list-orders-product-id" name="productId""#), "{filter}");
    // A filter's Find sends its query: it names no action.
    assert!(filter.contains(r#"<button type="submit" formnovalidate="""#) && !filter.contains("_action"), "{filter}");
    let (listings, lookups) = store.calls();
    assert_eq!(listings, 2, "the rows' gifts' listing and the filter's first page");
    assert_eq!(lookups.len(), 1, "one lookup for the rows' lines: {lookups:?}");

    // A post through the select places the order with the product's id.
    let placed = post(&app, "/console/orders/new", "clerk", &format!("lines[0].productId={anvil}&lines[0].quantity=2&giftSku=drum")).await;
    assert_eq!(placed.status, StatusCode::SEE_OTHER, "{}", placed.html);
    let order = store.orders.lock().unwrap().last().cloned().unwrap();
    assert_eq!((order.lines[0].product_id, order.gift_sku.as_deref()), (uuid(ANVIL), Some("drum")));
}

#[tokio::test]
async fn a_picker_finds_by_get_and_by_post() {
    let store = Store::new();
    let app = app(&store);
    // A filter's picker searches the text its query sends, beside the
    // list, which the product held filters.
    let crate_id = wire(&uuid(CRATE));
    let found = get(&app, &format!("/console/orders?productId={crate_id}&_find.productId=cr"), "clerk").await;
    assert_eq!(found.status, StatusCode::OK, "{}", found.html);
    assert_eq!(numbers(&found.html), ["A-1", "A-5"], "{}", found.html);
    let filter = picker(&found.html, "productId");
    assert!(filter.contains(r#"name="_find.productId" value="cr""#), "{filter}");
    assert_eq!(labels(filter), ["Crate"], "{filter}");
    assert_eq!(chosen(filter), Some(crate_id.clone()), "{filter}");
    assert!(!filter.contains("ss-pick-more"), "{filter}");
    // The pager's links keep the filter but not the search's text.
    let next = link(&get(&app, "/console/orders?_find.productId=cr", "clerk").await.html, "next").unwrap();
    assert!(!next.contains("_find"), "{next}");
    store.calls();

    // A post's Find renders the form again as it was sent, 200, without
    // the operation: every picker's text searched, the pressed one's and
    // the other's, so each box says what its select lists.
    let before = store.orders.lock().unwrap().len();
    let body = "lines[0].productId=&lines[0].quantity=2&note=soon&giftSku=&_find.lines[0].productId=dr&_find.giftSku=an&_action=find:lines[0].productId";
    let again = post(&app, "/console/orders/new", "clerk", body).await;
    assert_eq!(again.status, StatusCode::OK, "{}", again.html);
    assert_eq!(store.orders.lock().unwrap().len(), before, "a Find places no order");
    let line = picker(&again.html, "lines[0].productId");
    assert!(line.contains(r#"value="dr""#), "{line}");
    assert_eq!(labels(line), ["Drum"], "{line}");
    let gift = picker(&again.html, "giftSku");
    assert!(gift.contains(r#"name="_find.giftSku" value="an""#), "{gift}");
    assert_eq!(labels(gift), ["Anvil"], "{gift}");
    assert!(again.html.contains(r#"name="lines[0].quantity" type="number""#) && again.html.contains(r#"value="2""#), "{}", again.html);
    assert!(again.html.contains(r#"value="soon""#), "{}", again.html);
    assert!(!again.html.contains("form-error"), "{}", again.html);
    assert_eq!(store.calls(), (2, vec![]), "a listing of each text");

    // A row button's post, and a refused one, search each text too: a
    // line's box that held dr when the reader added a line still lists
    // what dr finds, beside the new line's first page.
    let added = post(&app, "/console/orders/new", "clerk", "lines[0].productId=&lines[0].quantity=2&_find.lines[0].productId=dr&_action=add:lines").await;
    assert_eq!(added.status, StatusCode::OK, "{}", added.html);
    let line = picker(&added.html, "lines[0].productId");
    assert!(line.contains(r#"name="_find.lines[0].productId" value="dr""#), "{line}");
    assert_eq!(labels(line), ["Drum"], "{line}");
    assert_eq!(labels(picker(&added.html, "lines[1].productId")), ["Anvil", "Bucket"], "{}", added.html);
    store.calls();
    let refused = post(&app, "/console/orders/new", "clerk", "lines[0].productId=&lines[0].quantity=2&_find.lines[0].productId=dr").await;
    assert_eq!(refused.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", refused.html);
    assert_eq!(labels(picker(&refused.html, "lines[0].productId")), ["Drum"], "{}", refused.html);
    assert_eq!(store.calls().0, 2, "the line's text and the gift's first page");

    // The value held past the first page is an option, named through the
    // lookup, and selected.
    let drum = wire(&uuid(DRUM));
    let body = format!("lines[0].productId={drum}&lines[0].quantity=1&_find.giftSku=bu&_action=find:giftSku");
    let held = post(&app, "/console/orders/new", "clerk", &body).await;
    assert_eq!(held.status, StatusCode::OK, "{}", held.html);
    let line = picker(&held.html, "lines[0].productId");
    assert_eq!(labels(line), ["Drum", "Anvil", "Bucket"], "{line}");
    assert_eq!(chosen(line), Some(drum.clone()), "{line}");
    assert_eq!(labels(picker(&held.html, "giftSku")), ["Bucket"], "{}", held.html);
    assert_eq!(store.calls(), (2, vec![vec![drum.clone()]]), "two listings and one lookup of the value held");

    // Enter presses the hidden submit, which names no action: the form is
    // placed.
    let placed = post(&app, "/console/orders/new", "clerk", &format!("lines[0].productId={drum}&lines[0].quantity=1&_find.lines[0].productId=dr")).await;
    assert_eq!(placed.status, StatusCode::SEE_OTHER, "{}", placed.html);
    assert_eq!(store.orders.lock().unwrap().len(), before + 1);

    // A text past the search argument's maxLength shows the route's
    // message, and no matches.
    let long = get(&app, &format!("/console/orders?_find.productId={}", "x".repeat(51)), "clerk").await;
    assert_eq!(long.status, StatusCode::OK, "{}", long.html);
    let refused = picker(&long.html, "productId");
    assert!(refused.contains(r#"<p class="field-error">Invalid query parameter q"#), "{refused}");
    assert!(labels(refused).is_empty(), "{refused}");
}

#[tokio::test]
async fn a_removed_lines_search_text_goes_with_it() {
    let store = Store::new();
    let app = app(&store);
    // Removing the first line moves the second up with its box's text: it
    // still says dr and lists what dr finds, and an is gone with its line.
    let body = "lines[0].productId=&lines[0].quantity=1&lines[1].productId=&lines[1].quantity=2&_find.lines[0].productId=an&_find.lines[1].productId=dr&_action=remove:lines[0]";
    let removed = post(&app, "/console/orders/new", "clerk", body).await;
    assert_eq!(removed.status, StatusCode::OK, "{}", removed.html);
    let line = picker(&removed.html, "lines[0].productId");
    assert!(line.contains(r#"name="_find.lines[0].productId" value="dr""#), "{line}");
    assert_eq!(labels(line), ["Drum"], "{line}");
    assert!(removed.html.contains(r#"name="lines[0].quantity" type="number""#) && removed.html.contains(r#"value="2""#), "{}", removed.html);
    assert!(!removed.html.contains(r#"data-ss-pick="lines[1].productId""#), "{}", removed.html);
    // A line its list's listMin keeps stays, and so does its text.
    let kept = post(&app, "/console/orders/new", "clerk", "lines[0].productId=&lines[0].quantity=1&_find.lines[0].productId=an&_action=remove:lines[0]").await;
    assert_eq!(kept.status, StatusCode::OK, "{}", kept.html);
    let line = picker(&kept.html, "lines[0].productId");
    assert!(line.contains(r#"value="an""#), "{line}");
    assert_eq!(labels(line), ["Anvil"], "{line}");
}

#[tokio::test]
async fn a_pickers_shard_renders_what_the_text_finds() {
    let store = Store::new();
    let app = app(&store);
    let new = get(&app, "/console/orders/new", "clerk").await;
    let line = picker(&new.html, "lines[0].productId");
    // The box's input sets the shard's searched and text signals, the
    // select's change its value signal.
    let identity = between(&new.html, r#"::topcoat::shard::start("/_superschematic/fixture-console-api/_pick/product", ""#, "\"");
    let ids = |markup: &str| -> Vec<String> {
        markup.split("&quot;Signal&quot;,&quot;id&quot;:&quot;").skip(1).map(|rest| rest.split("&quot;").next().unwrap().to_owned()).collect()
    };
    let input = between(line, r#"<input type="search""#, "<button");
    let signals = ids(input);
    assert_eq!(signals.len(), 2, "{line}");
    // Find's click counts its presses in a signal of its own, which the
    // shard reads tracked: each press renders it again, the text unchanged
    // too.
    let find = between(line, r#"value="find:lines[0].productId""#, "</button>");
    let presses = ids(find);
    assert_eq!(presses.len(), 1, "{find}");
    assert!(!signals.contains(&presses[0]), "{line}");
    assert!(new.html.contains(&format!("::topcoat::dep(\"{}\")", presses[0])), "the shard does not depend on Find's presses: {}", new.html);
    store.calls();
    // The reader typed dr with Bucket held: the shard lists Drum, and
    // Bucket, named through the lookup, stays selected.
    let bucket = wire(&uuid(BUCKET));
    // The arguments as the runtime sends them back, records, a list and an
    // option tagged as the shard's start marker writes them.
    let record = |fields: serde_json::Value| serde_json::json!({"t": "Record", "v": fields});
    let control = record(serde_json::json!({"name": "lines[0].productId", "id": "order-input-lines-0-product-id", "required": true, "invalid": false, "value": bucket, "post": true}));
    let pick = |text: &str, options: Vec<serde_json::Value>, more: bool| {
        record(serde_json::json!({"text": text, "options": {"t": "Vec", "bits": 64, "v": options}, "more": more, "refused": {"t": "Option", "v": null}}))
    };
    let body = serde_json::json!({"args": [control, pick("", Vec::new(), false)], "signals": {signals[0].clone(): true, signals[1].clone(): "dr"}});
    let mut request = http::Request::builder().method("POST").uri("/_superschematic/fixture-console-api/_pick/product").header("x-caller", "clerk");
    request = request.header(header::CONTENT_TYPE, "application/json").header("x-topcoat-identity", identity);
    let response = app.handle(request.body(Body::from(body.to_string())).unwrap()).await;
    assert_eq!(response.status(), StatusCode::OK);
    let html = String::from_utf8(to_bytes(response.into_body(), usize::MAX).await.unwrap().to_vec()).unwrap();
    let shard = picker(&html, "lines[0].productId");
    assert!(shard.contains(r#"name="_find.lines[0].productId" value="dr""#), "{shard}");
    assert_eq!(labels(shard), ["Bucket", "Drum"], "{shard}");
    assert_eq!(chosen(shard), Some(bucket.clone()), "{shard}");
    assert_eq!(store.calls(), (1, vec![vec![bucket.clone()]]), "one listing of the text, one lookup of the value");
    // A press of Find with the text as the page rendered it, after a post
    // say, searches it: the pick it was given is stale.
    let press = serde_json::json!({"t": "u32", "bits": 32, "v": "1"});
    let stale = pick("dr", vec![record(serde_json::json!({"value": "v", "label": "V"}))], true);
    let body = serde_json::json!({"args": [control, stale], "signals": {signals[0].clone(): false, signals[1].clone(): "dr", presses[0].clone(): press}});
    let mut request = http::Request::builder().method("POST").uri("/_superschematic/fixture-console-api/_pick/product").header("x-caller", "clerk");
    request = request.header(header::CONTENT_TYPE, "application/json").header("x-topcoat-identity", identity);
    let response = app.handle(request.body(Body::from(body.to_string())).unwrap()).await;
    assert_eq!(response.status(), StatusCode::OK);
    let html = String::from_utf8(to_bytes(response.into_body(), usize::MAX).await.unwrap().to_vec()).unwrap();
    assert_eq!(labels(picker(&html, "lines[0].productId")), ["Bucket", "Drum"], "{html}");
    assert_eq!(store.calls(), (1, vec![vec![bucket]]), "the text searched again");
    // Without the searched signal the shard renders the pick it was given.
    let body = serde_json::json!({"args": [control, pick("x", vec![record(serde_json::json!({"value": "v", "label": "V"}))], true)]});
    let response = app.handle(
        http::Request::builder()
            .method("POST")
            .uri("/_superschematic/fixture-console-api/_pick/product")
            .header("x-caller", "clerk")
            .header(header::CONTENT_TYPE, "application/json")
            .body(Body::from(body.to_string()))
            .unwrap(),
    ).await;
    let html = String::from_utf8(to_bytes(response.into_body(), usize::MAX).await.unwrap().to_vec()).unwrap();
    assert_eq!(labels(picker(&html, "lines[0].productId")), ["V"], "{html}");
    assert_eq!(store.calls(), (0, vec![]));
}

#[tokio::test]
async fn a_caller_who_may_not_list_the_items_gets_the_plain_control_and_the_value() {
    let store = Store::new();
    let app = app(&store);
    let new = get(&app, "/console/orders/new", "taker").await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.html);
    assert!(new.html.contains(r#"<input id="order-input-lines-0-product-id" name="lines[0].productId" type="text" required"#), "{}", new.html);
    assert!(!new.html.contains("ss-pick"), "{}", new.html);
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
    assert!(!new.html.contains(r#"data-ss-pick="lines[0].productId""#), "{}", new.html);
    assert_eq!(labels(picker(&new.html, "giftSku")), ["Anvil", "Bucket"], "{}", new.html);
    assert_eq!(store.calls().0, 1, "the gift's listing alone");
    let item = get(&app, &format!("/console/orders/{}", wire(&uuid(FIRST))), "clerk").await;
    assert!(item.html.contains(r#"<td data-field="productId">The anvil</td>"#), "{}", item.html);
    assert_eq!(store.calls().1, Vec::<Vec<String>>::new(), "no lookup for a path the app names");
}

#[tokio::test]
async fn more_items_than_a_page_are_picked_and_named_in_chunks() {
    let store = Store::new();
    store.products.lock().unwrap().extend([product(EASEL, "Easel"), product(FUNNEL, "Funnel")]);
    let all = [ANVIL, BUCKET, CRATE, DRUM, EASEL, FUNNEL];
    store.orders.lock().unwrap().push(order(9, types::OrderStatus::Placed, None, None, all.iter().map(|id| line(id, 1.0)).collect()));
    let app = app(&store);
    // A picker however many items there are.
    let new = get(&app, "/console/orders/new", "clerk").await;
    let line = picker(&new.html, "lines[0].productId");
    assert_eq!(labels(line), ["Anvil", "Bucket"], "{line}");
    assert!(line.contains("ss-pick-more"), "{line}");
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
    let headers = response.headers().clone();
    let html = String::from_utf8(to_bytes(response.into_body(), usize::MAX).await.unwrap().to_vec()).unwrap();
    enter::check_page("/console/_live/orders", &headers, &html);
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
    // The record's sort orders the page, and a sort the route does not
    // offer is its 400 (D60, amended).
    let reversed = get(&app, "/app/orders-procedure?sort=-number", "clerk").await;
    assert_eq!(between(&reversed.html, r#"<p id="said">"#, "</p>").split_once(" next=").unwrap().0, "A-5,A-4,A-3");
    let refused = get(&app, "/app/orders-procedure?sort=status", "clerk").await;
    assert_eq!(between(&refused.html, r#"<p id="said">"#, "</p>"), "400 Invalid query parameter sort: expected one of number, -number");
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
    // A cursor of 1024 characters, the most a route reads, after A-1 and
    // bound to the orders' default sort (D60, amended), and entries of 1024
    // characters, which are cursors by the rule though no page returned
    // them.
    let long = Cursor::encode(&format!("A-1{}", "x".repeat(739))).unwrap().bind(&Sort::parse("number").unwrap()).unwrap();
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

    // A refused limit is the route's 400, with no cursor sent: no size form,
    // nor a link to another page.
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
async fn a_refused_limit_is_kept_by_no_link_or_form() {
    let app = app(&seven_orders());
    let next = link(&get(&app, "/console/orders?statuses=placed", "clerk").await.html, "next").unwrap();
    let cursor = param(&next, "cursor").unwrap().to_owned();
    for limit in ["6", "0", "two"] {
        // The route's 400, whose filter holds no limit, so its submit starts
        // at the route's default, not at the 400 again.
        let refused = get(&app, &format!("/console/orders?statuses=placed&limit={limit}"), "clerk").await;
        assert_eq!(refused.status, StatusCode::BAD_REQUEST, "limit={limit}: {}", refused.html);
        let filter = between(&refused.html, r#"<form method="get" class="ss-filter""#, "</form>");
        assert!(!filter.contains(r#"name="limit""#), "limit={limit}: {filter}");
        assert!(link(&refused.html, "first").is_none(), "limit={limit}: {}", refused.html);
        // With a cursor, the First link keeps the filter and drops the
        // limit, so it is a page and not the 400.
        let later = get(&app, &format!("/console/orders?statuses=placed&limit={limit}&cursor={cursor}"), "clerk").await;
        assert_eq!(later.status, StatusCode::BAD_REQUEST, "limit={limit}: {}", later.html);
        let first = link(&later.html, "first").unwrap();
        assert_eq!(first, "/console/orders?statuses=placed", "limit={limit}");
        let filter = between(&later.html, r#"<form method="get" class="ss-filter""#, "</form>");
        assert!(!filter.contains(r#"name="limit""#), "limit={limit}: {filter}");
        assert_eq!(get(&app, &first, "clerk").await.status, StatusCode::OK, "limit={limit}");
    }
    // A limit the route admits stays, in the First link and the filter.
    let kept = get(&app, &format!("/console/orders?statuses=placed&limit=5&cursor={cursor}"), "clerk").await;
    assert_eq!(kept.status, StatusCode::OK, "{}", kept.html);
    assert_eq!(link(&kept.html, "first").as_deref(), Some("/console/orders?statuses=placed&limit=5"), "{}", kept.html);
    let filter = between(&kept.html, r#"<form method="get" class="ss-filter""#, "</form>");
    assert!(filter.contains(r#"<input type="hidden" name="limit" value="5">"#), "{filter}");
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
    let headers = response.headers().clone();
    let html = String::from_utf8(to_bytes(response.into_body(), usize::MAX).await.unwrap().to_vec()).unwrap();
    enter::check_page("/console/_live/orders", &headers, &html);
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

// The header cell of a table's column, from its start tag to its end.
fn header<'a>(html: &'a str, field: &str) -> &'a str {
    between(html, &format!(r#"<th scope="col" data-field="{field}""#), "</th>")
}

// The href of a header's sort link, unescaped, or none.
fn sort_link(html: &str, field: &str) -> Option<String> {
    let cell = header(html, field);
    cell.contains("<a href=").then(|| between(cell, r#"<a href=""#, "\"").replace("&amp;", "&"))
}

// The names of the products a page's table shows, in order.
fn names(html: &str) -> Vec<String> {
    html.split(r#"<th scope="row" data-field="name">"#)
        .skip(1)
        .map(|cell| {
            let cell = cell.split("</th>").next().unwrap();
            cell.rsplit_once("\">").map_or(cell, |(_, text)| text).trim_end_matches("</a>").to_owned()
        })
        .collect()
}

#[tokio::test]
async fn the_headers_sort_the_list() {
    let app = app(&Store::new());
    // The column of the default order says so, and its link reverses it;
    // the other sort field's sorts by it ascending; a column that is no
    // sort field keeps its label.
    let list = get(&app, "/console/products", "clerk").await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    assert_eq!(names(&list.html), ["Anvil", "Bucket"], "{}", list.html);
    assert!(header(&list.html, "name").contains(r#"aria-sort="ascending""#), "{}", list.html);
    assert!(header(&list.html, "name").contains(r#"data-ss-sort="name""#), "{}", list.html);
    assert_eq!(sort_link(&list.html, "name").as_deref(), Some("/console/products?sort=-name"));
    assert!(!header(&list.html, "sku").contains("aria-sort"), "{}", list.html);
    assert_eq!(sort_link(&list.html, "sku").as_deref(), Some("/console/products?sort=sku"));
    assert_eq!(sort_link(&list.html, "supplierId"), None, "{}", list.html);
    assert!(!header(&list.html, "supplierId").contains("aria-sort"), "{}", list.html);

    // The reversed order: its column descends, its link sorts ascending,
    // and the pages after it keep the order.
    let reversed = get(&app, "/console/products?sort=-name", "clerk").await;
    assert_eq!(names(&reversed.html), ["Drum", "Crate"], "{}", reversed.html);
    assert!(header(&reversed.html, "name").contains(r#"aria-sort="descending""#), "{}", reversed.html);
    assert_eq!(sort_link(&reversed.html, "name").as_deref(), Some("/console/products?sort=name"));
    let next = link(&reversed.html, "next").unwrap();
    assert!(next.starts_with("/console/products?sort=-name&cursor="), "{next}");
    let second = get(&app, &next, "clerk").await;
    assert_eq!(second.status, StatusCode::OK, "{}", second.html);
    assert_eq!(names(&second.html), ["Bucket", "Anvil"], "{}", second.html);
    assert_eq!(link(&second.html, "first").as_deref(), Some("/console/products?sort=-name"), "{}", second.html);
    // A header's link starts again at the first page: no cursor, no back.
    let resort = sort_link(&second.html, "sku").unwrap();
    assert_eq!(resort, "/console/products?sort=sku", "{}", second.html);
    assert_eq!(names(&get(&app, &resort, "clerk").await.html), ["Anvil", "Bucket"]);

    // The filter holds the sort as a hidden input, and the size form and
    // the pager keep it, beside the search and the limit.
    let searched = get(&app, "/console/products?q=r&sort=-sku&limit=1", "clerk").await;
    assert_eq!(names(&searched.html), ["Drum"], "{}", searched.html);
    let filter = between(&searched.html, r#"<form method="get" class="ss-filter""#, "</form>");
    assert!(filter.contains(r#"<input type="hidden" name="sort" value="-sku"><input type="hidden" name="limit" value="1">"#), "{filter}");
    assert!(size_form(&searched.html).contains(r#"<input type="hidden" name="sort" value="-sku">"#), "{}", searched.html);
    let next = link(&searched.html, "next").unwrap();
    assert!(next.starts_with("/console/products?q=r&sort=-sku&limit=1&cursor="), "{next}");
    assert_eq!(sort_link(&searched.html, "name").as_deref(), Some("/console/products?q=r&sort=name&limit=1"));

    // A sort the route does not offer is its 400, the form's own message,
    // and no link or form keeps it.
    let refused = get(&app, "/console/products?sort=price&limit=1", "clerk").await;
    assert_eq!(refused.status, StatusCode::BAD_REQUEST, "{}", refused.html);
    assert!(refused.html.contains(r#"<p class="form-error">sort: expected one of name, -name, sku, -sku</p>"#), "{}", refused.html);
    let filter = between(&refused.html, r#"<form method="get" class="ss-filter""#, "</form>");
    assert!(!filter.contains(r#"name="sort""#), "{filter}");
    // A cursor returned under another sort is the route's 400 too.
    let cursor = param(&next, "cursor").unwrap();
    let other = get(&app, &format!("/console/products?sort=name&cursor={cursor}"), "clerk").await;
    assert_eq!(other.status, StatusCode::BAD_REQUEST, "{}", other.html);
    assert!(other.html.contains(r#"<p class="form-error">cursor: returned under another sort</p>"#), "{}", other.html);

    // A list under an item sorts by its own headers, by a number among
    // them; the item's page shows its first page with none.
    let anvil = wire(&uuid(ANVIL));
    let reviews = get(&app, &format!("/console/products/{anvil}/reviews?sort=-rating"), "clerk").await;
    assert_eq!(reviews.status, StatusCode::OK, "{}", reviews.html);
    assert!(header(&reviews.html, "rating").contains(r#"aria-sort="descending""#), "{}", reviews.html);
    assert_eq!(sort_link(&reviews.html, "title"), Some(format!("/console/products/{anvil}/reviews?sort=title")));
    let ratings: Vec<&str> = reviews.html.split(r#"<td data-field="rating">"#).skip(1).map(|cell| cell.split('<').next().unwrap()).collect();
    assert_eq!(ratings, ["5", "4"], "{}", reviews.html);
    let item = get(&app, &format!("/console/products/{anvil}"), "clerk").await;
    let section = between(&item.html, r#"<section class="ss-section" data-resource="reviews">"#, "</section>");
    assert!(!section.contains("data-ss-sort") && !section.contains("aria-sort"), "{section}");
}

#[tokio::test]
async fn a_live_list_sorts_in_place() {
    let app = app(&Store::new());
    let list = get(&app, "/console/products", "clerk").await;
    // A header's link sets the shard's query and the address bar.
    let cell = header(&list.html, "sku");
    assert!(cell.contains("data-topcoat-on:click=") && cell.contains("history.replaceState"), "{cell}");
    // The shard renders the order its signal's query sets, under its
    // identity, the headers following it.
    let identity = between(&list.html, r#"::topcoat::shard::start("/console/_live/products", ""#, "\"");
    let signal = between(&list.html, "::topcoat::signal({&quot;t&quot;:&quot;signal&quot;,&quot;id&quot;:&quot;", "&quot;");
    let body = serde_json::json!({"args": ["", "/console/products"], "signals": {signal: "sort=-sku"}});
    let mut request = http::Request::builder().method("POST").uri("/console/_live/products").header("x-caller", "clerk");
    request = request.header(header::CONTENT_TYPE, "application/json").header("x-topcoat-identity", identity);
    let response = app.handle(request.body(Body::from(body.to_string())).unwrap()).await;
    assert_eq!(response.status(), StatusCode::OK);
    let headers = response.headers().clone();
    let html = String::from_utf8(to_bytes(response.into_body(), usize::MAX).await.unwrap().to_vec()).unwrap();
    enter::check_page("/console/_live/products", &headers, &html);
    assert_eq!(names(&html), ["Drum", "Crate"], "{html}");
    assert!(header(&html, "sku").contains(r#"aria-sort="descending""#), "{html}");
    assert!(!header(&html, "name").contains("aria-sort"), "{html}");
    assert_eq!(sort_link(&html, "sku").as_deref(), Some("/console/products?sort=sku"), "{html}");
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

#[path = "support/enter.rs"]
mod enter;

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
    let (status, headers) = (response.status(), response.headers().clone());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    (status, html)
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

// hostedAppTest is tests/app.rs of the Topcoat crate of the copy of
// fixture-console-api whose Supplier, and its options, are
// fixture-console-suppliers-api's, which one app hosts beside it.
const hostedAppTest = `use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_console_api_topcoat::api::runtime::page::{Cursor, Page};
use schemas_fixture_console_api_topcoat::api::runtime::{search, ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_console_api_topcoat::api::{
    types, Implementations, OrderAddLineArgs, OrderGetOrderArgs, OrderImplementation, OrderListOrdersArgs, OrderPlaceOrderArgs,
    ProductCreateProductArgs, ProductGetProductArgs, ProductImplementation, ProductListProductsArgs, ReviewImplementation,
    ReviewListReviewsArgs, SiteImplementation, SiteListPagesArgs, SupplierImplementation, SupplierListSupplierProductsArgs,
};
use schemas_fixture_console_api_topcoat::pages::Settings;
use schemas_fixture_console_api_topcoat::{PageAuthenticator, RouterBuilderFixtureConsoleApiExt};
use schemas_fixture_console_suppliers_api_topcoat as suppliers;
use suppliers::RouterBuilderFixtureConsoleSuppliersApiExt;
use topcoat::context::Cx;
use topcoat::router::{header, page, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};
use topcoat::view::{view, View};

#[path = "support/enter.rs"]
mod enter;

const ANVIL: &str = "8d1f6c9e-0000-4000-8000-000000000001";
const BUCKET: &str = "8d1f6c9e-0000-4000-8000-000000000002";
const CRATE: &str = "8d1f6c9e-0000-4000-8000-000000000003";

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(serde_json::json!(text)).unwrap()
}

fn product(id: &str, name: &str, supplier: &str) -> types::Product {
    types::Product { id: uuid(id), name: name.to_owned(), sku: name.to_lowercase(), supplier_id: supplier.to_owned(), in_stock: true }
}

// The console's products, and nothing else: it lists no supplier.
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

// The suppliers' API: Acme, Bolt and Cog, by name, searched by their names.
struct Directory;

#[async_trait]
impl suppliers::api::SupplierImplementation for Directory {
    async fn list_suppliers(&self, _ctx: RequestContext, args: suppliers::api::SupplierListSuppliersArgs) -> Result<Page<suppliers::api::types::Supplier>, ApiError> {
        let terms = search::terms(args.q.as_deref().unwrap_or_default());
        let after: Option<String> = args.page.cursor.as_ref().map(Cursor::decode).transpose()?;
        let rows: Vec<suppliers::api::types::Supplier> = ["Acme", "Bolt", "Cog"]
            .iter()
            .filter(|name| search::matches(name, &terms) && after.as_ref().is_none_or(|after| **name > after.as_str()))
            .map(|name| suppliers::api::types::Supplier { id: name.to_lowercase(), name: (*name).to_owned() })
            .collect();
        Page::from_rows(rows, args.page.limit, |supplier| supplier.name.clone())
    }
}

struct NoRequests;

#[async_trait]
impl Authenticator for NoRequests {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

// Every page's caller is a clerk, who reads products and suppliers, in
// both crates.
struct Clerk;

fn clerk() -> Option<Principal> {
    Some(Principal::new("clerk", ["orders", "products.read"]))
}

#[async_trait]
impl PageAuthenticator for Clerk {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(clerk())
    }
}

#[async_trait]
impl suppliers::PageAuthenticator for Clerk {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(clerk())
    }
}

fn console(shop: &Arc<Shop>) -> Implementations {
    Implementations {
        order: Arc::clone(shop) as _,
        product: Arc::clone(shop) as _,
        review: Arc::clone(shop) as _,
        site: Arc::clone(shop) as _,
        supplier: Arc::clone(shop) as _,
        authenticator: Arc::new(NoRequests),
    }
}

fn directory() -> suppliers::api::Implementations {
    suppliers::api::Implementations { supplier: Arc::new(Directory), authenticator: Arc::new(NoRequests) }
}

fn shop() -> Arc<Shop> {
    Arc::new(Shop(Mutex::new(vec![product(ANVIL, "Anvil", "acme"), product(BUCKET, "Bucket", "bolt")])))
}

// The app that hosts both services: the console's JSON API at /api, the
// suppliers' crate without one.
fn app(shop: &Arc<Shop>) -> Router {
    Router::builder()
        .discover()
        .app_context(Settings::new().title("Shop console"))
        .fixture_console_api(console(shop), Clerk)
        .fixture_console_suppliers_api_without_api(directory(), Clerk)
        .build()
}

// An app that hosts the console alone, though it links the suppliers'
// crate.
fn console_alone(shop: &Arc<Shop>) -> Router {
    Router::builder().discover().app_context(Settings::new().title("Shop console")).fixture_console_api(console(shop), Clerk).build()
}

// The suppliers' in-process call, from the app's own page: how many
// suppliers its first page holds, or its refusal.
#[page("/app/suppliers")]
async fn app_suppliers(cx: &Cx) -> topcoat::Result<impl View> {
    let args = suppliers::api::SupplierListSuppliersArgs { q: None, page: suppliers::api::SupplierListSuppliersArgs::PAGE.first() };
    let said = match suppliers::operations::supplier_list_suppliers(cx, args).await {
        Ok(page) => page.items.len().to_string(),
        Err(err) => format!("{} {}", err.status.as_u16(), err.message),
    };
    Ok(view! { <p id="said">(said)</p> })
}

struct Answer {
    status: StatusCode,
    headers: http::HeaderMap,
    html: String,
}

async fn send(router: &Router, method: &str, uri: &str, body: &str) -> Answer {
    let mut request = http::Request::builder().method(method).uri(uri);
    if method == "POST" {
        request = request.header(header::CONTENT_TYPE, "application/x-www-form-urlencoded");
    }
    let response = router.handle(request.body(Body::from(body.to_owned())).unwrap()).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    Answer { status, headers, html }
}

async fn get(router: &Router, uri: &str) -> Answer {
    send(router, "GET", uri, "").await
}

fn between<'a>(html: &'a str, start: &str, end: &str) -> &'a str {
    let rest = &html[html.find(start).unwrap_or_else(|| panic!("no {start} in {html}")) + start.len()..];
    &rest[..rest.find(end).unwrap()]
}

// The labels of the options of the picker of the control named name.
fn labels(html: &str, name: &str) -> Vec<String> {
    let picker = between(html, &format!(r#"<div class="ss-pick" data-ss-pick="{name}">"#), "</select>");
    picker
        .split("<option value=\"")
        .skip(1)
        .filter(|option| !option.starts_with('"'))
        .map(|option| option.split_once('>').unwrap().1.trim_end_matches("</option>").to_owned())
        .collect()
}

#[tokio::test]
async fn a_product_names_its_supplier_through_the_hosted_crate() {
    let app = app(&shop());
    let list = get(&app, "/console/products").await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    assert!(list.html.contains(r#"<td data-field="supplierId">Acme</td>"#) && list.html.contains(r#"<td data-field="supplierId">Bolt</td>"#), "{}", list.html);
    let anvil = serde_json::to_value(uuid(ANVIL)).unwrap().as_str().unwrap().to_owned();
    let item = get(&app, &format!("/console/products/{anvil}")).await;
    assert!(item.html.contains("<dd>Acme</dd>"), "{}", item.html);
    // The trail names a supplier through the reference of its products'
    // route, from the suppliers' listing.
    let products = get(&app, "/console/suppliers/bolt/products").await;
    assert!(products.html.contains("<li><span>Bolt</span></li>"), "{}", products.html);
}

#[tokio::test]
async fn a_new_product_picks_its_supplier_through_the_hosted_crate() {
    let shop = shop();
    let app = app(&shop);
    // The suppliers' listSuppliers searches, so the supplier is a picker of
    // its first page, one supplier, and more match.
    let new = get(&app, "/console/products/new").await;
    assert_eq!(new.status, StatusCode::OK, "{}", new.html);
    assert_eq!(labels(&new.html, "supplierId"), ["Acme"], "{}", new.html);
    assert!(new.html.contains("More suppliers match"), "{}", new.html);
    // Find searches the suppliers' API in-process.
    let found = send(&app, "POST", "/console/products/new", "name=Crate&sku=crate&supplierId=&_find.supplierId=bo&_action=find:supplierId").await;
    assert_eq!(found.status, StatusCode::OK, "{}", found.html);
    assert_eq!(labels(&found.html, "supplierId"), ["Bolt"], "{}", found.html);
    let placed = send(&app, "POST", "/console/products/new", "name=Crate&sku=crate&supplierId=bolt").await;
    assert_eq!(placed.status, StatusCode::SEE_OTHER, "{}", placed.html);
    assert_eq!(shop.0.lock().unwrap().last().map(|product| product.supplier_id.clone()).as_deref(), Some("bolt"));
}

#[tokio::test]
async fn a_page_meets_the_suppliers_rate_limit() {
    let app = app(&shop());
    // listSuppliers takes three requests a minute a client: the products'
    // list, whose rows it names, counts against the suppliers' limiter.
    for _ in 0..3 {
        assert_eq!(get(&app, "/console/products").await.status, StatusCode::OK);
    }
    let refused = get(&app, "/console/products").await;
    assert_eq!(refused.status, StatusCode::TOO_MANY_REQUESTS, "{}", refused.html);
    assert!(refused.headers.contains_key(header::RETRY_AFTER));
    // A page that calls no supplier is not counted.
    assert_eq!(get(&app, "/console/orders").await.status, StatusCode::OK);
}

#[tokio::test]
async fn a_pickers_shard_meets_the_suppliers_rate_limit() {
    let app = app(&shop());
    // The supplier's picker's shard takes from listSuppliers' limiter, three
    // requests a minute a client, as a page that calls it does: the fourth
    // is the route's 429, with Retry-After, in the place of the picker.
    let record = |fields: serde_json::Value| serde_json::json!({"t": "Record", "v": fields});
    let control = record(serde_json::json!({"name": "supplierId", "id": "product-input-supplier-id", "required": true, "invalid": false, "value": "", "post": true}));
    let pick = record(serde_json::json!({"text": "", "options": {"t": "Vec", "bits": 64, "v": []}, "more": false, "refused": {"t": "Option", "v": null}}));
    let body = serde_json::json!({"args": [control, pick]}).to_string();
    let mut answers = Vec::new();
    for _ in 0..4 {
        let request = http::Request::builder()
            .method("POST")
            .uri("/_superschematic/fixture-console-api/_pick/supplier")
            .header(header::CONTENT_TYPE, "application/json")
            .body(Body::from(body.clone()))
            .unwrap();
        let response = app.handle(request).await;
        let (status, headers) = (response.status(), response.headers().clone());
        let html = String::from_utf8(to_bytes(response.into_body(), usize::MAX).await.unwrap().to_vec()).unwrap();
        answers.push(Answer { status, headers, html });
    }
    for answer in &answers[..3] {
        assert_eq!(answer.status, StatusCode::OK, "{}", answer.html);
        assert!(answer.html.contains(r#"data-ss-pick="supplierId""#), "{}", answer.html);
    }
    let refused = &answers[3];
    assert_eq!(refused.status, StatusCode::TOO_MANY_REQUESTS, "{}", refused.html);
    assert!(refused.headers.contains_key(header::RETRY_AFTER), "{:?}", refused.headers);
    assert_eq!(refused.html, r#"<p class="field-error" data-status="429">Too Many Requests</p>"#);
}

#[tokio::test]
async fn an_app_without_the_suppliers_names_them_by_their_ids() {
    let alone = console_alone(&shop());
    let list = get(&alone, "/console/products").await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    assert!(list.html.contains(r#"<td data-field="supplierId">acme</td>"#), "{}", list.html);
    let new = get(&alone, "/console/products/new").await;
    assert!(new.html.contains(r#"<input id="product-input-supplier-id" name="supplierId" type="text" required"#), "{}", new.html);
    // A call into the crate the app did not set up is the 500, not a panic.
    let said = get(&alone, "/app/suppliers").await;
    assert_eq!(
        between(&said.html, r#"<p id="said">"#, "</p>"),
        "500 the app does not host fixture-console-suppliers-api: its router builder calls neither fixture_console_suppliers_api nor fixture_console_suppliers_api_without_api"
    );
    let hosting = app(&shop());
    assert_eq!(between(&get(&hosting, "/app/suppliers").await.html, r#"<p id="said">"#, "</p>"), "1");
}

#[tokio::test]
async fn two_services_serve_their_pages_and_one_json_api() {
    let app = app(&shop());
    // Each crate's pages, under its own prefix.
    let index = get(&app, "/console").await;
    assert_eq!(index.status, StatusCode::OK, "{}", index.html);
    let suppliers = get(&app, "/suppliers/suppliers").await;
    assert_eq!(suppliers.status, StatusCode::OK, "{}", suppliers.html);
    assert!(suppliers.html.contains(">Acme</th>"), "{}", suppliers.html);
    // The console's JSON API at /api, which refuses a caller it does not
    // know, as JSON.
    let api = get(&app, "/api/products").await;
    assert_eq!(api.status, StatusCode::UNAUTHORIZED, "{}", api.html);
    assert!(api.headers.get(header::CONTENT_TYPE).and_then(|value| value.to_str().ok()).is_some_and(|kind| kind.contains("json")), "{:?}", api.headers);
}

#[test]
fn a_second_json_api_is_refused() {
    let shop = shop();
    let built = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        Router::builder().fixture_console_api(console(&shop), Clerk).fixture_console_suppliers_api(directory(), Clerk).build()
    }));
    let Err(panic) = built else {
        panic!("an app with two JSON APIs at /api built");
    };
    let message = panic.downcast_ref::<String>().cloned().or_else(|| panic.downcast_ref::<&str>().map(|text| (*text).to_owned())).unwrap_or_default();
    assert!(message.contains("duplicate any-method route registered for"), "{message}");
}
`

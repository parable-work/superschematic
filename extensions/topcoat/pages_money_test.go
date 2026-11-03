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

// moneyPagesService is a service whose edit page's form holds amounts in
// GBP: a price's amount and its tiers, each a decimal control (D44,
// amended), which the item holds in minor units.
const moneyPagesService = "fixture-pages-money-api"

// writeMoneyPagesService writes moneyPagesService into a test's own
// directory.
func writeMoneyPagesService(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), moneyPagesService)
	files := map[string]string{
		"schema.config.yaml": "name: " + moneyPagesService + "\nkind: API\noutputs: {}\n",
		"src/prices.schema.yaml": `name: ` + moneyPagesService + `
kind: API
scalars:
  Finance.Money:
    name: Finance.Money
    languagePrimitive: number
types:
  Price:
    name: Price
    role: APIView
    display:
      noun: Price
      titleField: title
    fields:
      - name: id
        typeRef: { name: string }
        required: true
      - name: title
        typeRef: { name: string }
        required: true
      - name: amount
        typeRef: { name: Finance.Money }
        display: { currency: GBP }
      - name: tiers
        typeRef: { name: Finance.Money, isArray: true }
        display: { currency: GBP }
  PriceInput:
    name: PriceInput
    role: APIInput
    fields:
      - name: title
        typeRef: { name: string }
        required: true
      - name: amount
        typeRef: { name: Finance.Money }
        display: { currency: GBP }
      - name: tiers
        typeRef: { name: Finance.Money, isArray: true }
        display: { currency: GBP }
operationSets:
  - name: Prices
    operations:
      - name: listPrices
        typeRef: { name: Price, isArray: true }
        required: true
        httpMethod: GET
        restPath: prices
      - name: getPrice
        typeRef: { name: Price }
        required: true
        httpMethod: GET
        restPath: "prices/{id}"
        arguments:
          - name: id
            typeRef: { name: string }
            required: true
      - name: replacePrice
        typeRef: { name: Price }
        required: true
        httpMethod: PUT
        restPath: "prices/{id}"
        arguments:
          - name: id
            typeRef: { name: string }
            required: true
          - name: input
            typeRef: { name: PriceInput }
            required: true
`,
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestAnEditPageHoldsAnAmountAsADecimal: the edit page of a price fills
// its GBP amount's decimal control, and each of its tiers', from the
// item's minor units as the control holds them (forms::held_decimal), so
// "4.50" for 450; saving the form unchanged keeps the same minor units.
func TestAnEditPageHoldsAnAmountAsADecimal(t *testing.T) {
	dir := writeMoneyPagesService(t)
	root := testpaths.TempDir(t)
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, pagesOutputs(), nil); err != nil {
		t.Fatalf("build: %v", err)
	}
	pages, err := os.ReadFile(filepath.Join(topcoat.Dir(root, moneyPagesService), "src", "pages.rs"))
	if err != nil {
		t.Fatalf("pages.rs: %v", err)
	}
	for _, want := range []string{
		`form.amount = held_decimal(&json["amount"], "GBP");`,
		`form.tiers = rows.iter().map(|row| held_decimal(row, "GBP")).collect();`,
	} {
		if !strings.Contains(string(pages), want) {
			t.Errorf("pages.rs lacks %s", want)
		}
	}

	cargoTestCrateWith(t, moneyPagesService, moneyPagesAppTest, crateOptions{dir: dir, outputs: pagesOutputs()})
}

// moneyPagesAppTest serves a price's edit page, then posts its form back
// as it was filled, through the crate's router.
const moneyPagesAppTest = `use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use schemas_fixture_pages_money_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_pages_money_api_topcoat::api::{
    types, Implementations, PricesGetPriceArgs, PricesImplementation, PricesReplacePriceArgs,
};
use schemas_fixture_pages_money_api_topcoat::RouterBuilderFixturePagesMoneyApiExt;
use topcoat::router::{to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

/// The one price, which replacePrice changes.
struct Prices(Mutex<types::Price>);

#[async_trait]
impl PricesImplementation for Prices {
    async fn list_prices(&self, _ctx: RequestContext) -> Result<Vec<types::Price>, ApiError> {
        Ok(vec![self.0.lock().unwrap().clone()])
    }
    async fn get_price(&self, _ctx: RequestContext, args: PricesGetPriceArgs) -> Result<types::Price, ApiError> {
        let price = self.0.lock().unwrap().clone();
        if args.id == price.id { Ok(price) } else { Err(ApiError::not_found("no such price")) }
    }
    async fn replace_price(&self, _ctx: RequestContext, args: PricesReplacePriceArgs) -> Result<types::Price, ApiError> {
        let mut price = self.0.lock().unwrap();
        price.title = args.input.title;
        price.amount = args.input.amount;
        price.tiers = args.input.tiers;
        Ok(price.clone())
    }
}

async fn send(router: &Router, method: &str, uri: &str, form: &[(&str, &str)]) -> (StatusCode, String) {
    let mut request = http::Request::builder().method(method).uri(uri).header("host", "app.example.com");
    let body = if method == "POST" {
        request = request.header("content-type", "application/x-www-form-urlencoded").header("sec-fetch-site", "same-origin");
        let encoded: Vec<String> = form.iter().map(|(name, value)| format!("{}={}", encode(name), encode(value))).collect();
        Body::from(encoded.join("&"))
    } else {
        Body::empty()
    };
    let response = router.handle(request.body(body).unwrap()).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    (status, html)
}

fn encode(value: &str) -> String {
    value
        .bytes()
        .map(|byte| if byte.is_ascii_alphanumeric() || b"-._~".contains(&byte) { char::from(byte).to_string() } else { format!("%{byte:02X}") })
        .collect()
}

#[tokio::test]
async fn an_edit_page_holds_an_amount_as_a_decimal_and_saves_it_unchanged() {
    let price = types::Price { id: "p1".to_owned(), title: "Tea".to_owned(), amount: Some(450), tiers: Some(vec![100, 2550]) };
    let prices = Arc::new(Prices(Mutex::new(price.clone())));
    let implementations = Implementations { prices: prices.clone() };
    let router = Router::builder().discover().fixture_pages_money_api(implementations).build();

    let (status, html) = send(&router, "GET", "/console/prices/p1/edit", &[]).await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert!(html.contains(r#"name="amount" type="text" inputmode="decimal""#), "{html}");
    assert!(html.contains(r#"value="4.50""#), "{html}");
    assert!(html.contains(r#"value="1.00""#), "{html}");
    assert!(html.contains(r#"value="25.50""#), "{html}");
    assert!(!html.contains(r#"value="450""#), "{html}");

    // The form posted back as it was filled keeps the same minor units.
    let form = [("id", "p1"), ("title", "Tea"), ("amount", "4.50"), ("tiers[0]", "1.00"), ("tiers[1]", "25.50")];
    let (status, html) = send(&router, "POST", "/console/prices/p1/edit", &form).await;
    assert_eq!(status, StatusCode::SEE_OTHER, "{html}");
    let saved = prices.0.lock().unwrap().clone();
    assert_eq!(saved.amount, Some(450));
    assert_eq!(saved.tiers, Some(vec![100, 2550]));
}
`

package topcoat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// formsWithoutViewsOutputs are fixture-forms-api's outputs with its
// display components off: its forms take amounts as decimals, so the
// crate formats money (WritesMoney) and has no component.
func formsWithoutViewsOutputs() map[string]any {
	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "views": false}
	return outputs
}

// TestMoneyTextWithoutViews builds fixture-forms-api with its display
// components off: views::money_text is reachable whenever the crate
// formats money (D44, amended), so views.rs is written holding it alone,
// lib.rs declares the module, and the crate passes cargo clippy with
// warnings denied and formats an amount through crate::views::money_text,
// as an engine panel calls it in a crate whose console has no component.
func TestMoneyTextWithoutViews(t *testing.T) {
	root := testpaths.TempDir(t)
	if _, err := buildService(t, formsService, root, formsWithoutViewsOutputs()); err != nil {
		t.Fatalf("build: %v", err)
	}
	src := filepath.Join(topcoat.Dir(root, formsService), "src")
	views, err := os.ReadFile(filepath.Join(src, "views.rs"))
	if err != nil {
		t.Fatalf("views.rs: %v", err)
	}
	if !strings.Contains(string(views), "pub fn money_text(amount: i64, currency: &str) -> String {") || strings.Contains(string(views), "#[component]") || strings.Contains(string(views), "use crate::") {
		t.Errorf("views.rs is not money_text alone:\n%s", views)
	}
	lib, err := os.ReadFile(filepath.Join(src, "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lib), "\npub mod views;\n") {
		t.Errorf("lib.rs declares no views module:\n%s", lib)
	}

	cargoTestCrateWith(t, formsService, `use schemas_fixture_forms_api_topcoat::views::money_text;

#[path = "support/enter.rs"]
mod enter;

#[test]
fn money_text_is_reachable_without_components() {
    assert_eq!(money_text(123456, "GBP"), "GBP 1,234.56");
}
`, crateOptions{outputs: formsWithoutViewsOutputs()})
}

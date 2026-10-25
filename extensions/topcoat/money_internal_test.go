package topcoat

import (
	"bytes"
	"strings"
	"testing"
)

// TestEnginePanelsWithoutViewsWriteMoneyText: a crate whose engine panels
// show money and which has no display component writes views.rs, holding
// money_text alone, which the panels call as crate::views::money_text, and
// lib.rs declares it; a crate without either writes no views.rs.
func TestEnginePanelsWithoutViewsWriteMoneyText(t *testing.T) {
	if (&crate{}).HasViews() {
		t.Fatal("a crate without components or money writes views.rs")
	}
	c := &crate{SchemaName: "shop-db", EnginePanels: &enginePanelsModel{}, ScalarCrate: "superscalar"}
	if !c.HasViews() {
		t.Fatal("a crate whose engine panels show money writes no views.rs")
	}
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var views bytes.Buffer
	if err := tmpl.ExecuteTemplate(&views, "views.tmpl", c); err != nil {
		t.Fatal(err)
	}
	got := views.String()
	for _, want := range []string{
		"//! The views of shop-db, which has no display component",
		"pub fn money_text(amount: i64, currency: &str) -> String {",
		"superscalar::scalars::finance_currency::lookup(currency)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("views.rs lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "use crate::") || strings.Contains(got, "#[component]") {
		t.Errorf("views.rs of money alone imports or renders a component:\n%s", got)
	}
}

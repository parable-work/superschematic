package rustutil

import "testing"

func TestIsRustKeyword(t *testing.T) {
	for _, kw := range []string{"type", "match", "self", "Self", "async", "yield"} {
		if !IsRustKeyword(kw) {
			t.Errorf("IsRustKeyword(%q) = false, want true", kw)
		}
	}
	for _, name := range []string{"tenant", "value", "Type", "selfish"} {
		if IsRustKeyword(name) {
			t.Errorf("IsRustKeyword(%q) = true, want false", name)
		}
	}
}

func TestCollapseUnderscores(t *testing.T) {
	cases := map[string]string{
		"a__b":    "a_b",
		"a___b_c": "a_b_c",
		"ab":      "ab",
	}
	for in, want := range cases {
		if got := CollapseUnderscores(in); got != want {
			t.Errorf("CollapseUnderscores(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWrapOptionalType(t *testing.T) {
	if got := WrapOptionalType("String"); got != "Option<String>" {
		t.Errorf("WrapOptionalType(String) = %q", got)
	}
	if got := WrapOptionalType("Option<String>"); got != "Option<String>" {
		t.Errorf("WrapOptionalType(Option<String>) = %q", got)
	}
}

func TestCrateNameToModulePath(t *testing.T) {
	cases := map[string]string{
		"acme-fixture-db-types": "acme_fixture_db_types",
		"acme-web-api-types":    "acme_web_api_types",
		"":                      "dep",
		"123-crate":             "dep_123_crate",
		"type":                  "type_",
	}
	for in, want := range cases {
		if got := CrateNameToModulePath(in); got != want {
			t.Errorf("CrateNameToModulePath(%q) = %q, want %q", in, got, want)
		}
	}
}

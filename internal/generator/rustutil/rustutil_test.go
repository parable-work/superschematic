package rustutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// keywords lists every word IsRustKeyword reports.
var keywords = []string{
	"as", "break", "const", "continue", "crate", "else", "enum", "extern", "false",
	"fn", "for", "if", "impl", "in", "let", "loop", "match", "mod", "move", "mut", "pub",
	"ref", "return", "self", "Self", "static", "struct", "super", "trait", "true", "type",
	"unsafe", "use", "where", "while", "async", "await", "dyn", "abstract", "become", "box",
	"do", "final", "macro", "override", "priv", "try", "typeof", "unsized", "virtual", "yield",
}

func TestEscapeKeyword(t *testing.T) {
	for in, want := range map[string]string{
		"tenant": "tenant",
		"type":   "r#type",
		"match":  "r#match",
		"self":   "self_",
		"Self":   "Self_",
		"crate":  "crate_",
		"super":  "super_",
	} {
		if got := EscapeKeyword(in); got != want {
			t.Errorf("EscapeKeyword(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEscapedKeywordsCompile binds every escaped keyword in a Rust function
// and compiles it: crate, self, Self and super are no raw identifiers, so an
// escape that wrote r#self would fail here.
func TestEscapedKeywordsCompile(t *testing.T) {
	rustc, err := exec.LookPath("rustc")
	if err != nil {
		t.Skip("rustc not available")
	}
	for _, kw := range keywords {
		if !IsRustKeyword(kw) {
			t.Errorf("IsRustKeyword(%q) = false", kw)
		}
	}
	var src strings.Builder
	src.WriteString("#![allow(non_snake_case, unused_variables)]\npub fn bindings() {\n")
	for _, kw := range keywords {
		src.WriteString("    let " + EscapeKeyword(kw) + " = 0;\n")
	}
	src.WriteString("}\n")
	dir := t.TempDir()
	file := filepath.Join(dir, "keywords.rs")
	if err := os.WriteFile(file, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(rustc, "--edition", "2021", "--crate-type", "lib", "--emit", "metadata", "-o", filepath.Join(dir, "keywords.rmeta"), file)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rustc refused the escaped keywords: %v\n%s\n%s", err, out, src.String())
	}
}

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

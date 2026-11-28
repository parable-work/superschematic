package codegen

import "testing"

func TestQuoteTOML(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:           `"plain"`,
		`C:\dir "x"`:      `"C:\\dir \"x\""`,
		"cr\rlf\ntab\t":   `"cr\rlf\ntab\t"`,
		"bs\bff\f":        `"bs\bff\f"`,
		"bell\x07del\x7f": `"bell\u0007del\u007F"`,
	} {
		if got := QuoteTOML(in); got != want {
			t.Errorf("QuoteTOML(%q) = %s, want %s", in, got, want)
		}
	}
}

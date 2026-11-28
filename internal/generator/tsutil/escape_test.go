package tsutil

import "testing"

func TestEscapers(t *testing.T) {
	for _, tc := range []struct{ in, double, single string }{
		{`plain`, `plain`, `plain`},
		{`a"b'c\d`, `a\"b'c\\d`, `a"b\'c\\d`},
		{"cr\rlf\ntab\t", `cr\rlf\ntab\t`, `cr\rlf\ntab\t`},
		{"bell\x07", `bell\u0007`, `bell\u0007`},
		{"ls\u2028ps\u2029", `ls\u2028ps\u2029`, `ls\u2028ps\u2029`},
	} {
		if got := EscapeString(tc.in); got != tc.double {
			t.Errorf("EscapeString(%q) = %s, want %s", tc.in, got, tc.double)
		}
		if got := EscapeSingleQuoted(tc.in); got != tc.single {
			t.Errorf("EscapeSingleQuoted(%q) = %s, want %s", tc.in, got, tc.single)
		}
		if got, want := Quote(tc.in), "'"+tc.single+"'"; got != want {
			t.Errorf("Quote(%q) = %s, want %s", tc.in, got, want)
		}
	}
	if got, want := EscapeTemplateLiteral("a`b\\c${d}\n"), "a\\`b\\\\c\\${d}\n"; got != want {
		t.Errorf("EscapeTemplateLiteral = %q, want %q", got, want)
	}
}

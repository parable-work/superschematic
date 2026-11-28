package pygen

import "testing"

func TestPythonEscape(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:         `plain`,
		`a"b\c`:         `a\"b\\c`,
		"cr\rlf\ntab\t": `cr\rlf\ntab\t`,
		"bell\x07":      `bell\x07`,
		"del\x7f":       `del\x7f`,
	} {
		if got := pythonEscape(in); got != want {
			t.Errorf("pythonEscape(%q) = %s, want %s", in, got, want)
		}
		if got := pythonQuote(in); got != `"`+want+`"` {
			t.Errorf("pythonQuote(%q) = %s, want %q", in, got, `"`+want+`"`)
		}
	}
}

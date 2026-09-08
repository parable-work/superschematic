package tsreader

import "testing"

func TestLineCol(t *testing.T) {
	text := "abc\ndef\nghi"
	cases := []struct {
		pos, line, col int
	}{
		{0, 1, 1},
		{2, 1, 3},
		{4, 2, 1},
		{6, 2, 3},
		{8, 3, 1},
		{100, 3, 4}, // clamped to end
	}
	for _, c := range cases {
		line, col := lineCol(text, c.pos)
		if line != c.line || col != c.col {
			t.Errorf("lineCol(%d) = %d:%d, want %d:%d", c.pos, line, col, c.line, c.col)
		}
	}
}

func TestSkipTrivia(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"  x", 2},
		{"\n\t x", 3},
		{"// comment\nx", 11},
		{"/* block */x", 11},
		{"/* a */ // b\n  x", 15},
		{"x", 0},
		{"/* unterminated", 15},
	}
	for _, c := range cases {
		if got := skipTrivia(c.text, 0); got != c.want {
			t.Errorf("skipTrivia(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

func TestSchemaErrorFormat(t *testing.T) {
	e := &SchemaError{File: "a/b.schema.ts", Line: 3, Col: 7, Msg: "boom"}
	if e.Error() != "a/b.schema.ts:3:7: boom" {
		t.Errorf("unexpected format: %s", e.Error())
	}
	bare := &SchemaError{Msg: "no location"}
	if bare.Error() != "no location" {
		t.Errorf("unexpected format: %s", bare.Error())
	}
	list := SchemaErrorList{e, bare}
	if list.Error() != "a/b.schema.ts:3:7: boom\nno location" {
		t.Errorf("unexpected list format: %s", list.Error())
	}
}

func TestFormatLiteral(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"active", "active"},
		{true, "true"},
		{false, "false"},
		{float64(8080), "8080"},
		{float64(1.5), "1.5"},
		{nil, "null"},
	}
	for _, c := range cases {
		if got := formatLiteral(c.in); got != c.want {
			t.Errorf("formatLiteral(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestServiceNameForPackage(t *testing.T) {
	cases := map[string]string{
		"@parable-platform/web-db": "web-db",
		"@scope/name":              "name",
		"plain":                    "plain",
	}
	for in, want := range cases {
		if got := serviceNameForPackage(in); got != want {
			t.Errorf("serviceNameForPackage(%q) = %q, want %q", in, got, want)
		}
	}
}

package sqlutil

import "testing"

func TestQuoteIdentifier(t *testing.T) {
	cases := map[string]string{
		"tenant":     "tenant",
		"user":       `"user"`,
		"order":      `"order"`,
		"created_at": "created_at",
		"Name":       `"Name"`,
		"with space": `"with space"`,
		"table":      `"table"`,
		"id":         "id",
	}
	for in, want := range cases {
		if got := QuoteIdentifier(in); got != want {
			t.Errorf("QuoteIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

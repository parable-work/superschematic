package topcoat

import "testing"

// TestHTMLPattern keeps a pattern rule off an input when a browser, which
// reads the attribute with the v flag, would read it otherwise or refuse
// it.
func TestHTMLPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		ok      bool
	}{
		{`^[a-z0-9]+$`, true},
		{`^[0-9]{5}$`, true},
		{`^\d{3}-\d{4}$`, true},
		{`^[A-Z][a-z]*( [A-Z][a-z]*)*$`, true},
		{`^[a-z\-]+$`, true},
		{"", false},
		// A class with a trailing or leading hyphen, which the v flag refuses.
		{`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+$`, false},
		{`^[-a-z]+$`, false},
		// Characters the v flag reserves in a class, and a doubled
		// punctuator.
		{`^[a-z(]+$`, false},
		{`^[a-z|]+$`, false},
		{`^[a-z&&]+$`, false},
		// Group syntax and an escape the v flag refuses outside a class.
		{`^(?:ab)+$`, false},
		{`^a\-b$`, false},
		{`^\p{L}+$`, false},
		// An unclosed class.
		{`^[a-z`, false},
	} {
		got, ok := htmlPattern(tc.pattern)
		if ok != tc.ok || (ok && got != tc.pattern) {
			t.Errorf("htmlPattern(%q) = %q, %v; want ok %v", tc.pattern, got, ok, tc.ok)
		}
	}
}

func TestLabelsAndIDs(t *testing.T) {
	for name, want := range map[string]string{
		"displayName": "Display name",
		"email":       "Email",
		"userID":      "User ID",
		"postal_code": "Postal code",
		"Free":        "Free",
	} {
		if got := humanize(name); got != want {
			t.Errorf("humanize(%q) = %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{
		"SignupInput": "signup-input",
		"displayName": "display-name",
		"type":        "type",
	} {
		if got := kebabCase(name); got != want {
			t.Errorf("kebabCase(%q) = %q, want %q", name, got, want)
		}
	}
}

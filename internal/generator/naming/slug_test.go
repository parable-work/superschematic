package naming

import "testing"

// TestSlug: words of letters and digits, lower case, joined by one hyphen
// each, with nothing before the first or after the last.
func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"shop":      "shop",
		"Notes App": "notes-app",
		"acme.io":   "acme-io",
		"_shop_":    "shop",
		"my__shop":  "my-shop",
		"MyShop":    "myshop",
		"3d Prints": "3d-prints",
		"--":        "",
		"":          "",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q; want %q", in, got, want)
		}
	}
}

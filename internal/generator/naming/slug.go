package naming

import "strings"

// Slug turns free text, such as a directory's name or an npm scope, into
// lower-case words of letters and digits joined by hyphens, or "" when it
// holds no letter or digit: "My Shop" is my-shop, "acme.io" is acme-io,
// "_shop_" is shop. It splits no words at capitals ("MyShop" is myshop);
// an identifier takes ir.Kebab. init derives the project's Go module and
// npm scope from it, and stack init the stack's name.
func Slug(text string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(text) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(r)
			continue
		}
		hyphen = true
	}
	return b.String()
}

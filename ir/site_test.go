package ir

import (
	"strings"
	"testing"
)

// TestContentHashedName: a name holds a content hash when the part before
// its extension, or its last part after a dot or a hyphen, is 8 or more
// lowercase hex digits with a digit and a letter among them.
func TestContentHashedName(t *testing.T) {
	for name, want := range map[string]bool{
		"app.3f9a2c1d.js":                true,
		"assets/vendor-3f9a2c1d.css":     true,
		"fonts/inter.3f9a2c1d5e7b.woff2": true,
		"3f9a2c1d4e5b6a7c8d9e.png":       true,
		"main.0123456789abcdef0123.js":   true,
		"index.html":                     false,
		"app.js":                         false,
		"app.3f9a2c1.js":                 false,
		"app.3F9A2C1D.js":                false,
		"photo-20261012.jpg":             false,
		"deadbeef.js":                    false,
		"index-5psy29er.js":              false,
		"og-1200x630.png":                false,
		"app.3f9a2c1d.js.map":            false,
		"3f9a2c1d":                       false,
		"app_3f9a2c1d.js":                false,
	} {
		if got := ContentHashedName(name); got != want {
			t.Errorf("ContentHashedName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestSiteFileCacheControl: a content-hashed file is cached a year, unless
// it is HTML or the fallback; every other file is revalidated.
func TestSiteFileCacheControl(t *testing.T) {
	for _, tc := range []struct {
		rel, fallback, want string
	}{
		{"app.3f9a2c1d.js", "index.html", SiteCacheImmutable},
		{"assets/logo.3f9a2c1d.svg", "", SiteCacheImmutable},
		{"index.html", "index.html", SiteCacheRevalidate},
		{"app.js", "", SiteCacheRevalidate},
		{"page.3f9a2c1d.html", "", SiteCacheRevalidate},
		{"shell.3f9a2c1d.js", "shell.3f9a2c1d.js", SiteCacheRevalidate},
		{"app/shell.3f9a2c1d.js", "./app/shell.3f9a2c1d.js", SiteCacheRevalidate},
		{"index-5psy29er.js", "index.html", SiteCacheRevalidate},
	} {
		if got := SiteFileCacheControl(tc.rel, tc.fallback); got != tc.want {
			t.Errorf("SiteFileCacheControl(%q, %q) = %q, want %q", tc.rel, tc.fallback, got, tc.want)
		}
	}
}

// TestSiteDevConfig: a dev server's command defaults to the dev script,
// and Check refuses a command with a space in it and a port that is none
// (D82).
func TestSiteDevConfig(t *testing.T) {
	if got := (SiteConfig{Dev: &SiteDevConfig{Port: 5173}}).WithDefaults().Dev; got == nil || got.Command != DefaultSiteDevCommand || got.Port != 5173 {
		t.Fatalf("WithDefaults().Dev = %+v, want the dev script on 5173", got)
	}
	if got := (SiteConfig{}).WithDefaults().Dev; got != nil {
		t.Fatalf("a site without a dev server got %+v", got)
	}
	for _, tc := range []struct {
		dev  SiteDevConfig
		want string
	}{
		{SiteDevConfig{Command: "vite"}, ""},
		{SiteDevConfig{Command: "vite --host"}, `site.dev.command "vite --host" is no script name`},
		{SiteDevConfig{Port: 70000}, "site.dev.port 70000 is no port"},
		{SiteDevConfig{Port: -1}, "site.dev.port -1 is no port"},
	} {
		err := SiteConfig{Dev: &tc.dev}.Check()
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("Check(%+v) = %v, want %q", tc.dev, err, tc.want)
		}
	}
	if got, want := SiteDevAPIPath("shop-api"), "/api/shop-api"; got != want {
		t.Errorf("SiteDevAPIPath = %q, want %q", got, want)
	}
}

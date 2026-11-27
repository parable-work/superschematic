package tasks

import (
	"strings"
	"testing"
)

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"go.mod", "go.mod", true},
		{"go.mod", "go.sum", false},
		// A pattern that names a directory covers every file under it.
		{"go", "go/main.go", true},
		{"go", "go/pkg/deep/file.go", true},
		{"go", "golang/main.go", false},
		{"go/*.go", "go/main.go", true},
		{"go/*.go", "go/pkg/main.go", false},
		{"go/**", "go/pkg/main.go", true},
		{"go/**/*.go", "go/main.go", true},
		{"go/**/*.go", "go/a/b/main.go", true},
		{"go/**/*.go", "go/a/b/main.rs", false},
		{"**/*.proto", "api/v1/shop.proto", true},
		{"schemas/dist", "schemas/dist/types/go/shop-api/go.mod", true},
		{"docs/[a-c]*.md", "docs/build.md", true},
		{"docs/[a-c]*.md", "docs/readme.md", false},
		{"a/b/c", "a/b", false},
	} {
		if got := Covers(c.pattern, c.name); got != c.want {
			t.Errorf("Covers(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestCheckPattern(t *testing.T) {
	for _, ok := range []string{"go", "go/**", "go/**/*.go", "**/*.proto", "a/b/c.txt", "docs/[a-c]*.md"} {
		if err := CheckPattern(ok); err != nil {
			t.Errorf("CheckPattern(%q): %v", ok, err)
		}
	}
	for pattern, want := range map[string]string{
		"":          "is empty",
		"/etc":      "is absolute",
		"../other":  "leaves the repository",
		".":         "leaves the repository or names all of it",
		"a//b":      "not in its shortest form",
		`a\b`:       "backslash",
		"go/**.go":  "holds ** inside a segment",
		"docs/[a-c": "is no pattern",
	} {
		if err := CheckPattern(pattern); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckPattern(%q) = %v, want an error holding %q", pattern, err, want)
		}
	}
	// A path is a pattern with no wildcard, and CheckPath reads no
	// segment as one.
	if err := CheckPath("docs/[a-c"); err != nil {
		t.Errorf("CheckPath of a path with a bracket: %v", err)
	}
}

func TestLiteralPrefix(t *testing.T) {
	for pattern, want := range map[string]string{
		"go/api":            "go/api",
		"go/*/main.go":      "go",
		"schemas/dist/**":   "schemas/dist",
		"**/*.proto":        "",
		"crates/shop-?/src": "crates",
	} {
		if got := LiteralPrefix(pattern); got != want {
			t.Errorf("LiteralPrefix(%q) = %q, want %q", pattern, got, want)
		}
	}
	if HasWildcard("go/api") || !HasWildcard("go/*") {
		t.Error("HasWildcard")
	}
}

package registry

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// TestRegisterPackageDir keeps a directory in registration order and
// refuses a language with no manifest rule, a core kind, a path that is not
// clean and relative, and a directory registered twice.
func TestRegisterPackageDir(t *testing.T) {
	r := New(naming.Default())
	for _, d := range []PackageDir{
		{Extension: "acme", Language: LangGo, Dir: "cache/go", Kind: "cache"},
		{Extension: "acme", Language: LangRust, Dir: "cache/rust", Kind: "cache"},
	} {
		if err := r.RegisterPackageDir(d); err != nil {
			t.Fatalf("RegisterPackageDir(%+v): %v", d, err)
		}
	}
	if got := r.PackageDirs(); len(got) != 2 || got[0].Dir != "cache/go" || got[1].Dir != "cache/rust" {
		t.Errorf("PackageDirs = %+v", got)
	}
	for _, tc := range []struct {
		d    PackageDir
		want string
	}{
		{PackageDir{Extension: "acme", Language: "java", Dir: "cache/java", Kind: "cache"}, `language "java" is none of`},
		{PackageDir{Extension: "acme", Language: LangGo, Dir: "x/go", Kind: "types"}, `kind "types" must be a lower-case word other than`},
		{PackageDir{Extension: "acme", Language: LangGo, Dir: "x/go", Kind: "Cache"}, `kind "Cache" must be`},
		{PackageDir{Extension: "acme", Language: LangGo, Dir: "../go", Kind: "cache"}, "must be a clean relative path"},
		{PackageDir{Extension: "acme", Language: LangGo, Dir: "/go", Kind: "cache"}, "must be a clean relative path"},
		{PackageDir{Extension: "acme", Language: LangGo, Dir: "cache/go", Kind: "cache"}, "is already registered by acme"},
	} {
		err := r.RegisterPackageDir(tc.d)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("RegisterPackageDir(%+v) = %v, want an error holding %q", tc.d, err, tc.want)
		}
	}
}

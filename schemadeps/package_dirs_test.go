package schemadeps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCollectFromDistReadsAnExtensionsPackageDirs: a package under a
// directory an extension registers enters the graph in its language with
// the ID <service>-<kind>, its kind, its manifest's name and its edges to
// the core's packages, in each of the four languages. Without the
// directory the graph holds none of them.
func TestCollectFromDistReadsAnExtensionsPackageDirs(t *testing.T) {
	dist := t.TempDir()
	write := func(rel, file, content string) {
		t.Helper()
		dir := filepath.Join(dist, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const pseudo = " v0.0.0-00010101000000-000000000000"
	write("types/go/shop", "go.mod", "module example.com/schemas/types/go/shop\n\ngo 1.27.3\n")
	write("cache/go/shop", "go.mod", "module example.com/schemas/cache/go/shop\n\ngo 1.27.3\n\nrequire (\n\texample.com/schemas/types/go/shop"+pseudo+"\n)\n")
	write("types/typescript/shop", "package.json", `{"name": "@schemas/shop-types"}`)
	write("cache/typescript/shop", "package.json", `{"name": "@schemas/shop-cache", "dependencies": {"@schemas/shop-types": "file:../../../types/typescript/shop"}}`)
	write("types/python/shop", "pyproject.toml", "[project]\nname = \"schemas_types_shop\"\n")
	write("cache/python/shop", "pyproject.toml", "[project]\nname = \"schemas_cache_shop\"\ndependencies = [\n  \"schemas_types_shop>=0\",\n]\n")
	write("types/rust/shop", "Cargo.toml", "[package]\nname = \"schemas-shop-types\"\n")
	write("cache/rust/shop", "Cargo.toml", "[package]\nname = \"schemas-shop-cache\"\n\n[dependencies]\nschemas-shop-types = { path = \"../../../types/rust/shop\" }\n")

	plain, err := CollectFromDist(dist, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plain.Packages {
		if p.Kind == "cache" {
			t.Errorf("without the package directories the graph holds %s/%s", p.Language, p.ID)
		}
	}

	var dirs []PackageDir
	for _, lang := range []string{"go", "typescript", "python", "rust"} {
		dirs = append(dirs, PackageDir{Language: lang, Dir: "cache/" + lang, Kind: "cache"})
	}
	g, err := CollectFromDist(dist, map[string]string{
		"types/go/shop": "shop", "cache/go/shop": "shop",
		"types/typescript/shop": "shop", "cache/typescript/shop": "shop",
		"types/python/shop": "shop", "cache/python/shop": "shop",
		"types/rust/shop": "shop", "cache/rust/shop": "shop",
	}, dirs...)
	if err != nil {
		t.Fatal(err)
	}
	for lang, name := range map[string]string{
		"go":         "example.com/schemas/cache/go/shop",
		"typescript": "@schemas/shop-cache",
		"python":     "schemas_cache_shop",
		"rust":       "schemas-shop-cache",
	} {
		p := mustPkg(t, g, lang, "shop-cache")
		if p.Kind != "cache" || p.Name != name || p.Path != "cache/"+lang+"/shop" || p.Service != "shop" {
			t.Errorf("%s package = %+v, want kind cache, name %s, path cache/%s/shop, service shop", lang, p, name, lang)
		}
		if got := strings.Join(p.Deps, ","); got != "shop-types" {
			t.Errorf("%s package deps = [%s], want [shop-types]", lang, got)
		}
	}
}

package servergen_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// TestAScaffoldWarnsOfADependencyItsPackageLacks (D100): every build
// writes a site's config.generated.ts, which imports each SDK, and builds
// the servers, jobs and workers of a stack against each TypeScript
// implementation, but writes the package.json of either once, with its
// scaffold. A site whose package predates the API its config calls lacks
// the SDK, and an implementation whose package predates its API's worker
// and memory store lacks the database's types and the store's
// definitions: the site's build and the stack's then warn, naming the
// lines to add, the stack's once although ts-shop's server, job and
// worker each build against it, and write into neither package. Packages
// that have them draw no warning.
func TestAScaffoldWarnsOfADependencyItsPackageLacks(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	paths := tsFakePaths(repoRoot)
	f.build(t, repoRoot, paths, append(slices.Clone(tsOrder[:len(tsOrder)-1]), tsSite)...)
	build := func(service string) []string {
		t.Helper()
		result, err := generator.Run(f.schemas[service], f.configs[service], f.options(repoRoot, paths))
		if err != nil {
			t.Fatalf("build %s: %v", service, err)
		}
		return result.Warnings
	}
	if warnings := build(tsSite); len(warnings) != 0 {
		t.Errorf("the scaffolded site warns: %q", warnings)
	}
	if warnings := build("ts-stack"); len(warnings) != 0 {
		t.Errorf("the scaffolded implementations warn: %q", warnings)
	}

	// The packages as their authors left them: the lines a later config
	// needs are not in them.
	drop := func(dir string, lines ...string) string {
		t.Helper()
		path := filepath.Join(dir, "package.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		manifest := string(data)
		for _, line := range lines {
			if !strings.Contains(manifest, line) {
				t.Fatalf("%s holds no %s:\n%s", path, line, manifest)
			}
			manifest = strings.Replace(manifest, line, "", 1)
		}
		if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		return manifest
	}
	site := naming.Default().SiteImplementationDir(repoRoot, tsSite)
	siteManifest := drop(site, `
    "@schemas/ts-shop-sdk": "workspace:*"`)
	shop := naming.Default().TypeScriptImplementationDir(repoRoot, "ts-shop")
	shopManifest := drop(shop, `
    "@schemas/ts-db-types": "workspace:*",`, `
    "@schemas/ts-cache-types": "workspace:*",`)

	want := []string{`site ts-web: its package, at web/ts-web, does not depend on @schemas/ts-shop-sdk, which config.generated.ts imports; the build wrote the package's package.json once and does not write it again, so add

    "@schemas/ts-shop-sdk": "workspace:*"

to its dependencies (D100)`}
	if warnings := build(tsSite); !reflect.DeepEqual(warnings, want) {
		t.Errorf("the site's warnings =\n%q\nwant\n%q", warnings, want)
	}
	want = []string{`stack ts-stack: the implementation of ts-shop, at typescript/ts-shop, does not depend on @schemas/ts-db-types and @schemas/ts-cache-types, which its scaffold depends on now; the build wrote the package's package.json once and does not write it again, so add

    "@schemas/ts-db-types": "workspace:*",
    "@schemas/ts-cache-types": "workspace:*"

to its dependencies (D100)`}
	if warnings := build("ts-stack"); !reflect.DeepEqual(warnings, want) {
		t.Errorf("the stack's warnings =\n%q\nwant\n%q", warnings, want)
	}
	for dir, manifest := range map[string]string{site: siteManifest, shop: shopManifest} {
		if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err != nil || string(data) != manifest {
			t.Errorf("a build wrote %s/package.json: %v\n%s", dir, err, data)
		}
	}
}

// TestARustScaffoldWarnsOfADependencyItsCrateLacks (D100): a stack's
// build builds against a Rust implementation it scaffolded once and no
// longer writes. One whose Cargo.toml predates its API's manual route and
// memory store lacks axum and the store's definitions: the stack's build
// warns once, naming the entries its scaffold writes now, and writes
// nothing into the crate.
func TestARustScaffoldWarnsOfADependencyItsCrateLacks(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadRustFixture(t)
	paths := rustFakePaths(repoRoot)
	result := f.build(t, repoRoot, paths, rustOrder[:len(rustOrder)-1]...)
	if len(result.Warnings) != 0 {
		t.Errorf("the scaffolded implementations warn: %q", result.Warnings)
	}
	path := filepath.Join(naming.Default().RustImplementationDir(repoRoot, "rs-stock"), "Cargo.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept, dropped []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "axum = ") || strings.HasPrefix(line, "schemas-rs-cache-types = ") {
			dropped = append(dropped, "    "+line)
			continue
		}
		kept = append(kept, line)
	}
	if len(dropped) != 2 {
		t.Fatalf("rs-stock's scaffold depends on axum and schemas-rs-cache-types:\n%s", data)
	}
	manifest := strings.Join(kept, "\n")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = generator.Run(f.schemas[rustStack], f.configs[rustStack], f.options(repoRoot, paths))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"stack rs-stack: the implementation of rs-stock, at rust/rs-stock, does not depend on axum and schemas-rs-cache-types, which its scaffold depends on now; the build wrote the crate's Cargo.toml once and does not write it again, so add\n\n" +
		strings.Join(dropped, "\n") + "\n\nto its [dependencies] (D100)"}
	if !reflect.DeepEqual(result.Warnings, want) {
		t.Errorf("the stack's warnings =\n%q\nwant\n%q", result.Warnings, want)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != manifest {
		t.Errorf("a build wrote %s: %v\n%s", path, err, data)
	}
}

package ext_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/loader"
	"github.com/parable-work/superschematic/registry"
)

var update = flag.Bool("update", false, "rewrite the goldens in testdata/golden")

// TestRatingProjectEngine builds shop-ratings as a service of this
// project, whose [paths] name the checkout's engine crates (D58): the
// core's projectengine generator writes the project's engine crate,
// engine/rust/shop-ratings, which depends on the engine's crates and on
// acme-behaviors at crates/behaviors and registers acme_behaviors::Rating
// after the core's behaviors, under acme's invocation policy and vendor
// keys, and its ffi member, the C ABI's archive over those behaviors that
// a Go server links. The crate is held to
// testdata/golden/shop-ratings-engine, the checkout's path as <checkout>;
// rewrite the goldens with:
//
//	go test ./ext -run TestRatingProjectEngine -update
//
// The smoke builds and tests the crate the binary writes.
func TestRatingProjectEngine(t *testing.T) {
	reg, names := assemble(t)
	schema, cfg, err := loader.LoadServiceWithConfig(shopRatings, loader.WithRegistry(reg), loader.WithNaming(names))
	if err != nil {
		t.Fatal(err)
	}
	project, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	out, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Generate(schema, cfg, registry.Options{
		OutputRoot:     out,
		ServicePath:    shopRatings,
		Naming:         names,
		Registry:       reg,
		RepositoryRoot: project,
		Paths:          names.LocalPaths(project),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dir := filepath.Join(out, "engine", "rust", "shop-ratings")
	if result.Outputs["engine-rust"] != dir {
		t.Fatalf("Outputs[engine-rust] = %q, want %q (outputs %v)", result.Outputs["engine-rust"], dir, result.Outputs)
	}
	// Each crate's paths are relative to its own directory: the ffi
	// member's to ffi/.
	for _, file := range []string{"Cargo.toml", filepath.Join("src", "lib.rs"), filepath.Join("ffi", "Cargo.toml"), filepath.Join("ffi", "src", "lib.rs")} {
		crate := dir
		if strings.HasPrefix(file, "ffi"+string(filepath.Separator)) {
			crate = filepath.Join(dir, "ffi")
		}
		checkout, err := filepath.Rel(crate, filepath.Join(project, "..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		got := strings.ReplaceAll(string(data), `"`+filepath.ToSlash(checkout)+"/", `"<checkout>/`)
		golden := filepath.Join("testdata", "golden", "shop-ratings-engine", file+".golden")
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (rewrite it with -update)", err)
		}
		if got != string(want) {
			t.Errorf("%s differs from %s (rewrite it with -update):\n%s", file, golden, got)
		}
	}
}

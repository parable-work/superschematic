package rustrestgen

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// TestSetDepsRefusesAClash: a callee whose client would take the name of
// another Deps field is refused.
func TestSetDepsRefusesAClash(t *testing.T) {
	output := generateRustAPI(t, userRoutesAPI, false, "", nil)
	if err := output.SetDeps(DepsInfo{Calls: []DepsCall{{Service: "db", Field: DepsField("db")}}}); err == nil ||
		!strings.Contains(err.Error(), "calls db, whose client would be Deps.db, the field of Deps.db") {
		t.Errorf("a callee named db: %v", err)
	}
	if err := output.SetDeps(DepsInfo{Calls: []DepsCall{{Service: "shop-api", Field: "shop_api"}, {Service: "shop_api", Field: "shop_api"}}}); err == nil ||
		!strings.Contains(err.Error(), "the field of shop-api") {
		t.Errorf("two callees with one field: %v", err)
	}
}

// TestRuntimeFeatures: the crate turns on identity for the user model,
// postgres for a database, identity-postgres for both, and http-client for
// a callee (D57); a crate without Deps keeps the features it had.
func TestRuntimeFeatures(t *testing.T) {
	output := generateRustAPI(t, userRoutesAPI, false, "", nil)
	if got := output.RuntimeFeatures(); !slices.Equal(got, []string{"identity"}) {
		t.Errorf("without Deps: %v", got)
	}
	if err := output.SetDeps(DepsInfo{Database: "fixture-user-model-db", Calls: []DepsCall{{Service: "shop-api", Field: "shop_api"}}}); err != nil {
		t.Fatal(err)
	}
	if got := output.RuntimeFeatures(); !slices.Equal(got, []string{"identity", "postgres", "identity-postgres", "http-client"}) {
		t.Errorf("with a database and a callee: %v", got)
	}
	plain := generateMultiwordAPIRust(t)
	if err := plain.SetDeps(DepsInfo{Database: "shop-db"}); err != nil {
		t.Fatal(err)
	}
	if got := plain.RuntimeFeatures(); !slices.Equal(got, []string{"postgres"}) {
		t.Errorf("a database without the user model: %v", got)
	}
}

// TestWriteAPIWritesDepsWithItsDeps: the crate has src/deps.rs, which
// lib.rs declares, when the output has its Deps, and a crate written again
// without them keeps no stale module.
func TestWriteAPIWritesDepsWithItsDeps(t *testing.T) {
	output := generateMultiwordAPIRust(t)
	if err := SetReplacePaths(output, naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, "/repo/schemas/dist/api/fixture-multiword-api"); err != nil {
		t.Fatal(err)
	}
	if err := output.SetDeps(DepsInfo{}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	deps, err := os.ReadFile(filepath.Join(dir, "src", "deps.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pub struct Deps {", "pub span: tracing::Span,", "pub pool_search: Arc<dyn PoolSearchImplementation>,", "pub fn new(handlers: Handlers) -> Self {"} {
		if !strings.Contains(string(deps), want) {
			t.Errorf("src/deps.rs does not contain %q:\n%s", want, deps)
		}
	}
	lib, err := os.ReadFile(filepath.Join(dir, "src", "lib.rs"))
	if err != nil || !strings.Contains(string(lib), "pub mod deps;") || !strings.Contains(string(lib), "pub use deps::{Deps, Handlers};") {
		t.Errorf("lib.rs does not declare deps: %s, %v", lib, err)
	}

	output.Deps = nil
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "deps.rs")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a crate written again without Deps keeps src/deps.rs: %v", err)
	}
}

// TestImplementationScaffoldNeedsItsDeps: the scaffold's create returns
// the Handlers deps.rs declares, so an output without Deps is refused.
func TestImplementationScaffoldNeedsItsDeps(t *testing.T) {
	output := generateMultiwordAPIRust(t)
	if _, err := WriteImplementationScaffold(output, t.TempDir(), t.TempDir(), naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, naming.Default()); err == nil || !strings.Contains(err.Error(), "needs its Deps") {
		t.Errorf("a scaffold without Deps: %v", err)
	}
}

package tsgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// requireOrSkipTSTooling decides what a missing TypeScript toolchain means.
// Locally it stays a skip so `go test ./...` runs without bun. CI sets
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1, where a skip would hide a broken gate,
// so it fails instead.
func requireOrSkipTSTooling(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("SUPERSCHEMATIC_REQUIRE_TS_CHECKS") == "1" {
		t.Fatalf("SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1 requires this TypeScript gate to run: %s", reason)
	}
	t.Skipf("skipping TypeScript gate: %s", reason)
}

// tsPackageCase is one generated types package: a schema and the loaded
// dependency schemas it imports from.
type tsPackageCase struct {
	name   string
	schema *ir.Schema
	deps   map[string]*ir.Schema
}

// buildTSPackages writes each case's TypeScript types package, wired
// against the real superscalar runtime, into <temp>/types/typescript/<name>
// under a Bun workspace root, as a build lays them out. It installs once at
// the workspace root and then type-checks (tsc) each package in order, so
// list a dependency before its consumer. It returns the directory holding
// the packages and the bun binary, and skips (or fails under
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1) when bun is missing or the install
// fails.
//
// The one install runs at the root on purpose. With bun 1.4.0, a
// `bun install` inside a member after another install wrote the root
// lockfile resolves the lockfile's root-relative superscalar file: path
// from the member directory and fails; with 1.4.2 an install inside a
// member that has a sibling file: dependency fails the same way.
func buildTSPackages(t *testing.T, cases []tsPackageCase) (typesRoot, bunPath string) {
	t.Helper()
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}

	paths := testpaths.Local(t)

	// Resolve symlinks (macOS /var -> /private/var) so the relative file:
	// spec computed against the temp dir resolves correctly at install time.
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	typesRoot = filepath.Join(tempRoot, "types", "typescript")
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	for _, tc := range cases {
		output, err := Generate(tc.schema, Options{
			SchemaName:   tc.name,
			Dependencies: tc.deps,
			Clock:        fixedClock,
		})
		if err != nil {
			t.Fatalf("generate %s: %v", tc.name, err)
		}

		outDir := filepath.Join(typesRoot, tc.name)
		if err := SetScalarLibSpec(output, paths, outDir); err != nil {
			t.Fatalf("set superscalar spec for %s: %v", tc.name, err)
		}
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
	}
	// The directory holding the packages is a Bun workspace root, so a
	// sibling file:../<name> dependency and the file: superscalar spec
	// resolve for every package.
	if err := WriteWorkspaceRoot(typesRoot, naming.Naming{}); err != nil {
		t.Fatalf("write workspace root: %v", err)
	}

	install := exec.Command(bunPath, "install")
	install.Dir = typesRoot
	if out, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun install failed (likely offline): %v\n%s", err, out))
	}

	for _, tc := range cases {
		build := exec.Command(bunPath, "x", "tsc")
		build.Dir = filepath.Join(typesRoot, tc.name)
		if out, err := build.CombinedOutput(); err != nil {
			t.Errorf("generated package %s does not type-check: %v\n%s", tc.name, err, out)
		}
	}
	return typesRoot, bunPath
}

// TestGeneratedPackagesCompile generates the TypeScript type packages for
// the fixture services, including every arrays-of-arrays fixture, into a
// temp tree wired against the real superscalar runtime and runs tsc on
// each. This is the cheap end-to-end compile check for the tsgen port.
// Skips when bun is unavailable.
func TestGeneratedPackagesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	load := func(service string) *ir.Schema {
		t.Helper()
		schema, err := loader.LoadService(filepath.Join(fixturesDir, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		return schema
	}
	dbSchema := load("fixture-db")

	cases := []tsPackageCase{
		{name: "fixture-db", schema: dbSchema},
		{name: "fixture-api", schema: load("fixture-api"), deps: map[string]*ir.Schema{"fixture-db": dbSchema}},
		{name: "fixture-nested-arrays", schema: load("fixture-nested-arrays")},
		{name: "fixture-nested-arrays-db", schema: load("fixture-nested-arrays-db")},
		{name: "fixture-nested-arrays-api", schema: load("fixture-nested-arrays-api")},
	}
	buildTSPackages(t, append(cases, loadNestedArraysEdges(t)...))
}
